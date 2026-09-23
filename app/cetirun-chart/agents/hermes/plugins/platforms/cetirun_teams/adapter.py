# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""CetiRun Teams platform adapter — webhook server and message dispatch."""

import json
import logging
import os
import re
import tempfile
from collections import OrderedDict
from typing import Optional
from urllib.parse import urlparse

import fcntl
import httpx
from aiohttp import web

from gateway.platforms import BasePlatformAdapter, MessageEvent, SendResult
from gateway.config import Platform
from .auth import BotTokenProvider, InboundJwtValidator, BOT_FRAMEWORK_SCOPE

logger = logging.getLogger(__name__)

MENTION_PATTERN = re.compile(r"<at>[^<]*</at>\s*")
MAX_MESSAGE_LENGTH = 28000
TRUNCATION_SUFFIX = "\n\n...(truncated)"
SERVICE_URL_CACHE_MAX = 1024

ALLOWED_USERS_ENV = "CETIRUN_TEAMS_ALLOWED_USERS"
GLOBAL_ALLOWED_USERS_ENV = "GATEWAY_ALLOWED_USERS"

# Where learned serviceUrls are persisted so an out-of-process or
# cold-cache cron delivery can recover them (see _standalone_send).
SERVICE_URL_STATE_ENV = "CETIRUN_TEAMS_SERVICE_URL_STATE"

# Bot Framework conversation IDs combine digits, letters, ':', '@', '.',
# '-', '_', plus the ';messageid=' reply-chain suffix Teams appends. Reject
# anything outside this set (notably '/') so a hostile chat_id cannot
# path-traverse out of /v3/conversations/<id>/activities.
CONV_ID_RE = re.compile(r"^[A-Za-z0-9:@._;=-]+\Z")

CONV_TYPE_TO_CHAT_TYPE = {
    "personal": "dm",
    "groupChat": "group",
}

# Outbound calls only go to Bot Framework / Teams service hosts.
TRUSTED_SERVICE_HOSTS = (
    ".botframework.com",
    ".trafficmanager.net",
    ".skype.com",
)


def _is_trusted_service_url(service_url: str) -> bool:
    try:
        parsed = urlparse(service_url)
    except ValueError:
        return False
    if parsed.scheme != "https" or not parsed.hostname:
        return False
    host = parsed.hostname.lower()
    return any(host == s.lstrip(".") or host.endswith(s) for s in TRUSTED_SERVICE_HOSTS)


def _channel_base(conversation_id: str) -> str:
    """Strip the ';messageid=...' reply-chain suffix Teams appends.

    All threads in a Teams channel share one serviceUrl, so the channel base
    is a stable key for serviceUrl lookup even when a cron job is pinned to a
    thread whose exact conversation_id was never (re)seen since startup.
    """
    return conversation_id.split(";messageid=", 1)[0]


def _remember_service_url(cache: "OrderedDict[str, str]", conversation_id: str,
                          service_url: str) -> None:
    """Store serviceUrl in an in-memory LRU under both the full conversation
    id and its channel base, evicting oldest entries past the cap."""
    for key in (conversation_id, _channel_base(conversation_id)):
        cache[key] = service_url
        cache.move_to_end(key)
    while len(cache) > SERVICE_URL_CACHE_MAX:
        cache.popitem(last=False)


def _service_url_state_path() -> str:
    explicit = os.environ.get(SERVICE_URL_STATE_ENV)
    if explicit:
        return explicit
    from hermes_constants import get_hermes_home

    return str(get_hermes_home() / "cetirun_teams_service_urls.json")


def _load_service_url_state() -> "OrderedDict[str, str]":
    path = _service_url_state_path()
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, ValueError):
        return OrderedDict()
    if not isinstance(data, dict):
        return OrderedDict()
    urls = data.get("service_urls")
    if not isinstance(urls, dict):
        return OrderedDict()
    return OrderedDict((k, v) for k, v in urls.items() if isinstance(v, str))


