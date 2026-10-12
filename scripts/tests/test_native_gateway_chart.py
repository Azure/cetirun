# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Render the protected OpenClaw/Gateway packaging with synthetic inputs only."""

import copy
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
RUNTIME_CHART = ROOT / "app/cetirun-chart"
GATEWAY_CHART = ROOT / "app/cetirun-ai-gateway/chart"
OFFLINE = {
    **os.environ,
    "KUBECONFIG": os.devnull,
    "HELM_REGISTRY_CONFIG": os.devnull,
    "HELM_REPOSITORY_CONFIG": os.devnull,
}
MODEL = {
    "model_name": "example-model",
    "litellm_params": {
        "model": "openai/example-model",
        "api_base": "https://models.example.com/v1",
        "api_key": "os.environ/EXAMPLE_MODEL_KEY",
    },
    "model_info": {"input_cost_per_token": 0.001, "access_groups": ["example-readers"]},
}


def render(chart, values, *, error=None):
    result = subprocess.run(
        [
            "helm", "template", "native-test", str(chart),
            "--namespace", "example-agent", "--kube-version", "1.37.0",
            "--values", "-",
        ],
        input=json.dumps(values), text=True, capture_output=True,
        env=OFFLINE, timeout=30, check=False,
    )
    if error is not None:
        assert result.returncode != 0
        assert error in result.stderr, result.stderr
        return None
    assert result.returncode == 0, result.stderr
    return [document for document in yaml.safe_load_all(result.stdout) if document]


def object_of_kind(objects, kind):
    return next(document for document in objects if document["kind"] == kind)


def native_values():
    security = {
        "runAsUser": 1024, "runAsNonRoot": True,
        "readOnlyRootFilesystem": True, "allowPrivilegeEscalation": False,
        "capabilities": {"drop": ["ALL"]},
        "seccompProfile": {"type": "RuntimeDefault"},
    }
    resources = {
        "requests": {"cpu": "250m", "memory": "512Mi", "ephemeral-storage": "1Gi"},
        "limits": {"cpu": "1", "memory": "1Gi", "ephemeral-storage": "2Gi"},
    }
    return {
        "agentType": "openclaw",
        "replicaCount": 0,
        "runtimeClassName": "example-kata",
        "sandboxWorkload": {
            "contractVersion": "v1", "executionHeld": True,
            "podResources": {
                "requests": {"cpu": "250m", "memory": "512Mi"},
                "limits": {"cpu": "1", "memory": "1Gi"},
            },
            "helperResources": resources,
            "securityContext": security,
            "scratchSizeLimit": "2Gi",
            "ownerReferences": [{
                "apiVersion": "cetirun.microsoft.com/v1alpha1", "kind": "Agent",
                "name": "example-agent", "uid": "example-agent-uid", "controller": True,
            }],
            "podLabels": {"cetirun.microsoft.com/agent-uid": "example-agent-uid"},
            "dnsConfig": {"nameservers": ["192.0.2.53"]},
            "identityVolumes": [], "identityMounts": [], "identityEnvironment": [],
            "objects": [{
                "apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
                "metadata": {"name": "example-compiler-policy"},
                "spec": {
                    "endpointSelector": {
                        "matchLabels": {"cetirun.microsoft.com/agent-uid": "example-agent-uid"},
                    },
                    "ingress": [], "egress": [],
                },
            }],
        },
        "podAnnotations": {"sidecar.istio.io/inject": "false"},
        "sidecarScope": {"enabled": False},
        "serviceAccount": {"automountToken": False},
        "securityContext": security,
        "resources": resources,
        "persistence": {"existingClaim": "example-home", "useStatefulSetVolumeClaim": False},
        "skills": {"shared": True, "existingClaim": "example-skills"},
        "desktop": {"enabled": False},
        "ttyd": {"enabled": False},
        "logger": {"enabled": False},
        "execRestriction": {"enabled": False},
        "openclaw": {
            "image": {"repository": "registry.example.com/openclaw-gateway", "tag": "example"},
            "config": {
                "browser": {"enabled": False},
                "auth": {"profiles": {"openai:personal": {"provider": "openai"}}},
                "models": {"providers": {"other": {"baseUrl": "https://other.example.com"}}},
            },
        },
        "aiGateway": {
            "enabled": True, "keySecretName": "example-model-key",
            "models": [{"id": "example-model", "name": "Example model"}],
        },
        "networkPolicy": {"enabled": True, "gatewayNamespace": "example-platform"},
        "secrets": {
            "existingSecret": "example-gateway-token",
            "provider": "openai", "model": "example-model",
            "apiBase": "http://gateway.example-platform.svc.cluster.local:4000/v1",
        },
    }


