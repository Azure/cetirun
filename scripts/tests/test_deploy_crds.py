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
  cluster-info|get|label) exit 0 ;;
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
        self.assertEqual(calls[:7], [
            "helm plan",
            "helm show crds",
            "create crds",
            "helm upgrade",
            "rollout cetirun-operator",
            "rollout cetirun-sandbox-admission",
            "apply crds: apply --server-side --force-conflicts --field-manager=cetirun-platform-deploy -f -",
        ])
        self.assertEqual(calls[7:], ["rollout cetirun-portal"])
        self.assertIn("sandboxes.example.com created", result.stdout)

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
            "rollout cetirun-operator",
            "apply crds: apply --server-side --force-conflicts --field-manager=cetirun-platform-deploy -f -",
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
            "helm upgrade",
            "rollout cetirun-operator",
            "rollout cetirun-sandbox-admission",
            "apply crds: apply --server-side --force-conflicts --field-manager=cetirun-operator-deploy -f -",
        ])

    def test_operator_helper_leaves_crds_unchanged_when_admission_does_not_roll_out(self):
        result, calls = self.operator(
            DEPLOYMENTS="cetirun-operator cetirun-sandbox-admission", FAIL_ROLLOUT="cetirun-sandbox-admission",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls[-1], "rollout cetirun-sandbox-admission")


if __name__ == "__main__":
    unittest.main()
