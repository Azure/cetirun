# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for cluster version preflight, Container Insights agent settings, and the monitoring of the template.

deploy.sh runs against stand-ins for az and kubectl that log their arguments.
The settings are created on a cluster that has none; settings a cluster
already has are checked and never replaced, since replacing them would drop
the cluster's own collection settings. The compiled template turns Container
Insights and managed Prometheus off, and creates Azure Data Explorer for the
ADX-Mon collection by default. Its Kusto scripts create every table that a
query reads, and the audit events and the alerter identity are optional.
"""

import json
import os
import pathlib
import re
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
DEPLOY = ROOT / "infra" / "cluster" / "deploy.sh"
SETTINGS_FILE = ROOT / "infra" / "cluster" / "container-azm-ms-agentconfig.yaml"
VALIDATOR = ROOT / "scripts" / "validate-cluster-kubernetes.py"

FAKE_BICEP = """#!/usr/bin/env sh
echo "bicep $*" >> "$CALLS_LOG"
[ "$FAKE_BICEP_FAIL" = "1" ] && exit 1
if [ "$1" = "build" ]; then
  cat "$FAKE_TEMPLATE"
else
  cat "$FAKE_BUNDLE"
fi
"""

FAKE_AZ = """#!/usr/bin/env sh
echo "az $*" >> "$CALLS_LOG"
case "$1 $2" in
  "aks update")
    if [ "$3" = "--help" ]; then
      [ "$FAKE_GATEWAY_UNSUPPORTED" = "1" ] && exit 1
      echo --enable-gateway-api
    fi
    if [ "$REPLACE_PARAMETERS" = "1" ]; then
      echo '{"parameters":{"kubernetesVersion":{"value":"1.34"}}}' > "$PARAM_FILE"
    fi ;;
  "account show")
    if [ "$FAKE_WRONG_TENANT" = "1" ]; then
      echo '{"id":"11111111-1111-1111-1111-111111111111","tenantId":"wrong"}'
    else
      echo '{"id":"11111111-1111-1111-1111-111111111111","tenantId":"22222222-2222-2222-2222-222222222222"}'
    fi ;;
  "aks get-credentials")
    touch "$KUBECONFIG"
    python3 -c 'import os, stat; assert stat.S_IMODE(os.stat(os.environ["KUBECONFIG"]).st_mode) == 0o600'
    [ "$FAKE_CREDENTIAL_FAILURE" = "1" ] && exit 1 ;;
  "feature show")
    if [ "$FAKE_FEATURE_PENDING" = "1" ]; then echo Registering; exit 0; fi
    if [ "$FAKE_REGISTER_FEATURE" = "1" ] && [ ! -f "$FEATURE_REGISTERED" ]; then echo NotRegistered; else echo Registered; fi ;;
  "feature register") touch "$FEATURE_REGISTERED" ;;
  "provider show")
    if [ "$FAKE_PROVIDER_PENDING" = "1" ]; then echo Registering; else echo Registered; fi ;;
  "group show") echo westus3 ;;
  "deployment group")
    if [ "$3" = "show" ]; then
      echo example-cluster
    else
      python3 -c '
import json, os, pathlib, stat, sys
args = sys.argv
template_path = pathlib.Path(args[args.index("--template-file") + 1])
params_arg = args[args.index("--parameters") + 1]
assert params_arg.startswith("@"), "Azure CLI JSON parameter files must use @path"
params_path = pathlib.Path(params_arg[1:])
template = json.loads(template_path.read_text())
params = json.loads(params_path.read_text())
version = params["parameters"].get("kubernetesVersion", {}).get("value", template["parameters"]["kubernetesVersion"]["defaultValue"])
assert stat.S_IMODE(template_path.parent.stat().st_mode) == 0o700
assert stat.S_IMODE(template_path.stat().st_mode) == 0o600
assert stat.S_IMODE(params_path.stat().st_mode) == 0o600
with open(os.environ["CALLS_LOG"], "a") as log:
    print("snapshot-version", version, file=log)
' "$@" || exit 1
      echo '{}'
    fi ;;
  "aks show")
    case "$*" in
      *oidcIssuerProfile*) echo https://issuer.example.invalid/ ;;
      *) echo '{"id":"/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/example-rg/providers/Microsoft.ContainerService/managedClusters/example-cluster","fqdn":"cluster.example.invalid"}' ;;
    esac ;;
esac
exit 0
"""

FAKE_KUBECTL = """#!/usr/bin/env sh
echo "kubectl $*" >> "$CALLS_LOG"
if [ "$1 $2" = "config view" ]; then
  if [ "$FAKE_WRONG_CLUSTER" = "1" ]; then
    echo '{"clusters":[{"cluster":{"server":"https://wrong.example.invalid"}}]}'
  else
    echo '{"clusters":[{"cluster":{"server":"https://cluster.example.invalid"}}]}'
  fi
fi
if [ "$1 $2" = "get configmap" ]; then
  [ -n "$FAKE_KUBECTL_FAIL" ] && exit 1
  case "$*" in
    *--ignore-not-found*)
      [ -f "$FAKE_SETTINGS" ] && echo configmap/container-azm-ms-agentconfig
      exit 0 ;;
    *jsonpath*)
      cat "$FAKE_SETTINGS"
      exit 0 ;;
  esac