def hermes_native_values():
    values = native_values()
    values["agentType"] = "hermes"
    values["runtime"] = {
        "contractVersion": "v1",
        "agent": {
            "type": "hermes",
            "image": "registry.example.com/hermes:example",
            "username": "vibe",
            "home": "/home/vibe",
            "port": 8642,
            "webUI": False,
        },
        "profileID": "hermes:0.21.5",
        "configFiles": {"hermes-config.yaml": "model: example-model\n"},
        "ports": [
            {"name": "api", "number": 8642},
            {"name": "dashboard", "number": 9119},
        ],
        "http": {
            "portName": "api",
            "healthPath": "/health",
            "readinessPath": "/health",
            "startupPath": "/health",
        },
        "environment": [],
    }
    return values


def gateway_models(model, *, integrated):
    objects = render(GATEWAY_CHART, {"operatorIntegration": integrated, "models": [model]})
    config = yaml.safe_load(object_of_kind(objects, "ConfigMap")["data"]["config.yaml"])
    return config["model_list"]


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3 or 4")
class NativeGatewayChartTests(unittest.TestCase):
    def test_empty_skills_are_read_only_without_extra_storage_or_grants(self):
        values = native_values()
        values["sandboxWorkload"]["podFinalizers"] = ["cetirun.microsoft.com/native-pod-lifetime"]
        baseline = render(RUNTIME_CHART, values)
        values["sandboxWorkload"]["skillsMode"] = "empty"
        values["skills"] = {"shared": False, "existingClaim": ""}
        documents = render(RUNTIME_CHART, values)
        self.assertEqual(
            [(d["kind"], d["metadata"]["name"]) for d in documents],
            [(d["kind"], d["metadata"]["name"]) for d in baseline],
        )
        self.assertFalse(any(d["kind"] in ("PersistentVolume", "PersistentVolumeClaim") for d in documents))
        expected = copy.deepcopy(object_of_kind(baseline, "StatefulSet"))
        pod = expected["spec"]["template"]["spec"]
        volumes = pod["volumes"]
        index = next(i for i, volume in enumerate(volumes) if volume["name"] == "shared-skills")
        volumes[index] = {"name": "empty-skills", "emptyDir": {}}
        mounts = pod["containers"][0]["volumeMounts"]
        index = next(i for i, mount in enumerate(mounts) if mount["name"] == "shared-skills")
        mounts[index] = {"name": "empty-skills", "mountPath": "/home/vibe/.aks/skills", "readOnly": True}
        self.assertEqual(object_of_kind(documents, "StatefulSet"), expected)
        for kind in ("Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "ServiceAccount", "CiliumNetworkPolicy"):
            self.assertEqual(
                [d for d in documents if d["kind"] == kind],
                [d for d in baseline if d["kind"] == kind],
            )

    def test_empty_skills_reject_inconsistent_internal_values(self):
        for path, value, error in (
            (("sandboxWorkload", "skillsMode"), "unknown", "skillsMode"),
            (("sandboxWorkload", "skillsMode"), False, "skillsMode"),
            (("sandboxWorkload", "podFinalizers"), [], "podFinalizers"),
            (("skills", "shared"), True, "shared"),
            (("skills", "existingClaim"), "example-skills", "existingClaim"),
            (("skills", "npm"), ["example-package"], "npm"),
            (("skills", "clawHub"), ["example-skill"], "clawHub"),
            (("skills", "mountPath"), "/other-skills", "mountPath"),
            (("aiGateway", "enabled"), False, "enabled"),
            (("agentType",), "custom", "agentType"),
        ):
            with self.subTest(path=path, value=value):
                values = native_values()
                values["sandboxWorkload"].update({
                    "skillsMode": "empty",
                    "podFinalizers": ["cetirun.microsoft.com/native-pod-lifetime"],
                })
                values["skills"] = {"shared": False, "existingClaim": ""}
                target = values
                for key in path[:-1]:
                    target = target[key]
                target[path[-1]] = value
                render(RUNTIME_CHART, values, error=error)

    def test_native_probes_use_exec_without_changing_network_controls_or_timing(self):
        values = native_values()
        values["service"] = {"port": 18793, "targetPort": 18793}
        values["openclaw"]["config"]["gateway"] = {"port": 18793}
        paths = {"livenessProbe": "/healthz", "readinessProbe": "/readyz", "startupProbe": "/startupz"}
        for kind in paths:
            values[kind] = {
                "initialDelaySeconds": 2, "periodSeconds": 7, "timeoutSeconds": 3,
                "failureThreshold": 4, "successThreshold": 1,
            }
        objects = render(RUNTIME_CHART, values)
        pod = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]
        main = pod["containers"][0]
        for kind, path in paths.items():
            with self.subTest(probe=kind):
                self.assertEqual(main[kind], {
                    **values[kind],
                    "exec": {"command": ["node", "/etc/cetirun/health-probe.cjs", "18793", path, "3000"]},
                })
        self.assertEqual(object_of_kind(objects, "CiliumNetworkPolicy"), values["sandboxWorkload"]["objects"][0])
        data = object_of_kind(objects, "ConfigMap")["data"]
        self.assertEqual(data["health-probe.cjs"], (
            RUNTIME_CHART / "agents/openclaw/bootstrap/health-probe.cjs"
        ).read_text())

    def test_disabling_native_probes_does_not_render_a_replacement_probe(self):
        values = native_values()
        for kind in ("livenessProbe", "readinessProbe", "startupProbe"):
            values[kind] = {"enabled": False}
        objects = render(RUNTIME_CHART, values)
        main = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]["containers"][0]
        self.assertFalse(any(kind in main for kind in ("livenessProbe", "readinessProbe", "startupProbe")))

    def test_held_hermes_probes_use_resolved_runtime_over_loopback(self):
        values = hermes_native_values()
        objects = render(RUNTIME_CHART, values)
        main = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]["containers"][0]
        for kind in ("livenessProbe", "readinessProbe", "startupProbe"):
            with self.subTest(probe=kind):
                self.assertEqual(main[kind]["exec"]["command"][:4], [
                    "/usr/local/lib/hermes-agent/venv/bin/python",
                    "/etc/cetirun/hermes-health-probe.py",
                    "8642",
                    "/health",
                ])
                self.assertNotIn("httpGet", main[kind])
        data = object_of_kind(objects, "ConfigMap")["data"]
        self.assertIn("hermes-health-probe.py", data)
        self.assertNotIn("health-probe.cjs", data)

    def test_packaged_chart_contains_the_native_runtime_assets(self):
        values = native_values()
        expected = render(RUNTIME_CHART, values)
        with tempfile.TemporaryDirectory(prefix="cetirun-native-chart-") as directory:
            result = subprocess.run(
                ["helm", "package", str(RUNTIME_CHART), "--destination", directory],
                env=OFFLINE, text=True, capture_output=True, timeout=30, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            packages = list(Path(directory).glob("*.tgz"))
            self.assertEqual(len(packages), 1)
            self.assertEqual(render(packages[0], values), expected)

    def test_native_gateway_renders_without_an_istio_sidecar(self):
        values = native_values()
        objects = render(RUNTIME_CHART, values)
        workload = object_of_kind(objects, "StatefulSet")
        self.assertEqual(workload["spec"]["replicas"], 0)
        self.assertEqual(workload["metadata"]["ownerReferences"], values["sandboxWorkload"]["ownerReferences"])
        pod = workload["spec"]["template"]["spec"]
        self.assertEqual(pod["runtimeClassName"], "example-kata")
        self.assertIs(pod["automountServiceAccountToken"], False)
        self.assertEqual(pod["dnsPolicy"], "None")
        self.assertEqual(pod["dnsConfig"], {"nameservers": ["192.0.2.53"]})
        self.assertFalse(any(item["kind"] in {"Sidecar", "NetworkPolicy", "PersistentVolumeClaim"} for item in objects))
        self.assertEqual(object_of_kind(objects, "CiliumNetworkPolicy"), values["sandboxWorkload"]["objects"][0])
        main = pod["containers"][0]
        env = {item["name"]: item for item in main["env"]}
        self.assertEqual(env["OPENAI_API_KEY"]["valueFrom"]["secretKeyRef"], {
            "name": "example-model-key", "key": "api-key",
        })
        self.assertEqual(main["image"], "registry.example.com/openclaw-gateway:example")
        init = pod["initContainers"]
        self.assertEqual([item["name"] for item in init][-1], "retire-direct-model-auth")
        initializer = next(item for item in init if item["name"] == "init-home-data")
        self.assertIn({"name": "CETIRUN_NATIVE_SANDBOX", "value": "true"}, initializer["env"])
        for container in init:
            self.assertEqual(container["securityContext"], values["sandboxWorkload"]["securityContext"])
            self.assertEqual(container["resources"], values["sandboxWorkload"]["helperResources"])
        data = object_of_kind(objects, "ConfigMap")["data"]
        self.assertEqual(data["retire-direct-model-auth.sh"], (
            RUNTIME_CHART / "agents/openclaw/bootstrap/retire-direct-model-auth.sh"
        ).read_text())
        config = json.loads(data["openclaw.json"])
        self.assertEqual(config["auth"], {"profiles": {}})
        self.assertEqual(list(config["models"]["providers"]), ["openai"])
        self.assertEqual(config["models"]["providers"]["openai"]["apiKey"], {
            "source": "env", "provider": "default", "id": "OPENAI_API_KEY",
        })
        self.assertEqual(config["agents"]["defaults"]["modelPolicy"], {"allow": ["openai/example-model"]})

    def test_native_invalid_controls_are_not_silently_ignored(self):
        cases = [
            ("sidecarScope", "enabled", True, "Sandbox network controls are protected"),
            ("networkPolicy", "enabled", False, "Sandbox network controls are protected"),
            ("secrets", "apiBase", "", "Sandbox Gateway routing requires secrets.apiBase"),
            ("secrets", "model", "ungranted", "Sandbox default model must be in aiGateway.models"),
            ("aiGateway", "models", [], "Sandbox default model must be in aiGateway.models"),
            ("sandboxWorkload", "executionHeld", False, "executionHeld"),
        ]
        for section, key, value, message in cases:
            with self.subTest(section=section, key=key, value=value):
                values = native_values()
                values[section][key] = value
                render(RUNTIME_CHART, values, error=message)

    def test_native_packaging_does_not_release_execution(self):
        values = native_values()
        values["replicaCount"] = 1
        render(RUNTIME_CHART, values, error="Sandbox render plans must retain the execution hold")

    def test_legacy_gateway_keeps_its_existing_model_and_init_behavior(self):
        values = native_values()
        values["sandboxWorkload"] = None
        values["sidecarScope"]["enabled"] = True
        objects = render(RUNTIME_CHART, values)
        pod = object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]
        self.assertNotIn("retire-direct-model-auth", [item["name"] for item in pod["initContainers"]])
        self.assertFalse(any(
            variable["name"] == "CETIRUN_NATIVE_SANDBOX"
            for item in pod["initContainers"] for variable in item.get("env", [])
        ))
        config = json.loads(object_of_kind(objects, "ConfigMap")["data"]["openclaw.json"])
        self.assertIn("other", config["models"]["providers"])
        self.assertNotIn("health-probe.cjs", object_of_kind(objects, "ConfigMap")["data"])
        for kind, path in (
            ("livenessProbe", "/healthz"), ("readinessProbe", "/readyz"), ("startupProbe", "/startupz"),
        ):
            with self.subTest(probe=kind):
                self.assertEqual(pod["containers"][0][kind]["httpGet"], {"path": path, "port": "gateway"})
                self.assertNotIn("exec", pod["containers"][0][kind])
        values["sidecarScope"]["enabled"] = False
        render(RUNTIME_CHART, values, error="aiGateway.enabled requires sidecarScope.enabled")

    def test_integrated_gateway_fingerprints_the_complete_model_configuration(self):
        suffix = hashlib.sha256(
            json.dumps(MODEL, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()[:16]
        configured = gateway_models(MODEL, integrated=True)[0]
        self.assertEqual(configured["litellm_params"], MODEL["litellm_params"])
        self.assertEqual(configured["model_info"], {
            **MODEL["model_info"],
            "id": f"cetirun-shared-{suffix}",
            "access_groups": ["example-readers", f"cetirun-model-{suffix}"],
        })
        self.assertEqual(gateway_models(dict(reversed(list(MODEL.items()))), integrated=True), [configured])
        changed = copy.deepcopy(MODEL)
        changed["litellm_params"]["api_base"] = "https://replacement.example.com/v1"
        replacement = gateway_models(changed, integrated=True)[0]
        self.assertNotEqual(replacement["model_info"]["id"], configured["model_info"]["id"])
        self.assertNotEqual(replacement["model_info"]["access_groups"][-1], configured["model_info"]["access_groups"][-1])

    def test_only_integrated_gateway_reserves_model_bindings(self):
        for model_info in [
            {"id": "operator-cannot-trust-this"},
            {"id": ""},
            {"access_groups": ["cetirun-model-injected"]},
            {"access_groups": ["CetiRun-model-injected"]},
        ]:
            with self.subTest(model_info=model_info):
                model = {**MODEL, "model_info": model_info}
                render(
                    GATEWAY_CHART, {"operatorIntegration": True, "models": [model]},
                    error="reserved",
                )
                self.assertEqual(gateway_models(model, integrated=False), [model])

    def test_standalone_gateway_model_configuration_is_unchanged(self):
        self.assertEqual(gateway_models(MODEL, integrated=False), [MODEL])


@unittest.skipUnless(shutil.which("node"), "requires Node for the runtime health probe")
class NativeHealthProbeTests(unittest.TestCase):
    def setUp(self):
        self.status = 200
        self.stall = False
        self.requests = []
        self.release = threading.Event()
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                fixture.requests.append((self.path, self.headers))
                if fixture.stall:
                    fixture.release.wait(timeout=5)
                    return
                self.send_response(fixture.status)
                if fixture.status == 302:
                    self.send_header("Location", "/must-not-follow")
                self.send_header("Content-Length", "0")
                self.end_headers()

            def log_message(self, *_args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)

    def stop_server(self):
        self.release.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())

    def probe(self, path="/healthz", *, port=None, timeout=1000, args=None, env=None):
        if args is None:
            args = [str(port or self.server.server_port), path, str(timeout)]
        return subprocess.run(
            ["node", str(RUNTIME_CHART / "agents/openclaw/bootstrap/health-probe.cjs"), *args],
            env={**OFFLINE, "OPENAI_API_KEY": "sk-dummy", **(env or {})},
            text=True, capture_output=True, timeout=5, check=False,
        )

    def test_each_health_endpoint_uses_loopback_without_credentials(self):
        for path in ("/healthz", "/readyz", "/startupz"):
            with self.subTest(path=path):
                result = self.probe(path)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "")
                observed_path, headers = self.requests[-1]
                self.assertEqual(observed_path, path)
                self.assertEqual(headers["Host"], f"127.0.0.1:{self.server.server_port}")
                self.assertIsNone(headers["Authorization"])
                self.assertIsNone(headers["Cookie"])

    def test_http_status_controls_health_without_following_redirects(self):
        for status in (200, 204, 302, 399, 400, 401, 500, 503):
            with self.subTest(status=status):
                self.status = status
                count = len(self.requests)
                result = self.probe()
                self.assertEqual(result.returncode, 0 if 200 <= status < 400 else 1)
                self.assertEqual(len(self.requests), count + 1)
                if status >= 400:
                    self.assertIn(f"HTTP {status}", result.stderr)

    def test_connection_failure_is_unhealthy(self):
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            result = self.probe(port=reserved.getsockname()[1])
        self.assertEqual(result.returncode, 1)
        self.assertIn("connection failed", result.stderr)

    def test_request_deadline_is_bounded(self):
        self.stall = True
        started = time.monotonic()
        result = self.probe(timeout=100)
        elapsed = time.monotonic() - started
        self.assertEqual(result.returncode, 1)
        self.assertIn("request timed out", result.stderr)
        self.assertGreaterEqual(elapsed, 0.1)
        self.assertLess(elapsed, 4)

    def test_proxy_environment_cannot_redirect_the_probe(self):
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            proxy = f"http://127.0.0.1:{reserved.getsockname()[1]}"
            result = self.probe(env={
                "HTTP_PROXY": proxy, "HTTPS_PROXY": proxy, "ALL_PROXY": proxy,
                "http_proxy": proxy, "https_proxy": proxy, "all_proxy": proxy,
                "NO_PROXY": "", "no_proxy": "", "NODE_USE_ENV_PROXY": "1",
            })
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(self.requests), 1)
        self.assertEqual(self.requests[0][0], "/healthz")

    def test_invalid_arguments_fail_without_a_request(self):
        for args in (
            [], ["0", "/healthz", "1000"], ["65536", "/healthz", "1000"],
            ["not-a-port", "/healthz", "1000"], ["18789", "https://example.com", "1000"],
            ["18789", "/healthz", "0"], ["18789", "/healthz", "2147483648"],
            ["18789", "/healthz", "1000", "extra"],
        ):
            with self.subTest(args=args):
                result = self.probe(args=args)
                self.assertEqual(result.returncode, 1)
                self.assertIn("invalid probe arguments", result.stderr)
        self.assertEqual(self.requests, [])


if __name__ == "__main__":
    unittest.main()
