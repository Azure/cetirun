# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Qualify stable authentication, explicit revocation and startup failure on a real gateway."""

import asyncio
from http.cookies import SimpleCookie
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time

from native_network import install

install()

import httpx
from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed, InvalidStatus

from hermes_cli.gateway_client import private_control_query
from plugins.dashboard_auth.basic import _sign, _unsign

SOURCE = Path(os.environ["HERMES_QUALIFICATION_SOURCE"])
PREFIX = "/ui/auth-fixture"
HOST = "auth.example.com"
HEADERS = {"Host": HOST, "Origin": "https://" + HOST, "X-Forwarded-Proto": "https", "X-Forwarded-Prefix": PREFIX}


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


class Gateway:
    def __init__(self, root):
        self.root = root
        self.home = root / "home/.hermes"
        self.home.mkdir(parents=True)
        self.web_port, self.api_port = free_port(), free_port()
        self.api_key = "synthetic-api-key-original-0123456789"
        self.signing_key = "synthetic-signing-key-original-0123456789"
        self.process = None
        self.boots = 0
        self.config = {
            "gateway": {"multiplex_profiles": False},
            "webapp": {"enabled": True, "host": "127.0.0.1", "port": self.web_port,
                       "public_url": "https://" + HOST + PREFIX},
            "model": {"provider": "cetirun-gateway", "default": "synthetic-model"},
            "providers": {"cetirun-gateway": {
                "name": "CetiRun AI Gateway", "api": "http://127.0.0.1:1/v1", "key_env": "CETIRUN_GATEWAY_API_KEY",
                "transport": "chat_completions", "default_model": "synthetic-model", "discover_models": False,
                "models": {"synthetic-model": {}},
            }},
            "auxiliary": {"title_generation": {"enabled": False}},
        }
        (self.home / ".env").write_text("")
        (self.home / "models_dev_cache.json").write_text('{"hermes-test-offline":{"env":[],"models":{}}}')

    def environment(self):
        env = {name: os.environ[name] for name in ("PATH", "LANG", "LC_ALL", "TMPDIR") if name in os.environ}
        env.update(
            HOME=str(self.home.parent), HERMES_HOME=str(self.home),
            PYTHONPATH=str(Path(__file__).parent) + os.pathsep + str(SOURCE),
            HERMES_RUNTIME_DIR=str(SOURCE / "tools"), HERMES_WEBAPP_DIST=str(SOURCE / "apps/desktop/dist-webapp"),
            PYTHONDONTWRITEBYTECODE="1", HERMES_DISABLE_LAZY_INSTALLS="1", AWS_EC2_METADATA_DISABLED="true", PYTHONUNBUFFERED="1",
            API_SERVER_ENABLED="true", API_SERVER_HOST="127.0.0.1", API_SERVER_PORT=str(self.api_port),
            API_SERVER_KEY=self.api_key, CETIRUN_AUTH_KEY=self.signing_key,
            HERMES_DASHBOARD_BASIC_AUTH_USERNAME="cetirun", HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=self.api_key,
            HERMES_DASHBOARD_BASIC_AUTH_SECRET=self.signing_key, CETIRUN_GATEWAY_API_KEY="synthetic-model-key",
        )
        return env

    def control(self, verb, timeout=1):
        request = json.dumps({"protocol": 1, "id": 1, "verb": verb, "params": {}}).encode() + b"\n"
        return json.loads(private_control_query(self.home, request, timeout=timeout))

    def spawn(self):
        (self.home / "config.yaml").write_text(json.dumps(self.config))
        self.boots += 1
        self.log = self.root / f"gateway-{self.boots}.log"
        with self.log.open("wb") as log:
            self.process = subprocess.Popen([sys.executable, "-m", "gateway.run"], cwd=SOURCE, env=self.environment(),
                                            stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                                            start_new_session=True)

    def start(self):
        self.spawn()
        deadline = time.monotonic() + 60
        while self.process.poll() is None and time.monotonic() < deadline:
            try:
                identity = self.control("identify").get("result", {})
                webapp = self.control("webapp-status").get("result", {})
                if identity.get("state") == "ready" and webapp.get("state") == "serving":
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.05)
        raise AssertionError("real gateway did not become ready: " + self.log.read_text()[-4000:])

    def stop(self):
        if self.process is None:
            return
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=10)
        self.process = None


async def ping(ws):
    await ws.send(json.dumps({"jsonrpc": "2.0", "id": "ping", "method": "gateway.ping"}))
    async with asyncio.timeout(5):
        while True:
            response = json.loads(await ws.recv())
            if response.get("id") == "ping":
                assert response.get("result", {}).get("ok") is True
                return


async def closed(ws):
    async with asyncio.timeout(5):
        try:
            while True:
                await ws.recv()
        except ConnectionClosed:
            return
    raise AssertionError("revoked browser socket stayed open")


