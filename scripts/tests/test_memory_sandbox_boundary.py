# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Memory wiring follows the common executor while retained lifetimes stay unchanged."""

import copy
import json
import shutil
import unittest

import yaml

from test_native_gateway_chart import RUNTIME_CHART, native_values, object_of_kind, render


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class MemorySandboxBoundaryTests(unittest.TestCase):
    def test_removed_memory_assets_switch_is_not_accepted_by_protected_schema(self):
        schema = json.loads((RUNTIME_CHART / "values.schema.json").read_text())
        self.assertNotIn("memoryAssets", schema["properties"]["sandboxWorkload"]["properties"])
        self.assertEqual(schema["properties"]["skills"]["properties"]["mountPath"]["default"], "/home/vibe/.aks/skills")

    def test_protected_packaging_does_not_inherit_memory_defaults(self):
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                values = native_values()
                values["agentType"] = agent_type
                values["memoryContext"] = {"enabled": True, "image": "registry.example.com/memory:1.2.3"}
                enabled = render(RUNTIME_CHART, values)
                disabled = copy.deepcopy(values)
                disabled["memoryContext"]["enabled"] = False
                self.assertEqual(enabled, render(RUNTIME_CHART, disabled))
                pod = object_of_kind(enabled, "StatefulSet")["spec"]["template"]["spec"]
                self.assertNotIn("memory-assets", [c["name"] for c in pod["initContainers"]])
                self.assertTrue({v["name"] for v in pod["volumes"]}.isdisjoint({"memory-assets", "context-view"}))
                self.assertIn(
                    {"name": "shared-skills", "mountPath": "/home/vibe/.aks/skills", "readOnly": True},
                    pod["containers"][0]["volumeMounts"],
                )
                if agent_type == "hermes":
                    config = object_of_kind(enabled, "ConfigMap")["data"]["hermes-config.yaml"]
                    self.assertNotIn("/home/vibe/.agents/skills", config)

    def test_reconciled_openclaw_registers_the_installed_memory_adapter(self):
        for enabled in (False, True):
            with self.subTest(enabled=enabled):
                values = native_values()
                values["agentType"] = "openclaw"
                values["sandboxWorkload"]["reconciled"] = True
                values["sandboxWorkload"]["executionHeld"] = False
                values["memoryContext"] = {"enabled": enabled, "image": "registry.example.com/memory:1.2.3"}
                objects = render(RUNTIME_CHART, values)
                pod = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]
                config = json.loads(object_of_kind(objects, "ConfigMap")["data"]["openclaw.json"])
                plugin = config.get("plugins", {}).get("entries", {}).get("cetirun-memory-context", {})
                self.assertEqual(plugin.get("enabled", False), enabled)
                self.assertEqual(plugin.get("hooks", {}).get("allowConversationAccess", False), enabled)
                self.assertEqual(
                    config.get("plugins", {}).get("load", {}).get("paths", []).count("/opt/cetirun-memory/adapters/openclaw"),
                    int(enabled),
                )
                self.assertEqual(
                    config.get("plugins", {}).get("allow", []).count("cetirun-memory-context"), int(enabled),
                )
                self.assertEqual(
                    config.get("skills", {}).get("load", {}).get("extraDirs", []).count("/home/vibe/.agents/skills"),
                    int(enabled),
                )
                self.assertEqual("memory-assets" in [c["name"] for c in pod["initContainers"]], enabled)

    def test_reconciled_hermes_enables_the_installed_memory_plugin(self):
        for enabled in (False, True):
            for shared in (False, True):
                with self.subTest(enabled=enabled, shared=shared):
                    values = native_values()
                    values["agentType"] = "hermes"
                    values["skills"]["shared"] = shared
                    values["sandboxWorkload"].update(reconciled=True, executionHeld=False)
                    values["memoryContext"] = {"enabled": enabled, "image": "registry.example.com/memory:1.2.3"}
                    objects = render(RUNTIME_CHART, values)
                    pod = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]
                    config = yaml.safe_load(object_of_kind(objects, "ConfigMap")["data"]["hermes-config.yaml"])
                    self.assertEqual(
                        config.get("plugins", {}).get("enabled", []).count("cetirun-memory-context"), int(enabled),
                    )
                    self.assertEqual(
                        config.get("skills", {}).get("external_dirs", []).count("/home/vibe/.agents/skills"), int(enabled),
                    )
                    self.assertEqual("memory-assets" in [c["name"] for c in pod["initContainers"]], enabled)

    def test_ordinary_packaging_keeps_memory_enabled_by_default(self):
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                values = {
                    "agentType": agent_type,
                    "memoryContext": {"image": "registry.example.com/memory:1.2.3"},
                    "desktop": {"enabled": False},
                    "openclaw": {"config": {"browser": {"enabled": False}}},
                    "secrets": {"provider": "openai", "model": "example-model", "existingSecret": "synthetic-agent-secret", "apiBase": "http://gateway.example.com/v1"},
                    "aiGateway": {"enabled": True, "keySecretName": "synthetic-model-key"},
                }
                objects = render(RUNTIME_CHART, values)
                pod = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]
                self.assertEqual(pod["initContainers"][0]["name"], "memory-assets")
                self.assertIn(
                    {"name": "memory-assets", "mountPath": "/opt/cetirun-memory", "readOnly": True},
                    pod["containers"][0]["volumeMounts"],
                )


if __name__ == "__main__":
    unittest.main()