def _persist_service_url(conversation_id: str, service_url: str) -> None:
    """Record serviceUrl under both the full conversation id and its channel
    base, atomically, so a separate cron process can read it back.

    The whole read-modify-write runs under an exclusive file lock so two
    processes learning different conversations at once (e.g. an overlapping
    ``--replace`` gateway restart) merge rather than clobber each other.
    """
    if not conversation_id or not service_url:
        return

    path = _service_url_state_path()
    directory = os.path.dirname(path) or "."
    try:
        os.makedirs(directory, exist_ok=True)
    except OSError as e:
        logger.warning("Failed to persist Teams serviceUrl: %s", e)
        return

    lock_path = path + ".lock"
    try:
        with open(lock_path, "w", encoding="utf-8") as lock_f:
            try:
                fcntl.flock(lock_f, fcntl.LOCK_EX)
            except OSError:
                pass  # lock unavailable (e.g. some network FS) — proceed best-effort

            state = _load_service_url_state()
            for key in (conversation_id, _channel_base(conversation_id)):
                state.pop(key, None)  # re-insert at end to keep LRU order
                state[key] = service_url
            while len(state) > SERVICE_URL_CACHE_MAX:
                state.popitem(last=False)

            fd, tmp = tempfile.mkstemp(dir=directory, prefix=".svcurl-", suffix=".tmp")
            try:
                with os.fdopen(fd, "w", encoding="utf-8") as f:
                    json.dump({"service_urls": state}, f)
                os.replace(tmp, path)
            finally:
                if os.path.exists(tmp):
                    os.unlink(tmp)
    except OSError as e:
        logger.warning("Failed to persist Teams serviceUrl: %s", e)


def _resolve_service_url(
    chat_id: str, memory_cache: Optional[dict] = None
) -> Optional[str]:
    """Find a trusted serviceUrl for chat_id, trying exact id then channel
    base, in the in-memory cache first and the on-disk state second.

    The disk read happens only on a memory miss, so the outbound hot path
    (live adapter, cache populated from inbound webhooks) never touches disk.
    """
    base = _channel_base(chat_id)
    if memory_cache:
        for url in (memory_cache.get(chat_id), memory_cache.get(base)):
            if url and _is_trusted_service_url(url):
                return url
    state = _load_service_url_state()
    for url in (state.get(chat_id), state.get(base)):
        if url and _is_trusted_service_url(url):
            return url
    return None


async def _standalone_send(
    pconfig,
    chat_id: str,
    message: str,
    *,
    thread_id: Optional[str] = None,
    media_files: Optional[list] = None,
    force_document: bool = False,
) -> dict:
    """Acquire a Bot Framework token via workload identity and POST a single
    message activity, without a live in-process adapter.

    Registered as the plugin's ``standalone_sender_fn`` so ``deliver=cetirun_teams``
    cron jobs work when the gateway runner is not reachable in this process
    (e.g. cron running separately, or mid-restart). Without it those jobs fail
    with "No live adapter for platform 'cetirun_teams'".

    The serviceUrl is read from the state file learned from inbound webhook
    activities — never pinned via env — so any tenant works without operator
    configuration once the bot has received one message in the chat.

    ``media_files`` / ``force_document`` are accepted for signature parity but
    not implemented here; the live adapter handles attachments via the SDK.
    """
    if not chat_id:
        return {"error": "cetirun_teams standalone send: chat_id is required"}
    if not CONV_ID_RE.match(chat_id):
        return {
            "error": "cetirun_teams standalone send: chat_id contains characters "
            "outside the Bot Framework conversation ID set"
        }

    service_url = _resolve_service_url(chat_id)
    if not service_url:
        return {
            "error": "cetirun_teams standalone send: no serviceUrl learned yet for "
            f"'{chat_id}'. Send a message in the Teams chat so the bot can cache "
            "its serviceUrl, then retry."
        }

    if len(message) > MAX_MESSAGE_LENGTH:
        message = message[: MAX_MESSAGE_LENGTH - len(TRUNCATION_SUFFIX)] + TRUNCATION_SUFFIX

    try:
        token = await BotTokenProvider().get_token(BOT_FRAMEWORK_SCOPE)
    except Exception as e:
        return {"error": f"cetirun_teams standalone send: token acquisition failed: {e}"}

    url = f"{service_url.rstrip('/')}/v3/conversations/{chat_id}/activities"
    activity = {"type": "message", "text": message, "textFormat": "markdown"}
    try:
        async with httpx.AsyncClient(timeout=30) as client:
            resp = await client.post(
                url,
                json=activity,
                headers={
                    "Authorization": f"Bearer {token}",
                    "Content-Type": "application/json",
                },
            )
    except Exception as e:
        return {"error": f"cetirun_teams standalone send failed: {e}"}

    if resp.status_code in (200, 201):
        return {"success": True, "message_id": resp.json().get("id", "")}
    return {
        "error": f"cetirun_teams standalone send: HTTP {resp.status_code}: {resp.text[:200]}"
    }