fi
exit 0
"""


def shipped_log_settings():
    """The log-data-collection-settings text of the shipped settings file."""
    lines = SETTINGS_FILE.read_text(encoding="utf-8").splitlines()
    start = next(i for i, line in enumerate(lines) if line.startswith("  log-data-collection-settings:"))
    block = []
    for line in lines[start + 1:]:
        if line.strip() and not line.startswith("    "):
            break
        block.append(line[4:])
    return "\n".join(block) + "\n"


class ClusterDeployTests(unittest.TestCase):
    def run_deploy(self, existing_settings=None, kubectl_fails=False, *,
                   parameters=None, default_version="1.37", bicep_fails=False,
                   json_parameters=False, replace_parameters=False, extra_env=None):
        with tempfile.TemporaryDirectory() as directory:
            temp = pathlib.Path(directory)
            for name, body in (("az", FAKE_AZ), ("kubectl", FAKE_KUBECTL), ("bicep", FAKE_BICEP),
                               ("sleep", "#!/usr/bin/env sh\nexec /usr/bin/sleep 1\n")):
                stub = temp / name
                stub.write_text(body, encoding="utf-8")
                stub.chmod(0o755)
            calls = temp / "calls.log"
            calls.write_text("", encoding="utf-8")
            template = json.dumps({"parameters": {"kubernetesVersion": {"defaultValue": default_version}}})
            parameter_text = json.dumps({"parameters": parameters or {}})
            (temp / "template.json").write_text(template, encoding="utf-8")
            (temp / "bundle.json").write_text(json.dumps({
                "templateJson": template, "parametersJson": parameter_text,
            }), encoding="utf-8")
            parameter_file = temp / ("example.json" if json_parameters else "example.bicepparam")
            parameter_file.write_text(parameter_text, encoding="utf-8")
            environment = {
                **os.environ,
                "PATH": f"{temp}{os.pathsep}{os.environ['PATH']}",
                "CALLS_LOG": str(calls),
                "FAKE_SETTINGS": str(temp / "settings.toml"),
                "RESOURCE_GROUP": "example-rg",
                "AZURE_SUBSCRIPTION_ID": "11111111-1111-1111-1111-111111111111",
                "AZURE_TENANT_ID": "22222222-2222-2222-2222-222222222222",
                "KUBECONFIG": str(temp / "kubeconfig"),
                "FEATURE_REGISTERED": str(temp / "feature-registered"),
                "LOCATION": "westus3",
                "PARAM_FILE": str(parameter_file),
                "FAKE_TEMPLATE": str(temp / "template.json"),
                "FAKE_BUNDLE": str(temp / "bundle.json"),
                "FAKE_BICEP_FAIL": "1" if bicep_fails else "0",
                "FAKE_KUBECTL_FAIL": "1" if kubectl_fails else "",
                "REPLACE_PARAMETERS": "1" if replace_parameters else "0",
                "TMPDIR": str(temp),
            }
            environment.update(extra_env or {})
            if existing_settings is not None:
                (temp / "settings.toml").write_text(existing_settings, encoding="utf-8")
            result = subprocess.run(["bash", str(DEPLOY)], capture_output=True, text=True, env=environment,
                                    check=False)
            self.assertFalse(list(temp.glob("tmp.*")), "Deployment snapshots must be removed on exit")
            if replace_parameters:
                self.assertEqual(json.loads(parameter_file.read_text())["parameters"]["kubernetesVersion"]["value"], "1.34")
            return result, calls.read_text(encoding="utf-8").splitlines()

    def assert_preflight_blocked(self, result, calls):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("stable Kubernetes 1.37 or newer", result.stderr)
        self.assertFalse([c for c in calls if c.startswith(("az ", "kubectl "))], calls)

    def test_default_is_checked_before_any_azure_call(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("preflight passed: 1.37", result.stdout)
        self.assertTrue(calls[0].startswith("bicep build-params "), calls)
        self.assertIn(f"--bicep-file {ROOT / 'infra/cluster/main.bicep'}", calls[0])
        self.assertIn("--no-restore", calls[0])
        self.assertIn("--stdout", calls[0])

    def test_every_resource_call_has_explicit_subscription(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        for call in calls:
            if call.startswith("az ") and "--help" not in call:
                self.assertIn("--subscription 11111111-1111-1111-1111-111111111111", call)
        credentials = next(c for c in calls if c.startswith("az aks get-credentials"))
        self.assertIn("--file ", credentials)
        self.assertNotIn("az account set", "\n".join(calls))
        self.assertNotIn("az extension add", "\n".join(calls))

    def test_wrong_tenant_stops_before_cloud_writes(self):
        result, calls = self.run_deploy(extra_env={"FAKE_WRONG_TENANT": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("subscription/tenant", result.stderr)
        self.assertEqual([c for c in calls if c.startswith("az ")],
                         ["az account show --subscription 11111111-1111-1111-1111-111111111111 -o json"])

    def test_wrong_cluster_stops_before_kubernetes_writes(self):
        result, calls = self.run_deploy(extra_env={"FAKE_WRONG_CLUSTER": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("explicit AKS target", result.stderr)
        self.assert_no_settings_written(calls)

    def test_unsupported_cli_stops_without_installing_extensions(self):
        result, calls = self.run_deploy(extra_env={"FAKE_GATEWAY_UNSUPPORTED": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("pinned Azure CLI", result.stderr)
        self.assertFalse(any(c.startswith("az group create") for c in calls))
        self.assertFalse(any(c.startswith("az extension add") for c in calls))

    def test_unsafe_kubeconfig_paths_stop_before_azure_calls(self):
        for path in ("relative", "/tmp/one:/tmp/two", str(pathlib.Path.home() / ".kube/config")):
            with self.subTest(path=path):
                result, calls = self.run_deploy(extra_env={"KUBECONFIG": path})
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(c.startswith(("az ", "kubectl ")) for c in calls))

    def test_feature_and_provider_registration_precede_deployment(self):
        result, calls = self.run_deploy(extra_env={"FAKE_REGISTER_FEATURE": "1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        feature = next(i for i, c in enumerate(calls) if c.startswith("az feature register"))
        provider = next(i for i, c in enumerate(calls) if c.startswith("az provider register"))
        ready = next(i for i, c in enumerate(calls) if c.startswith("az provider show"))
        create = next(i for i, c in enumerate(calls) if c.startswith("az group create"))
        self.assertLess(feature, provider)
        self.assertLess(provider, ready)
        self.assertLess(ready, create)

    def test_previously_registered_feature_still_propagates_provider(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any(c.startswith("az feature register") for c in calls))
        self.assertTrue(any(c.startswith("az provider register") for c in calls))
        self.assertTrue(any(c.startswith("az provider show") for c in calls))

    def test_failed_credential_command_leaves_private_permissions(self):
        result, calls = self.run_deploy(extra_env={"FAKE_CREDENTIAL_FAILURE": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("AssertionError", result.stderr)
        self.assert_no_settings_written(calls)

    def test_registration_timeouts_stop_before_resource_group_writes(self):
        for variable in ("FAKE_FEATURE_PENDING", "FAKE_PROVIDER_PENDING"):
            with self.subTest(variable=variable):
                result, calls = self.run_deploy(extra_env={variable: "1", "GATEWAY_REGISTRATION_TIMEOUT": "1"})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("registration is still pending", result.stderr)
                self.assertFalse(any(c.startswith("az group create") for c in calls))

    def test_hardlink_to_default_kubeconfig_is_rejected_without_modifying_it(self):
        # Exercise the actual local guard with synthetic files. Do not inspect
        # or link the developer's real kubeconfig or change their HOME.
        guard = DEPLOY.read_text().split("<<'PYCONFIG'\n", 1)[1].split("\nPYCONFIG", 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            default = root / ".kube/config"
            default.parent.mkdir()
            default.write_text("synthetic-default")
            alias = root / "private-config"
            os.link(default, alias)
            script = "from pathlib import Path\nPath.home = classmethod(lambda cls: Path(" + repr(directory) + "))\n" + guard
            result = subprocess.run([sys.executable, "-c", script],
                                    env={**os.environ, "KUBECONFIG": str(alias)}, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("outside the default kubeconfig", result.stderr)
            self.assertEqual(default.read_text(), "synthetic-default")

    def test_an_outdated_template_default_also_stops_before_writes(self):
        self.assert_preflight_blocked(*self.run_deploy(default_version="1.34"))

    def test_old_prerelease_and_unresolved_overrides_stop_before_writes(self):
        versions = ("1.34", "1.34.12", "1.36.99", "1.9.9", "1.37.0-alpha.1",
                    "1.38.0-rc.1", "", "latest", "v1.37.0", "1.37.0+build", "1.037",
                    "[parameters('version')]", "private-version-sentinel", None, 137, {})
        for use_json in (False, True):
            for version in versions:
                with self.subTest(version=version, json_parameters=use_json):
                    result, calls = self.run_deploy(parameters={"kubernetesVersion": {"value": version}},
                                                   json_parameters=use_json)
                    self.assert_preflight_blocked(result, calls)
                    self.assertNotIn("private-version-sentinel", result.stdout + result.stderr)

    def test_stable_minimum_patch_and_newer_overrides_are_accepted(self):
        for use_json in (False, True):
            for version in ("1.37", "1.37.0", "1.37.12", "1.38", "1.100.0", "2.0.0"):
                with self.subTest(version=version, json_parameters=use_json):
                    result, calls = self.run_deploy(parameters={"kubernetesVersion": {"value": version}},
                                                   json_parameters=use_json)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn(f"preflight passed: {version}", result.stdout)
                    self.assertTrue(any(c.startswith("az deployment group create") for c in calls), calls)

    def test_compiler_failure_stops_before_writes(self):
        self.assert_preflight_blocked(*self.run_deploy(bicep_fails=True))

    def test_reference_cannot_silently_select_the_default(self):
        self.assert_preflight_blocked(*self.run_deploy(parameters={"kubernetesVersion": {"reference": {}}}))

    def test_malformed_json_parameters_stop_before_writes(self):
        self.assert_preflight_blocked(*self.run_deploy(parameters=["unexpected"], json_parameters=True))

    def test_deployment_uses_validated_snapshot_after_source_is_replaced(self):
        for use_json in (False, True):
            with self.subTest(json_parameters=use_json):
                result, calls = self.run_deploy(parameters={"kubernetesVersion": {"value": "1.38.1"}},
                                               json_parameters=use_json, replace_parameters=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual([c for c in calls if c.startswith("snapshot-version")],
                                 ["snapshot-version 1.38.1", "snapshot-version 1.38.1"])

    def assert_no_settings_written(self, calls):
        self.assertFalse([c for c in calls if c.startswith(("kubectl create", "kubectl apply", "kubectl patch",
                                                           "kubectl replace", "kubectl edit"))], calls)

    def test_a_cluster_without_settings_gets_them_before_anything_else(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        created = [i for i, c in enumerate(calls) if c == f"kubectl create -f {SETTINGS_FILE}"]
        self.assertEqual(len(created), 1, calls)
        gateway_api = [i for i, c in enumerate(calls) if c.startswith("az aks update") and "--help" not in c]
        self.assertTrue(gateway_api and created[0] < gateway_api[0], calls)

    def test_the_shipped_settings_pass_the_check(self):
        result, calls = self.run_deploy(existing_settings=shipped_log_settings())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_no_settings_written(calls)
        self.assertIn("left unchanged", result.stdout)
        self.assertNotIn("multiline", result.stderr)

    def test_existing_settings_that_turn_collection_off_are_left_unchanged(self):
        settings = textwrap.dedent("""\
            [log_collection_settings]
               [log_collection_settings.stdout]
                  enabled = true
                  exclude_namespaces = ["kube-system", "team-a"]
               [log_collection_settings.env_var]
                  enabled=false # set by the cluster's owner
            """)
        result, calls = self.run_deploy(existing_settings=settings)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_no_settings_written(calls)
        self.assertIn("multiline", result.stderr)
        self.assertTrue([c for c in calls if c.startswith("az aks update") and "--help" not in c], calls)

    def test_existing_settings_that_keep_collection_on_stop_the_script(self):
        cases = {
            "collection on": "[log_collection_settings.env_var]\n   enabled = true\n",
            "no env_var section": "[log_collection_settings.stdout]\n   exclude_namespaces = [\"team-a\"]\n",
            "off in another section": "[log_collection_settings.stdout]\n   enabled = false\n",
            "no log settings": "",
        }
        for name, settings in cases.items():
            with self.subTest(name):
                result, calls = self.run_deploy(existing_settings=settings)
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn("left unchanged", result.stderr)
                self.assert_no_settings_written(calls)
                self.assertFalse([c for c in calls if c.startswith("az aks update") and "--help" not in c], calls)

    def test_settings_that_cannot_be_read_stop_the_script(self):
        result, calls = self.run_deploy(kubectl_fails=True)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assert_no_settings_written(calls)
        self.assertFalse([c for c in calls if c.startswith("az aks update") and "--help" not in c], calls)


@unittest.skipUnless(shutil.which("bicep"), "Bicep CLI is required for compilation regression tests")
class CompiledVersionTests(unittest.TestCase):
    def test_real_template_default_and_computed_overrides(self):
        with tempfile.TemporaryDirectory() as directory:
            temp = pathlib.Path(directory)
            template = ROOT / "infra/cluster/main.bicep"
            params = temp / "example.bicepparam"
            required = f"using '{os.path.relpath(template, temp)}'\n" + textwrap.dedent("""\
                param resourceGroupName = 'example-rg'
                param location = 'westus3'
                param clusterName = 'example-cluster'
                param dnsPrefix = 'example-cluster'
                param nodeResourceGroup = 'example-rg-nodes'
                """)
            for extra, expected in (
                ("", "1.37"),
                ("var selected = '1.34.12'\nparam kubernetesVersion = selected\n", None),
                ("param kubernetesVersion = '1.38.1'\n", "1.38.1"),
            ):
                with self.subTest(extra=extra):
                    params.write_text(required + extra, encoding="utf-8")
                    result = subprocess.run(
                        [sys.executable, "-B", str(VALIDATOR), "--template", str(template),
                         "--parameters", str(params)], capture_output=True, text=True, check=False,
                    )
                    if expected is None:
                        self.assertEqual(result.returncode, 1, result.stdout)
                        self.assertIn("stable Kubernetes 1.37 or newer", result.stderr)
                    else:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn(f"preflight passed: {expected}", result.stdout)
            self.assertEqual(list(temp.iterdir()), [params], "Compilation must not leave generated files")


def template_resources(node):
    """The resources of a compiled template, which lists them or maps them by symbolic name."""
    items = node.get("resources", [])
    return list(items.values() if isinstance(items, dict) else items)


@unittest.skipUnless(shutil.which("bicep"), "Bicep CLI is required for compilation regression tests")
class CompiledMonitoringTests(unittest.TestCase):
    def test_every_pool_uses_coredns_without_node_local_redirection(self):
        compiled = subprocess.run(["bicep", "build", str(ROOT / "infra/cluster/modules/aks-cluster.bicep"),
                                   "--stdout", "--no-restore"], capture_output=True, text=True, check=True)
        cluster = next(resource for resource in template_resources(json.loads(compiled.stdout))
                       if resource["type"] == "Microsoft.ContainerService/managedClusters")
        pools = cluster["properties"]["agentPoolProfiles"]
        self.assertEqual({pool["name"] for pool in pools}, {"system", "agent", "agent2"})
        for pool in pools:
            with self.subTest(pool=pool["name"]):
                self.assertEqual(pool["localDNSProfile"], {"mode": "Disabled"})

    def test_azure_monitor_is_off_and_azure_data_explorer_is_the_default(self):
        compiled = subprocess.run(["bicep", "build", str(ROOT / "infra/cluster/main.bicep"), "--stdout", "--no-restore"],
                                  capture_output=True, text=True, check=True)
        template = json.loads(compiled.stdout)
        self.assertEqual(template["parameters"]["adxMode"]["defaultValue"], "create")
        for name in ("enableContainerInsights", "enablePrometheusMetrics", "logAnalyticsWorkspaceId"):
            self.assertNotIn(name, template["parameters"])

        def resources(node):
            items = node.get("resources", [])
            for resource in items.values() if isinstance(items, dict) else items:
                yield resource
                yield from resources(resource.get("properties", {}).get("template", {}))

        everything = list(resources(template))
        monitoring = [resource["type"] for resource in everything if resource["type"].split("/")[0].lower()
                      in ("microsoft.operationalinsights", "microsoft.monitor", "microsoft.insights")]
        # The only one is the diagnostic setting of the audit events, in a module that runs only with adxAuditLogs.
        self.assertEqual(monitoring, ["Microsoft.Insights/diagnosticSettings"], "No workspace or data collection rule")
        audit = next(resource for resource in template_resources(template)
                     if resource["type"] == "Microsoft.Resources/deployments" and resource["name"] == "deploy-aks-audit")
        self.assertIn("parameters('adxAuditLogs')", audit["condition"])
        self.assertEqual([item["type"] for item in resources(audit["properties"]["template"])
                          if item["type"].startswith("Microsoft.Insights/")], ["Microsoft.Insights/diagnosticSettings"])
        self.assertIs(template["parameters"]["adxAuditLogs"]["defaultValue"], False)
        clusters = [resource for resource in everything if resource["type"] == "Microsoft.ContainerService/managedClusters"
                    and not resource.get("existing")]
        self.assertEqual(len(clusters), 1)
        # Off explicitly, so that a deployment over a cluster from an earlier template turns them off.
        self.assertIs(clusters[0]["properties"]["addonProfiles"]["omsagent"]["enabled"], False)
        self.assertIs(clusters[0]["properties"]["azureMonitorProfile"]["metrics"]["enabled"], False)


ADX_SCRIPTS = ROOT / "infra" / "cluster" / "modules" / "adx"
LOG_COLUMNS = ("Timestamp:datetime, ObservedTimestamp:datetime, TraceId:string, SpanId:string, SeverityText:string, "
               "SeverityNumber:int, Body:dynamic, Resource:dynamic, Attributes:dynamic, Cluster:string, Container:string, "
               "Namespace:string, Pod:string")
METRIC_COLUMNS = ("Timestamp:datetime, SeriesId:long, Labels:dynamic, Value:real, Container:string, Namespace:string, "
                  "Pod:string, Cluster:string, Host:string")
# A metric table: its metric name in PascalCase, from a component that CetiRun collects.
METRIC_TABLE = r"\b((?:Adxmon|Kube|Litellm|Cetirun)[A-Z]\w*)\b"
LOG_TABLE = r"\b(Platform|Gateway|Ingress|Telemetry|Kubelet|Containerd)\b"


def kusto_commands(name):
    """The management commands of a script as the ADX module runs it: without its comment lines."""
    text = "\n".join(line for line in (ADX_SCRIPTS / name).read_text().splitlines() if not line.startswith("//"))
    return [block.strip() for block in re.split(r"\n\s*\n", text) if block.strip()]


def created_tables(name):
    tables = {}
    for command in kusto_commands(name):
        if command.startswith(".create-merge tables"):
            tables.update(re.findall(r"^\s+(\w+) \((.*)\),?$", command, re.M))
    return tables


class AdxScriptTests(unittest.TestCase):
    def test_each_command_is_one_block(self):
        # A blank line ends a command: the module joins the scripts with one.
        for path in ADX_SCRIPTS.glob("*.kql"):
            for command in kusto_commands(path.name):
                self.assertTrue(command.startswith("."), f"{path.name}: {command[:60]}")

    def test_every_table_that_a_query_reads_exists_before_its_rows(self):
        logs, metrics = created_tables("cetirun-logs.kql"), created_tables("cetirun-metrics.kql")
        agents = created_tables("cetirun-agents.kql")
        self.assertEqual(set(logs), {"Platform", "Gateway", "Ingress", "Telemetry", "Kubelet", "Containerd"})
        self.assertEqual(set(agents), {"AgentLogs", "CopilotProxy"})
        # The columns that the ADX-Mon ingestor gives a log table and a metric table.
        self.assertEqual(set([*logs.values(), *agents.values()]), {LOG_COLUMNS})
        self.assertEqual(set(metrics.values()), {METRIC_COLUMNS})
        health = (ADX_SCRIPTS / "cetirun-health.kql").read_text()
        dashboard = json.loads((ROOT / "app/cetirun-observability/dashboards/cetirun.json").read_text())
        databases = {source["id"]: source["database"] for source in dashboard["dataSources"]}
        texts = {"CetiRunLogs": [], "CetiRunMetrics": [health]}
        for query in dashboard["queries"]:
            texts[databases[query["dataSource"]["dataSourceId"]]].append(query["text"])
        # A metric table that does not exist yet fails the whole query, CollectionHealth() included.
        read = {table for text in texts["CetiRunMetrics"] for table in re.findall(METRIC_TABLE, text)}
        self.assertIn("AdxmonCollectorLogsDropped", read)
        self.assertLessEqual(read, set(metrics))
        # The log tables that the functions, the health check and the dashboard read.
        log_reads = set(re.findall(r'database\("__LOGS_DATABASE__"\)\.(\w+)', health))
        log_reads |= set(re.findall(r"^ {4}(\w+)$", (ADX_SCRIPTS / "cetirun-logs.kql").read_text(), re.M))
        log_reads |= {table for text in texts["CetiRunLogs"] for table in re.findall(LOG_TABLE, text)}
        self.assertEqual(log_reads - set(logs), set())
        self.assertIn("AgentLogs", re.findall(r"^ {4}(\w+)$", (ADX_SCRIPTS / "cetirun-agents.kql").read_text(), re.M))

    def test_lifted_columns_follow_the_chart(self):
        # The collector lifts labels and resources into columns in the order of their names.
        helpers = (ROOT / "app/cetirun-observability/chart/templates/_helpers.tpl").read_text()

        def lifted(key):
            block = re.search(rf"^{key} = \[\n(.*?)^\]", helpers, re.M | re.S)[1]
            pairs = re.findall(r'\{ name = "(\w+)", column = "(\w+)" \}', block)
            return ", ".join(f"{column}:string" for _, column in sorted(pairs, key=lambda pair: pair[0].lower()))

        self.assertTrue(METRIC_COLUMNS.endswith("Value:real, " + lifted("lift-labels")))
        self.assertTrue(LOG_COLUMNS.endswith("Attributes:dynamic, " + lifted("lift-resources")))

    def test_gateway_daily_usage_reads_only_its_table_and_follows_it(self):
        create, retention, caching = kusto_commands("cetirun-gateway-usage.kql")
        self.assertTrue(create.startswith(".create ifnotexists materialized-view with (folder = "))
        # No backfill: Kusto refuses it while earlier rows of the table are outside
        # the hot cache, which would fail the script on every existing database.
        self.assertNotIn("backfill", create)
        self.assertIn(" GatewayDailyUsageV2 on table Gateway {", create)
        # A materialized view reads its source table only.
        self.assertEqual(re.findall(r"^ {4}(\w+)$", create, re.M), ["Gateway"])
        self.assertTrue(retention.startswith(".alter materialized-view GatewayDailyUsageV2 policy retention"))
        self.assertIn("__RETENTION_DAYS__.00:00:00", retention)
        self.assertTrue(caching.startswith(".alter materialized-view GatewayDailyUsageV2 policy caching"))
        # The view of earlier templates keeps its rows: the script leaves it alone.
        commands = "\n".join(kusto_commands("cetirun-gateway-usage.kql"))
        self.assertNotRegex(commands, r"\bGatewayDailyUsage\b(?!V2)")
        module = (ROOT / "infra/cluster/modules/adx.bicep").read_text()
        line = next(text for text in module.splitlines() if text.startswith("var logsFunctions"))
        # After the script that creates the Gateway table, with the retention of the template.
        self.assertLess(line.index("adx/cetirun-logs.kql"), line.index("adx/cetirun-gateway-usage.kql"))
        self.assertIn("'__RETENTION_DAYS__', string(gatewayUsageRetentionDays)", line)

    def test_gateway_daily_usage_groups_by_identities_and_copilot_routes(self):
        create = kusto_commands("cetirun-gateway-usage.kql")[0]
        aggregations, keys = create.split("\n        by ", 1)
        # Agents by their UID and users by the user ID of the key: names and
        # aliases can repeat.
        for key in ("AgentUid = tostring(Metadata['cetirun_agent_uid'])", "Agent = tostring(Metadata['cetirun_agent'])",
                    "UserId = tostring(Body['user_id'])", "Owner = tostring(Metadata['cetirun_user'])",
                    "Model = tostring(Body['model'])", "ProviderModel = tostring(Body['provider_model'])",
                    "Provider = tostring(Body['provider'])",
                    "CopilotRoute = tostring(Body['deployment_id']) startswith \"cetirun-copilot-\""):
            self.assertIn(key, keys)
        for aggregation in ("Calls = count()", "Spend = sum(toreal(Body['spend']))",
                            "CacheCreationTokens = sum(tolong(Body['cache_creation_tokens']))"):
            self.assertIn(aggregation, aggregations)

    def test_copilot_routes_are_told_apart_by_the_operators_prefix(self):
        operator = (ROOT / "app/cetirun-operator/internal/controller/ai_gateway_copilot.go").read_text()
        prefix = re.search(r'aiGatewayCopilotGroupPrefix = "([^"]+)"', operator)[1]
        # A Copilot route's ID is the prefix, the Agent UID, the model and a revision.
        self.assertIn('fmt.Sprintf("%s%s-%s-%s", aiGatewayCopilotGroupPrefix, instance.UID, model, revision)', operator)
        # The request log names the deployment by LiteLLM's model ID, which is the route's ID.
        request_log = (ROOT / "app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py").read_text()
        self.assertIn('"deployment_id": clip(payload.get("model_id"))', request_log)
        for name in ("cetirun-gateway-usage.kql", "cetirun-logs.kql"):
            self.assertIn(f"CopilotRoute = tostring(Body['deployment_id']) startswith \"{prefix}\"",
                          (ADX_SCRIPTS / name).read_text())

    def test_gateway_coverage_counts_the_heartbeats_and_follows_the_usage_view(self):
        create, retention, caching, gaps = kusto_commands("cetirun-gateway-coverage.kql")
        self.assertTrue(create.startswith(".create ifnotexists materialized-view with (folder = "))
        # No backfill, for the reason of the usage view.
        self.assertNotIn("backfill", create)
        self.assertIn(" GatewayCoverage on table Gateway {", create)
        self.assertEqual(re.findall(r"^ {4}(\w+)$", create, re.M), ["Gateway"])
        # The heartbeats that the AI Gateway image writes, per 10 minutes, cluster and Pod.
        request_log = (ROOT / "app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py").read_text()
        log_type = re.search(r'^HEARTBEAT_LOG_TYPE = "([^"]+)"$', request_log, re.M)[1]
        self.assertIn(f"tostring(Body['log_type']) == \"{log_type}\"", create)
        self.assertIn("by Interval = bin(Timestamp, 10m), Cluster, Pod", create)
        # A running Pod writes two heartbeats in each interval, so one late line leaves it covered.
        interval = int(re.search(r"^HEARTBEAT_INTERVAL_SECONDS = (\d+)$", request_log, re.M)[1])
        self.assertEqual(600 % interval, 0)
        self.assertGreaterEqual(600 // interval, 2)
        # Kept as long as the usage view.
        self.assertTrue(retention.startswith(".alter materialized-view GatewayCoverage policy retention"))
        self.assertIn("__RETENTION_DAYS__.00:00:00", retention)
        self.assertTrue(caching.startswith(".alter materialized-view GatewayCoverage policy caching"))
        self.assertIn("GatewayCoverageGaps(cluster:string, fromTime:datetime, toTime:datetime)", gaps)
        self.assertIn("join kind=leftanti covered on Interval", gaps)
        module = (ROOT / "infra/cluster/modules/adx.bicep").read_text()
        line = next(text for text in module.splitlines() if text.startswith("var logsFunctions"))
        # After the usage view, with the same retention.
        self.assertLess(line.index("adx/cetirun-gateway-usage.kql"), line.index("adx/cetirun-gateway-coverage.kql"))
        self.assertEqual(line.count("'__RETENTION_DAYS__', string(gatewayUsageRetentionDays)"), 2)

    def test_audit_messages_land_whole_and_expand_into_events(self):
        table, mapping, function = kusto_commands("cetirun-audit.kql")
        self.assertEqual(table, ".create-merge table KubeAuditLogs (Records:dynamic)")
        self.assertIn('KubeAuditLogs ingestion json mapping "KubeAuditLogsMapping"', mapping)
        self.assertEqual(json.loads(re.search(r"'(\[.*\])'", mapping)[1]),
                         [{"column": "Records", "path": "$.records", "datatype": "dynamic"}])
        self.assertIn("AuditEvents()", function)
        self.assertIn("mv-expand Record = Records", function)
        connection = (ROOT / "infra/cluster/modules/adx-audit-connection.bicep").read_text()
        # A message is {"records": [...]}: MULTIJSON takes the whole message as one row.
        for setting in ("dataFormat: 'MULTIJSON'", "tableName: 'KubeAuditLogs'", "mappingRuleName: 'KubeAuditLogsMapping'",
                        "managedIdentityResourceId: cluster.id"):
            self.assertIn(setting, connection)


@unittest.skipUnless(shutil.which("bicep"), "Bicep CLI is required for compilation regression tests")
class CompiledObservabilityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        compiled = subprocess.run(["bicep", "build", str(ROOT / "infra/cluster/main.bicep"), "--stdout", "--no-restore"],
                                  capture_output=True, text=True, check=True)
        cls.template = json.loads(compiled.stdout)

    def module(self, name):
        return next(resource for resource in template_resources(self.template)
                    if resource["type"] == "Microsoft.Resources/deployments" and resource["name"] == name)

    def test_audit_logs_and_the_alerter_are_off_by_default(self):
        parameters = self.template["parameters"]
        self.assertIs(parameters["adxAuditLogs"]["defaultValue"], False)
        self.assertIs(parameters["adxAlerter"]["defaultValue"], False)
        self.assertEqual(parameters["adxGatewayUsageRetentionDays"]["defaultValue"], 400)
        for name in ("deploy-aks-audit", "deploy-adx-audit-connection"):
            self.assertIn("parameters('adxAuditLogs')", self.module(name)["condition"])
        self.assertIn("parameters('adxAlerter')", self.module("deploy-adx-alerter-identity")["condition"])

    def test_the_alerter_identity_belongs_to_the_alerter_service_account(self):
        parameters = self.module("deploy-adx-alerter-identity")["properties"]["parameters"]
        self.assertEqual(parameters["serviceAccountName"]["value"], "cetirun-adx-mon-alerter")
        self.assertEqual(parameters["namespace"]["value"], "[parameters('observabilityNamespace')]")

    def test_audit_events_go_to_one_hub_that_only_the_kusto_cluster_reads(self):
        resources = {resource["type"]: resource for resource in
                     template_resources(self.module("deploy-aks-audit")["properties"]["template"])
                     if not resource.get("existing")}
        setting = resources["Microsoft.Insights/diagnosticSettings"]["properties"]
        # kube-audit-admin: every request except get and list.
        self.assertEqual(setting["logs"], [{"category": "kube-audit-admin", "enabled": True}])
        self.assertIn("RootManageSharedAccessKey", setting["eventHubAuthorizationRuleId"])
        self.assertEqual(resources["Microsoft.EventHub/namespaces"]["sku"]["name"], "Standard")
        role = resources["Microsoft.Authorization/roleAssignments"]
        audit = self.module("deploy-aks-audit")["properties"]["template"]
        # Azure Event Hubs Data Receiver, on the namespace.
        self.assertEqual(audit["variables"]["receiverRoleId"], "a638d3c7-ab3a-418d-83e6-5f17a39d4fde")
        self.assertIn("variables('receiverRoleId')", role["properties"]["roleDefinitionId"])
        self.assertIn("Microsoft.EventHub/namespaces", role["scope"])

    def test_each_event_hub_gets_a_data_connection_of_its_own(self):
        # CetiRun clusters that share the Kusto cluster share the audit database too. With one fixed
        # name, the last deployment would point the connection of every cluster at its own hub.
        template = self.module("deploy-adx-audit-connection")["properties"]["template"]
        connection = next(resource for resource in template_resources(template)
                          if resource["type"] == "Microsoft.Kusto/clusters/databases/dataConnections")
        self.assertIn("uniqueString(parameters('eventHubId'))", connection["name"])
        self.assertEqual(connection["properties"]["eventHubResourceId"], "[parameters('eventHubId')]")


class ManagedPrometheusStandInTests(unittest.TestCase):
    def test_stand_in_serves_the_podmonitor_api_of_earlier_platform_releases(self):
        import yaml

        definition = yaml.safe_load((ROOT / "infra/cluster/managed-prometheus-podmonitor-crd.yaml").read_text())
        spec = definition["spec"]
        self.assertEqual((definition["apiVersion"], definition["kind"]), ("apiextensions.k8s.io/v1", "CustomResourceDefinition"))
        # Earlier Gateway charts rendered a PodMonitor of azmonitoring.coreos.com/v1 in the release namespace.
        self.assertEqual((spec["group"], spec["names"]["kind"], spec["scope"]), ("azmonitoring.coreos.com", "PodMonitor", "Namespaced"))
        self.assertEqual(definition["metadata"]["name"], f"{spec['names']['plural']}.{spec['group']}")
        self.assertEqual([(version["name"], version["served"], version["storage"]) for version in spec["versions"]], [("v1", True, True)])
        # Helm only maps the earlier object and deletes it, so the stand-in validates nothing.
        self.assertIs(spec["versions"][0]["schema"]["openAPIV3Schema"]["x-kubernetes-preserve-unknown-fields"], True)


class CollectionHealthTests(unittest.TestCase):
    def test_a_metric_table_that_does_not_exist_reads_as_empty(self):
        # ADX-Mon creates the table of a metric with its first value. A counter with labels, such as the refused
        # records, has no table until it first counts, and some metrics exist only in some versions: v0.3.0 has no
        # ingestor health status. A missing table must not fail the whole function, so each one is read in a fuzzy
        # union with an empty table.
        text = (ROOT / "infra/cluster/modules/adx/cetirun-health.kql").read_text()
        tables = set(re.findall(r"\b((?:Adxmon|Kube)[A-Z]\w*)\b", text))
        self.assertLessEqual({"AdxmonIngestorHealthStatus", "AdxmonIngestorHealthCheck", "AdxmonCollectorLogsDropped",
                              "AdxmonTableQuotaRejectedRecordsTotal", "AdxmonIngestorUploadFailuresTotal"}, tables)
        for table in tables:
            self.assertEqual(len(re.findall(rf"\b{table}\b", text)),
                             len(re.findall(rf"union isfuzzy=true none, {table}\b", text)), table)


if __name__ == "__main__":
    unittest.main()