async def qualify(gateway):
    origin = f"http://127.0.0.1:{gateway.web_port}"
    async with httpx.AsyncClient(base_url=origin, headers=HEADERS, timeout=10) as client:
        async def login(password):
            response = await client.post("/auth/password-login", json={
                "provider": "basic", "username": "cetirun", "password": password,
            })
            assert response.status_code == 200
            cookies = response.headers.get_list("set-cookie")
            assert cookies and all("Path=" + PREFIX in cookie for cookie in cookies)
            return "; ".join(cookie.split(";", 1)[0] for cookie in cookies)

        async def me(cookie):
            return await client.get("/api/auth/me", headers={"Cookie": cookie})

        async def mint(cookie):
            response = await client.post("/api/auth/ws-ticket", headers={"Cookie": cookie}, json={})
            assert response.status_code == 200
            return response.json()["ticket"]

        async def socket_for(ticket):
            return await connect("ws://" + HOST + "/api/ws?ticket=" + ticket,
                                 host="127.0.0.1", port=gateway.web_port,
                                 origin="https://" + HOST,
                                 additional_headers={"X-Forwarded-Proto": "https", "X-Forwarded-Prefix": PREFIX})

        async def api(key):
            async with httpx.AsyncClient(timeout=10) as native:
                response = await native.get(f"http://127.0.0.1:{gateway.api_port}/v1/models",
                                            headers={"Authorization": "Bearer " + key})
            return response.status_code

        initial_cookie = await login(gateway.api_key)
        me_status, api_status = (await me(initial_cookie)).status_code, await api(gateway.api_key)
        assert (me_status, api_status) == (200, 200), (me_status, api_status)
        original_credential = (gateway.home / "webapp/session-token").read_bytes()
        old_api_key = gateway.api_key
        live = await socket_for(await mint(initial_cookie))
        await ping(live)
        gateway.stop()
        await closed(live)
        gateway.start()
        assert (gateway.home / "webapp/session-token").read_bytes() == original_credential
        assert (await me(initial_cookie)).status_code == 200 and await api(old_api_key) == 200
        await login(old_api_key)

        expired = SimpleCookie(initial_cookie)
        secret = gateway.signing_key.encode()
        for suffix, kind in (("session_at_", "access"), ("session_rt_", "refresh")):
            name, cookie = next((name, cookie) for name, cookie in expired.items() if suffix in name)
            payload = _unsign(cookie.value, secret, kind)
            assert payload is not None
            expired[name] = _sign(payload | {"exp": int(time.time()) - 1}, secret)
        expired_header = "; ".join(cookie.OutputString() for cookie in expired.values())
        assert (await me(expired_header)).status_code == 401
        assert (await me(await login(old_api_key))).status_code == 200 and await api(old_api_key) == 200
        assert (gateway.home / "webapp/session-token").read_bytes() == original_credential

        live = await socket_for(await mint(initial_cookie))
        await ping(live)
        unused_ticket = await mint(initial_cookie)
        gateway.api_key = "synthetic-api-key-rotated-9876543210"
        gateway.signing_key = "synthetic-signing-key-rotated-9876543210"
        gateway.stop()
        await closed(live)
        gateway.start()
        assert (await me(initial_cookie)).status_code == 401
        assert await api(old_api_key) == 401 and await api(gateway.api_key) == 200
        invalid = await client.post("/auth/password-login", json={
            "provider": "basic", "username": "cetirun", "password": old_api_key,
        })
        assert invalid.status_code == 401
        try:
            stale = await socket_for(unused_ticket)
        except InvalidStatus as exc:
            assert exc.response.status_code in (401, 403)
        else:
            await stale.close()
            raise AssertionError("old WebSocket ticket survived key rotation")
        rotated_cookie = await login(gateway.api_key)
        assert (await me(rotated_cookie)).status_code == 200
        live = await socket_for(await mint(rotated_cookie))
        await ping(live)
        before_signout = (gateway.home / "webapp/session-token").read_bytes()
        signed_out = await asyncio.to_thread(gateway.control, "webapp-sign-out")
        assert signed_out.get("ok") is True
        await closed(live)
        assert gateway.process.poll() is None
        assert (gateway.home / "webapp/session-token").read_bytes() != before_signout
        assert (await me(rotated_cookie)).status_code == 401 and await api(gateway.api_key) == 200
        assert (await me(await login(gateway.api_key))).status_code == 200


def busy_port_refused(root):
    gateway = Gateway(root)
    with socket.socket() as blocker:
        blocker.bind(("127.0.0.1", gateway.web_port))
        blocker.listen()
        gateway.spawn()
        ready = False
        try:
            deadline = time.monotonic() + 20
            while gateway.process.poll() is None and time.monotonic() < deadline:
                try:
                    state = gateway.control("identify").get("result", {})
                    ready = ready or state.get("state") == "ready"
                except (OSError, ValueError):
                    pass
                time.sleep(0.02)
            assert gateway.process.poll() is not None and gateway.process.returncode != 0
            assert not ready
            assert "Webapp unavailable" in gateway.log.read_text()
        finally:
            gateway.stop()


def main():
    root = Path(os.environ["HERMES_QUALIFICATION_WORKDIR"])
    gateway = Gateway(root / "authentication")
    try:
        gateway.start()
        asyncio.run(qualify(gateway))
    finally:
        gateway.stop()
    busy_port_refused(root / "occupied-port")
    print(json.dumps({"restart_preserves_login": True, "expired_cookie_same_key_login": True, "key_rotation_revokes_auth": True,
                      "signout_revokes_browsers_without_restart": True, "occupied_port_fails_without_fallback": True}))


if __name__ == "__main__":
    main()
