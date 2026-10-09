# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Static contracts for the environment E2E deployment workflow."""

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / ".github" / "workflows" / "e2e.yml"
E2E_SCRIPT = ROOT / "scripts" / "e2e-test.sh"
ISTIO_DEPLOY = ROOT / "infra" / "istio" / "deploy.sh"


class E2EWorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.e2e_script = E2E_SCRIPT.read_text(encoding="utf-8")
        cls.istio_deploy = ISTIO_DEPLOY.read_text(encoding="utf-8")

    def test_first_enablement_is_reviewable_and_manual_only(self):
        self.assertIn("  pull_request:", self.workflow)
        self.assertIn("  workflow_dispatch:", self.workflow)
        self.assertNotIn("  push:", self.workflow)
        self.assertNotIn("  schedule:", self.workflow)
        self.assertNotIn("if: false", self.workflow)
        self.assertIn("cancel-in-progress: false", self.workflow)
        self.assertIn(
            "if: github.event_name == 'workflow_dispatch'", self.workflow
        )

    def test_preflight_blocks_unmigrated_policy_and_legacy_identity_agents(self):
        self.assertIn("(.spec.team // \"\") | length", self.workflow)
        self.assertIn("(.spec.sandboxRef // \"\") | length", self.workflow)
        self.assertIn(
            "Existing Agents must complete the Team and Sandbox policy migration",
            self.workflow,
        )
        self.assertIn(
            "typed credential-policy enforcement is required",
            self.workflow,
        )

    def test_legacy_credentials_and_environment_are_explicit(self):
        for value in (
            "environment: cetiruntest",
            "secrets.E2E_KUBE_CONFIG",
            "secrets.E2E_ACR_USERNAME",
            "secrets.E2E_ACR_PASSWORD",
            "secrets.E2E_TLS_PFX",
        ):
            with self.subTest(value=value):
                self.assertIn(value, self.workflow)
        self.assertNotIn("azure/login", self.workflow)
        self.assertNotIn("id-token: write", self.workflow)
        self.assertNotIn("cetirun-environment-deploy.sh", self.workflow)
        self.assertGreaterEqual(self.workflow.count("umask 077"), 5)

    def test_pipeline_publishes_and_deploys_current_components(self):
        for value in (
            "scripts/cetirun-docker-build.sh",
            "scripts/operator-docker-build.sh",
            "scripts/terminal-docker-build.sh",
            "scripts/ai-gateway-docker-build.sh",
            "scripts/agent-broker-docker-build.sh",
            "scripts/cetirun-helm-release.sh",
            "scripts/platform-helm-release.sh",
            "scripts/agent-broker-helm-release.sh",
            "oci://${REGISTRY}/helm/cetirun-agent-broker",
            "oci://${REGISTRY}/helm/cetirun-platform",
        ):
            with self.subTest(value=value):
                self.assertIn(value, self.workflow)
        self.assertIn("Resolve immutable image digests", self.workflow)
        self.assertIn("spec: .spec", self.workflow)
        self.assertIn('cmp "${agents_before}" "${agents_after}"', self.workflow)
        self.assertIn(
            'del(.["cetirun-operator"].cetirun.helmValues.openclaw.image)',
            self.workflow,
        )
        self.assertIn(
            'del(.["cetirun-operator"].cetirun.helmValues.hermes.image)',
            self.workflow,
        )
        self.assertIn("--reset-values", self.workflow)
        self.assertIn('--values "${preserved_values_file}"', self.workflow)

    def test_portal_uses_an_explicit_accepted_pin(self):
        self.assertNotIn("bash scripts/portal-docker-build.sh", self.workflow)
        self.assertIn("secrets.PORTAL_IMAGE_TAG", self.workflow)
        self.assertIn("secrets.PORTAL_IMAGE_DIGEST", self.workflow)
        self.assertIn(
            'image: {tag: env.PORTAL_TAG, digest: env.PORTAL_DIGEST}',
            self.workflow,
        )
        self.assertIn(
            'global: {registry: env.REGISTRY, version: ""}',
            self.workflow,
        )
        self.assertIn(
            "Portal image: preserved at the environment-pinned tag and digest",
            self.workflow,
        )

    def test_runtime_reuses_an_explicit_accepted_base_pin(self):
        self.assertIn('BUILD_BASE: "false"', self.workflow)
        self.assertIn('BUILD_IMAGE: "false"', self.workflow)
        self.assertIn("secrets.BASE_IMAGE_TAG", self.workflow)
        self.assertIn("secrets.BASE_IMAGE_DIGEST", self.workflow)
        self.assertIn("secrets.OPENCLAW_IMAGE_TAG", self.workflow)
        self.assertIn("secrets.OPENCLAW_IMAGE_DIGEST", self.workflow)
        self.assertIn("secrets.HERMES_IMAGE_TAG", self.workflow)
        self.assertIn("secrets.HERMES_IMAGE_DIGEST", self.workflow)
        self.assertIn("secrets.DESKTOP_IMAGE_TAG", self.workflow)
        self.assertIn("secrets.DESKTOP_IMAGE_DIGEST", self.workflow)
        self.assertIn(
            'export BASE_IMAGE="${REGISTRY}/cetirun-base:${BASE_IMAGE_TAG}@${BASE_IMAGE_DIGEST}"',
            self.workflow,
        )
        self.assertIn(
            "Runtime base image: preserved at the environment-pinned tag and digest",
            self.workflow,
        )
        self.assertIn('CHART_DIR="${staged_chart}"', self.workflow)
        self.assertNotIn('openclaw: {image: {repository: (env.REGISTRY', self.workflow)
        self.assertNotIn('hermes: {image: {repository: (env.REGISTRY', self.workflow)
        self.assertIn(
            "OpenClaw image: preserved at the environment-pinned tag and digest",
            self.workflow,
        )
        self.assertIn(
            "Hermes and Desktop candidates: built from source; deployment preserved",
            self.workflow,
        )
        self.assertIn(
            "Protected runtime image pins do not match every existing built-in Agent",
            self.workflow,
        )
        self.assertIn('.spec.agent.image == $openclaw', self.workflow)
        self.assertIn('.spec.agent.image == $hermes', self.workflow)

    def test_application_e2e_uses_the_bounded_resource_profile(self):
        self.assertIn("E2E_RESOURCE_PROFILE: constrained", self.workflow)

    def test_original_runtime_cases_and_agent_identity_case_are_present(self):
        self.assertIn("AGENT_TYPE=openclaw scripts/e2e-test.sh", self.workflow)
        self.assertIn("AGENT_TYPE=hermes scripts/e2e-test.sh", self.workflow)
        self.assertNotIn("TEST_ALIAS=e2e-", self.workflow)
        self.assertIn('AGENT_IDENTITY_ENABLED: "true"', self.workflow)
        self.assertIn("secrets.E2E_OWNER_OBJECT_ID", self.workflow)
        for value in ("E2E_KUBECONFIG", "E2E_CONTEXT", "E2E_CLUSTER_UID"):
            with self.subTest(value=value):
                self.assertIn(value, self.workflow)
        self.assertGreaterEqual(self.workflow.count("secrets.E2E_CONTEXT"), 5)
        self.assertGreaterEqual(self.workflow.count("secrets.E2E_CLUSTER_UID"), 5)
        self.assertGreaterEqual(self.workflow.count("get namespace kube-system"), 4)
        for value in (
            "Testing suspend / unsuspend cycle",
            "Verifying gateway routing",
            "Verifying EnvoyFilter alias-check configuration",
            "Agent Identity to reach Ready",
            "cetirun-agent auth status",
            "Verifying Agent Identity recovery after a Broker restart",
            "Cleanup verified: owned Agent and namespace are absent",
        ):
            with self.subTest(value=value):
                self.assertIn(value, self.e2e_script)

    def test_e2e_failures_do_not_print_private_identifiers_or_image_refs(self):
        self.assertNotIn('kubectl describe agent "${TEST_ALIAS}"', self.e2e_script)
        self.assertNotIn("${EXPECTED_CLIENT_ID})", self.e2e_script)
        self.assertNotIn("expected '${EXPECTED_CLIENT_ID}'", self.e2e_script)
        self.assertNotIn("got '${ACTUAL_IMAGE}'", self.e2e_script)
        self.assertNotIn("login.microsoftonline.com: ${LOCATION}", self.e2e_script)

    def test_istio_upgrade_can_preserve_live_values_with_supplied_pfx(self):
        self.assertIn("HELM_RESET_THEN_REUSE_VALUES", self.istio_deploy)
        self.assertIn("HELM_ARGS+=(--reset-then-reuse-values)", self.istio_deploy)
        self.assertNotIn(
            'KEY_VAULT_NAME="${KEY_VAULT_NAME:?KEY_VAULT_NAME is required}"',
            self.istio_deploy,
        )
        self.assertIn(
            "KEY_VAULT_NAME is required when certificate material is not provided",
            self.istio_deploy,
        )


if __name__ == "__main__":
    unittest.main()
