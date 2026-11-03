# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Platform umbrella wiring of the optional Skill Marketplace subchart."""

import copy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
OFFLINE = {
    **os.environ,
    "KUBECONFIG": os.devnull,
    "HELM_REGISTRY_CONFIG": os.devnull,
    "HELM_REPOSITORY_CONFIG": os.devnull,
}
CHARTS = ("cetirun-platform", "cetirun-operator/chart", "cetirun-portal/chart", "cetirun-ai-gateway/chart",
          "cetirun-connections-mcp/chart", "cetirun-skill-marketplace/chart")
NAMESPACE = "cetirun-system"
# Render-only PEM framing, not a certificate.
CA_BUNDLE = "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClFRPT0KLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQ=="
MINIMAL = {
    "istio": {"enabled": True},
    "cetirun-operator": {"namespace": NAMESPACE, "domainSuffix": "agents.example.invalid"},
    "cetirun-portal": {"istio": {"host": "portal.example.invalid"}, "aiGateway": {"catalogNamespace": NAMESPACE}},
}
# Synthetic native Sandbox platform with the marketplace switched on everywhere.
VALUES = {
    "istio": {"enabled": True},
    "global": {"registry": "registry.example.invalid", "version": "9.8.7"},
    "cetirun-operator": {
        "namespace": NAMESPACE, "domainSuffix": "agents.example.invalid",
        "sandboxAdmission": {
            "enabled": True, "enforce": True, "podsEnabled": True, "tlsSecretName": "sandbox-admission-tls",
            "caBundle": CA_BUNDLE, "adminUsers": ["admin@example.invalid"],
        },
        "sandboxSources": {
            "enabled": True, "nativeExecution": True, "bootstrapOwnedPrerequisites": True,
            "platformConfigMapName": "sandbox-platform",
        },
        "skillMarketplace": {"enabled": True},
    },
    "cetirun-portal": {
        "istio": {"host": "portal.example.invalid"}, "aiGateway": {"catalogNamespace": NAMESPACE},
        "skillMarketplace": {"enabled": True, "catalogNamespace": NAMESPACE},
    },
    "cetirun-skill-marketplace": {
        "enabled": True,
        "repository": {
            "url": "https://git.example.invalid/example/skills.git", "auth": "token",
            "existingSecret": "skills-repository",
        },
        "storage": {"resourceGroup": "example-skills-rg", "storageAccount": "exampleskills", "containerName": "marketplace"},
    },
}


