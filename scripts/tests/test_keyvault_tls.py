# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
import pathlib
import subprocess
import unittest
import yaml

ROOT = pathlib.Path(__file__).parents[2]
CHART = ROOT / "infra" / "keyvault-tls"


class KeyVaultTlsChartTests(unittest.TestCase):
    def render(self, *args):
        return subprocess.run(
            ["helm", "template", "tls", str(CHART), *args],
            cwd=ROOT, text=True, capture_output=True,
        )

    def test_render_contains_csi_sync_and_hardened_consumers(self):
        result = self.render(
            "--namespace", "aks-istio-ingress",
            "--set", "keyVault.name=prodvault",
            "--set", "keyVault.tenantId=11111111-1111-4111-8111-111111111111",
            "--set", "keyVault.clientId=22222222-2222-4222-8222-222222222222",
            "--set", "keyVault.certificateSecretName=cetirun-public",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        docs = [doc for doc in yaml.safe_load_all(result.stdout) if doc]
        by_kind = {doc["kind"]: doc for doc in docs}
        spc = by_kind["SecretProviderClass"]
        self.assertEqual(spc["metadata"]["namespace"], "aks-istio-ingress")
        self.assertEqual(spc["spec"]["secretObjects"][0]["secretName"], "cetirun-tls")
        self.assertEqual([x["objectName"] for x in spc["spec"]["secretObjects"][0]["data"]], ["certificate-bundle", "certificate-bundle"])
        self.assertEqual(spc["spec"]["parameters"]["objects"].count("objectType: secret"), 1)
        self.assertIn('objectFormat: pem', spc["spec"]["parameters"]["objects"])
        self.assertIn('objectVersion: ""', spc["spec"]["parameters"]["objects"])
        deployment = by_kind["Deployment"]
        pod = deployment["spec"]["template"]["spec"]
        self.assertEqual(pod["serviceAccountName"], "cetirun-tls-sync")
        self.assertEqual(pod["nodeSelector"]["cetirun.microsoft.com/pool"], "system")
        self.assertEqual(pod["tolerations"][0]["key"], "CriticalAddonsOnly")
        self.assertEqual(pod["affinity"]["podAntiAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"][0]["matchLabelKeys"], ["pod-template-hash"])
        self.assertEqual(pod["containers"][0]["resources"]["requests"]["cpu"], "10m")
        self.assertEqual(by_kind["ServiceAccount"]["metadata"]["namespace"], "aks-istio-ingress")
        self.assertEqual(by_kind["PodDisruptionBudget"]["spec"]["minAvailable"], 1)
        self.assertNotIn("Secret", by_kind)
        self.assertIn("sha256:1f9457e9e325de0301c85d0c18765777820e06290c03849545868b4d22781496", result.stdout)

    def test_invalid_identity_is_rejected(self):
        for name in ("ProdVault", "x", "ab", "1vault", "a--b", "a" * 25):
            with self.subTest(name=name):
                result = self.render("--set", "keyVault.name=" + name)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("keyVault.name", result.stderr)

    def test_invalid_digest_is_rejected(self):
        result = self.render(
            "--set", "keyVault.name=prodvault",
            "--set", "keyVault.tenantId=11111111-1111-4111-8111-111111111111",
            "--set", "keyVault.clientId=22222222-2222-4222-8222-222222222222",
            "--set", "keyVault.certificateSecretName=cetirun-public",
            "--set", "image.digest=sha256:not-a-digest",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("image.digest", result.stderr)

    def test_fractional_replica_count_is_rejected(self):
        result = self.render(
            "--set", "keyVault.name=prodvault",
            "--set", "keyVault.tenantId=11111111-1111-0111-0111-111111111111",
            "--set", "keyVault.clientId=22222222-2222-0222-0222-222222222222",
            "--set", "keyVault.certificateSecretName=cetirun-public",
            "--set", "replicaCount=2.5",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("replicaCount", result.stderr)


if __name__ == "__main__":
    unittest.main()