def _load_allowlist() -> set[str]:
    raw = (os.environ.get(ALLOWED_USERS_ENV, "") + "," +
           os.environ.get(GLOBAL_ALLOWED_USERS_ENV, ""))
    return {entry.strip() for entry in raw.split(",") if entry.strip()}


class CetiRunTeamsAdapter(BasePlatformAdapter):
    def __init__(self, config):
        super().__init__(config, Platform("cetirun_teams"))
        extra = config.extra if hasattr(config, "extra") else {}
        self._app_id = os.environ.get("MSTEAMS_APP_ID") or extra.get("app_id", "")
        if not self._app_id:
            raise ValueError("MSTEAMS_APP_ID is required")
        self._tenant_id = os.environ.get("MSTEAMS_TENANT_ID") or extra.get("tenant_id")
        self._port = int(
            os.environ.get("MSTEAMS_WEBHOOK_PORT") or extra.get("port", 3978)
        )
        self._token_provider = BotTokenProvider()
        self._jwt_validator = InboundJwtValidator(self._app_id, self._tenant_id)
        self._service_urls: OrderedDict[str, str] = OrderedDict()
        self._runner: Optional[web.AppRunner] = None
        self._http: Optional[httpx.AsyncClient] = None

    @property
    def name(self) -> str:
        return "cetirun_teams"

    async def connect(self) -> bool:
        app = web.Application()
        app.router.add_get("/health", self._handle_health)
        app.router.add_post("/api/messages", self._handle_webhook)

        self._runner = web.AppRunner(app)
        await self._runner.setup()
        site = web.TCPSite(self._runner, "0.0.0.0", self._port)
        await site.start()
        self._http = httpx.AsyncClient(timeout=30)
        self._running = True
        logger.info("CetiRun Teams webhook listening on port %d", self._port)
        return True

    async def disconnect(self):
        if self._runner:
            await self._runner.cleanup()
            self._runner = None
        if self._http:
            await self._http.aclose()
            self._http = None
        self._running = False

    async def get_chat_info(self, chat_id: str) -> Optional[dict]:
        service_url = self._service_urls.get(chat_id)
        if not service_url:
            return None
        return {
            "chat_id": chat_id,
            "service_url": service_url,
            "platform": self.name,
        }

    async def _handle_health(self, request: web.Request) -> web.Response:
        return web.json_response({"status": "ok", "platform": "cetirun_teams"})

    async def _handle_webhook(self, request: web.Request) -> web.Response:
        auth_header = request.headers.get("Authorization", "")
        try:
            await self._jwt_validator.validate(auth_header)
        except Exception as e:
            logger.warning("JWT validation failed: %s", e)
            return web.Response(status=401, text="Unauthorized")

        try:
            activity = await request.json()
        except (ValueError, web.HTTPException):
            return web.Response(status=400, text="Invalid JSON")

        activity_type = activity.get("type", "")

        service_url = activity.get("serviceUrl", "")
        conversation_id = activity.get("conversation", {}).get("id", "")
        if service_url and conversation_id and _is_trusted_service_url(service_url):
            _remember_service_url(self._service_urls, conversation_id, service_url)
            # Persist so an out-of-process / cold-cache cron delivery can
            # recover the serviceUrl without a live adapter.
            _persist_service_url(conversation_id, service_url)
        elif service_url:
            logger.warning("Rejecting untrusted serviceUrl: %s", service_url)

        if activity_type == "message":
            await self._handle_message_activity(activity)
        elif activity_type == "invoke":
            return await self._handle_invoke(activity)

        return web.Response(status=200)

    async def _handle_message_activity(self, activity: dict):
        from_id = activity.get("from", {}).get("id", "")
        recipient_id = activity.get("recipient", {}).get("id", "")
        if from_id == recipient_id:
            return

        text = activity.get("text", "") or ""
        text = MENTION_PATTERN.sub("", text).strip()

        if not text and not activity.get("attachments"):
            return

        conversation = activity.get("conversation", {})
        conversation_id = conversation.get("id", "")
        sender_id = activity.get("from", {}).get("aadObjectId") or from_id
        sender_name = activity.get("from", {}).get("name", "")
        message_id = activity.get("id", "")

        allowlist = _load_allowlist()
        if allowlist and "*" not in allowlist and sender_id not in allowlist:
            logger.warning(
                "Denying unauthorized Teams user: %s (%s)", sender_id, sender_name
            )
            await self._send_unauthorized_reply(conversation_id, sender_id, sender_name)
            return

        conv_type = conversation.get("conversationType", "personal")
        chat_type = CONV_TYPE_TO_CHAT_TYPE.get(conv_type, "channel")

        source = self.build_source(
            chat_id=conversation_id,
            chat_type=chat_type,
            user_id=sender_id,
            user_name=sender_name,
        )

        event = MessageEvent(
            text=text,
            source=source,
            message_id=message_id,
        )
        await self.handle_message(event)

    async def _send_unauthorized_reply(
        self, conversation_id: str, sender_id: str, sender_name: str
    ):
        name = sender_name or "user"
        text = (
            f"Hi {name}, you're not authorized to use this bot.\n\n"
            f"Your AAD object ID is `{sender_id}`. Ask the bot owner to add it to "
            f"the `{ALLOWED_USERS_ENV}` env var on the CetiRun instance, then retry."
        )
        payload = {"type": "message", "text": text, "textFormat": "markdown"}
        try:
            await self._post_activity(conversation_id, payload)
        except Exception as e:
            logger.debug("Failed to send unauthorized reply: %s", e)

    async def _handle_invoke(self, activity: dict) -> web.Response:
        return web.json_response({"status": 200}, status=200)

    async def _post_activity(self, chat_id: str, payload: dict) -> Optional[httpx.Response]:
        if not chat_id or not CONV_ID_RE.match(chat_id):
            raise ValueError(f"malformed Teams chat_id: {chat_id!r}")
        service_url = _resolve_service_url(chat_id, self._service_urls)
        if not service_url or self._http is None:
            return None
        # Warm the in-memory cache when the hit came from disk, so subsequent
        # sends to this chat stay off the disk path until the next restart.
        if self._service_urls.get(chat_id) != service_url:
            _remember_service_url(self._service_urls, chat_id, service_url)
        token = await self._token_provider.get_token(BOT_FRAMEWORK_SCOPE)
        url = f"{service_url.rstrip('/')}/v3/conversations/{chat_id}/activities"
        headers = {
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        }
        return await self._http.post(url, json=payload, headers=headers)

    async def send(
        self,
        chat_id: str,
        content: str,
        reply_to: Optional[str] = None,
        **kwargs,
    ) -> SendResult:
        if len(content) > MAX_MESSAGE_LENGTH:
            content = content[: MAX_MESSAGE_LENGTH - len(TRUNCATION_SUFFIX)] + TRUNCATION_SUFFIX

        payload = {
            "type": "message",
            "text": content,
            "textFormat": "markdown",
        }
        if reply_to:
            payload["replyToId"] = reply_to

        try:
            resp = await self._post_activity(chat_id, payload)
        except Exception as e:
            logger.error("Teams send failed for %s: %s", chat_id, e)
            return SendResult(success=False, error=str(e))

        if resp is None:
            return SendResult(success=False, error="No cached service URL")
        if resp.status_code in (200, 201):
            data = resp.json()
            return SendResult(success=True, message_id=data.get("id", ""))
        error_msg = f"HTTP {resp.status_code}: {resp.text[:200]}"
        logger.error("Failed to send Teams message: %s", error_msg)
        return SendResult(success=False, error=error_msg)

    async def send_typing(self, chat_id: str):
        try:
            await self._post_activity(chat_id, {"type": "typing"})
        except Exception as e:
            logger.debug("Teams typing indicator failed for %s: %s", chat_id, e)
