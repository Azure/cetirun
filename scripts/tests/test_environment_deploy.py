# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Behavior tests for the environment deployment entry point."""

from __future__ import annotations

import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "cetirun-environment-deploy.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "environment-deploy.yml"


class EnvironmentDeployTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary_directory.cleanup)
        self.root = Path(self.temporary_directory.name)
        self.bin_directory = self.root / "bin"
        self.state_directory = self.root / "state"
        self.bin_directory.mkdir()
        self.state_directory.mkdir()
        self.command_log = self.state_directory / "commands.log"
        self.agent_fixture = self.state_directory / "agents.json"
        self.mesh_fixture = self.state_directory / "mesh-configmap.json"
        self.mesh_fixture.write_text(
            json.dumps(
                {
                    "metadata": {"resourceVersion": "1"},
                    "data": {
                        "mesh": """extensionProviders:
- name: oauth2-proxy
  envoyExtAuthzHttp:
    service: oauth2-proxy.cetirun-istio.svc.cluster.local
    port: 4180
    headersToUpstreamOnAllow:
    - x-auth-request-user
"""
                    },
                }
            ),
            encoding="utf-8",
        )
        self.agent_fixture.write_text(
            json.dumps(
                {
                    "items": [
                        {
                            "metadata": {"name": "existing-a", "uid": "uid-a", "generation": 2},
                            "spec": {
                                "agent": {"type": "hermes"},
                                "helm": {"version": "0.1.0-accepted.abcdef0"},
                            },
                            "status": {
                                "observedGeneration": 2,
                                "conditions": [{"type": "Ready", "status": "True"}],
                            },
                        },
                        {
                            "metadata": {"name": "existing-b", "uid": "uid-b", "generation": 1},
                            "spec": {
                                "agent": {"type": "hermes"},
                                "agentIdentity": {"enabled": False},
                                "helm": {"version": "0.1.0-accepted.abcdef0"},
                            },
                            "status": {
                                "observedGeneration": 1,
                                "conditions": [{"type": "Ready", "status": "True"}],
                            },
                        },
                    ]
                }
            ),
            encoding="utf-8",
        )
        self._write_executable("az", AZ_STUB)
        self._write_executable("helm", HELM_STUB)
        self._write_executable("kubectl", KUBECTL_STUB)

    def _write_executable(self, name: str, body: str) -> None:
        path = self.bin_directory / name
        path.write_text(body, encoding="utf-8")
        path.chmod(path.stat().st_mode | stat.S_IXUSR)

    def _environment(self, **overrides: str) -> dict[str, str]:
        digest = "sha256:" + "a" * 64
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_directory}:{environment['PATH']}",
                "FAKE_AGENT_FIXTURE": str(self.agent_fixture),
                "FAKE_COMMAND_LOG": str(self.command_log),
                "FAKE_MESH_FIXTURE": str(self.mesh_fixture),
                "FAKE_STATE": str(self.state_directory),
                "KUBECONFIG": str(self.state_directory / "kubeconfig"),
                "ACR_DOCKER_SECRET_NAME": "registry-pull",
                "ACR_NAME": "exampleregistry",
                "ACR_REGISTRY": "exampleregistry.azurecr.io",
                "AGENT_ID_AUTHORIZATION_CLIENT_ID": "authorization-client",
                "AGENT_ID_BLUEPRINT_CLIENT_ID": "blueprint-client",
                "AGENT_ID_ENCRYPTION_CLIENT_ID": "encryption-client",
                "AGENT_ID_KEY_VAULT_KEY_URL": "https://example.vault.azure.net/keys/cache/version",
                "AGENT_ID_TENANT_ID": "00000000-0000-0000-0000-000000000001",
                "AGENT_PERSISTENCE_ACCESS_MODE": "ReadWriteMany",
                "AGENT_PERSISTENCE_SIZE": "20Gi",
                "AGENT_PERSISTENCE_STORAGE_CLASS": "example-shared-storage",
                "AI_GATEWAY_IMAGE_DIGEST": digest,
                "AKS_CLUSTER_NAME": "example-aks",
                "AKS_RESOURCE_GROUP": "example-rg",
                "AGENT_PRESERVATION_ATTEMPTS": "1",
                "AGENT_PRESERVATION_INTERVAL_SECONDS": "0",
                "AZURE_SUBSCRIPTION_ID": "00000000-0000-0000-0000-000000000002",
                "BROKER_IMAGE_DIGEST": digest,
                "CETIRUN_DOMAIN": "agents.example.com",
                "DESKTOP_IMAGE_DIGEST": digest,
                "DESKTOP_IMAGE_TAG": "accepted-desktop",
                "HERMES_IMAGE_DIGEST": digest,
                "HERMES_IMAGE_TAG": "accepted-hermes",
                "OPENCLAW_IMAGE_DIGEST": digest,
                "OPENCLAW_IMAGE_TAG": "accepted",
                "OPERATOR_IMAGE_DIGEST": digest,
                "PORTAL_IMAGE_DIGEST": digest,
                "PORTAL_ADMIN_ALIASES": "alice,bob",
                "PORTAL_OIDC_CLIENT_ID": "portal-client",
                "REDIS_CONNECTION_URL": "redis://:do-not-log@example-redis:6379/0",
                "REDIS_SECRET_NAME": "redis-auth",
                "RELEASE_VERSION": "0.1.1-main.1.1.abcdef0",
                "TERMINAL_IMAGE_DIGEST": digest,
            }
        )
        environment.update(overrides)
        return environment

    def _run(self, **overrides: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", str(SCRIPT)],
            cwd=ROOT,
            env=self._environment(**overrides),
            check=False,
            capture_output=True,
            text=True,
        )

    def test_success_uses_atomic_upgrades_and_preserves_agents(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.command_log.read_text(encoding="utf-8")
        upgrade_lines = [line for line in log.splitlines() if line.startswith("helm upgrade ")]
        self.assertEqual(len(upgrade_lines), 3, log)
        self.assertTrue(all(" --atomic " in f" {line} " for line in upgrade_lines), log)
        self.assertIn("--reset-then-reuse-values", upgrade_lines[0])
        self.assertIn("--reset-then-reuse-values", upgrade_lines[2])
        self.assertNotIn("do-not-log", log)
        istio_values = json.loads(
            (self.state_directory / "cetirun-istio-values.json").read_text(encoding="utf-8")
        )
        self.assertEqual(
            istio_values["authPolicy"],
            {
                "adminAliases": ["alice", "bob"],
                "webhookBypassPaths": ["/{*}/api/messages"],
                "legacySubdomainWebhookBypassPaths": ["/api/messages"],
                "apexPostBypassPaths": ["/api/v1/agent-authorization/callback"],
            },
        )
        platform_values = json.loads(
            (self.state_directory / "cetirun-platform-values.json").read_text(encoding="utf-8")
        )
        self.assertEqual(
            platform_values["cetirun-operator"]["cetirun"]["helmValues"]["persistence"],
            {
                "storageClass": "example-shared-storage",
                "accessMode": "ReadWriteMany",
                "size": "20Gi",
            },
        )
        self.assertEqual(
            platform_values["cetirun-portal"]["admins"]["aliases"],
            ["alice", "bob"],
        )
        self.assertIn(
            "- authorization",
            json.loads(self.mesh_fixture.read_text(encoding="utf-8"))["data"]["mesh"],
        )
        self.assertLess(
            log.index("helm upgrade cetirun-istio"),
            log.index("kubectl get configmap istio-shared-configmap-asm-1-25"),
        )
        self.assertLess(
            log.index("kubectl patch configmap istio-shared-configmap-asm-1-25"),
            log.index("helm upgrade --install cetirun-agent-broker"),
        )
        self.assertGreaterEqual(log.count("kubectl get agents.cetirun.microsoft.com"), 2)
        self.assertNotIn("cmp -s", SCRIPT.read_text(encoding="utf-8"))
        self.assertIn(
            "kubectl get secret registry-pull --namespace cetirun-system",
            log,
        )
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("HERMES_IMAGE_TAG: ${{ secrets.HERMES_IMAGE_TAG }}", workflow)
        self.assertIn("DESKTOP_IMAGE_TAG: ${{ secrets.DESKTOP_IMAGE_TAG }}", workflow)

    def test_platform_failure_restores_prior_successful_stages(self) -> None:
        result = self._run(FAKE_FAIL_PLATFORM="true")
        self.assertNotEqual(result.returncode, 0)
        log = self.command_log.read_text(encoding="utf-8")
        self.assertIn("helm uninstall cetirun-agent-broker", log)
        self.assertIn("helm rollback cetirun-istio 3", log)
        self.assertLess(
            log.index("helm uninstall cetirun-agent-broker"),
            log.index("helm rollback cetirun-istio 3"),
        )

    def test_native_missing_runtime_defaults_may_be_pinned(self) -> None:
        result = self._run(FAKE_FILL_NATIVE_DEFAULTS="true")
        self.assertEqual(result.returncode, 0, result.stderr)
        updated = json.loads(self.agent_fixture.read_text(encoding="utf-8"))
        self.assertTrue(
            all(
                item["spec"]["agent"]["image"].startswith(
                    "exampleregistry.azurecr.io/hermes-gateway:accepted-hermes@sha256:"
                )
                and item["spec"]["agent"]["port"] == 8642
                and item["spec"]["helm"]["version"] == "0.1.0-accepted.abcdef0"
                for item in updated["items"]
            )
        )

    def test_existing_image_cannot_change_when_missing_port_is_filled(self) -> None:
        agents = json.loads(self.agent_fixture.read_text(encoding="utf-8"))
        for item in agents["items"]:
            item["spec"]["agent"]["image"] = "registry.example.com/runtime:original"
        self.agent_fixture.write_text(json.dumps(agents), encoding="utf-8")

        result = self._run(FAKE_REPLACE_NATIVE_RUNTIME="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("existing Agent collection", result.stderr)

    def test_existing_port_cannot_change_when_missing_image_is_filled(self) -> None:
        agents = json.loads(self.agent_fixture.read_text(encoding="utf-8"))
        for item in agents["items"]:
            item["spec"]["agent"]["port"] = 9000
        self.agent_fixture.write_text(json.dumps(agents), encoding="utf-8")

        result = self._run(FAKE_REPLACE_NATIVE_RUNTIME="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("existing Agent collection", result.stderr)

    def test_custom_agent_cannot_receive_native_default_exception(self) -> None:
        agents = json.loads(self.agent_fixture.read_text(encoding="utf-8"))
        for item in agents["items"]:
            item["spec"]["agent"]["type"] = "custom"
        self.agent_fixture.write_text(json.dumps(agents), encoding="utf-8")

        result = self._run(FAKE_FILL_CUSTOM_RUNTIME="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("existing Agent collection", result.stderr)

    def test_existing_agent_chart_version_cannot_change(self) -> None:
        result = self._run(FAKE_MUTATE_HELM_VERSION="true")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("existing Agent collection", result.stderr)

    def test_workflow_deploys_exact_validated_revision(self) -> None:
        workflow = WORKFLOW.read_text(encoding="utf-8")
        self.assertIn("on:\n  workflow_dispatch:\n", workflow)
        self.assertNotIn("\n  push:\n", workflow)
        self.assertIn("environment: cetiruntest", workflow)
        self.assertNotIn("environment: cetirun\n", workflow)
        self.assertIn("source_sha: ${{ steps.source.outputs.sha }}", workflow)
        self.assertIn("ref: ${{ needs.validate.outputs.source_sha }}", workflow)
        self.assertIn("${GITHUB_RUN_ATTEMPT}", workflow)
        self.assertEqual(workflow.count("id-token: write"), 1)
        self.assertIn("PORTAL_IMAGE_DIGEST: ${{ secrets.PORTAL_IMAGE_DIGEST }}", workflow)
        self.assertIn("PORTAL_ADMIN_ALIASES: ${{ secrets.PORTAL_ADMIN_ALIASES }}", workflow)
        self.assertIn(
            "AGENT_PERSISTENCE_STORAGE_CLASS: ${{ secrets.AGENT_PERSISTENCE_STORAGE_CLASS }}",
            workflow,
        )
        self.assertNotIn('echo "HERMES_IMAGE_DIGEST=$(image_digest hermes-gateway)"', workflow)
        self.assertNotIn('echo "DESKTOP_IMAGE_DIGEST=$(image_digest cetirun-desktop)"', workflow)
        self.assertNotIn("bash scripts/portal-docker-build.sh", workflow)

    def test_invalid_admin_aliases_fail_before_live_discovery(self) -> None:
        for aliases in ("Alice,bob", "alice,,bob", "alice,alice"):
            with self.subTest(aliases=aliases):
                self.command_log.write_text("", encoding="utf-8")
                result = self._run(PORTAL_ADMIN_ALIASES=aliases)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("PORTAL_ADMIN_ALIASES", result.stderr)
                log = self.command_log.read_text(encoding="utf-8")
                self.assertNotIn("az account show", log)
                self.assertNotIn("helm upgrade", log)

    def test_missing_broker_mesh_sidecar_rolls_back_before_platform(self) -> None:
        result = self._run(FAKE_MISSING_BROKER_SIDECAR="true")
        self.assertNotEqual(result.returncode, 0)
        log = self.command_log.read_text(encoding="utf-8")
        self.assertIn("helm uninstall cetirun-agent-broker", log)
        self.assertIn("helm rollback cetirun-istio 3", log)
        platform_upgrades = [
            line
            for line in log.splitlines()
            if line.startswith("helm upgrade cetirun-platform ")
        ]
        self.assertEqual(platform_upgrades, [])


AZ_STUB = r"""#!/usr/bin/env bash
set -euo pipefail
printf 'az' >>"${FAKE_COMMAND_LOG}"
printf ' %s' "$@" >>"${FAKE_COMMAND_LOG}"
printf '\n' >>"${FAKE_COMMAND_LOG}"
if [[ "$1 $2" == "account show" ]]; then
  exit 0
fi
if [[ "$1 $2" == "aks show" ]]; then
  printf '/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s\n' \
    "${AZURE_SUBSCRIPTION_ID}" "${AKS_RESOURCE_GROUP}" "${AKS_CLUSTER_NAME}"
  exit 0
fi
if [[ "$1 $2" == "aks get-credentials" ]]; then
  previous=""
  for argument in "$@"; do
    if [[ "${previous}" == "--file" ]]; then
      : >"${argument}"
      exit 0
    fi
    previous="${argument}"
  done
fi
exit 1
"""


HELM_STUB = r"""#!/usr/bin/env bash
set -euo pipefail
printf 'helm' >>"${FAKE_COMMAND_LOG}"
printf ' %s' "$@" >>"${FAKE_COMMAND_LOG}"
printf '\n' >>"${FAKE_COMMAND_LOG}"
command_name=$1
shift
case "${command_name}" in
  status)
    release=$1
    if [[ "${release}" == "cetirun-agent-broker" && ! -f "${FAKE_STATE}/broker-installed" ]]; then
      exit 1
    fi
    if [[ " $* " == *" --output json "* ]]; then
      if [[ "${release}" == "cetirun-istio" ]]; then
        printf '{"version":3}\n'
      elif [[ "${release}" == "cetirun-platform" ]]; then
        printf '{"version":5}\n'
      else
        printf '{"version":2}\n'
      fi
    fi
    ;;
  upgrade)
    release=""
    values_file=""
    previous=""
    for argument in "$@"; do
      case "${argument}" in
        cetirun-istio|cetirun-agent-broker|cetirun-platform) release="${argument}" ;;
      esac
      if [[ "${previous}" == "--values" ]]; then
        values_file="${argument}"
      fi
      previous="${argument}"
    done
    if [[ -n "${release}" && -n "${values_file}" ]]; then
      cp "${values_file}" "${FAKE_STATE}/${release}-values.json"
    fi
    if [[ "${release}" == "cetirun-platform" && "${FAKE_FAIL_PLATFORM:-}" == "true" ]]; then
      exit 42
    fi
    if [[ "${release}" == "cetirun-agent-broker" ]]; then
      : >"${FAKE_STATE}/broker-installed"
    fi
    if [[ "${release}" == "cetirun-platform" && "${FAKE_FILL_NATIVE_DEFAULTS:-}" == "true" ]]; then
      jq '
        .items |= map(
          . as $old |
          if .spec.agent.type == "hermes" then
            .spec.agent.image //= (env.ACR_REGISTRY + "/hermes-gateway:" + env.HERMES_IMAGE_TAG + "@" + env.HERMES_IMAGE_DIGEST) |
            .spec.agent.port //= 8642
          elif .spec.agent.type == "openclaw" then
            .spec.agent.image //= (env.ACR_REGISTRY + "/openclaw-gateway:" + env.OPENCLAW_IMAGE_TAG + "@" + env.OPENCLAW_IMAGE_DIGEST) |
            .spec.agent.port //= 18789
          else .
          end |
          if .spec.agent != $old.spec.agent then
            .metadata.generation += 1 | .status.observedGeneration += 1
          else .
          end
        )
      ' "${FAKE_AGENT_FIXTURE}" >"${FAKE_STATE}/agents.next.json"
      mv "${FAKE_STATE}/agents.next.json" "${FAKE_AGENT_FIXTURE}"
    fi
    if [[ "${release}" == "cetirun-platform" && "${FAKE_REPLACE_NATIVE_RUNTIME:-}" == "true" ]]; then
      jq '
        .items |= map(
          if .spec.agent.type == "hermes" then
            .spec.agent.image = (env.ACR_REGISTRY + "/hermes-gateway:" + env.HERMES_IMAGE_TAG + "@" + env.HERMES_IMAGE_DIGEST) |
            .spec.agent.port = 8642 |
            .metadata.generation += 1 |
            .status.observedGeneration += 1
          elif .spec.agent.type == "openclaw" then
            .spec.agent.image = (env.ACR_REGISTRY + "/openclaw-gateway:" + env.OPENCLAW_IMAGE_TAG + "@" + env.OPENCLAW_IMAGE_DIGEST) |
            .spec.agent.port = 18789 |
            .metadata.generation += 1 |
            .status.observedGeneration += 1
          else .
          end
        )
      ' "${FAKE_AGENT_FIXTURE}" >"${FAKE_STATE}/agents.next.json"
      mv "${FAKE_STATE}/agents.next.json" "${FAKE_AGENT_FIXTURE}"
    fi
    if [[ "${release}" == "cetirun-platform" && "${FAKE_FILL_CUSTOM_RUNTIME:-}" == "true" ]]; then
      jq '
        .items |= map(
          .spec.agent.image = "registry.example.com/custom:unexpected" |
          .spec.agent.port = 8080 |
          .metadata.generation += 1 |
          .status.observedGeneration += 1
        )
      ' "${FAKE_AGENT_FIXTURE}" >"${FAKE_STATE}/agents.next.json"
      mv "${FAKE_STATE}/agents.next.json" "${FAKE_AGENT_FIXTURE}"
    fi
    if [[ "${release}" == "cetirun-platform" && "${FAKE_MUTATE_HELM_VERSION:-}" == "true" ]]; then
      jq '
        .items |= map(
          .spec.helm.version = env.RELEASE_VERSION |
          .metadata.generation += 1 |
          .status.observedGeneration += 1
        )
      ' "${FAKE_AGENT_FIXTURE}" >"${FAKE_STATE}/agents.next.json"
      mv "${FAKE_STATE}/agents.next.json" "${FAKE_AGENT_FIXTURE}"
    fi
    ;;
  get)
    printf '{"cetirun-operator":{"agentIdentity":{"enabled":true}},"cetirun-portal":{"features":{"agentIdentityEnabled":true}}}\n'
    ;;
  uninstall)
    rm -f "${FAKE_STATE}/broker-installed"
    ;;
  rollback)
    ;;
  *)
    exit 1
    ;;
esac
"""


KUBECTL_STUB = r"""#!/usr/bin/env bash
set -euo pipefail
printf 'kubectl' >>"${FAKE_COMMAND_LOG}"
printf ' %s' "$@" >>"${FAKE_COMMAND_LOG}"
printf '\n' >>"${FAKE_COMMAND_LOG}"
case "$1" in
  cluster-info|apply|label|rollout)
    if [[ "$1" == "apply" ]]; then cat >/dev/null; fi
    ;;
  create)
    printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: cetirun-credentials\n'
    ;;
  get)
    if [[ "$2" == "pods" ]]; then
      if [[ " $* " == *" --namespace aks-istio-system "* ]]; then
        printf 'asm-1-25'
      elif [[ "${FAKE_MISSING_BROKER_SIDECAR:-}" == "true" ]]; then
        printf '%s\n' '{"items":[{"metadata":{"labels":{"azure.workload.identity/use":"true"}},"spec":{"containers":[{"name":"broker"},{"name":"auth-sdk"}]},"status":{"containerStatuses":[{"name":"broker","ready":true},{"name":"auth-sdk","ready":true}]}}]}'
      else
        printf '%s\n' '{"items":[{"metadata":{"labels":{"azure.workload.identity/use":"true"}},"spec":{"containers":[{"name":"broker"},{"name":"auth-sdk"}],"initContainers":[{"name":"istio-proxy"}]},"status":{"containerStatuses":[{"name":"broker","ready":true},{"name":"auth-sdk","ready":true}],"initContainerStatuses":[{"name":"istio-proxy","ready":true}]}}]}'
      fi
    elif [[ "$2" == "configmap" ]]; then
      cat "${FAKE_MESH_FIXTURE}"
    elif [[ "$2" == "agents.cetirun.microsoft.com" ]]; then
      cat "${FAKE_AGENT_FIXTURE}"
    elif [[ "$2" == "peerauthentication" ]]; then
      printf '%s\n' '{"spec":{"mtls":{"mode":"STRICT"}}}'
    elif [[ "$2" == "networkpolicy" ]]; then
      printf '%s\n' '{"metadata":{"name":"cetirun-agent-broker"}}'
    fi
    ;;
  patch)
    resource=$2
    name=$3
    patch_json=""
    patch_file=""
    previous=""
    for argument in "$@"; do
      if [[ "${previous}" == "--patch" ]]; then patch_json="${argument}"; fi
      if [[ "${previous}" == "--patch-file" ]]; then patch_file="${argument}"; fi
      previous="${argument}"
    done
    if [[ "${resource}" == "configmap" && -n "${patch_file}" ]]; then
      jq --slurpfile patch "${patch_file}" '
        .metadata.resourceVersion = ((.metadata.resourceVersion | tonumber) + 1 | tostring) |
        .data.mesh = $patch[0].data.mesh
      ' "${FAKE_MESH_FIXTURE}" >"${FAKE_STATE}/mesh.next.json"
      mv "${FAKE_STATE}/mesh.next.json" "${FAKE_MESH_FIXTURE}"
      exit 0
    fi
    exit 1
    ;;
  *)
    exit 1
    ;;
esac
"""


if __name__ == "__main__":
    unittest.main()
