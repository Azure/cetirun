# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline safety and staged-flow tests for the public ingress helper."""

import copy
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import MagicMock, patch

import yaml


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("public_ingress", ROOT / "scripts/cetirun_public_ingress.py")
INGRESS = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(INGRESS)

SUBSCRIPTION = "11111111-1111-1111-1111-111111111111"
TENANT = "22222222-2222-2222-2222-222222222222"


def config(directory, *, mode="custom", cert_manager="existing"):
    result = {
        "target": {
            "kubeconfig": str(directory / "kubeconfig"),
            "context": "private-context",
            "cluster": "private-cluster",
            "server": "https://private-cluster.example.invalid:443",
            "clusterUID": "cluster-uid",
        },
        "ingress": {
            "namespace": "aks-istio-ingress",
            "service": "aks-istio-ingressgateway-external",
            "release": "cetirun-public-ingress",
            "gatewayName": "cetirun-acme-http",
        },
        "dns": {"mode": mode},
        "certManager": {"mode": cert_manager},
        "stateDirectory": str(directory / "state"),
        "timeoutSeconds": 30,
    }
    if mode == "custom":
        result["dns"]["domain"] = "portal.example.invalid"
    else:
        result["dns"]["label"] = "portal-ingress"
        result["azure"] = {
            "subscriptionId": SUBSCRIPTION,
            "tenantId": TENANT,
            "resourceGroup": "example-rg",
            "clusterName": "example-aks",
        }
    if cert_manager == "install":
        result["certManager"]["chartArchive"] = str(directory / "cert-manager-v1.21.3.tgz")
    return result


def owned(name, namespace, *, uid="object-uid"):
    return {
        "metadata": {
            "name": name,
            "namespace": namespace,
            "uid": uid,
            "annotations": {
                "meta.helm.sh/release-name": "cetirun-public-ingress",
                "meta.helm.sh/release-namespace": namespace,
            },
            "labels": {"app.kubernetes.io/managed-by": "Helm"},
        },
    }


class StagedIngress(INGRESS.Ingress):
    """No-cloud flow double that retains private-state behavior."""

    def __init__(self, value, scratch):
        super().__init__(value, scratch)
        self.calls = []

    def preflight(self):
        self.calls.append("preflight")
        self.service = {"uid": "service-uid", "ip": "192.0.2.10", "ports": [
            {"port": 80, "protocol": "TCP"}, {"port": 443, "protocol": "TCP"},
        ], "selector": {"istio": "managed"}}
        self.state["service"] = self.service

    def assign_dns(self):
        self.calls.append("dns")
        self.state["domain"] = self.domain
        self.save()

    def render(self, domain=None):
        self.calls.append("render")
        return {"domain": domain}

    def install_cert_manager(self):
        self.calls.append("cert-manager")

    def cert_manager_check(self):
        pass

    def ingress_ownership_check(self):
        pass

    def apply_stage(self, production):
        self.calls.append("production" if production else "staging")

    def until(self, check, message):
        self.calls.append("wait")
        self.assert_true = check()
        if not self.assert_true:
            raise AssertionError(message)

    def certificate_ready(self, stage):
        return stage in ("staging", "production")

    def public_certificate(self):
        self.calls.append("certificate")
        return "a" * 64

    def preserve_service(self):
        self.calls.append("preserve")


