# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Order of the operator CRD steps in the deployment helpers.

The helpers run against stub helm and kubectl commands that record each call,
so nothing contacts a cluster.
"""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
PLATFORM = ROOT / "scripts/cetirun-platform-deploy.sh"
OPERATOR = ROOT / "app/cetirun-operator/deploy/deploy.sh"

HELM = r"""#!/usr/bin/env bash
deployments() {
  for name in ${DEPLOYMENTS}; do
    printf -- '---\nkind: Deployment\nmetadata:\n  name: %s\n' "${name}"
  done
}
case "$1 $2" in
  "dependency build") exit 0 ;;
  "upgrade --install")
    if [[ " $* " == *" --dry-run=server "* ]]; then
      echo "helm plan" >> "${CALLS}"
      printf 'MANIFEST:\n'
      deployments
    else
      echo "helm upgrade" >> "${CALLS}"
    fi
    exit 0 ;;
  "get manifest") deployments; exit 0 ;;
  "show crds")
    echo "helm show crds" >> "${CALLS}"
    printf -- '---\nkind: CustomResourceDefinition\nmetadata:\n  name: agents.example.com\n'
    printf -- '---\nkind: CustomResourceDefinition\nmetadata:\n  name: sandboxes.example.com\n'
    exit 0 ;;