def changed(*changes):
    values = copy.deepcopy(VALUES)
    for path, value in changes:
        section = values
        for key in path[:-1]:
            section = section.setdefault(key, {})
        section[path[-1]] = value
    return values


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3 or 4")
class SkillMarketplacePlatformTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        directory = tempfile.TemporaryDirectory(prefix="cetirun-marketplace-platform-")
        cls.addClassCleanup(directory.cleanup)
        base = Path(directory.name)
        for relative in CHARTS:
            shutil.copytree(ROOT / "app" / relative, base / relative, ignore=shutil.ignore_patterns("charts"))
        cls.chart = base / "cetirun-platform"
        subprocess.run(["helm", "dependency", "build", "--skip-refresh", str(cls.chart)],
                       env=OFFLINE, check=True, capture_output=True, timeout=60)

    def helm(self, values, *, namespace=NAMESPACE, lint=False):
        command = ["helm", "lint", "--strict", str(self.chart)] if lint else [
            "helm", "template", "cetirun-platform", str(self.chart), "--kube-version", "1.37.0"]
        return subprocess.run([*command, "--namespace", namespace, "--values", "-"], input=json.dumps(values),
                              env=OFFLINE, text=True, capture_output=True, timeout=60, check=False)

    def render(self, values, **kwargs):
        result = self.helm(values, **kwargs)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return [document for document in yaml.safe_load_all(result.stdout) if document]

    @staticmethod
    def environment(objects, name):
        deployment = next(doc for doc in objects if doc["kind"] == "Deployment" and doc["metadata"]["name"] == name)
        return {item["name"]: item.get("value") for item in deployment["spec"]["template"]["spec"]["containers"][0].get("env", [])}

    def test_marketplace_runs_in_the_operator_namespace_with_the_operator_claim(self):
        lint = self.helm(VALUES, lint=True)
        self.assertEqual(lint.returncode, 0, lint.stdout + lint.stderr)
        for claim in ("skills", "team-skills"):
            with self.subTest(claim=claim):
                objects = self.render(changed((("cetirun-operator", "sandboxSources", "skillsClaimName"), claim),
                                              (("cetirun-skill-marketplace", "agentClaims", "claimName"), claim)))
                deployment = next(doc for doc in objects if doc["kind"] == "Deployment"
                                  and doc["metadata"]["name"] == "cetirun-skill-marketplace")
                self.assertEqual(deployment["metadata"]["namespace"], NAMESPACE)
                container = deployment["spec"]["template"]["spec"]["containers"][0]
                self.assertEqual(container["image"], "registry.example.invalid/cetirun-skill-marketplace:9.8.7")
                # The non-root process must be able to read the 0440 credentials Secret.
                pod_security = deployment["spec"]["template"]["spec"]["securityContext"]
                self.assertEqual((pod_security["runAsUser"], pod_security["fsGroup"]), (65532, 65532))
                self.assertIn(f"--catalog-namespace={NAMESPACE}", container["args"])
                self.assertIn(f"--claim-name={claim}", container["args"])
                catalog = next(doc for doc in objects if doc["kind"] == "Role"
                               and doc["metadata"]["name"] == "cetirun-skill-marketplace-catalog")
                self.assertEqual(catalog["metadata"]["namespace"], NAMESPACE)
                self.assertEqual(self.environment(objects, "cetirun-operator")["CETIRUN_SKILL_MARKETPLACE"], '{"enabled":true}')
                portal = self.environment(objects, "cetirun-portal")
                self.assertEqual(portal["SKILL_MARKETPLACE_ENABLED"], "true")
                self.assertEqual(portal["SKILL_MARKETPLACE_CATALOG_NAMESPACE"], NAMESPACE)
        # The catalog namespace may also name the operator namespace explicitly.
        self.render(changed((("cetirun-skill-marketplace", "catalog", "namespace"), NAMESPACE)))

    def test_default_platform_has_no_marketplace(self):
        objects = self.render(MINIMAL)
        self.assertFalse([doc for doc in objects if doc["metadata"]["name"].startswith("cetirun-skill-marketplace")])
        self.assertNotIn("CETIRUN_SKILL_MARKETPLACE", self.environment(objects, "cetirun-operator"))
        self.assertNotIn("SKILL_MARKETPLACE_ENABLED", self.environment(objects, "cetirun-portal"))

    def test_inconsistent_wiring_is_refused(self):
        other = "other-namespace"
        cases = (
            (("cetirun-operator", "enabled"), False, "cetirun-skill-marketplace requires the bundled Operator"),
            (("cetirun-operator", "skillMarketplace", "enabled"), False,
             "cetirun-operator.skillMarketplace.enabled must be true when cetirun-skill-marketplace is enabled"),
            (("cetirun-portal", "skillMarketplace", "enabled"), False,
             "cetirun-portal.skillMarketplace.enabled must be true when cetirun-skill-marketplace is enabled"),
            (("cetirun-portal", "skillMarketplace", "catalogNamespace"), other,
             f"cetirun-portal.skillMarketplace.catalogNamespace is {other}, but cetirun-operator.namespace is {NAMESPACE}"),
            (("cetirun-skill-marketplace", "catalog", "namespace"), other,
             f"cetirun-skill-marketplace.catalog.namespace is {other}, but cetirun-operator.namespace is {NAMESPACE}"),
            (("cetirun-skill-marketplace", "agentClaims", "claimName"), "other-skills",
             'agentClaims.claimName is "other-skills", but cetirun-operator.sandboxSources.skillsClaimName is "skills"'),
            (("cetirun-operator", "sandboxSources", "skillsClaimName"), "team-skills",
             'agentClaims.claimName is "skills", but cetirun-operator.sandboxSources.skillsClaimName is "team-skills"'),
        )
        for path, value, message in cases:
            with self.subTest(path=".".join(path)):
                result = self.helm(changed((path, value)))
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn(message, result.stderr)
        # The marketplace renders into the release namespace.
        result = self.helm(changed((("cetirun-ai-gateway", "enabled"), False)), namespace=other)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(f"the Skill Marketplace is installed in the release namespace {other}, "
                      f"but cetirun-operator.namespace is {NAMESPACE}", result.stderr)


if __name__ == "__main__":
    unittest.main()