class SyntheticAPIIngress(INGRESS.Ingress):
    """A stateful Kubernetes, Helm, and Azure API double for flow tests."""

    def __init__(self, value, scratch):
        super().__init__(value, scratch)
        self.api_calls = []
        self.live = {}
        self.helm_values = []
        self.azure_public_ip = {
            "id": f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes/providers/Microsoft.Network/publicIPAddresses/ingress",
            "etag": "etag-1",
            "ipAddress": "192.0.2.10",
            "sku": {"name": "Standard"},
            "publicIPAllocationMethod": "Static",
            "dnsSettings": {},
        }
        self.fail_patch_response = False
        self.controller_gateway_api_enabled = True
        self.controller_solver_image = self.lock["acmesolver"]["image"]
        self.cert_manager_status = "deployed"
        self.account_secret_name = None
        self.http_calls = []

    def _key(self, kind, name, namespace):
        return kind, name, namespace

    def render(self, domain=None):
        return super().render(domain)

    def _live_document(self, document):
        result = copy.deepcopy(document)
        metadata = result["metadata"]
        metadata["uid"] = metadata["name"] + "-uid"
        metadata["generation"] = 1
        metadata.setdefault("annotations", {}).update({
            "meta.helm.sh/release-name": self.config["ingress"]["release"],
            "meta.helm.sh/release-namespace": self.config["ingress"]["namespace"],
        })
        metadata.setdefault("labels", {})["app.kubernetes.io/managed-by"] = "Helm"
        if result["kind"] == "Gateway":
            result["status"] = {"conditions": [{"type": "Programmed", "status": "True", "observedGeneration": 1}]}
        elif result["kind"] == "HTTPRoute":
            result["status"] = {"parents": [{
                "parentRef": {"name": self.config["ingress"]["gatewayName"],
                              "namespace": self.config["ingress"]["namespace"], "sectionName": "acme-http"},
                "controllerName": "istio.io/gateway-controller",
                "conditions": [
                    {"type": "Accepted", "status": "True", "observedGeneration": 1},
                    {"type": "ResolvedRefs", "status": "True", "observedGeneration": 1},
                ],
            }]}
        elif result["kind"] == "Certificate":
            result["status"] = {
                "conditions": [{"type": "Ready", "status": "True", "observedGeneration": 1}],
                "notBefore": "2025-01-01T00:00:00Z",
                "renewalTime": "2030-01-01T00:00:00Z",
                "notAfter": "2031-01-01T00:00:00Z",
                "failedIssuanceAttempts": 0,
            }
        elif result["kind"] == "Issuer":
            result["status"] = {"conditions": [{"type": "Ready", "status": "True", "observedGeneration": 1}]}
        return result

    def _deploy_ingress(self, production):
        for document in self.documents:
            if document["kind"] == "Certificate" and document["metadata"]["name"] == INGRESS.CERTIFICATES["production"] and not production:
                continue
            group = document["apiVersion"].split("/", 1)[0]
            kind = document["kind"].lower() + "." + group
            live = self._live_document(document)
            self.live[self._key(kind, live["metadata"]["name"], live["metadata"]["namespace"])] = live

    def _controller_deployments(self):
        deployments = []
        for role in ("controller", "webhook", "cainjector"):
            container = {"image": self.lock[role]["image"]}
            if role == "controller":
                container.update(args=[
                    "--acme-http01-solver-image=" + self.controller_solver_image,
                    "--config=/etc/cert-manager/config.yaml",
                ], volumeMounts=[{"name": "controller-config", "mountPath": "/etc/cert-manager"}])
            spec = {"replicas": 1, "template": {"spec": {"containers": [container]}}}
            if role == "controller":
                spec["template"]["spec"]["volumes"] = [{"name": "controller-config", "configMap": {
                    "name": "controller-config", "items": [{"key": "config.yaml", "path": "config.yaml"}],
                }}]
            deployments.append({"metadata": {"generation": 1}, "spec": spec, "status": {
                "observedGeneration": 1, "updatedReplicas": 1, "readyReplicas": 1, "replicas": 1,
            }})
        return deployments

    def get(self, kind, name, namespace=None, *, metadata_only=False):
        self.api_calls.append(("kubectl-get", kind, name, namespace))
        ingress_namespace = self.config["ingress"]["namespace"]
        if kind == "namespace":
            result = {"metadata": {"uid": "cluster-uid" if name == "kube-system" else "ingress-namespace"}}
        elif kind == "service":
            result = {"metadata": {"uid": "service-uid"}, "spec": {
                "type": "LoadBalancer", "ports": [{"port": 80, "protocol": "TCP"}, {"port": 443, "protocol": "TCP"}],
                "selector": {"istio": "managed"},
            }, "status": {"loadBalancer": {"ingress": [{"ip": "192.0.2.10"}]}}}
        elif kind == "customresourcedefinition":
            result = {"spec": {"versions": [{"name": "v1", "served": True}]}}
        elif kind == "gatewayclass":
            result = {"spec": {"controllerName": "istio.io/gateway-controller"}, "status": {"conditions": [{
                "type": "Accepted", "status": "True",
            }]}}
        elif kind == "configmap" and name == "controller-config":
            result = {"data": {"config.yaml": yaml.safe_dump({"enableGatewayAPI": self.controller_gateway_api_enabled})}}
        elif kind == "secret" and name == self.account_secret_name:
            result = {"metadata": {"name": name}}
        elif kind == "secret" and name in INGRESS.CERTIFICATES.values():
            certificate = self.live.get(self._key("certificate.cert-manager.io", name, ingress_namespace))
            result = None if not certificate else {"metadata": {
                "annotations": {"cert-manager.io/certificate-name": name},
                "ownerReferences": [{"kind": "Certificate", "uid": name + "-uid"}],
            }}
        else:
            result = self.live.get(self._key(kind, name, namespace))
        if result is None:
            return None
        result = copy.deepcopy(result)
        return {"metadata": result["metadata"]} if metadata_only else result

    def kube(self, *args, **kwargs):
        self.api_calls.append(("kubectl", args))
        if args[:2] == ("config", "view"):
            return "private-cluster\nhttps://private-cluster.example.invalid:443"
        if args[:2] == ("get", "deployments"):
            return json.dumps({"items": self._controller_deployments()})
        if args[:2] == ("get", "certificates.cert-manager.io"):
            certificates = [value for (kind, _, _), value in self.live.items() if kind == "certificate.cert-manager.io"]
            return json.dumps({"items": certificates})
        raise AssertionError("unexpected kubectl invocation: " + repr(args))

    def helm(self, *args, **kwargs):
        self.api_calls.append(("helm", args))
        if args[0] == "list":
            release = args[args.index("--filter") + 1].strip("^").rstrip("$").replace("\\", "")
            if release == self.config["certManager"]["release"]:
                return json.dumps([{"status": self.cert_manager_status, "chart": "cert-manager-" + self.lock["chart"]["version"]}])
            if release == self.config["ingress"]["release"] and self.live:
                return json.dumps([{"status": "deployed", "chart": "cetirun-public-ingress-" + yaml.safe_load((self.chart / "Chart.yaml").read_text())["version"]}])
            return json.dumps([])
        if args[:2] == ("upgrade", "--install"):
            release = args[2]
            if release == self.config["ingress"]["release"]:
                values_path = Path(args[args.index("--values") + 1])
                values = yaml.safe_load(values_path.read_text())
                self.helm_values.append(values)
                self._deploy_ingress(values["acme"]["productionCertificateEnabled"])
            return ""
        raise AssertionError("unexpected Helm invocation: " + repr(args))

    def az(self, *args):
        self.api_calls.append(("az", args))
        if args[:2] == ("account", "show"):
            return {"id": SUBSCRIPTION, "tenantId": TENANT}
        if args[:2] == ("aks", "show"):
            return {"id": f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-rg/providers/Microsoft.ContainerService/managedClusters/example-aks",
                    "fqdn": "private-cluster.example.invalid", "nodeResourceGroup": "example-nodes"}
        if args[:3] == ("network", "public-ip", "list"):
            return [copy.deepcopy(self.azure_public_ip)]
        if args[:3] == ("network", "public-ip", "show"):
            return copy.deepcopy(self.azure_public_ip)
        if args[:3] == ("network", "public-ip", "update"):
            self.azure_public_ip["dnsSettings"] = {
                "domainNameLabel": self.config["dns"]["label"],
                "fqdn": self.config["dns"]["label"] + ".example.cloudapp.azure.com",
            }
            if self.fail_patch_response:
                raise RuntimeError("synthetic response loss")
            return copy.deepcopy(self.azure_public_ip)
        raise AssertionError("unexpected Azure invocation: " + repr(args))

    def public_certificate(self):
        return hashlib.sha256(b"synthetic-public-certificate").hexdigest()

    def http(self, path, secure=True):
        self.http_calls.append((path, secure))
        answers = {
            ("/", False): (301, "https://" + self.domain + "/", ""),
            ("/", True): (302, "https://login.microsoftonline.com/synthetic?redirect_uri=https%3A%2F%2F" + self.domain
                              + "%2Foauth2%2Fcallback&code_challenge_method=S256&code_challenge=" + "A" * 43, ""),
            ("/api/v1/me", True): (401, None, ""),
            ("/healthz", True): (200, None, "ok"),
            ("/readyz", True): (200, None, "ok"),
        }
        return answers[(path, secure)]


class PublicIngressTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-test-")
        self.directory = Path(self.temporary.name)
        os.chmod(self.directory, 0o700)
        (self.directory / "kubeconfig").write_text("synthetic")
        os.chmod(self.directory / "kubeconfig", 0o600)

    def tearDown(self):
        self.temporary.cleanup()

    def normalized(self, **kwargs):
        source = config(self.directory, **kwargs)
        return INGRESS.normalize(source)

    def pinned_cert_manager_archive(self, ingress):
        """Create only the public metadata needed to test archive pinning."""
        archive = Path(ingress.config["certManager"]["chartArchive"])
        with tarfile.open(archive, "w:gz") as output:
            payload = ("name: cert-manager\nversion: " + ingress.lock["chart"]["version"] + "\n").encode()
            entry = tarfile.TarInfo("cert-manager/Chart.yaml")
            entry.size = len(payload)
            import io
            output.addfile(entry, io.BytesIO(payload))
        ingress.lock["chart"]["archiveSHA256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
        return archive

    def failed_cert_manager_recovery(self, scratch):
        ingress = SyntheticAPIIngress(self.normalized(cert_manager="install"), scratch)
        archive = self.pinned_cert_manager_archive(ingress)
        ingress.state = {
            "steps": [{"name": "cert-manager-install", "status": "uncertain"}],
            "certManagerArchiveSHA256": ingress.lock["chart"]["archiveSHA256"],
        }
        ingress.cert_manager_status = "failed"
        return ingress, archive

    def test_normalize_is_decision_complete_and_rejects_cross_mode_inputs(self):
        custom = self.normalized()
        self.assertEqual(custom["ingress"]["release"], "cetirun-public-ingress")
        self.assertEqual(custom["certManager"]["namespace"], "cert-manager")
        self.assertEqual(custom["acme"]["email"], "")

        cases = [
            ({"dns": {"mode": "custom", "domain": "portal.example.invalid", "label": "also-a-label"}}, "Custom DNS"),
            ({"dns": {"mode": "custom", "domain": "*.example.invalid"}}, "Custom DNS"),
            ({"dns": {"mode": "custom", "domain": "portal.example.invalid"}, "azure": {"tenantId": TENANT}}, "Custom DNS"),
            ({"dns": {"mode": "azure-public-ip", "label": "portal", "domain": "predicted.example.invalid"}}, "Azure DNS"),
            ({"target": {"server": "http://private-cluster.example.invalid"}}, "HTTPS API origin"),
            ({"certManager": {"mode": "existing", "chartArchive": "/tmp/pinned.tgz"}}, "does not use chartArchive"),
            ({"stateDirectory": str(ROOT / "private-state")}, "outside the repository"),
        ]
        for override, error in cases:
            with self.subTest(override=override):
                candidate = config(self.directory)
                for key, value in override.items():
                    if isinstance(value, dict):
                        candidate.setdefault(key, {}).update(copy.deepcopy(value))
                    else:
                        candidate[key] = copy.deepcopy(value)
                with self.assertRaisesRegex(INGRESS.IngressError, error):
                    INGRESS.normalize(candidate)

    def test_azure_normalization_requires_explicit_identity_and_label(self):
        valid = self.normalized(mode="azure-public-ip")
        self.assertEqual(valid["azure"]["subscriptionId"], SUBSCRIPTION)
        invalid = config(self.directory, mode="azure-public-ip")
        invalid["azure"]["subscriptionId"] = "not-a-uuid"
        with self.assertRaisesRegex(INGRESS.IngressError, "subscriptionId"):
            INGRESS.normalize(invalid)
        invalid = config(self.directory, mode="azure-public-ip")
        invalid["dns"]["label"] = "UPPER"
        with self.assertRaisesRegex(INGRESS.IngressError, "Azure DNS"):
            INGRESS.normalize(invalid)

    def test_output_fragment_is_safe_for_existing_platform_workflow(self):
        self.assertEqual(INGRESS.deployment_values("portal.example.invalid"), {
            "domain": "portal.example.invalid",
            "istio": {"tlsMode": "external", "values": {
                "tls": {"enabled": False}, "gateway": {"includeWildcardHost": False},
            }},
        })

    def test_service_identity_requires_existing_dual_listener_and_selector(self):
        service = {"metadata": {"uid": "service-uid"}, "spec": {
            "type": "LoadBalancer", "ports": [{"port": 80}, {"port": 443, "protocol": "TCP"}],
            "selector": {"istio": "managed"},
        }, "status": {"loadBalancer": {"ingress": [{"ip": "192.0.2.10"}]}}}
        identity = INGRESS.service_identity(service)
        self.assertEqual(identity["ip"], "192.0.2.10")
        service["spec"]["ports"] = [{"port": 443}]
        with self.assertRaisesRegex(INGRESS.IngressError, "80 and 443"):
            INGRESS.service_identity(service)

    def test_staged_preparation_writes_private_fragment_after_staging_then_production(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = StagedIngress(value, scratch)
            result = ingress.prepare()
        self.assertEqual(ingress.calls[0:3], ["preflight", "dns", "render"])
        self.assertLess(ingress.calls.index("staging"), ingress.calls.index("production"))
        self.assertIn("certificate", ingress.calls)
        self.assertTrue(result["certificateReady"])
        self.assertEqual(result["oauthCallback"], "https://portal.example.invalid/oauth2/callback")
        fragment = yaml.safe_load((self.directory / "state" / "deployment-values.yaml").read_text())
        self.assertEqual(fragment, INGRESS.deployment_values("portal.example.invalid"))
        state = json.loads((self.directory / "state" / "state.json").read_text())
        self.assertTrue(state["prepared"])
        self.assertEqual((self.directory / "state").stat().st_mode & 0o077, 0)
        self.assertEqual((self.directory / "state" / "deployment-values.yaml").stat().st_mode & 0o077, 0)

    def test_partial_mutation_records_uncertain_step_and_is_not_silently_replayed(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            ingress.load_state(write=True)
            with self.assertRaisesRegex(RuntimeError, "lost response"):
                ingress.mutate("staging-resources", lambda: (_ for _ in ()).throw(RuntimeError("lost response")))
            saved = json.loads((self.directory / "state" / "state.json").read_text())
            self.assertEqual(saved["lastStep"], {"name": "staging-resources", "status": "uncertain"})
            next_run = INGRESS.Ingress(value, scratch)
            next_run.load_state()
            self.assertEqual(next_run.state["lastStep"]["status"], "uncertain")

    def test_unmanaged_resource_and_tls_secret_are_never_adopted(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            doc = {"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
                   "metadata": {"name": "cetirun-acme-http", "namespace": value["ingress"]["namespace"]}}
            ingress.get = lambda *args, **kwargs: {"metadata": {"annotations": {}, "labels": {}}}
            with self.assertRaisesRegex(INGRESS.IngressError, "ownership conflict"):
                ingress.resource_check(doc, value["ingress"]["release"], value["ingress"]["namespace"])

            certificate = owned("cetirun-tls", value["ingress"]["namespace"])
            certificate["spec"] = {"secretName": "cetirun-tls"}
            ingress.get = lambda *args, **kwargs: {"metadata": {
                "annotations": {"cert-manager.io/certificate-name": "cetirun-tls"},
                "ownerReferences": [{"kind": "Certificate", "uid": "other-certificate"}],
            }}
            with self.assertRaisesRegex(INGRESS.IngressError, "owner reference"):
                ingress.certificate_secret_check(certificate)

    def test_offline_render_never_uses_target_kubeconfig_or_azure(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            recorded = []

            def fake_run(command, **kwargs):
                recorded.append((command, kwargs))
                self.assertEqual(command[:2], ["helm", "template"])
                self.assertTrue(kwargs["offline"])
                return "apiVersion: v1\nkind: Gateway\nmetadata:\n  name: synthetic\n"

            ingress.run = fake_run
            ingress.archive_check = lambda: None
            ingress.render()
        self.assertEqual(len(recorded), 1)

    def test_dns_check_requires_exact_ipv4_and_no_ipv6(self):
        values = {
            ("https://dns.google/resolve", "A"): {"192.0.2.10"},
            ("https://dns.google/resolve", "AAAA"): set(),
            ("https://cloudflare-dns.com/dns-query", "A"): {"192.0.2.10"},
            ("https://cloudflare-dns.com/dns-query", "AAAA"): set(),
        }
        with patch.object(INGRESS, "query_dns", side_effect=lambda provider, domain, kind: values[(provider, kind)]):
            INGRESS.check_dns("portal.example.invalid", "192.0.2.10")
        values[("https://dns.google/resolve", "AAAA")] = {"2001:db8::1"}
        with patch.object(INGRESS, "query_dns", side_effect=lambda provider, domain, kind: values[(provider, kind)]):
            with self.assertRaisesRegex(INGRESS.IngressError, "without an AAAA"):
                INGRESS.check_dns("portal.example.invalid", "192.0.2.10")

    def test_commands_bind_kubectl_and_azure_to_configured_context_and_subscription(self):
        value = self.normalized(mode="azure-public-ip")
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            commands = []
            ingress.run = lambda command, **kwargs: commands.append(command) or "{}"
            ingress.kube("get", "namespace", "kube-system")
            ingress.az("account", "show")
        self.assertEqual(commands[0][:4], ["kubectl", "--context", "private-context", "--request-timeout=30s"])
        self.assertIn("--subscription", commands[1])
        self.assertEqual(commands[1][commands[1].index("--subscription") + 1], SUBSCRIPTION)
        self.assertNotIn("set", commands[1])

    def test_azure_preflight_binds_the_exact_aks_and_public_ip(self):
        value = self.normalized(mode="azure-public-ip")
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            ingress.service = {"uid": "service-uid", "ip": "192.0.2.10", "ports": [], "selector": {}}
            public_ip = {
                "id": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/example-nodes/providers/Microsoft.Network/publicIPAddresses/ingress",
                "etag": "etag-1", "ipAddress": "192.0.2.10", "sku": {"name": "Standard"},
                "publicIPAllocationMethod": "Static",
                "dnsSettings": {"domainNameLabel": "portal-ingress", "fqdn": "portal-ingress.example.cloudapp.azure.com"},
            }
            responses = [
                {"id": SUBSCRIPTION, "tenantId": TENANT},
                {"id": f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-rg/providers/Microsoft.ContainerService/managedClusters/example-aks",
                 "fqdn": "private-cluster.example.invalid", "nodeResourceGroup": "example-nodes"},
                [public_ip],
            ]
            commands = []
            ingress.az = lambda *args: commands.append(args) or responses.pop(0)
            ingress.azure_check()
        self.assertEqual(ingress.domain, "portal-ingress.example.cloudapp.azure.com")
        self.assertEqual(ingress.state["publicIPId"], public_ip["id"])
        self.assertEqual(commands[1], ("aks", "show", "--resource-group", "example-rg", "--name", "example-aks"))

    def test_azure_preflight_refuses_an_existing_different_dns_label(self):
        value = self.normalized(mode="azure-public-ip")
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            ingress.service = {"uid": "service-uid", "ip": "192.0.2.10", "ports": [], "selector": {}}
            responses = [
                {"id": SUBSCRIPTION, "tenantId": TENANT},
                {"id": f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-rg/providers/Microsoft.ContainerService/managedClusters/example-aks",
                 "fqdn": "private-cluster.example.invalid", "nodeResourceGroup": "example-nodes"},
                [{"id": "/public-ip", "ipAddress": "192.0.2.10", "sku": {"name": "Standard"},
                  "publicIPAllocationMethod": "Static", "dnsSettings": {"domainNameLabel": "already-owned"}}],
            ]
            ingress.az = lambda *args: responses.pop(0)
            with self.assertRaisesRegex(INGRESS.IngressError, "another DNS label"):
                ingress.azure_check()

    def test_target_preflight_refuses_context_or_cluster_uid_mismatch_before_writes(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            ingress.kube = lambda *args, **kwargs: "other-cluster\nhttps://private-cluster.example.invalid:443"
            with self.assertRaisesRegex(INGRESS.IngressError, "cluster/server mismatch"):
                ingress.target_check()

            ingress.kube = lambda *args, **kwargs: "private-cluster\nhttps://private-cluster.example.invalid:443"
            ingress.get = lambda *args, **kwargs: {"metadata": {"uid": "other-uid"}}
            with self.assertRaisesRegex(INGRESS.IngressError, "UID mismatch"):
                ingress.target_check()

    def test_pinned_cert_manager_archive_requires_exact_checksum_and_metadata(self):
        archive = self.directory / "cert-manager-v1.21.3.tgz"
        with tarfile.open(archive, "w:gz") as output:
            payload = b"name: cert-manager\nversion: v1.21.3\n"
            entry = tarfile.TarInfo("cert-manager/Chart.yaml")
            entry.size = len(payload)
            import io
            output.addfile(entry, io.BytesIO(payload))
        value = self.normalized(cert_manager="install")
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            ingress.lock["chart"]["archiveSHA256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
            ingress.archive_check()
            ingress.lock["chart"]["archiveSHA256"] = "0" * 64
            with self.assertRaisesRegex(INGRESS.IngressError, "checksum"):
                ingress.archive_check()

    def test_public_certificate_uses_only_public_tls_data_and_checks_hostname(self):
        value = self.normalized()
        certificate = owned("cetirun-tls", value["ingress"]["namespace"], uid="certificate-uid")
        certificate["spec"] = {"secretName": "cetirun-tls"}
        certificate["status"] = {"observedGeneration": 1, "conditions": [
            {"type": "Ready", "status": "True", "observedGeneration": 1},
        ], "notBefore": "2025-01-01T00:00:00Z", "renewalTime": "2030-01-01T00:00:00Z",
            "notAfter": "2031-01-01T00:00:00Z", "failedIssuanceAttempts": 0}
        certificate["metadata"]["generation"] = 1
        pem = "-----BEGIN CERTIFICATE-----\nsynthetic\n-----END CERTIFICATE-----\n"
        encoded = __import__("base64").b64encode(pem.encode()).decode()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            ingress.domain = "portal.example.invalid"
            ingress.get = lambda kind, *args, **kwargs: certificate if kind == "certificate.cert-manager.io" else None
            ingress.kube = lambda *args, **kwargs: encoded
            ingress.certificate_secret_check = lambda item: self.assertIs(item, certificate)
            commands = []
            ingress.run = lambda command, **kwargs: commands.append(command) or "ok"
            with patch.object(INGRESS.ssl, "PEM_cert_to_DER_cert", return_value=b"public-certificate"):
                fingerprint = ingress.public_certificate()
        self.assertEqual(fingerprint, hashlib.sha256(b"public-certificate").hexdigest())
        self.assertIn("portal.example.invalid", commands[1])
        self.assertNotIn("tls.key", " ".join(map(str, commands)))

    def test_tool_error_does_not_expose_subprocess_output(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = INGRESS.Ingress(value, scratch)
            result = subprocess.CompletedProcess(["helm"], 1, stdout="token=private", stderr="secret=private")
            with patch.object(subprocess, "run", return_value=result):
                with self.assertRaisesRegex(INGRESS.IngressError, "tool output is withheld") as failure:
                    ingress.run(["helm", "template"])
        self.assertNotIn("private", str(failure.exception))

    def test_custom_domain_prepare_and_rerun_preserve_the_production_certificate(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            with patch.object(INGRESS, "check_dns"):
                result = ingress.prepare()
            self.assertEqual(result["domain"], "portal.example.invalid")
            self.assertEqual([item["acme"]["productionCertificateEnabled"] for item in ingress.helm_values], [False, True])
            production_key = ingress._key("certificate.cert-manager.io", INGRESS.CERTIFICATES["production"], value["ingress"]["namespace"])
            production_before = copy.deepcopy(ingress.live[production_key])

            ingress.helm_values.clear()
            with patch.object(INGRESS, "check_dns"):
                rerun = ingress.prepare()

        self.assertTrue(rerun["certificateReady"])
        self.assertTrue(ingress.helm_values)
        self.assertTrue(all(item["acme"]["productionCertificateEnabled"] for item in ingress.helm_values))
        self.assertEqual(ingress.live[production_key], production_before)
        self.assertFalse(any(call[0] == "az" for call in ingress.api_calls))

    def test_azure_response_loss_is_recovered_from_the_assigned_dns_label_without_repatching(self):
        value = self.normalized(mode="azure-public-ip")
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            first = SyntheticAPIIngress(value, scratch)
            first.fail_patch_response = True
            with patch.object(INGRESS, "check_dns"):
                with self.assertRaisesRegex(RuntimeError, "response loss"):
                    first.prepare()
            receipt = json.loads((self.directory / "state" / "state.json").read_text())
            self.assertEqual(receipt["lastStep"], {"name": "azure-dns-label", "status": "uncertain"})
            self.assertEqual(first.azure_public_ip["dnsSettings"]["domainNameLabel"], "portal-ingress")

            retry = SyntheticAPIIngress(value, scratch)
            retry.azure_public_ip = first.azure_public_ip
            with patch.object(INGRESS, "check_dns"):
                result = retry.prepare()

        self.assertEqual(result["domain"], "portal-ingress.example.cloudapp.azure.com")
        retry_patches = [call for call in retry.api_calls if call[0] == "az" and call[1][:3] == ("network", "public-ip", "update")]
        self.assertEqual(retry_patches, [])

    def test_existing_cert_manager_controller_requires_config_mount_and_pinned_solver_digest(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            ingress.api_groups = set(INGRESS.CERT_CRDS)
            ingress.check_controller()
            ingress.controller_solver_image = "quay.io/jetstack/cert-manager-acmesolver:v1.21.3"
            with self.assertRaisesRegex(INGRESS.IngressError, "solver"):
                ingress.check_controller()
            ingress.controller_solver_image = ingress.lock["acmesolver"]["image"]
            ingress.controller_gateway_api_enabled = False
            with self.assertRaisesRegex(INGRESS.IngressError, "Gateway API"):
                ingress.check_controller()

    def test_preflight_refuses_unmanaged_acme_account_secret(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            ingress.render(value["dns"]["domain"])
            ingress.account_secret_name = "cetirun-letsencrypt-staging-account"
            with self.assertRaisesRegex(INGRESS.IngressError, "ACME account Secret"):
                ingress.ingress_ownership_check()

    def test_failed_cert_manager_release_is_not_recovered_without_our_receipt(self):
        value = self.normalized(cert_manager="install")
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            ingress.api_groups = set(INGRESS.CERT_CRDS)
            ingress.cert_manager_status = "failed"
            with self.assertRaisesRegex(INGRESS.IngressError, "not installed by this preparation"):
                ingress.cert_manager_check()

    def test_failed_pinned_cert_manager_install_with_matching_receipt_can_recover(self):
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress, _ = self.failed_cert_manager_recovery(scratch)
            ingress.cert_manager_check()
        self.assertTrue(ingress.cm_recovery)
        self.assertTrue(ingress.cm_needs_install)

    def test_failed_cert_manager_recovery_refuses_an_unmanaged_surviving_resource(self):
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress, _ = self.failed_cert_manager_recovery(scratch)
            document = {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {
                "name": "cert-manager", "namespace": "cert-manager"}, "spec": {"replicas": 1}}
            ingress.cm_documents = [document]
            ingress.get = lambda *args, **kwargs: {"metadata": {"name": "cert-manager", "annotations": {}, "labels": {}}}
            with self.assertRaisesRegex(INGRESS.IngressError, "ownership conflict"):
                ingress.cert_manager_check()

    def test_failed_cert_manager_recovery_allows_only_the_matching_unowned_post_install_hook(self):
        hook = {"apiVersion": "batch/v1", "kind": "Job", "metadata": {
            "name": "cert-manager-startupapicheck", "namespace": "cert-manager",
            "labels": {"app": "startupapicheck"}, "annotations": {"helm.sh/hook": "post-install"},
        }, "spec": {"template": {"spec": {"restartPolicy": "Never"}}}}
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress, _ = self.failed_cert_manager_recovery(scratch)
            ingress.cm_documents = [hook]
            ingress.get = lambda *args, **kwargs: copy.deepcopy(hook)
            ingress.cert_manager_check()
            self.assertTrue(ingress.cm_recovery)

            foreign = copy.deepcopy(hook)
            foreign["spec"]["template"]["spec"]["restartPolicy"] = "OnFailure"
            ingress.get = lambda *args, **kwargs: copy.deepcopy(foreign)
            with self.assertRaisesRegex(INGRESS.IngressError, "ownership conflict"):
                ingress.cert_manager_check()

    def test_cert_manager_install_journals_before_helm_and_uses_verified_archive_snapshot(self):
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(self.normalized(cert_manager="install"), scratch)
            source_archive = self.pinned_cert_manager_archive(ingress)
            ingress.load_state(write=True)
            calls = []

            def helm(*args, **kwargs):
                calls.append(args)
                self.assertEqual(ingress.state["lastStep"], {"name": "cert-manager-install", "status": "started"})
                self.assertEqual(ingress.state["certManagerArchiveSHA256"], ingress.lock["chart"]["archiveSHA256"])
                return ""

            ingress.helm = helm
            ingress.install_cert_manager()
            archive_snapshot_exists = ingress.cm_archive.is_file()

        self.assertEqual(len(calls), 1)
        self.assertEqual(Path(calls[0][3]), ingress.cm_archive)
        self.assertNotEqual(ingress.cm_archive, source_archive)
        self.assertTrue(archive_snapshot_exists)
        self.assertEqual(ingress.state["lastStep"], {"name": "cert-manager-install", "status": "completed"})

    def test_prepare_refuses_a_second_local_writer_for_the_same_state_directory(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            ingress.load_state(write=True)
            descriptor = os.open(self.directory / "state" / ".prepare.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                with self.assertRaisesRegex(INGRESS.IngressError, "Another ingress preparation"):
                    ingress.prepare()
            finally:
                os.close(descriptor)

    def test_verify_rejects_an_issuer_that_is_no_longer_ready(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            with patch.object(INGRESS, "check_dns"):
                ingress.prepare()
            production_issuer = ingress._key("issuer.cert-manager.io", "cetirun-letsencrypt-production", value["ingress"]["namespace"])
            ingress.live[production_issuer]["status"]["conditions"][0]["status"] = "False"
            with patch.object(INGRESS, "check_dns"):
                with self.assertRaisesRegex(INGRESS.IngressError, "Issuer is not Ready"):
                    ingress.verify()

    def test_preflight_rejects_redirect_and_solver_parent_reference_drift(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            with patch.object(INGRESS, "check_dns"):
                ingress.prepare()
            namespace = value["ingress"]["namespace"]
            route = ingress.live[ingress._key("httproute.gateway.networking.k8s.io", "cetirun-http-redirect", namespace)]
            issuer = ingress.live[ingress._key("issuer.cert-manager.io", "cetirun-letsencrypt-production", namespace)]
            references = [route["spec"]["parentRefs"][0], issuer["spec"]["acme"]["solvers"][0]["http01"]["gatewayHTTPRoute"]["parentRefs"][0]]
            for reference in references:
                for field, invalid in (("namespace", "other"), ("group", "other.example.invalid"), ("kind", "Service")):
                    with self.subTest(resource="redirect" if reference is references[0] else "solver", field=field):
                        original = reference[field]
                        reference[field] = invalid
                        with self.assertRaisesRegex(INGRESS.IngressError, "configuration differs"):
                            ingress.ingress_ownership_check()
                        reference[field] = original

    def test_verify_rejects_stale_gateway_and_unresolved_route_before_tls(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            with patch.object(INGRESS, "check_dns"):
                ingress.prepare()
            namespace = value["ingress"]["namespace"]
            gateway = ingress.live[ingress._key("gateway.gateway.networking.k8s.io", value["ingress"]["gatewayName"], namespace)]
            route = ingress.live[ingress._key("httproute.gateway.networking.k8s.io", "cetirun-http-redirect", namespace)]
            gateway["metadata"]["generation"] += 1
            writes_before = len(ingress.helm_values)
            with patch.object(INGRESS, "check_dns"), patch.object(INGRESS.socket, "create_connection") as connect:
                with self.assertRaisesRegex(INGRESS.IngressError, "Gateway is not Programmed"):
                    ingress.verify()
                gateway["status"]["conditions"][0]["observedGeneration"] = gateway["metadata"]["generation"]
                for condition in route["status"]["parents"][0]["conditions"]:
                    if condition["type"] == "ResolvedRefs":
                        condition["status"] = "False"
                with self.assertRaisesRegex(INGRESS.IngressError, "route is not Accepted with ResolvedRefs"):
                    ingress.verify()
                connect.assert_not_called()
            self.assertEqual(len(ingress.helm_values), writes_before)

    def test_renewal_check_rejects_an_elapsed_certificate_renewal_time(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            certificate = {"status": {
                "notBefore": "2020-01-01T00:00:00Z",
                "renewalTime": "2021-01-01T00:00:00Z",
                "notAfter": "2031-01-01T00:00:00Z",
                "failedIssuanceAttempts": 0,
            }}
            with self.assertRaisesRegex(INGRESS.IngressError, "healthy future renewal"):
                ingress.renewal_check(certificate)

    def test_verify_uses_only_read_apis_after_preparation(self):
        value = self.normalized()
        with tempfile.TemporaryDirectory(prefix="cetirun-public-ingress-scratch-") as scratch:
            ingress = SyntheticAPIIngress(value, scratch)
            with patch.object(INGRESS, "check_dns"):
                ingress.prepare()
            writes_before = len(ingress.helm_values)
            raw = MagicMock()
            raw.__enter__.return_value = raw
            tls = MagicMock()
            tls.__enter__.return_value = tls
            tls.getpeercert.return_value = b"synthetic-public-certificate"
            context = MagicMock()
            context.wrap_socket.return_value = tls
            with patch.object(INGRESS, "check_dns"), \
                 patch.object(INGRESS.socket, "create_connection", return_value=raw), \
                 patch.object(INGRESS.ssl, "create_default_context", return_value=context):
                verified = ingress.verify()

        self.assertEqual(verified["portalReadiness"], "verified")
        self.assertEqual(len(ingress.helm_values), writes_before)
        self.assertEqual(ingress.http_calls, [("/", False), ("/", True), ("/api/v1/me", True), ("/healthz", True), ("/readyz", True)])


if __name__ == "__main__":
    unittest.main()
