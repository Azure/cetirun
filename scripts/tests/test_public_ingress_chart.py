# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline rendering checks for CetiRun's public HTTP-01 ingress chart."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest

import yaml


CHART = Path(__file__).resolve().parents[2] / "infra/public-ingress"
NAMESPACE = "aks-istio-ingress"
VALUES = {
    "domain": "portal.example.invalid",
    "ingressNamespace": NAMESPACE,
    "ingressServiceHostname": "aks-istio-ingressgateway-external.aks-istio-ingress.svc.cluster.local",
}
OFFLINE = {**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull,
           "HELM_REPOSITORY_CONFIG": os.devnull}


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3 or 4")
class PublicIngressChartTests(unittest.TestCase):
    def render(self, values=None, *, namespace=NAMESPACE, error=None):
        command = ["helm", "template", "public-ingress", str(CHART), "--namespace", namespace,
                   "--values", "-"]
        result = subprocess.run(command, input=json.dumps({**VALUES, **(values or {})}), text=True,
                                capture_output=True, env=OFFLINE, timeout=30, check=False)
        if error:
            self.assertNotEqual(result.returncode, 0, "invalid values rendered")
            self.assertIn(error, result.stderr)
            return None
        self.assertEqual(result.returncode, 0, result.stderr)
        return {(doc["kind"], doc["metadata"]["namespace"], doc["metadata"]["name"]): doc
                for doc in yaml.safe_load_all(result.stdout) if doc}

    def test_default_bootstrap_uses_one_existing_listener_and_no_certificate(self):
        objects = self.render()
        gateway = objects[("Gateway", NAMESPACE, "cetirun-acme-http")]
        self.assertEqual(gateway["spec"]["gatewayClassName"], "istio")
        self.assertEqual(gateway["spec"]["addresses"], [{
            "type": "Hostname",
            "value": VALUES["ingressServiceHostname"],
        }])
        listener = gateway["spec"]["listeners"][0]
        self.assertEqual(listener["hostname"], VALUES["domain"])
        self.assertEqual(listener["port"], 80)
        self.assertEqual(listener["allowedRoutes"]["namespaces"], {"from": "Same"})
        route = objects[("HTTPRoute", NAMESPACE, "cetirun-http-redirect")]
        self.assertEqual(route["spec"]["parentRefs"], [{"name": "cetirun-acme-http", "namespace": NAMESPACE,
                                                                   "group": "gateway.networking.k8s.io", "kind": "Gateway", "sectionName": "acme-http"}])
        self.assertEqual(route["spec"]["hostnames"], [VALUES["domain"]])
        self.assertFalse(any(kind == "Certificate" for kind, _, _ in objects))
        self.assertFalse(any(kind == "Secret" for kind, _, _ in objects))
        self.assertFalse(any(kind == "Service" for kind, _, _ in objects))

    def test_issuers_limit_solver_routes_to_same_listener_without_sidecar(self):
        objects = self.render()
        issuers = [value for (kind, namespace, _), value in objects.items()
                   if kind == "Issuer" and namespace == NAMESPACE]
        self.assertCountEqual([item["metadata"]["name"] for item in issuers], [
            "cetirun-letsencrypt-staging", "cetirun-letsencrypt-production",
        ])
        for issuer in issuers:
            self.assertEqual(issuer["metadata"]["annotations"], {"helm.sh/resource-policy": "keep"})
            solver = issuer["spec"]["acme"]["solvers"][0]["http01"]
            route = solver["gatewayHTTPRoute"]
            self.assertEqual(route["parentRefs"], [{
                "name": "cetirun-acme-http", "namespace": NAMESPACE, "group": "gateway.networking.k8s.io",
                "sectionName": "acme-http", "kind": "Gateway", "port": 80,
            }])
            self.assertEqual(route["podTemplate"]["metadata"]["annotations"], {"sidecar.istio.io/inject": "false"})
            self.assertNotIn("podTemplate", solver)

    def test_staging_and_production_certificates_are_independent_and_retained(self):
        objects = self.render({"acme": {"stagingCertificateEnabled": True,
                                        "productionCertificateEnabled": True}})
        staged = objects[("Certificate", NAMESPACE, "cetirun-tls-staging")]
        production = objects[("Certificate", NAMESPACE, "cetirun-tls")]
        self.assertEqual(staged["spec"]["secretName"], "cetirun-tls-staging")
        self.assertEqual(production["spec"]["secretName"], "cetirun-tls")
        self.assertEqual(staged["spec"]["issuerRef"]["name"], "cetirun-letsencrypt-staging")
        self.assertEqual(production["spec"]["issuerRef"]["name"], "cetirun-letsencrypt-production")
        self.assertEqual(staged["metadata"]["annotations"], {"helm.sh/resource-policy": "keep"})
        self.assertEqual(production["metadata"]["annotations"], {"helm.sh/resource-policy": "keep"})

    def test_rejects_cross_namespace_and_unsafe_identity_inputs(self):
        self.render(namespace="other", error="release namespace")
        self.render({"domain": "*.example.invalid"}, error="without a wildcard")
        self.render({"domain": "portal.example.invalid", "ingressServiceHostname":
                     "gateway.other.svc.cluster.local"}, error="in ingressNamespace")
        self.render({"acme": {"stagingCertificateEnabled": "true"}}, error="must be booleans")
        self.render({"acme": {"stagingCertificateEnabled": True, "productionCertificateEnabled": True,
                               "stagingSecretName": "same", "productionSecretName": "same"}},
                    error="Secret names must differ")


if __name__ == "__main__":
    unittest.main()