esac
echo "unexpected helm $*" >&2
exit 2
"""

KUBECTL = r"""#!/usr/bin/env bash
case "$1" in
  cluster-info|label) exit 0 ;;
  get)
    if [ "$2" = deployment ] && [[ "$*" == *".spec.selector.matchLabels"* ]]; then
      printf 'app.kubernetes.io/instance=example,app.kubernetes.io/name=%s,' "$3"
      exit 0
    fi
    if [ "$2" = pods ]; then
      while [ "$#" -gt 0 ]; do
        [ "$1" != -l ] || echo "list pods $2" >> "${CALLS}"
        shift
      done
      printf '%s' "${TERMINATING_PODS:-}"
    fi
    exit 0 ;;
  wait)
    if [[ " $* " == *" --for=condition=Established "* ]]; then
      cat >/dev/null
      echo "wait established" >> "${CALLS}"
      if [ -n "${FAIL_ESTABLISHED:-}" ]; then
        echo 'error: timed out waiting for the condition on customresourcedefinitions/sandboxes.example.com' >&2
        exit 1
      fi
      exit 0
    fi
    if [[ " $* " == *" --for=delete "* ]]; then
      pods=()
      for arg in "$@"; do
        [[ "${arg}" != pod/* ]] || pods+=("${arg}")
      done
      echo "wait deleted ${pods[*]}" >> "${CALLS}"
      if [ -n "${FAIL_POD_EXIT:-}" ]; then
        echo "error: timed out waiting for the condition on pods/${pods[0]#pod/}" >&2
        exit 1
      fi
      exit 0
    fi ;;
  rollout)
    name="${3#deployment/}"
    echo "rollout ${name}" >> "${CALLS}"
    [ "${name}" != "${FAIL_ROLLOUT:-}" ] || exit 1
    exit 0 ;;
  create)
    if [ "$2" = namespace ]; then
      printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: example\n'
      exit 0
    fi
    cat >/dev/null
    echo "create crds" >> "${CALLS}"
    if [ "${CREATE_MODE:-}" = forbidden ]; then
      echo 'Error from server (Forbidden): customresourcedefinitions.apiextensions.k8s.io is forbidden' >&2
      exit 1
    fi
    echo "customresourcedefinition.apiextensions.k8s.io/sandboxes.example.com created"
    echo 'Error from server (AlreadyExists): customresourcedefinitions.apiextensions.k8s.io "agents.example.com" already exists' >&2
    exit 1 ;;
  apply)
    cat >/dev/null
    if [[ " $* " == *" --server-side "* ]]; then
      echo "apply crds: $*" >> "${CALLS}"
    fi
    exit 0 ;;
esac
echo "unexpected kubectl $*" >&2
exit 2
"""

ALL = "cetirun-operator cetirun-sandbox-admission cetirun-portal"
ADMISSION_PODS = "list pods app.kubernetes.io/instance=example,app.kubernetes.io/name=cetirun-sandbox-admission"
APPLY = "apply crds: apply --server-side --force-conflicts --field-manager={} -f -"


class DeployCRDOrderTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        for name, body in (("helm", HELM), ("kubectl", KUBECTL)):
            stub = bin_dir / name
            stub.write_text(body)
            stub.chmod(0o755)
        self.chart = self.root / "chart"
        self.chart.mkdir()
        (self.chart / "Chart.yaml").write_text("name: example\n")
        self.calls = self.root / "calls.log"
        self.base_env = {
            **os.environ,
            "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
            "CALLS": str(self.calls),
        }

    def run_helper(self, script, **env):
        self.calls.write_text("")
        result = subprocess.run(
            ["bash", str(script)],
            env={**self.base_env, **env},
            capture_output=True, text=True, timeout=60,
        )
        calls = [line for line in self.calls.read_text().splitlines() if "oauth2-proxy" not in line]
        return result, calls

    def platform(self, **env):
        return self.run_helper(
            PLATFORM, DOMAIN="example.com", ISTIO_REVISION="asm-test",
            CHART_DIR=str(self.chart), **env,
        )

    def operator(self, **env):
        return self.run_helper(OPERATOR, LOCAL_CHART="1", **env)

    def test_platform_applies_crd_changes_after_admission_and_before_portal(self):
        result, calls = self.platform(DEPLOYMENTS=ALL)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, [
            "helm plan",
            "helm show crds",
            "create crds",
            "wait established",
            "helm upgrade",
            "rollout cetirun-operator",
            "rollout cetirun-sandbox-admission",
            ADMISSION_PODS,
            APPLY.format("cetirun-platform-deploy"),
            "rollout cetirun-portal",
        ])
        self.assertIn("sandboxes.example.com created", result.stdout)

    def test_platform_stops_before_the_upgrade_when_new_crds_are_not_established(self):
        result, calls = self.platform(DEPLOYMENTS=ALL, FAIL_ESTABLISHED="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not every operator CRD is Established", result.stderr)
        self.assertEqual(calls, ["helm plan", "helm show crds", "create crds", "wait established"])

    def test_platform_waits_for_replaced_admission_pods_before_applying_crd_changes(self):
        result, calls = self.platform(
            DEPLOYMENTS=ALL, TERMINATING_PODS="pod/admission-old-1 pod/admission-old-2 ",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[6:], [
            "rollout cetirun-sandbox-admission",
            ADMISSION_PODS,
            "wait deleted pod/admission-old-1 pod/admission-old-2",
            APPLY.format("cetirun-platform-deploy"),
            "rollout cetirun-portal",
        ])
        self.assertIn("Waiting for the replaced cetirun-sandbox-admission Pods to exit", result.stdout)

    def test_platform_leaves_crds_unchanged_when_replaced_admission_pods_do_not_exit(self):
        result, calls = self.platform(
            DEPLOYMENTS=ALL, TERMINATING_PODS="pod/admission-old-1 ", FAIL_POD_EXIT="1",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("existing CRDs were left unchanged", result.stderr)
        self.assertEqual(calls[-1], "wait deleted pod/admission-old-1")
        self.assertFalse([call for call in calls if call.startswith("apply crds")])

    def test_platform_leaves_crds_unchanged_when_admission_does_not_roll_out(self):
        result, calls = self.platform(DEPLOYMENTS=ALL, FAIL_ROLLOUT="cetirun-sandbox-admission")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls[-1], "rollout cetirun-sandbox-admission")
        self.assertFalse([call for call in calls if call.startswith("apply crds")])

    def test_platform_leaves_crds_unchanged_when_the_operator_does_not_roll_out(self):
        result, calls = self.platform(DEPLOYMENTS=ALL, FAIL_ROLLOUT="cetirun-operator")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls[-1], "rollout cetirun-operator")

    def test_platform_without_admission_waits_for_the_operator_only(self):
        result, calls = self.platform(DEPLOYMENTS="cetirun-operator cetirun-portal")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[4:], [
            "helm upgrade",
            "rollout cetirun-operator",
            APPLY.format("cetirun-platform-deploy"),
            "rollout cetirun-portal",
        ])

    def test_platform_without_the_operator_does_not_touch_crds(self):
        result, calls = self.platform(DEPLOYMENTS="cetirun-portal")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, ["helm plan", "helm upgrade", "rollout cetirun-portal"])

    def test_platform_stops_before_the_upgrade_when_crds_cannot_be_created(self):
        result, calls = self.platform(DEPLOYMENTS=ALL, CREATE_MODE="forbidden")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Forbidden", result.stderr)
        self.assertEqual(calls, ["helm plan", "helm show crds", "create crds"])

    def test_operator_helper_uses_the_same_order(self):
        result, calls = self.operator(DEPLOYMENTS="cetirun-operator cetirun-sandbox-admission")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, [
            "helm show crds",
            "create crds",
            "wait established",
            "helm upgrade",
            "rollout cetirun-operator",
            "rollout cetirun-sandbox-admission",
            ADMISSION_PODS,
            APPLY.format("cetirun-operator-deploy"),
        ])

    def test_operator_helper_stops_before_the_upgrade_when_new_crds_are_not_established(self):
        result, calls = self.operator(
            DEPLOYMENTS="cetirun-operator cetirun-sandbox-admission", FAIL_ESTABLISHED="1",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, ["helm show crds", "create crds", "wait established"])

    def test_operator_helper_leaves_crds_unchanged_when_replaced_admission_pods_do_not_exit(self):
        result, calls = self.operator(
            DEPLOYMENTS="cetirun-operator cetirun-sandbox-admission",
            TERMINATING_PODS="pod/admission-old-1 ", FAIL_POD_EXIT="1",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls[-1], "wait deleted pod/admission-old-1")
        self.assertFalse([call for call in calls if call.startswith("apply crds")])

    def test_operator_helper_leaves_crds_unchanged_when_admission_does_not_roll_out(self):
        result, calls = self.operator(
            DEPLOYMENTS="cetirun-operator cetirun-sandbox-admission", FAIL_ROLLOUT="cetirun-sandbox-admission",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls[-1], "rollout cetirun-sandbox-admission")


if __name__ == "__main__":
    unittest.main()
