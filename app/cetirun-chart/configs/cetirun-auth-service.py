#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""CetiRun in-pod auth helper service.

Exposes a tiny HTTP API on 0.0.0.0:18790 that the Portal "Copilot Auth" UI
calls (via Istio: /<instance>/cetirun-auth/* → / → :18790) to:

  POST /v1/login/start                  start a GitHub Copilot OAuth device-code flow
  GET  /v1/login/status?session_id=...  poll the in-progress flow
  POST /v1/login/cancel                 cancel the in-progress flow
  GET  /v1/status                       report current Copilot auth + provider/model
  GET  /v1/models                       list live GitHub Copilot models for this login

The service runs in *both* Hermes and OpenClaw pods. It dispatches on the
``CETIRUN_AGENT_TYPE`` environment variable (``hermes`` | ``openclaw``) at
exactly four points: env-file path, token env-var name, supervisord program
to restart, and the "is the token still good" check. The HTTP surface and
OAuth flow are identical.

Token handling stays inside the pod: when the OAuth flow finishes, this
service writes the token into the agent's own dotenv file
(``~/.hermes/.env`` or ``~/.openclaw/.env``) and runs
``supervisorctl restart <agent-program>`` so the gateway picks up the new
credential without restarting the pod.

Bearer auth: every /v1/* request requires
``Authorization: Bearer ${CETIRUN_AUTH_KEY}``. For backward compatibility
with the original Hermes deployment, ``API_SERVER_KEY`` is accepted as a
fallback when ``CETIRUN_AUTH_KEY`` is unset.
"""
from __future__ import annotations

import argparse
import asyncio
import hashlib
import json
import logging
import os
import re
import secrets
import subprocess
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Dict, Optional
from urllib.parse import urlparse

import aiohttp
import yaml
from aiohttp import web

logger = logging.getLogger("cetirun-auth")

# The catalog requires exchanging the GitHub OAuth token at
# /copilot_internal/v2/token. That endpoint only accepts OAuth credentials
# issued to an allowlisted Copilot client. Use the official GitHub
# Copilot/VS Code client for both agents so a fresh Portal login produces a
# credential that works for both inference and live catalog discovery.
COPILOT_OAUTH_CLIENT_ID = "Iv1.b507a08c87ecfe98"
COPILOT_OAUTH_SCOPE = "read:user"
GITHUB_HOST = os.environ.get("COPILOT_GH_HOST", "github.com").rstrip("/")
DEVICE_CODE_URL = f"https://{GITHUB_HOST}/login/device/code"
ACCESS_TOKEN_URL = f"https://{GITHUB_HOST}/login/oauth/access_token"
USER_AGENT = "CetiRunAuthService/1.0"

# A GitHub OAuth device-flow token is not accepted by the Copilot API. Exchange
# it at GitHub first, then use the resulting short-lived Copilot API token for
# catalog requests. These endpoints deliberately are not environment
# configurable: instance Helm values can set extraEnv, and allowing them to
# choose a URL here would let an instance exfiltrate its Copilot credentials.
COPILOT_TOKEN_URL = "https://api.github.com/copilot_internal/v2/token"
DEFAULT_COPILOT_API_BASE_URL = "https://api.individual.githubcopilot.com"
COPILOT_INTEGRATION_ID = "vscode-chat"
COPILOT_EDITOR_VERSION = "vscode/1.107.0"
COPILOT_EDITOR_PLUGIN_VERSION = "copilot-chat/0.35.0"
COPILOT_USER_AGENT = "GitHubCopilotChat/0.35.0"
COPILOT_GITHUB_API_VERSION = "2025-04-01"

# Polling defaults (RFC 8628). Server may override via "interval" / "slow_down".
DEFAULT_POLL_INTERVAL = 5
POLL_SAFETY_MARGIN = 3
DEFAULT_FLOW_TIMEOUT = 30 * 60  # 30 min — GitHub device codes expire ~15 min
STATUS_CACHE_TTL = 30  # seconds
MODEL_CATALOG_CACHE_TTL = 5 * 60  # seconds

# The catalog can occasionally include non-chat objects (for example embeddings
# and account routers). Only expose models that an instance can select for chat.
COPILOT_CHAT_ENDPOINTS = {
    "/v1/chat/completions",
    "/v1/responses",
    "/v1/messages",
    "/chat/completions",
    "/responses",
    "/messages",
}

LISTEN_HOST = os.environ.get("CETIRUN_AUTH_HOST", "0.0.0.0")
LISTEN_PORT = int(os.environ.get("CETIRUN_AUTH_PORT", "18790"))
SUPERVISOR_CONF = os.environ.get("SUPERVISOR_CONF", "/tmp/supervisord.conf")

AGENT_TYPE = os.environ.get("CETIRUN_AGENT_TYPE", "hermes").strip().lower()


# ─── Agent profile ─────────────────────────────────────────────────────────


@dataclass
class AgentProfile:
    """Per-agent settings the service branches on."""

    name: str
    env_file: Path
    token_env_var: str
    supervisord_program: str
    # GitHub OAuth client_id used for the device-code flow. It must be
    # accepted by /copilot_internal/v2/token for catalog discovery.
    oauth_client_id: str
    # Returns (provider, model) for /v1/status. Best-effort; falls back to
    # ("copilot", "") if config is unreadable.
    read_provider_and_model: Callable[[], Dict[str, str]]
    # Returns True iff the agent currently has a usable Copilot token.
    is_authenticated: Callable[[], bool]
    # Persists ``token`` into the agent's env file. Must be safe to call from
    # an executor thread; should not raise on transient errors.
    save_token: Callable[[str], None]
    # Returns the active GitHub OAuth token, or an empty string when absent.
    # The token must never leave this process.
    read_token: Callable[[], str]


def _atomic_write(path: Path, content: str) -> None:
    """Write ``content`` to ``path`` atomically with mode 0600."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(content)
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def _read_dotenv(path: Path) -> Dict[str, str]:
    """Parse ``KEY=VALUE`` lines from a dotenv file. Comments / blanks ignored."""
    entries: Dict[str, str] = {}
    if not path.exists():
        return entries
    try:
        with path.open("r", encoding="utf-8") as f:
            for raw in f:
                line = raw.strip()
                if not line or line.startswith("#"):
                    continue
                if "=" not in line:
                    continue
                key, _, value = line.partition("=")
                key = key.strip()
                value = value.strip()
                if (value.startswith('"') and value.endswith('"')) or (
                    value.startswith("'") and value.endswith("'")
                ):
                    value = value[1:-1]
                if key:
                    entries[key] = value
    except OSError as exc:
        logger.debug("failed to read %s: %s", path, exc)
    return entries


def _write_dotenv(path: Path, entries: Dict[str, str]) -> None:
    """Serialize entries back to dotenv form and write atomically."""
    lines = [f"{k}={v}" for k, v in entries.items()]
    _atomic_write(path, "\n".join(lines) + "\n")


# --- Hermes profile -------------------------------------------------------


def _hermes_env_file() -> Path:
    home = Path(os.environ.get("HERMES_HOME", str(Path.home() / ".hermes")))
    return home / ".env"


def _hermes_save_token(token: str) -> None:
    """Use Hermes' own helper so format/locking matches what the gateway reads."""
    from hermes_cli.config import save_env_value  # type: ignore

    save_env_value("COPILOT_GITHUB_TOKEN", token)
    logger.info("COPILOT_GITHUB_TOKEN saved to %s", _hermes_env_file())


def _hermes_read_provider_and_model() -> Dict[str, str]:
    home = Path(os.environ.get("HERMES_HOME", str(Path.home() / ".hermes")))
    config_path = home / "config.yaml"
    provider = "copilot"
    model_name = ""
    try:
        with config_path.open("r", encoding="utf-8") as f:
            cfg = yaml.safe_load(f) or {}
        model_cfg = cfg.get("model")
        if isinstance(model_cfg, dict):
            provider = str(model_cfg.get("provider") or provider)
            model_name = str(model_cfg.get("default") or "")
        elif model_cfg:
            model_name = str(model_cfg)
    except FileNotFoundError:
        # Config not yet present (fresh pod) — fall through to defaults.
        pass
    except Exception as exc:  # noqa: BLE001 — config is best-effort
        logger.debug("failed to read hermes config.yaml: %s", exc)
    return {"provider": provider, "model": model_name}


def _hermes_is_authenticated() -> bool:
    try:
        from hermes_cli.auth import get_auth_status  # type: ignore
    except Exception as exc:  # noqa: BLE001
        logger.debug("hermes_cli.auth import failed: %s", exc)
        return False
    try:
        status = get_auth_status("copilot")
    except Exception as exc:  # noqa: BLE001 — keep service running
        logger.debug("get_auth_status(copilot) raised: %s", exc)
        return False
    return bool(status.get("logged_in"))


def _hermes_read_token() -> str:
    return _read_dotenv(_hermes_env_file()).get("COPILOT_GITHUB_TOKEN", "")


# --- OpenClaw profile -----------------------------------------------------


def _openclaw_state_dir() -> Path:
    raw = os.environ.get("OPENCLAW_STATE_DIR")
    if raw:
        return Path(raw)
    return Path.home() / ".openclaw"


def _openclaw_env_file() -> Path:
    return _openclaw_state_dir() / ".env"


def _openclaw_agents_dir() -> Path:
    return _openclaw_state_dir() / "agents"


def _openclaw_write_auth_profile(token: str) -> None:
    """Write the github-copilot token into each agent's auth-profiles.json.

    OpenClaw's chat runtime reads the Copilot token from
    ``~/.openclaw/agents/<agent>/agent/auth-profiles.json`` under
    ``profiles["github-copilot:github"].token`` — NOT from the ``.env`` file.
    Setting ``OPENCLAW_GITHUB_TOKEN`` alone is insufficient.

    This mirrors the merge logic in ``configs/init-home.sh``
    ``migrate_agent_auth_profiles``: for every existing agent dir, merge a
    ``{"type": "token", "provider": "github-copilot", "token": ...}`` profile
    into the file; if no agent dir exists yet, seed ``main/agent/``.
    """
    agents_dir = _openclaw_agents_dir()
    agents_dir.mkdir(parents=True, exist_ok=True)

    profile = {
        "type": "token",
        "provider": "github-copilot",
        "token": token,
    }

    agent_subdirs = [p for p in agents_dir.iterdir() if p.is_dir()] if agents_dir.exists() else []
    if not agent_subdirs:
        # Seed the default "main" agent on first login.
        (agents_dir / "main" / "agent").mkdir(parents=True, exist_ok=True)
        agent_subdirs = [agents_dir / "main"]

    for agent_root in agent_subdirs:
        agent_dir = agent_root / "agent"
        agent_dir.mkdir(parents=True, exist_ok=True)
        auth_file = agent_dir / "auth-profiles.json"

        data: Dict[str, Any] = {"version": 1, "profiles": {}}
        if auth_file.exists():
            try:
                with auth_file.open("r", encoding="utf-8") as f:
                    loaded = json.load(f) or {}
                if isinstance(loaded, dict):
                    data = loaded
                    if not isinstance(data.get("profiles"), dict):
                        data["profiles"] = {}
                    data.setdefault("version", 1)
            except (OSError, json.JSONDecodeError) as exc:
                logger.warning("auth-profiles.json at %s unreadable, rewriting: %s", auth_file, exc)

        data["profiles"]["github-copilot:github"] = profile
        _atomic_write(auth_file, json.dumps(data, indent=2) + "\n")
        logger.info("github-copilot profile written to %s", auth_file)


def _openclaw_save_token(token: str) -> None:
    """Persist the Copilot token for OpenClaw.

    Writes two locations:

    1. ``~/.openclaw/.env`` — sets ``OPENCLAW_GITHUB_TOKEN``. This is what the
       documented precedence chain (OPENCLAW_GITHUB_TOKEN → COPILOT_GITHUB_TOKEN
       → GH_TOKEN → GITHUB_TOKEN) reads for environment-based lookups.
    2. ``~/.openclaw/agents/*/agent/auth-profiles.json`` — sets the
       ``github-copilot:github`` profile. The OpenClaw chat runtime reads from
       here; without it the gateway raises ``FailoverError: No API key found
       for provider "github-copilot"`` even with the env var set.

    The lower-priority ``COPILOT_GITHUB_TOKEN`` is cleared from .env to avoid
    surprising staleness after re-authentication.
    """
    path = _openclaw_env_file()
    entries = _read_dotenv(path)
    entries["OPENCLAW_GITHUB_TOKEN"] = token
    entries.pop("COPILOT_GITHUB_TOKEN", None)
    _write_dotenv(path, entries)
    logger.info("OPENCLAW_GITHUB_TOKEN saved to %s", path)

    try:
        _openclaw_write_auth_profile(token)
    except Exception as exc:  # noqa: BLE001 — env file save already succeeded
        logger.exception("failed to write auth-profiles.json: %s", exc)
        raise


def _openclaw_read_provider_and_model() -> Dict[str, str]:
    """Read agents.defaults.model.primary from openclaw.json.

    Format is ``<provider>/<model>``, e.g. ``github-copilot/gpt-6-astra``.
    Falls back to ``copilot``/empty model when the file is missing or shaped
    unexpectedly.
    """
    config_path = Path(
        os.environ.get(
            "OPENCLAW_CONFIG_PATH",
            str(_openclaw_state_dir() / "openclaw.json"),
        )
    )
    provider = "copilot"
    model_name = ""
    try:
        with config_path.open("r", encoding="utf-8") as f:
            cfg = json.load(f) or {}
        primary = (
            cfg.get("agents", {})
               .get("defaults", {})
               .get("model", {})
               .get("primary", "")
        )
        if isinstance(primary, str) and primary:
            if "/" in primary:
                provider, model_name = primary.split("/", 1)
            else:
                model_name = primary
    except FileNotFoundError:
        # Config not yet present (fresh pod) — fall through to defaults.
        pass
    except Exception as exc:  # noqa: BLE001 — config is best-effort
        logger.debug("failed to read openclaw.json: %s", exc)
    return {"provider": provider, "model": model_name}


def _openclaw_is_authenticated() -> bool:
    """Check that *both* persistence locations have a token.

    The OpenClaw runtime only reads ``auth-profiles.json``; the env file is a
    redundancy for env-var-aware tooling. We require both because a stale env
    var with no profile would still report "logged in" from the user's POV
    while chat fails. Likewise a profile written without an env var means we
    were called before the user even ran login here.
    """
    entries = _read_dotenv(_openclaw_env_file())
    env_ok = any(
        entries.get(var)
        for var in (
            "OPENCLAW_GITHUB_TOKEN",
            "COPILOT_GITHUB_TOKEN",
            "GH_TOKEN",
            "GITHUB_TOKEN",
        )
    )
    if not env_ok:
        return False

    agents_dir = _openclaw_agents_dir()
    if not agents_dir.exists():
        return False
    for agent_root in agents_dir.iterdir():
        auth_file = agent_root / "agent" / "auth-profiles.json"
        if not auth_file.is_file():
            continue
        try:
            with auth_file.open("r", encoding="utf-8") as f:
                data = json.load(f) or {}
        except (OSError, json.JSONDecodeError):
            continue
        profiles = data.get("profiles") if isinstance(data, dict) else None
        if isinstance(profiles, dict):
            entry = profiles.get("github-copilot:github")
            if isinstance(entry, dict) and entry.get("token"):
                return True
    return False


def _openclaw_read_token() -> str:
    """Read the active OpenClaw Copilot credential without exposing it."""
    agents_dir = _openclaw_agents_dir()
    if agents_dir.exists():
        for agent_root in agents_dir.iterdir():
            auth_file = agent_root / "agent" / "auth-profiles.json"
            try:
                with auth_file.open("r", encoding="utf-8") as f:
                    data = json.load(f) or {}
                profile = data.get("profiles", {}).get("github-copilot:github", {})
                token = profile.get("token", "") if isinstance(profile, dict) else ""
                if isinstance(token, str) and token:
                    return token
            except (OSError, json.JSONDecodeError, AttributeError):
                continue
    entries = _read_dotenv(_openclaw_env_file())
    for key in ("OPENCLAW_GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"):
        if entries.get(key):
            return entries[key]
    return ""


# --- Profile resolution ---------------------------------------------------


def _resolve_profile(agent: str) -> AgentProfile:
    if agent == "openclaw":
        return AgentProfile(
            name="openclaw",
            env_file=_openclaw_env_file(),
            token_env_var="OPENCLAW_GITHUB_TOKEN",
            supervisord_program=os.environ.get(
                "CETIRUN_GATEWAY_PROGRAM", "openclaw"
            ),
            oauth_client_id=COPILOT_OAUTH_CLIENT_ID,
            read_provider_and_model=_openclaw_read_provider_and_model,
            is_authenticated=_openclaw_is_authenticated,
            save_token=_openclaw_save_token,
            read_token=_openclaw_read_token,
        )
    # Default → Hermes.
    return AgentProfile(
        name="hermes",
        env_file=_hermes_env_file(),
        token_env_var="COPILOT_GITHUB_TOKEN",
        supervisord_program=os.environ.get(
            "CETIRUN_GATEWAY_PROGRAM", "hermes-gateway"
        ),
        oauth_client_id=COPILOT_OAUTH_CLIENT_ID,
        read_provider_and_model=_hermes_read_provider_and_model,
        is_authenticated=_hermes_is_authenticated,
        save_token=_hermes_save_token,
        read_token=_hermes_read_token,
    )


PROFILE = _resolve_profile(AGENT_TYPE)


# ─── Login flow state ──────────────────────────────────────────────────────


@dataclass
class LoginFlow:
    session_id: str
    device_code: str
    user_code: str
    verification_uri: str
    expires_at: float
    interval: int
    state: str = "pending"  # pending | authenticated | expired | cancelled | error
    message: str = ""
    task: Optional[asyncio.Task] = field(default=None, repr=False)


_flows: Dict[str, LoginFlow] = {}
_flows_lock = asyncio.Lock()
_status_cache: Dict[str, Any] = {"value": None, "ts": 0.0}
_model_catalog_cache: Dict[str, Any] = {"value": None, "ts": 0.0, "token_fingerprint": None}


# ─── Bearer auth middleware ────────────────────────────────────────────────


def _expected_api_key() -> str:
    # Prefer the new dedicated key. Fall back to API_SERVER_KEY for legacy
    # Hermes deployments that haven't migrated yet.
    primary = os.environ.get("CETIRUN_AUTH_KEY", "").strip()
    if primary:
        return primary
    return os.environ.get("API_SERVER_KEY", "").strip()


@web.middleware
async def bearer_auth_middleware(request: web.Request, handler):
    if not request.path.startswith("/v1/"):
        return await handler(request)

    expected = _expected_api_key()
    if not expected:
        return web.json_response(
            {"error": "CETIRUN_AUTH_KEY is not configured"}, status=503
        )

    auth = request.headers.get("Authorization", "")
    if not auth.startswith("Bearer "):
        return web.json_response({"error": "missing bearer token"}, status=401)
    presented = auth[len("Bearer "):].strip()
    if not secrets.compare_digest(presented, expected):
        return web.json_response({"error": "invalid bearer token"}, status=401)

    return await handler(request)


# ─── Device code OAuth flow ────────────────────────────────────────────────


async def _request_device_code(
    session: aiohttp.ClientSession,
) -> Dict[str, Any]:
    data = {
        "client_id": PROFILE.oauth_client_id,
        "scope": COPILOT_OAUTH_SCOPE,
    }
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
        "User-Agent": USER_AGENT,
    }
    async with session.post(
        DEVICE_CODE_URL,
        data=data,
        headers=headers,
        timeout=aiohttp.ClientTimeout(total=15),
    ) as resp:
        body = await resp.text()
        if resp.status != 200:
            raise RuntimeError(
                f"device code request failed: HTTP {resp.status}: {body[:200]}"
            )
        try:
            return json.loads(body)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"device code response was not JSON: {exc}") from exc


async def _poll_access_token(
    session: aiohttp.ClientSession,
    device_code: str,
) -> Dict[str, Any]:
    data = {
        "client_id": PROFILE.oauth_client_id,
        "device_code": device_code,
        "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
    }
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/x-www-form-urlencoded",
        "User-Agent": USER_AGENT,
    }
    async with session.post(
        ACCESS_TOKEN_URL,
        data=data,
        headers=headers,
        timeout=aiohttp.ClientTimeout(total=10),
    ) as resp:
        body = await resp.text()
        try:
            return json.loads(body)
        except json.JSONDecodeError:
            return {"error": f"non_json_response_{resp.status}"}


def _save_token_and_restart_gateway(token: str) -> None:
    """Persist token via the active profile and restart the gateway."""
    PROFILE.save_token(token)

    cmd = [
        "supervisorctl",
        "-c",
        SUPERVISOR_CONF,
        "restart",
        PROFILE.supervisord_program,
    ]
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=30
        )
    except subprocess.TimeoutExpired:
        logger.warning("supervisorctl restart timed out")
        return
    if result.returncode != 0:
        logger.warning(
            "supervisorctl restart returned %s: %s",
            result.returncode, (result.stderr or result.stdout).strip(),
        )
    else:
        logger.info("%s restart requested", PROFILE.supervisord_program)


async def _run_login_flow(flow: LoginFlow) -> None:
    """Background task: poll GitHub until token / expiry / cancellation."""
    async with aiohttp.ClientSession() as session:
        while time.monotonic() < flow.expires_at:
            try:
                await asyncio.sleep(flow.interval + POLL_SAFETY_MARGIN)
            except asyncio.CancelledError:
                async with _flows_lock:
                    if flow.state == "pending":
                        flow.state = "cancelled"
                        flow.message = "Cancelled."
                return

            try:
                result = await _poll_access_token(session, flow.device_code)
            except asyncio.CancelledError:
                async with _flows_lock:
                    if flow.state == "pending":
                        flow.state = "cancelled"
                        flow.message = "Cancelled."
                return
            except Exception as exc:  # noqa: BLE001 — log and keep polling
                logger.debug("poll attempt failed: %s", exc)
                continue

            token = result.get("access_token")
            if token:
                try:
                    # Token persistence + supervisorctl call are blocking; run
                    # them off the event loop so we don't stall other requests.
                    await asyncio.get_running_loop().run_in_executor(
                        None, _save_token_and_restart_gateway, token
                    )
                except Exception as exc:  # noqa: BLE001
                    logger.exception("post-login persistence failed")
                    async with _flows_lock:
                        flow.state = "error"
                        flow.message = f"login succeeded but persistence failed: {exc}"
                    return
                # Invalidate the status cache so the next /v1/status reflects the
                # new token without waiting for the 30s TTL.
                _status_cache["value"] = None
                _status_cache["ts"] = 0.0
                _model_catalog_cache["value"] = None
                _model_catalog_cache["ts"] = 0.0
                _model_catalog_cache["token_fingerprint"] = None
                async with _flows_lock:
                    flow.state = "authenticated"
                    flow.message = "Authenticated; gateway is restarting."
                return

            error = result.get("error", "")
            if error == "authorization_pending":
                continue
            if error == "slow_down":
                server_interval = result.get("interval")
                if isinstance(server_interval, (int, float)) and server_interval > 0:
                    flow.interval = int(server_interval)
                else:
                    flow.interval += 5
                continue
            if error == "expired_token":
                async with _flows_lock:
                    flow.state = "expired"
                    flow.message = "Device code expired. Please try again."
                return
            if error == "access_denied":
                async with _flows_lock:
                    flow.state = "error"
                    flow.message = "Authorization was denied."
                return
            if error:
                async with _flows_lock:
                    flow.state = "error"
                    flow.message = f"Authorization failed: {error}"
                return

    async with _flows_lock:
        if flow.state == "pending":
            flow.state = "expired"
            flow.message = "Timed out waiting for authorization."


# ─── HTTP handlers ─────────────────────────────────────────────────────────


async def handle_login_start(request: web.Request) -> web.Response:
    async with aiohttp.ClientSession() as session:
        try:
            data = await _request_device_code(session)
        except Exception as exc:  # noqa: BLE001
            logger.exception("device code request failed")
            return web.json_response(
                {"error": f"device code request failed: {exc}"}, status=502
            )

    device_code = data.get("device_code", "")
    user_code = data.get("user_code", "")
    if not device_code or not user_code:
        return web.json_response(
            {"error": "GitHub did not return a device code"}, status=502
        )

    interval = max(int(data.get("interval", DEFAULT_POLL_INTERVAL)), 1)
    expires_in = int(data.get("expires_in", DEFAULT_FLOW_TIMEOUT))
    verification_uri = data.get(
        "verification_uri", f"https://{GITHUB_HOST}/login/device"
    )

    session_id = secrets.token_urlsafe(16)
    flow = LoginFlow(
        session_id=session_id,
        device_code=device_code,
        user_code=user_code,
        verification_uri=verification_uri,
        expires_at=time.monotonic() + expires_in,
        interval=interval,
    )

    async with _flows_lock:
        # Reap stale flows — keep only pending ones, evict everything else.
        for sid, existing in list(_flows.items()):
            if existing.state != "pending":
                _flows.pop(sid, None)
        _flows[session_id] = flow

    flow.task = asyncio.create_task(_run_login_flow(flow))

    return web.json_response(
        {
            "sessionId": session_id,
            "userCode": user_code,
            "verificationUri": verification_uri,
            "expiresIn": expires_in,
            "interval": interval,
        }
    )


async def handle_login_status(request: web.Request) -> web.Response:
    session_id = request.query.get("session_id") or request.query.get("sessionId")
    if not session_id:
        return web.json_response({"error": "session_id is required"}, status=400)

    async with _flows_lock:
        flow = _flows.get(session_id)

    if flow is None:
        return web.json_response({"error": "unknown session"}, status=404)

    return web.json_response({"state": flow.state, "message": flow.message})


async def handle_login_cancel(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception:  # noqa: BLE001
        body = {}
    session_id = body.get("session_id") or body.get("sessionId") or ""
    if not session_id:
        session_id = request.query.get("session_id") or request.query.get("sessionId") or ""
    if not session_id:
        return web.json_response({"error": "session_id is required"}, status=400)

    async with _flows_lock:
        flow = _flows.get(session_id)
        if flow is None:
            return web.json_response({"error": "unknown session"}, status=404)
        if flow.state == "pending":
            flow.state = "cancelled"
            flow.message = "Cancelled."

    if flow.task is not None and not flow.task.done():
        flow.task.cancel()

    return web.json_response({"state": flow.state})


async def handle_status(request: web.Request) -> web.Response:
    now = time.monotonic()
    cached = _status_cache["value"]
    cached_ts = _status_cache["ts"]
    if cached and (now - cached_ts) < STATUS_CACHE_TTL:
        return web.json_response(cached)

    info = await asyncio.get_running_loop().run_in_executor(
        None, PROFILE.read_provider_and_model
    )
    authenticated = await asyncio.get_running_loop().run_in_executor(
        None, PROFILE.is_authenticated
    )
    payload = {
        "authenticated": authenticated,
        "provider": info["provider"],
        "model": info["model"],
        "lastCheckedAt": int(time.time()),
    }
    _status_cache["value"] = payload
    _status_cache["ts"] = now
    return web.json_response(payload)


def _sanitize_model_catalog(payload: Any) -> list[Dict[str, Any]]:
    """Return only the safe, text-chat-capable part of a Copilot catalog."""
    candidates = payload.get("data", payload.get("models", [])) if isinstance(payload, dict) else payload
    if not isinstance(candidates, list):
        raise ValueError("GitHub Copilot returned an invalid model catalog")
    models: list[Dict[str, Any]] = []
    seen: set[str] = set()
    for candidate in candidates:
        if not isinstance(candidate, dict):
            continue
        model_id = candidate.get("id")
        if not isinstance(model_id, str):
            continue
        model_id = model_id.strip()
        if not model_id or model_id in seen or model_id.lower().startswith("accounts/"):
            continue
        if candidate.get("object") and candidate.get("object") != "model":
            continue
        capabilities = candidate.get("capabilities")
        if isinstance(capabilities, dict):
            capability_type = capabilities.get("type")
            if isinstance(capability_type, str) and capability_type.lower() != "chat":
                continue
        if candidate.get("model_picker_enabled") is False:
            continue
        endpoints = candidate.get("supported_endpoints", candidate.get("supportedEndpoints", []))
        if isinstance(endpoints, str):
            endpoints = [endpoints]
        if not isinstance(endpoints, list):
            endpoints = []
        endpoints = [
            item.strip() for item in endpoints if isinstance(item, str) and item.strip()
        ]
        chat_endpoints = [
            item
            for item in endpoints
            if item.rstrip("/").lower() in COPILOT_CHAT_ENDPOINTS
        ]
        if endpoints and not chat_endpoints:
            continue
        # Some catalog revisions omit endpoint metadata. Keep those entries;
        # GitHub is still the source of truth and the gateway will validate it.
        display_name = candidate.get("name", candidate.get("display_name", candidate.get("displayName", model_id)))
        entry: Dict[str, Any] = {
            "id": model_id,
            "displayName": str(display_name).strip() or model_id,
        }
        if chat_endpoints:
            entry["supportedEndpoints"] = chat_endpoints
        models.append(entry)
        seen.add(model_id)
    return models


def _copilot_headers(token: str, *, include_github_api_version: bool = False) -> Dict[str, str]:
    """Build headers shared by Copilot token exchange and catalog requests."""
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/json",
        "Copilot-Integration-Id": COPILOT_INTEGRATION_ID,
        "Editor-Version": COPILOT_EDITOR_VERSION,
        "Editor-Plugin-Version": COPILOT_EDITOR_PLUGIN_VERSION,
        "User-Agent": COPILOT_USER_AGENT,
    }
    if include_github_api_version:
        headers["X-Github-Api-Version"] = COPILOT_GITHUB_API_VERSION
    return headers


def _copilot_api_base_url(token: str) -> str:
    """Derive a trusted Copilot API origin from a token's proxy endpoint.

    GitHub may issue tenant-specific Copilot tokens that identify a proxy via
    ``proxy-ep``. The value is untrusted input, so accept only a proxy host in
    GitHub's Copilot domain; otherwise use the public individual endpoint.
    """
    match = re.search(r"(?:^|;)\s*proxy-ep=([^;\s]+)", token.strip(), re.IGNORECASE)
    proxy_endpoint = match.group(1).strip() if match else ""
    if not proxy_endpoint:
        return DEFAULT_COPILOT_API_BASE_URL
    url_text = (
        proxy_endpoint
        if re.match(r"^https?://", proxy_endpoint, re.IGNORECASE)
        else f"https://{proxy_endpoint}"
    )
    try:
        parsed = urlparse(url_text)
        host = (parsed.hostname or "").lower()
    except ValueError:
        return DEFAULT_COPILOT_API_BASE_URL
    if not host.startswith("proxy.") or not host.endswith(".githubcopilot.com"):
        return DEFAULT_COPILOT_API_BASE_URL
    return f"https://api.{host[len('proxy.'):]}"


class CatalogRequestError(Exception):
    """An upstream catalog failure that can be safely returned to the Portal."""

    def __init__(self, code: str, message: str, status: int) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.status = status


async def _load_copilot_model_catalog(
    session: aiohttp.ClientSession, github_token: str
) -> list[Dict[str, Any]]:
    """Exchange a GitHub token and load its entitled Copilot chat models."""
    async with session.get(
        COPILOT_TOKEN_URL,
        headers=_copilot_headers(github_token, include_github_api_version=True),
    ) as response:
        if response.status in (401, 403):
            raise CatalogRequestError(
                "catalog_forbidden",
                "GitHub Copilot did not authorize model catalog access for this login.",
                response.status,
            )
        if response.status == 404 and PROFILE.name == "hermes":
            # Hermes credentials created by image versions before this change
            # used Hermes' client ID, which GitHub will not exchange. OAuth
            # tokens cannot be migrated between clients, so require one
            # re-authentication through the updated Portal flow.
            raise CatalogRequestError(
                "catalog_reauthenticate_required",
                "Re-authenticate with GitHub Copilot to load the live model catalog.",
                409,
            )
        if response.status != 200:
            logger.warning("Copilot token exchange returned HTTP %s", response.status)
            raise CatalogRequestError(
                "catalog_unavailable",
                "GitHub Copilot model catalog is temporarily unavailable.",
                502,
            )
        exchange = await response.json(content_type=None)
    copilot_token = exchange.get("token") if isinstance(exchange, dict) else None
    if not isinstance(copilot_token, str) or not copilot_token.strip():
        raise CatalogRequestError(
            "catalog_unavailable",
            "GitHub Copilot returned an invalid token response.",
            503,
        )

    models_url = f"{_copilot_api_base_url(copilot_token).rstrip('/')}/models"
    async with session.get(models_url, headers=_copilot_headers(copilot_token)) as response:
        if response.status in (401, 403):
            raise CatalogRequestError(
                "catalog_forbidden",
                "GitHub Copilot did not authorize model catalog access for this login.",
                response.status,
            )
        if response.status != 200:
            logger.warning("Copilot model catalog returned HTTP %s", response.status)
            raise CatalogRequestError(
                "catalog_unavailable",
                "GitHub Copilot model catalog is temporarily unavailable.",
                502,
            )
        return _sanitize_model_catalog(await response.json(content_type=None))


async def handle_models(request: web.Request) -> web.Response:
    """Proxy the logged-in user's live Copilot model catalog, sans secrets."""
    now = time.monotonic()
    token = await asyncio.get_running_loop().run_in_executor(None, PROFILE.read_token)
    if not token:
        return web.json_response(
            {"error": {"code": "not_authenticated", "message": "Sign in to GitHub Copilot before loading models."}},
            status=401,
        )
    token_fingerprint = hashlib.sha256(token.encode("utf-8")).hexdigest()
    cached = _model_catalog_cache["value"]
    if (
        cached
        and _model_catalog_cache["token_fingerprint"] == token_fingerprint
        and (now - _model_catalog_cache["ts"]) < MODEL_CATALOG_CACHE_TTL
    ):
        return web.json_response({**cached, "cached": True})

    try:
        timeout = aiohttp.ClientTimeout(total=15)
        async with aiohttp.ClientSession(timeout=timeout) as session:
            models = await _load_copilot_model_catalog(session, token)
    except CatalogRequestError as exc:
        return web.json_response(
            {"error": {"code": exc.code, "message": exc.message}},
            status=exc.status,
        )
    except (aiohttp.ClientError, asyncio.TimeoutError, json.JSONDecodeError, ValueError) as exc:
        logger.warning("failed to load Copilot model catalog: %s", exc)
        return web.json_response(
            {"error": {"code": "catalog_unavailable", "message": "Could not load the GitHub Copilot model catalog."}},
            status=503,
        )

    payload = {"models": models, "source": "github-copilot", "cached": False}
    _model_catalog_cache["value"] = payload
    _model_catalog_cache["ts"] = now
    _model_catalog_cache["token_fingerprint"] = token_fingerprint
    return web.json_response(payload)


async def handle_health(request: web.Request) -> web.Response:
    return web.json_response({"ok": True, "agent": PROFILE.name})


def build_app() -> web.Application:
    app = web.Application(middlewares=[bearer_auth_middleware])
    app.router.add_get("/health", handle_health)
    app.router.add_post("/v1/login/start", handle_login_start)
    app.router.add_get("/v1/login/status", handle_login_status)
    app.router.add_post("/v1/login/cancel", handle_login_cancel)
    app.router.add_get("/v1/status", handle_status)
    app.router.add_get("/v1/models", handle_models)
    return app


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default=LISTEN_HOST)
    parser.add_argument("--port", type=int, default=LISTEN_PORT)
    parser.add_argument("--log-level", default=os.environ.get("LOG_LEVEL", "INFO"))
    args = parser.parse_args()

    logging.basicConfig(
        level=args.log_level.upper(),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    logger.info(
        "cetirun-auth starting: agent=%s program=%s env_file=%s",
        PROFILE.name, PROFILE.supervisord_program, PROFILE.env_file,
    )
    if not _expected_api_key():
        logger.warning(
            "CETIRUN_AUTH_KEY is empty; service will reject all /v1 requests."
        )

    web.run_app(build_app(), host=args.host, port=args.port, print=lambda *_a: None)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
