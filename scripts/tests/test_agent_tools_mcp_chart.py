# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline contract tests for the standalone Agent Tools MCP Helm chart."""

import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/agent-tools-mcp/chart"
OFFLINE = {
    **os.environ,
    "KUBECONFIG": os.devnull,
    "HELM_REGISTRY_CONFIG": os.devnull,
    "HELM_REPOSITORY_CONFIG": os.devnull,
}

VALUES = {
    "tls": {"secretName": "synthetic-mcp-server-tls"},
    "broker": {
        "baseURL": "https://cetirun-agent-broker.cetirun-credentials.svc.cluster.local",
        "clientTLSSecretName": "synthetic-mcp-broker-client-tls",
    },
    "portal": {"baseURL": "https://portal.example.invalid"},
    "workloadTokenAudience": "agent-tools-mcp",
    "istio": {
        "enabled": True,
        "sidecarInjection": True,
        "revision": "asm-synthetic",
        "brokerNamespace": "cetirun-credentials",
        "controlPlaneNamespace": "aks-istio-system",
        "agentNamespaces": ["cetirun-agent-synthetic"],
    },
}


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3 or 4")
class AgentToolsMCPChartTests(unittest.TestCase):
    def helm(self, values=None, *, namespace="cetirun-tools", lint=False):
        command = ["helm", "lint", "--strict", str(CHART)] if lint else [
            "helm", "template", "agent-tools-mcp-test", str(CHART),
            "--namespace", namespace, "--kube-version", "1.37.0",
        ]
        return subprocess.run(
            [*command, "--values", "-"],
            cwd=ROOT,
            env=OFFLINE,
            input=json.dumps(VALUES if values is None else values),
            text=True,
            capture_output=True,
            timeout=30,
            check=False,
        )

    def render(self, values=None, *, namespace="cetirun-tools"):
        lint = self.helm(values, namespace=namespace, lint=True)
        self.assertEqual(lint.returncode, 0, lint.stdout + lint.stderr)
        result = self.helm(values, namespace=namespace)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return [document for document in yaml.safe_load_all(result.stdout) if document]

    def reject(self, values=None, *, namespace="cetirun-tools", message):
        result = self.helm(values, namespace=namespace)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(message, result.stderr)

    @staticmethod
    def document(objects, kind, name="agent-tools-mcp"):
        return next(
            document for document in objects
            if document["kind"] == kind and document["metadata"]["name"] == name
        )

    @staticmethod
    def update(values, path, value):
        updated = copy.deepcopy(values)
        current = updated
        for part in path[:-1]:
            current = current.setdefault(part, {})
        current[path[-1]] = value
        return updated

    def test_standalone_chart_exposes_only_its_https_mcp_workload(self):
        objects = self.render()
        deployments = [document for document in objects if document["kind"] == "Deployment"]
        self.assertEqual([deployment["metadata"]["name"] for deployment in deployments], ["agent-tools-mcp"])
        self.assertFalse(any(
            document["kind"] in {"StatefulSet", "DaemonSet", "Job", "CronJob"}
            for document in objects
        ))

        service = self.document(objects, "Service")
        self.assertEqual(service["spec"]["ports"], [{"name": "https", "port": 443, "targetPort": "https"}])
        deployment = deployments[0]
        container = deployment["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual(container["ports"], [{"name": "https", "containerPort": 8443}])
        for probe in (container["readinessProbe"], container["livenessProbe"]):
            self.assertEqual(probe["httpGet"]["scheme"], "HTTPS")
            self.assertEqual(probe["httpGet"]["port"], "https")

        peer_authentication = self.document(objects, "PeerAuthentication")
        self.assertEqual(peer_authentication["spec"]["mtls"], {"mode": "STRICT"})
        authorization = self.document(objects, "AuthorizationPolicy")
        self.assertEqual(authorization["spec"]["rules"][0]["to"], [{"operation": {"ports": ["8443"]}}])

    def test_mcp_and_broker_credentials_are_mounted_as_distinct_read_only_material(self):
        objects = self.render()
        pod = self.document(objects, "Deployment")["spec"]["template"]["spec"]
        container = pod["containers"][0]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertEqual(
            {mount["name"]: mount for mount in container["volumeMounts"]},
            {
                "tls": {"name": "tls", "mountPath": "/var/run/cetirun/tls", "readOnly": True},
                "broker-tls": {"name": "broker-tls", "mountPath": "/var/run/cetirun/broker-tls", "readOnly": True},
                "kubernetes-api-credential": {"name": "kubernetes-api-credential", "mountPath": "/var/run/cetirun/kubernetes", "readOnly": True},
            },
        )
        volumes = {volume["name"]: volume for volume in pod["volumes"]}
        self.assertEqual(volumes["tls"]["secret"]["secretName"], "synthetic-mcp-server-tls")
        self.assertEqual(volumes["broker-tls"]["secret"]["secretName"], "synthetic-mcp-broker-client-tls")
        self.assertNotIn("subPath", json.dumps(container["volumeMounts"]))
        projected = volumes["kubernetes-api-credential"]["projected"]
        self.assertEqual(projected["defaultMode"], 0o440)
        self.assertEqual(projected["sources"][0]["serviceAccountToken"], {"path": "token", "expirationSeconds": 3600})
        self.assertEqual(projected["sources"][1]["configMap"]["name"], "kube-root-ca.crt")

        env = {item["name"]: item.get("value") for item in container["env"]}
        self.assertEqual(env["LISTEN_ADDRESS"], ":8443")
        self.assertEqual(env["AGENT_BROKER_BASE_URL"], VALUES["broker"]["baseURL"])
        self.assertEqual(env["TLS_CERT_FILE"], "/var/run/cetirun/tls/tls.crt")
        self.assertEqual(env["TLS_KEY_FILE"], "/var/run/cetirun/tls/tls.key")
        self.assertEqual(env["BROKER_TLS_CA_FILE"], "/var/run/cetirun/broker-tls/ca.crt")
        self.assertEqual(env["BROKER_TLS_CERT_FILE"], "/var/run/cetirun/broker-tls/tls.crt")
        self.assertEqual(env["BROKER_TLS_KEY_FILE"], "/var/run/cetirun/broker-tls/tls.key")

    def test_rbac_is_limited_to_token_review_and_named_workload_reads(self):
        objects = self.render()
        self.assertEqual([document for document in objects if document["kind"] == "Role"], [])
        role = self.document(objects, "ClusterRole")
        self.assertEqual(role["rules"], [
            {"apiGroups": ["authentication.k8s.io"], "resources": ["tokenreviews"], "verbs": ["create"]},
            {"apiGroups": [""], "resources": ["pods", "serviceaccounts", "namespaces"], "verbs": ["get"]},
            {"apiGroups": ["cetirun.microsoft.com"], "resources": ["agents"], "verbs": ["get"]},
        ])
        for document in objects:
            if document["kind"] in {"Role", "ClusterRole"}:
                self.assertFalse(any(
                    "secrets" in rule.get("resources", [])
                    for rule in document["rules"]
                ))
        binding = self.document(objects, "ClusterRoleBinding")
        self.assertEqual(binding["roleRef"], {
            "apiGroup": "rbac.authorization.k8s.io",
            "kind": "ClusterRole",
            "name": "agent-tools-mcp",
        })
        self.assertEqual(binding["subjects"], [{
            "kind": "ServiceAccount", "name": "agent-tools-mcp", "namespace": "cetirun-tools",
        }])

    def test_required_trust_and_namespace_boundaries_reject_invalid_values(self):
        cases = (
            (("tls", "secretName"), "", "tls.secretName is required", "cetirun-tools"),
            (("broker", "baseURL"), "http://broker.example.invalid", "broker.baseURL must use HTTPS", "cetirun-tools"),
            (("broker", "clientTLSSecretName"), "", "broker.clientTLSSecretName is required", "cetirun-tools"),
            (("portal", "baseURL"), "http://portal.example.invalid", "portal.baseURL must use HTTPS", "cetirun-tools"),
            (("workloadTokenAudience",), "", "workloadTokenAudience is required", "cetirun-tools"),
            (("serviceAccount", "create"), False, "dedicated ServiceAccount", "cetirun-tools"),
            (("istio", "enabled"), False, "requires Istio", "cetirun-tools"),
            (("istio", "sidecarInjection"), False, "sidecarInjection must be true", "cetirun-tools"),
            (("istio", "revision"), "", "istio.revision is required", "cetirun-tools"),
            (("service", "port"), 80, "service.port must be 443", "cetirun-tools"),
        )
        for path, value, message, namespace in cases:
            with self.subTest(path=".".join(path), value=value):
                self.reject(self.update(VALUES, path, value), namespace=namespace, message=message)
        self.reject(namespace="cetirun-credentials", message="require distinct namespaces")


if __name__ == "__main__":
    unittest.main()
