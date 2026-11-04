# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Install, enable, restart, disable and remove a mixed plugin through the real gateway."""

import asyncio
import json
import os
from pathlib import Path
import shutil
import subprocess

import httpx
from websockets.asyncio.client import connect

from hermes_auth_native import Gateway, HEADERS, HOST, PREFIX

PLUGIN_NAME = "cetirun-qualification"


class PersistentGateway(Gateway):
    def spawn(self):
        saved = self.home / "config.yaml"
        if saved.exists():
            from ruamel.yaml import YAML
            self.config = YAML(typ="safe").load(saved.read_text())
        super().spawn()


def plugin_repository(root):
    source = Path(__file__).with_name(PLUGIN_NAME)
    repository = root / PLUGIN_NAME
    shutil.copytree(source, repository)
    env = {name: os.environ[name] for name in ("PATH", "HOME", "LANG") if name in os.environ}
    env["GIT_CONFIG_NOSYSTEM"] = "1"
    for arguments in (
        ["init", "-b", "main"], ["add", "."],
        ["-c", "user.name=Synthetic Tester", "-c", "user.email=synthetic@example.com", "commit", "-m", "Synthetic plugin fixture"],
    ):
        subprocess.run(["git", "-C", str(repository), *arguments], env=env, check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    sha = subprocess.check_output(["git", "-C", str(repository), "rev-parse", "HEAD"], env=env, text=True).strip()
    return repository, sha


async def rpc(ws, **params):
    await ws.send(json.dumps({"jsonrpc": "2.0", "id": "plugins", "method": "plugins.manage", "params": params}))
    async with asyncio.timeout(90):
        while True:
            result = json.loads(await ws.recv())
            if result.get("id") == "plugins":
                assert "result" in result, result.get("error")
                return result["result"]


async def qualify(gateway, repository, sha):
    origin = f"http://127.0.0.1:{gateway.web_port}"
    async with httpx.AsyncClient(base_url=origin, headers=HEADERS, timeout=20) as client:
        response = await client.post("/auth/password-login", json={
            "provider": "basic", "username": "cetirun", "password": gateway.api_key,
        })
        response.raise_for_status()
        cookie = "; ".join(value.split(";", 1)[0] for value in response.headers.get_list("set-cookie"))
        client.headers["Cookie"] = cookie

        async def websocket():
            ticket = await client.post("/api/auth/ws-ticket", json={})
            ticket.raise_for_status()
            return await connect("ws://" + HOST + "/api/ws?ticket=" + ticket.json()["ticket"],
                                 host="127.0.0.1", port=gateway.web_port, origin="https://" + HOST,
                                 additional_headers={"X-Forwarded-Proto": "https", "X-Forwarded-Prefix": PREFIX})

        async def row(ws):
            listing = await rpc(ws, action="list")
            return next((item for item in listing["plugins"] if item["name"] == PLUGIN_NAME), None)

        async def command_loaded():
            response = await asyncio.to_thread(gateway.control, "reload-plugins", 15)
            assert response.get("ok")
            return any("qualification-status" in summary.get("activated_now", {}).get("gateway_commands", [])
                       for summary in response["result"].get("activations", []))

        ws = await websocket()
        inspected = await rpc(ws, action="inspect", identifier=repository.as_uri())
        assert inspected["ok"] and inspected["agent"] and inspected["desktop"] and inspected["sha"] == sha
        installed = await rpc(ws, action="install", identifier=repository.as_uri(), ref=sha, inspected_sha=sha, enable=True)
        assert installed["ok"] and installed["plugin_name"] == PLUGIN_NAME
        assert await command_loaded()
        current = await row(ws)
        assert current["status"] == "enabled", current
        key = current["key"]
        reconciled = await client.post("/api/desktop-plugins/reconcile")
        reconciled.raise_for_status()
        assert not reconciled.json().get("refused")
        folders = await client.get("/api/desktop-plugins/root")
        folders.raise_for_status()
        root = folders.json()["root"]
        installed_path = str(Path(root) / PLUGIN_NAME / "plugin.js")
        code = await client.get("/api/desktop-plugins/read", params={"path": installed_path})
        code.raise_for_status()
        assert "cetirun-qualification-plugin" in code.json()["text"] and not code.json()["truncated"]
        marker = await client.get("/api/desktop-plugins/read", params={"path": str(Path(root) / PLUGIN_NAME / ".hermes-package.json")})
        marker.raise_for_status()
        assert json.loads(marker.json()["text"])["package"] == PLUGIN_NAME
        await ws.close()

        gateway.stop()
        gateway.start()
        ws = await websocket()
        assert (await row(ws))["status"] == "enabled"
        assert await command_loaded()
        retained = await client.get("/api/desktop-plugins/read", params={"path": installed_path})
        retained.raise_for_status()
        assert retained.json()["text"] == code.json()["text"]
        disabled = await rpc(ws, action="toggle", key=key, enable=False)
        assert disabled["ok"] and (await row(ws))["status"] == "disabled"
        assert not await command_loaded()
        await ws.close()

        gateway.stop()
        gateway.start()
        ws = await websocket()
        assert (await row(ws))["status"] == "disabled"
        assert not await command_loaded()
        enabled = await rpc(ws, action="toggle", key=key, enable=True)
        assert enabled["ok"] and (await row(ws))["status"] == "enabled"
        assert await command_loaded()
        removed = await rpc(ws, action="remove", name=key)
        assert removed["ok"]
        assert await row(ws) is None
        assert not await command_loaded()
        reconciled = await client.post("/api/desktop-plugins/reconcile")
        reconciled.raise_for_status()
        missing = await client.get("/api/desktop-plugins/read", params={"path": installed_path})
        assert missing.status_code == 404
        assert not (gateway.home / "plugins" / PLUGIN_NAME).exists()
        assert gateway.config["providers"]["cetirun-gateway"]["key_env"] == "CETIRUN_GATEWAY_API_KEY"
        await ws.close()


def main():
    root = Path(os.environ["HERMES_QUALIFICATION_WORKDIR"])
    repository, sha = plugin_repository(root)
    gateway = PersistentGateway(root / "gateway")
    try:
        gateway.start()
        asyncio.run(qualify(gateway, repository, sha))
    finally:
        gateway.stop()
    print(json.dumps({"pinned_mixed_plugin_install": True, "desktop_half_served": True,
                      "enabled_and_disabled_survive_restart": True, "remove_prunes_desktop_half": True}))


if __name__ == "__main__":
    main()
