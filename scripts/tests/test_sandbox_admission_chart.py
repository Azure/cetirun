# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline Helm packaging checks; CEL behavior is covered by operator integration tests."""

import base64
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/cetirun-operator/chart"
NAME = "cetirun-sandbox-admission"
API_GROUP = "cetirun.microsoft.com"
RESOURCES = ["agents", "teams", "sandboxes", "agentpolicies"]
FINALIZERS = [
    f"{API_GROUP}/finalizer",
    f"{API_GROUP}/sandbox-hold",
    f"{API_GROUP}/custom-runtime-ownership",
    f"{API_GROUP}/custom-apply-completion",
]
OFFLINE_ENV = {
    **os.environ,
    "KUBECONFIG": os.devnull,
    "HELM_REGISTRY_CONFIG": os.devnull,
    "HELM_REPOSITORY_CONFIG": os.devnull,
}


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3")
class SandboxAdmissionChartTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        version = subprocess.run(
            ["helm", "version", "--short"], cwd=ROOT, env=OFFLINE_ENV,
            text=True, capture_output=True, check=True, timeout=30,
        ).stdout
        if not version.startswith("v3."):
            raise RuntimeError("Sandbox chart tests require Helm 3 on PATH")
        if shutil.which("openssl") is None:
            raise RuntimeError("Sandbox chart tests require OpenSSL for synthetic CA fixtures")
        # The synthetic key and certificate stay in memory, never in repository files.
        key = subprocess.run(
            ["openssl", "genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256"],
            cwd=ROOT, text=True, capture_output=True, check=True, timeout=30,
        ).stdout
        cls.ca_pem = subprocess.run(
            [
                "openssl", "req", "-new", "-x509", "-key", "/dev/stdin",
                "-subj", "/CN=sandbox-admission.example.com", "-days", "1",
                "-addext", "basicConstraints=critical,CA:TRUE",
            ],
            cwd=ROOT, input=key, text=True, capture_output=True, check=True, timeout=30,
        ).stdout
        cls.ca_bundle = cls.encode_ca(cls.ca_pem)

    @staticmethod
    def encode_ca(pem):
        return base64.b64encode(pem.encode()).decode()

    def server_values(self, **overrides):
        return {
            "enabled": True,
            "tlsSecretName": "synthetic-admission-tls",
            "adminUsers": ["admin@example.com"],
            **overrides,
        }

    def enforced_values(self, **overrides):
        return self.server_values(enforce=True, caBundle=self.ca_bundle, **overrides)

    def helm(self, admission=None, *, values=None, kube_version="1.30.0", lint=False):
        inputs = dict(values or {})
        if admission is not None:
            inputs["sandboxAdmission"] = admission
        command = (
            ["helm", "lint", "--strict", str(CHART)]
            if lint else ["helm", "template", "admission-test", str(CHART)]
        )
        return subprocess.run(
            [
                *command, "--namespace", "synthetic-release-namespace",
                "--kube-version", kube_version, "--values", "-",
            ],
            cwd=ROOT, env=OFFLINE_ENV, input=json.dumps(inputs),
            text=True, capture_output=True, timeout=30,
        )

    def render(self, admission=None, **kwargs):
        result = self.helm(admission, **kwargs)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return {
            (document["kind"], document["metadata"]["name"]): document
            for document in yaml.safe_load_all(result.stdout) if document
        }

    def assert_rejected(self, admission, message, **kwargs):
        result = self.helm(admission, **kwargs)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(message, result.stderr)

    def test_default_and_disabled_render_only_existing_operator_resources(self):
        default = self.render(kube_version="1.29.0")
        self.assertEqual(set(default), {
            ("Deployment", "cetirun-operator"),
            ("ServiceAccount", "cetirun-operator-controller-manager"),
            ("ClusterRole", "cetirun-operator-manager-role"),
            ("ClusterRoleBinding", "cetirun-operator-manager-rolebinding"),
        })
        disabled = self.render(
            {
                "enabled": False, "enforce": False, "replicaCount": 1,
                "tlsSecretName": "", "caBundle": "unused",
                "adminUsers": [], "adminGroups": [],
            },
            kube_version="1.29.0",
        )
        self.assertEqual(disabled, default)

    def test_bootstrap_deploys_server_without_webhooks_or_certificate_issuance(self):
        default = self.render(kube_version="1.29.0")
        bootstrap = self.render(self.server_values(), kube_version="1.29.0")
        for identity, document in default.items():
            self.assertEqual(bootstrap[identity], document)
        self.assertEqual(set(bootstrap) - set(default), {
            (kind, NAME) for kind in (
                "Deployment", "ServiceAccount", "Service", "ClusterRole",
                "ClusterRoleBinding", "PodDisruptionBudget",
            )
        })

    def test_read_only_rbac_uses_dedicated_service_account(self):
        documents = self.render(self.server_values())
        account = documents[("ServiceAccount", NAME)]
        self.assertNotIn("annotations", account["metadata"])
        self.assertEqual(account["metadata"]["namespace"], "cetirun-system")
        role = documents[("ClusterRole", NAME)]
        self.assertEqual(role["rules"], [{
            "apiGroups": [API_GROUP], "resources": RESOURCES, "verbs": ["get", "list"],
        }])
        binding = documents[("ClusterRoleBinding", NAME)]
        self.assertEqual(binding["roleRef"], {
            "apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": NAME,
        })
        self.assertEqual(binding["subjects"], [{
            "kind": "ServiceAccount", "name": NAME, "namespace": "cetirun-system",
        }])
        for identity, document in documents.items():
            if identity[0] == "ClusterRoleBinding" and identity[1] != NAME:
                self.assertNotIn(NAME, [subject["name"] for subject in document["subjects"]])

    def test_deployment_contract_tls_and_security_contexts(self):
        documents = self.render(self.server_values())
        deployment = documents[("Deployment", NAME)]
        pod = deployment["spec"]["template"]["spec"]
        self.assertEqual(pod["serviceAccountName"], NAME)
        self.assertTrue(pod["automountServiceAccountToken"])
        self.assertFalse(pod["enableServiceLinks"])
        self.assertEqual(len(pod["containers"]), 1)
        self.assertNotIn("initContainers", pod)
        self.assertNotIn("hostNetwork", pod)
        self.assertNotIn("hostPID", pod)
        container = pod["containers"][0]
        controller = documents[("Deployment", "cetirun-operator")]["spec"]["template"]["spec"]["containers"][0]
        self.assertEqual(container["image"], controller["image"])
        self.assertEqual(container["args"], [
            "--mode=sandbox-admission",
            "--admission-cert-dir=/var/run/cetirun-admission",
            "--admission-port=9443",
            "--health-probe-bind-address=:8081",
            "--metrics-bind-address=:8080",
            "--leader-elect=false",
        ])
        self.assertEqual([entry["name"] for entry in container["env"]], ["CETIRUN_SANDBOX_ADMISSION"])
        self.assertEqual(json.loads(container["env"][0]["value"]), {
            "adminUsers": ["admin@example.com"],
            "adminGroups": [],
            "controllerUsername": "system:serviceaccount:cetirun-system:cetirun-operator-controller-manager",
        })
        self.assertNotIn("envFrom", container)
        self.assertEqual(container["volumeMounts"], [{
            "name": "admission-tls", "mountPath": "/var/run/cetirun-admission", "readOnly": True,
        }])
        self.assertEqual(pod["volumes"], [{
            "name": "admission-tls",
            "secret": {
                "secretName": "synthetic-admission-tls",
                "defaultMode": 0o440,
                "items": [{"key": name, "path": name} for name in ("tls.crt", "tls.key")],
            },
        }])
        self.assertEqual(pod["securityContext"], {
            "runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532,
            "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"},
        })
        self.assertEqual(container["securityContext"], {
            "allowPrivilegeEscalation": False,
            "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]},
        })
        self.assertEqual(container["resources"], {
            "requests": {"cpu": "100m", "memory": "128Mi"},
            "limits": {"cpu": "500m", "memory": "256Mi"},
        })
        self.assertEqual(
            {port["name"]: port["containerPort"] for port in container["ports"]},
            {"admission": 9443, "health": 8081, "metrics": 8080},
        )
        for probe, path in (("livenessProbe", "/healthz"), ("readinessProbe", "/readyz")):
            self.assertEqual(container[probe]["httpGet"], {"path": path, "port": "health"})
        service = documents[("Service", NAME)]["spec"]
        self.assertEqual(service["type"], "ClusterIP")
        self.assertEqual(service["selector"], deployment["spec"]["selector"]["matchLabels"])
        self.assertEqual(service["ports"], [{
            "name": "admission", "port": 443, "targetPort": "admission", "protocol": "TCP",
        }])

    def test_high_availability_selectors_and_rollouts(self):
        for replicas in (2, 3):
            with self.subTest(replicas=replicas):
                documents = self.render(self.server_values(replicaCount=replicas))
                deployment = documents[("Deployment", NAME)]["spec"]
                self.assertEqual(deployment["replicas"], replicas)
                self.assertEqual(deployment["strategy"], {
                    "type": "RollingUpdate", "rollingUpdate": {"maxUnavailable": 0, "maxSurge": 1},
                })
                selector = deployment["selector"]
                self.assertEqual(selector, {"matchLabels": {"app.kubernetes.io/name": NAME}})
                for key, value in selector["matchLabels"].items():
                    self.assertEqual(deployment["template"]["metadata"]["labels"][key], value)
                pdb = documents[("PodDisruptionBudget", NAME)]["spec"]
                self.assertEqual(pdb, {"minAvailable": 1, "selector": selector})
                spreads = deployment["template"]["spec"]["topologySpreadConstraints"]
                self.assertEqual(
                    {spread["topologyKey"] for spread in spreads},
                    {"kubernetes.io/hostname", "topology.kubernetes.io/zone"},
                )
                for spread in spreads:
                    self.assertEqual(spread["maxSkew"], 1)
                    self.assertEqual(spread["labelSelector"], selector)
                self.assertEqual(spreads[0]["whenUnsatisfiable"], "DoNotSchedule")

    def test_placement_and_image_overrides_do_not_import_controller_credentials(self):
        values = {
            "namespace": "synthetic-platform",
            "serviceAccount": {
                "name": "synthetic-controller", "create": False,
                "annotations": {"example.com/controller-only": "true"},
            },
            "image": {
                "repository": "registry.example.com/unused", "tag": "unused",
                "digest": "sha256:" + "1" * 64, "pullPolicy": "Always",
            },
            "global": {"registry": "registry.example.com/runtime", "version": "2.0.0"},
            "imagePullSecrets": [{"name": "synthetic-image-pull"}],
            "acrDockerSecret": "synthetic-controller-registry",
            "azure": {"clientId": "synthetic-controller-client", "tenantId": "synthetic-tenant"},
            "agentIdentity": {
                "enabled": True, "tenantId": "synthetic-tenant",
                "blueprintClientId": "synthetic-blueprint",
            },
            "customRuntime": {"enabled": True},
            "podAnnotations": {
                "azure.workload.identity/inject-proxy-sidecar": "true",
                "example.com/controller-only": "true",
            },
            "nodeSelector": {"kubernetes.io/os": "linux", "example.com/pool": "admission"},
            "tolerations": [{"key": "example.com/admission", "operator": "Exists"}],
            "affinity": {
                "nodeAffinity": {"requiredDuringSchedulingIgnoredDuringExecution": {
                    "nodeSelectorTerms": [{
                        "matchExpressions": [{
                            "key": "example.com/pool", "operator": "In", "values": ["admission"],
                        }],
                    }],
                }},
            },
        }
        resources = {
            "requests": {"cpu": "200m", "memory": "192Mi"},
            "limits": {"cpu": "1", "memory": "512Mi"},
        }
        documents = self.render(self.enforced_values(resources=resources), values=values)
        deployment = documents[("Deployment", NAME)]
        self.assertEqual(deployment["metadata"]["namespace"], "synthetic-platform")
        pod = deployment["spec"]["template"]["spec"]
        for field in ("imagePullSecrets", "nodeSelector", "tolerations", "affinity"):
            self.assertEqual(pod[field], values[field])
        self.assertEqual(pod["serviceAccountName"], NAME)
        metadata = deployment["spec"]["template"]["metadata"]
        self.assertNotIn("azure.workload.identity/use", metadata["labels"])
        self.assertEqual(metadata["annotations"], {"sidecar.istio.io/inject": "false"})
        self.assertNotIn("annotations", documents[("ServiceAccount", NAME)]["metadata"])
        container = pod["containers"][0]
        self.assertEqual(container["image"], "registry.example.com/runtime/cetirun-operator:2.0.0@sha256:" + "1" * 64)
        self.assertEqual(container["imagePullPolicy"], "Always")
        self.assertEqual(container["resources"], resources)
        self.assertEqual([entry["name"] for entry in container["env"]], ["CETIRUN_SANDBOX_ADMISSION"])
        config = json.loads(container["env"][0]["value"])
        controller_username = "system:serviceaccount:synthetic-platform:synthetic-controller"
        self.assertEqual(config["controllerUsername"], controller_username)
        self.assertEqual([volume["secret"]["secretName"] for volume in pod["volumes"]], ["synthetic-admission-tls"])
        webhook = documents[("ValidatingWebhookConfiguration", NAME)]["webhooks"][0]
        self.assertIn(f'request.userInfo.username == "{controller_username}"', webhook["matchConditions"][0]["expression"])
        self.assertEqual(webhook["clientConfig"]["service"]["namespace"], "synthetic-platform")

    def test_admin_user_group_or_both_lists_are_serialized_exactly(self):
        for users, groups in (
            (["admin@example.com"], []),
            ([], ["sandbox-admins"]),
            (["admin@example.com", "second-admin@example.com"], ["sandbox-admins"]),
        ):
            with self.subTest(users=users, groups=groups):
                documents = self.render(self.server_values(adminUsers=users, adminGroups=groups))
                container = documents[("Deployment", NAME)]["spec"]["template"]["spec"]["containers"][0]
                config = json.loads(container["env"][0]["value"])
                self.assertEqual(config["adminUsers"], users)
                self.assertEqual(config["adminGroups"], groups)

    def test_enforcing_webhooks_match_only_configuration_operations(self):
        documents = self.render(self.enforced_values())
        for kind, path in (
            ("ValidatingWebhookConfiguration", "/validate-sandbox-configuration"),
            ("MutatingWebhookConfiguration", "/mutate-sandbox-agent"),
        ):
            with self.subTest(kind=kind):
                configuration = documents[(kind, NAME)]
                self.assertEqual(configuration["apiVersion"], "admissionregistration.k8s.io/v1")
                self.assertNotIn("namespace", configuration["metadata"])
                self.assertEqual(len(configuration["webhooks"]), 1)
                webhook = configuration["webhooks"][0]
                self.assertEqual(webhook["admissionReviewVersions"], ["v1"])
                self.assertEqual(webhook["sideEffects"], "None")
                self.assertEqual(webhook["failurePolicy"], "Fail")
                self.assertEqual(webhook["timeoutSeconds"], 2)
                self.assertEqual(webhook["matchPolicy"], "Equivalent")
                self.assertNotIn("namespaceSelector", webhook)
                self.assertNotIn("objectSelector", webhook)
                self.assertEqual(webhook["clientConfig"], {
                    "caBundle": self.ca_bundle,
                    "service": {"name": NAME, "namespace": "cetirun-system", "path": path, "port": 443},
                })
                operations = set()
                for rule in webhook["rules"]:
                    self.assertEqual(rule["apiGroups"], [API_GROUP])
                    self.assertEqual(rule["apiVersions"], ["v1alpha1"])
                    self.assertEqual(rule["scope"], "Cluster")
                    operations.update(
                        (operation, resource)
                        for operation in rule["operations"] for resource in rule["resources"]
                    )
                expected = {("CREATE", "agents")}
                if kind == "ValidatingWebhookConfiguration":
                    expected = {(operation, resource) for operation in ("CREATE", "UPDATE") for resource in RESOURCES}
                    expected.update(("DELETE", resource) for resource in RESOURCES if resource != "agents")
                else:
                    self.assertNotIn("matchConditions", webhook)
                self.assertEqual(operations, expected)

    def test_cleanup_condition_defaults_omitted_request_subresource(self):
        documents = self.render(self.enforced_values())
        condition = documents[("ValidatingWebhookConfiguration", NAME)]["webhooks"][0]["matchConditions"][0]
        expression = " ".join(condition["expression"].split())
        self.assertIn('(has(request.subResource) ? request.subResource : "") == "" &&', expression)
        self.assertNotIn('request.subResource == ""', expression)

    def test_cleanup_expression_is_narrow_and_handles_omitted_optional_fields(self):
        documents = self.render(self.enforced_values())
        conditions = documents[("ValidatingWebhookConfiguration", NAME)]["webhooks"][0]["matchConditions"]
        self.assertEqual([condition["name"] for condition in conditions], ["controller-cleanup-only"])
        expression = " ".join(conditions[0]["expression"].split())
        self.assertTrue(expression.startswith("!("), expression)
        self.assertTrue(expression.endswith(")"), expression)
        self.assertLessEqual(len(conditions[0]["expression"]), 5 * 1024)
        for fragment in (
            'request.operation == "UPDATE"',
            f'request.resource.group == "{API_GROUP}"',
            'request.resource.resource == "agents"',
            '(has(request.subResource) ? request.subResource : "") == ""',
            'request.userInfo.username == "system:serviceaccount:cetirun-system:cetirun-operator-controller-manager"',
            "object != null && oldObject != null",
            "has(oldObject.metadata.deletionTimestamp)",
            "oldObject.metadata.deletionTimestamp != null",
        ):
            self.assertIn(fragment + " &&", expression)
        fields = {
            "apiVersion": '""', "kind": '""', "spec": "{}", "status": "{}",
            "metadata.name": '""', "metadata.namespace": '""', "metadata.uid": '""',
            "metadata.labels": "{}", "metadata.annotations": "{}",
            "metadata.ownerReferences": "[]", "metadata.deletionTimestamp": "null",
            "metadata.deletionGracePeriodSeconds": "null", "metadata.generateName": '""',
            "metadata.creationTimestamp": "null", "metadata.generation": "0",
            "metadata.resourceVersion": '""', "metadata.selfLink": '""',
        }
        for field, default in fields.items():
            self.assertIn(
                f"(has(object.{field}) ? object.{field} : {default}) == "
                f"(has(oldObject.{field}) ? oldObject.{field} : {default}) &&",
                expression,
            )
        old = "(has(oldObject.metadata.finalizers) ? oldObject.metadata.finalizers : [])"
        new = "(has(object.metadata.finalizers) ? object.metadata.finalizers : [])"
        self.assertIn(f"size{new} < size{old} &&", expression)
        self.assertIn(f"{new}.all( finalizer, finalizer in {old} ) &&", expression)
        self.assertIn(f"{old}.exists( finalizer, !(finalizer in {new}) ) &&", expression)
        self.assertIn(f"{old}.all( finalizer, finalizer in {new} || finalizer in [", expression)
        self.assertEqual(re.findall(r'"(cetirun\.microsoft\.com/[^"]+)"', expression), FINALIZERS)
        self.assertEqual(expression.count("||"), 1)
        self.assertNotIn("managedFields", expression)

    def test_enforcement_requires_enabled_server(self):
        values = self.enforced_values()
        values["enabled"] = False
        self.assert_rejected(values, "sandboxAdmission.enforce requires sandboxAdmission.enabled")

    def test_server_requires_high_availability_certificates_and_admins(self):
        for replicas in (0, 1, -1, 1.5, 2.5, "invalid", True):
            with self.subTest(replicas=replicas):
                self.assert_rejected(self.server_values(replicaCount=replicas), "sandboxAdmission.replicaCount")
        for name in ("", " \t"):
            with self.subTest(tlsSecretName=name):
                self.assert_rejected(self.server_values(tlsSecretName=name), "sandboxAdmission.tlsSecretName")
        self.assert_rejected(
            self.server_values(adminUsers=[], adminGroups=[]),
            "at least one adminUsers or adminGroups entry",
        )
        for field in ("adminUsers", "adminGroups"):
            for identities in ([""], [" "], [" admin@example.com"], [12], "admin@example.com", {}):
                with self.subTest(field=field, identities=identities):
                    self.assert_rejected(self.server_values(**{field: identities}), f"sandboxAdmission.{field}")
        self.assert_rejected(
            self.server_values(), "must not use the operator controller service account",
            values={"serviceAccount": {"name": NAME}},
        )
        for values in ({"namespace": ""}, {"serviceAccount": {"name": ""}}):
            with self.subTest(values=values):
                self.assert_rejected(self.server_values(), "controller identity", values=values)

    def test_kubernetes_version_gate_only_applies_to_enforcement(self):
        self.render(self.server_values(), kube_version="1.29.0")
        self.render(kube_version="1.29.0")
        self.assert_rejected(self.enforced_values(), "Kubernetes >= 1.30", kube_version="1.29.9")
        for version in ("1.30.0", "1.30.1-gke.1", "1.31.0"):
            with self.subTest(version=version):
                self.render(self.enforced_values(), kube_version=version)

    def test_enforcement_rejects_missing_or_malformed_ca_bundle(self):
        invalid = (
            "", "not-base64", self.ca_pem,
            self.encode_ca("not a PEM certificate"),
            self.encode_ca("-----BEGIN PUBLIC KEY-----\nU3ludGhldGlj\n-----END PUBLIC KEY-----\n"),
            self.encode_ca("-----BEGIN CERTIFICATE-----\n%%invalid%%\n-----END CERTIFICATE-----\n"),
            self.encode_ca("-----BEGIN CERTIFICATE-----\nA===\n-----END CERTIFICATE-----\n"),
            self.encode_ca("-----BEGIN CERTIFICATE-----\n\n-----END CERTIFICATE-----\n"),
            self.encode_ca("-----BEGIN CERTIFICATE-----\nU3ludGhldGlj\n"),
            self.encode_ca(self.ca_pem + "unexpected trailing content"),
        )
        for bundle in invalid:
            with self.subTest(bundle=bundle[:32]):
                values = self.enforced_values()
                values["caBundle"] = bundle
                self.assert_rejected(values, "sandboxAdmission.caBundle")

    def test_enforcement_accepts_ca_rotation_bundle_and_base64_line_breaks(self):
        for pem in (self.ca_pem * 2, self.ca_pem.replace("\n", "\r\n")):
            with self.subTest(pem_lines=pem.count("\n")):
                encoded = base64.encodebytes(pem.encode()).decode()
                values = self.enforced_values()
                values["caBundle"] = encoded
                documents = self.render(values)
                for kind in ("ValidatingWebhookConfiguration", "MutatingWebhookConfiguration"):
                    actual = documents[(kind, NAME)]["webhooks"][0]["clientConfig"]["caBundle"]
                    self.assertEqual(actual, encoded.replace("\n", ""))
                    self.assertEqual(base64.b64decode(actual).decode(), pem)

    def test_helm_lint_default_bootstrap_and_enforcing_values(self):
        for admission in (None, self.server_values(), self.enforced_values()):
            with self.subTest(admission_enabled=admission is not None, enforcing=bool(admission and admission.get("enforce"))):
                result = self.helm(admission, lint=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
