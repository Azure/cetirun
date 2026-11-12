# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Structure of the cetirun-observability chart's rendered objects."""

import os
from pathlib import Path
import re
import shutil
import subprocess
import tomllib
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/cetirun-observability/chart"
OFFLINE = {**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull, "HELM_REPOSITORY_CONFIG": os.devnull}
BASE = ["--namespace", "cetirun-system", "--set-string", "clusterName=example",
        "--set-string", "adx.endpoint=https://example.westus.kusto.windows.net",
        "--set-string", "ingestor.clientId=66666666-6666-6666-6666-666666666666"]
ALERTER = ["--set-string", "alerter.endpoint=https://alerts.example.com",
           "--set-string", "alerter.clientId=77777777-7777-7777-7777-777777777777",
           "--set-string", "alerter.destination=example-team"]


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class ObservabilityChartTests(unittest.TestCase):
    def render(self, *extra):
        result = subprocess.run(["helm", "template", "observability", str(CHART), *BASE, *extra],
                                env=OFFLINE, capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stderr)
        return [doc for doc in yaml.safe_load_all(result.stdout) if doc]

    @staticmethod
    def find(documents, kind, name):
        return next(doc for doc in documents if doc["kind"] == kind and doc["metadata"]["name"] == name)

    def collector_config(self, documents):
        return tomllib.loads(self.find(documents, "ConfigMap", "cetirun-adx-mon-collector")["data"]["config.toml"])

    def test_log_routes_put_agent_pods_first_and_cover_the_platform(self):
        config = self.collector_config(self.render())
        targets = config["host-log"][0]["static-pod-target"]
        routes = [(target.get("namespace", ""), target.get("label-targets", {}), target["destination"]) for target in targets]
        # The first matching route wins: Agent Pods, in any namespace, before any
        # namespace route, so an Agent's logs never reach the platform tables.
        agent = [index for index, (_, _, destination) in enumerate(routes) if destination.startswith("CetiRunAgents:")]
        platform = [index for index, (namespace, labels, _) in enumerate(routes) if namespace and not labels]
        self.assertEqual(agent, [0, 1, 2])
        self.assertTrue(platform and min(platform) > max(agent))
        self.assertEqual({labels.get("app.kubernetes.io/name") for _, labels, _ in routes[:3]},
                         {"cetirun", "cetirun-terminal", "cetirun-copilot-proxy"})
        self.assertIn(("cetirun-system", {}, "CetiRunLogs:Platform"), routes)
        self.assertNotIn("kube-system", {namespace for namespace, _, _ in routes})
        self.assertEqual({tuple(target["parsers"]) for target in targets}, {("json",)})

    def test_collector_has_no_write_endpoint_and_keeps_only_listed_metrics(self):
        config = self.collector_config(self.render())
        for section in ("otel-log", "otel-metric", "prometheus-remote-write", "kubelet-discovery"):
            self.assertNotIn(section, config)
        self.assertEqual(config["listen-addr"], "127.0.0.1:8080")
        self.assertNotIn("insecure-skip-verify", config)
        scrape = config["prometheus-scrape"]
        self.assertIs(scrape["default-drop-metrics"], True)
        self.assertIn("^litellm_", scrape["keep-metrics"])
        self.assertEqual(scrape["scrape-interval"], 60)
        keep = [re.compile(pattern) for pattern in scrape["keep-metrics"]]
        drop = [re.compile(pattern) for pattern in scrape["drop-metrics"]]

        def collected(name):
            return any(p.search(name) for p in keep) and not any(p.search(name) for p in drop)

        # What CollectionHealth() and the docs read is collected.
        for name in ("adxmon_table_backlog_rejected_records", "adxmon_table_backlog_rejected_bytes",
                     "adxmon_table_backlog_oldest_age_seconds", "adxmon_collector_logs_dropped",
                     "adxmon_collector_wal_segments_max_age_seconds", "adxmon_ingestor_health_status",
                     "kube_pod_container_status_restarts_total", "kube_pod_status_phase", "kube_node_status_condition"):
            self.assertTrue(collected(name), name)
        # Series that repeat each Pod's spec or each table's backlog every minute are not.
        for name in ("adxmon_table_backlog_bytes", "adxmon_table_backlog_limit_bytes", "kube_pod_tolerations",
                     "kube_pod_container_info", "kube_pod_status_qos_class", "container_network_receive_bytes_total"):
            self.assertFalse(collected(name), name)
        self.assertFalse([pattern for pattern in scrape["keep-metrics"] if "network" in pattern])
        # Each metric names its node, which ADX-Mon lifts into the Host column: the
        # kubelet and the collector report the same labels on every node.
        self.assertEqual(config["add-labels"], {"cluster": "example", "host": "$(HOSTNAME)"})
        self.assertIn({"name": "host", "column": "Host"}, config["lift-labels"])
        urls = [target["url"] for target in scrape["static-scrape-target"]]
        # The kubelet is addressed by the node's name, the only variable of every
        # ADX-Mon release; --hostname sets it.
        self.assertIn("https://$(HOSTNAME):10250/metrics/cadvisor", urls)
        self.assertFalse([url for url in urls if "$(HOSTIP)" in url])
        daemonset = self.find(self.render(), "DaemonSet", "cetirun-adx-mon-collector")
        args = daemonset["spec"]["template"]["spec"]["containers"][0]["args"]
        self.assertIn("--hostname=$(NODE_NAME)", args)
        self.assertFalse([arg for arg in args if arg.startswith("--host-ip")])

    def test_roles_read_only_what_collection_needs(self):
        documents = self.render()
        rules = {doc["metadata"]["name"]: doc["rules"] for doc in documents if doc["kind"] in ("ClusterRole", "Role")}
        self.assertEqual(rules["cetirun-adx-mon-collector"], [
            {"apiGroups": [""], "resources": ["pods"], "verbs": ["list", "watch"]},
            {"apiGroups": [""], "resources": ["nodes/metrics"], "verbs": ["get"]},
        ])
        for name, items in rules.items():
            for rule in items:
                self.assertNotIn("secrets", rule["resources"], name)
                self.assertNotIn("configmaps", rule["resources"], name)
                self.assertNotIn("nodes/proxy", rule["resources"], name)
                if name != "cetirun-adx-mon-ingestor":
                    self.assertLessEqual(set(rule["verbs"]), {"get", "list", "watch"}, name)
        self.assertEqual(rules["cetirun-telemetry"][1]["resources"], ["agents", "teams", "agentpolicies", "sandboxes"])

    def test_every_container_drops_privileges(self):
        documents = self.render(*ALERTER)
        self.find(documents, "Deployment", "cetirun-adx-mon-alerter")
        for doc in documents:
            if doc["kind"] not in ("Deployment", "StatefulSet", "DaemonSet"):
                continue
            pod = doc["spec"]["template"]
            self.assertEqual(pod["metadata"]["annotations"]["sidecar.istio.io/inject"], "false", doc["metadata"]["name"])
            for container in pod["spec"]["containers"]:
                security = container["securityContext"]
                self.assertIs(security["allowPrivilegeEscalation"], False)
                self.assertIs(security["readOnlyRootFilesystem"], True)
                self.assertEqual(security["capabilities"], {"drop": ["ALL"]})
                self.assertNotIn("privileged", security)
                for port in container.get("ports", []):
                    self.assertNotIn("hostPort", port)
            self.assertNotIn("hostNetwork", pod["spec"])
            self.assertNotIn("hostPID", pod["spec"])

    def test_collector_mounts_logs_read_only_and_writes_only_its_own_directory(self):
        daemonset = self.find(self.render(), "DaemonSet", "cetirun-adx-mon-collector")
        spec = daemonset["spec"]["template"]["spec"]
        host_paths = {volume["name"]: volume["hostPath"]["path"] for volume in spec["volumes"] if "hostPath" in volume}
        self.assertEqual(host_paths, {"log": "/var/log", "run-log": "/run/log", "machine-id": "/etc/machine-id",
                                      "storage": "/var/lib/cetirun-adx-mon/collector"})
        mounts = {mount["name"]: mount for mount in spec["containers"][0]["volumeMounts"]}
        for name in ("log", "run-log", "machine-id"):
            self.assertIs(mounts[name]["readOnly"], True, name)
        self.assertNotIn("readOnly", mounts["storage"])
        self.assertEqual(spec["tolerations"], [{"operator": "Exists"}])

    def test_the_node_journal_of_the_kubelet_and_containerd_goes_to_the_logs_database(self):
        config = self.collector_config(self.render())
        targets = {target["table"]: target for target in config["host-log"][0]["journal-target"]}
        self.assertEqual(set(targets), {"Kubelet", "Containerd"})
        self.assertEqual(targets["Kubelet"]["matches"], ["_SYSTEMD_UNIT=kubelet.service"])
        self.assertEqual(targets["Containerd"]["matches"], ["_SYSTEMD_UNIT=containerd.service"])
        # Each line stays whole in Body.message. The klog parser would drop a kubelet
        # line's severity; the keyvalue parser would turn a line that is not key=value,
        # such as a stack trace, into empty keys.
        for target in targets.values():
            self.assertNotIn("parsers", target)
            self.assertEqual(target["database"], "CetiRunLogs")
            self.assertEqual(target["journal-fields"], ["_HOSTNAME"])
        documents = self.render("--set", "logs.node.enabled=false")
        self.assertNotIn("journal-target", self.collector_config(documents)["host-log"][0])
        spec = self.find(documents, "DaemonSet", "cetirun-adx-mon-collector")["spec"]["template"]["spec"]
        self.assertEqual({volume["name"] for volume in spec["volumes"]} & {"run-log", "machine-id"}, set())

    def test_images_that_cetirun_does_not_build_stay_pinned(self):
        documents = self.render("--set-string", "global.registry=registry.example.com/team", *ALERTER)
        images = {doc["metadata"]["name"]: doc["spec"]["template"]["spec"]["containers"][0]["image"]
                  for doc in documents if doc["kind"] in ("Deployment", "StatefulSet", "DaemonSet")}
        values = yaml.safe_load((CHART / "values.yaml").read_text())
        for name, component in (("cetirun-adx-mon-collector", "collector"), ("cetirun-adx-mon-ingestor", "ingestor"),
                                ("cetirun-adx-mon-alerter", "alerter"), ("cetirun-kube-state-metrics", "kubeStateMetrics")):
            image = values[component]["image"]
            self.assertRegex(image["digest"], r"^sha256:[0-9a-f]{64}$", component)
            self.assertEqual(images[name], f"{image['repository']}:{image['tag']}@{image['digest']}")
        for component in ("collector", "ingestor", "alerter"):
            self.assertTrue(values[component]["image"]["repository"].startswith("mcr.microsoft.com/aks/ai-runtime/adx-mon/"))
        self.assertTrue(images["cetirun-telemetry"].startswith("registry.example.com/team/cetirun-operator:"))
        self.assertNotIn("image", values)

    def test_alerter_runs_only_with_an_endpoint_and_reads_logs_and_metrics_only(self):
        self.assertFalse([doc for doc in self.render() if "alerter" in doc["metadata"]["name"]])
        documents = self.render(*ALERTER)
        account = self.find(documents, "ServiceAccount", "cetirun-adx-mon-alerter")
        self.assertEqual(account["metadata"]["annotations"]["azure.workload.identity/client-id"],
                         "77777777-7777-7777-7777-777777777777")
        self.assertIs(account["automountServiceAccountToken"], False)
        role = self.find(documents, "ClusterRole", "cetirun-adx-mon-alerter")
        self.assertEqual(role["rules"], [
            {"apiGroups": ["adx-mon.azure.com"], "resources": ["alertrules"], "verbs": ["get", "list"]},
            {"apiGroups": ["adx-mon.azure.com"], "resources": ["alertrules/status"], "verbs": ["update"]},
        ])
        deployment = self.find(documents, "Deployment", "cetirun-adx-mon-alerter")
        self.assertEqual((deployment["spec"]["replicas"], deployment["spec"]["strategy"]["type"]), (1, "Recreate"))
        pod = deployment["spec"]["template"]
        self.assertEqual(pod["metadata"]["labels"]["azure.workload.identity/use"], "true")
        container = pod["spec"]["containers"][0]
        endpoint = "https://example.westus.kusto.windows.net"
        self.assertEqual(container["args"], ["--port=4023", f"--kusto-endpoint=CetiRunLogs={endpoint}",
                                             f"--kusto-endpoint=CetiRunMetrics={endpoint}",
                                             "--alerter-address=https://alerts.example.com", "--region=example"])
        self.assertIn({"name": "AZURE_TOKEN_CREDENTIALS", "value": "WorkloadIdentityCredential"}, container["env"])
        policy = self.find(documents, "NetworkPolicy", "cetirun-adx-mon-alerter")
        self.assertEqual(policy["spec"]["ingress"], [{
            "from": [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "cetirun-adx-mon-collector",
                                                      "app.kubernetes.io/instance": "observability"}}}],
            "ports": [{"protocol": "TCP", "port": 4023}],
        }])

    def test_collection_health_rule_is_a_hook_of_this_release(self):
        rule = self.find(self.render(*ALERTER), "AlertRule", "cetirun-collection-health")
        annotations = rule["metadata"]["annotations"]
        self.assertEqual(annotations["helm.sh/hook"], "post-install,post-upgrade")
        self.assertEqual(annotations["helm.sh/hook-delete-policy"], "before-hook-creation")
        self.assertEqual((annotations["meta.helm.sh/release-name"], annotations["meta.helm.sh/release-namespace"]),
                         ("observability", "cetirun-system"))
        spec = rule["spec"]
        self.assertEqual((spec["database"], spec["interval"], spec["destination"]), ("CetiRunMetrics", "5m", "example-team"))
        query = spec["query"]
        self.assertIn('| where Cluster == "example" and Status == "Failed"', query)
        for column in ("Title =", "Severity = 3", "Summary =", "CorrelationId ="):
            self.assertIn(column, query)
        documents = self.render(*ALERTER, "--set", "alerter.rules.collectionHealth.enabled=false")
        self.assertFalse([doc for doc in documents if doc["kind"] == "AlertRule"])

    def test_ingestor_writes_three_databases_with_its_workload_identity(self):
        documents = self.render()
        statefulset = self.find(documents, "StatefulSet", "cetirun-adx-mon-ingestor")
        pod = statefulset["spec"]["template"]
        self.assertEqual(pod["metadata"]["labels"]["azure.workload.identity/use"], "true")
        self.assertEqual(pod["metadata"]["labels"]["app"], "ingestor")
        args = pod["spec"]["containers"][0]["args"]
        endpoint = "https://example.westus.kusto.windows.net"
        self.assertIn(f"--metrics-kusto-endpoints=CetiRunMetrics={endpoint}", args)
        self.assertIn(f"--logs-kusto-endpoints=CetiRunLogs={endpoint}", args)
        self.assertIn(f"--logs-kusto-endpoints=CetiRunAgents={endpoint}", args)
        account = self.find(documents, "ServiceAccount", "cetirun-adx-mon-ingestor")
        self.assertEqual(account["metadata"]["annotations"]["azure.workload.identity/client-id"], "66666666-6666-6666-6666-666666666666")
        secret = self.find(documents, "Secret", "cetirun-adx-mon-ingestor-tls")
        self.assertEqual(set(secret["data"]), {"tls.crt", "tls.key", "ca.crt"})

    def test_crds_are_the_upstream_definitions(self):
        documents = self.render()
        names = sorted(doc["metadata"]["name"] for doc in documents if doc["kind"] == "CustomResourceDefinition")
        self.assertEqual(names, ["alertrules.adx-mon.azure.com", "functions.adx-mon.azure.com",
                                 "managementcommands.adx-mon.azure.com", "summaryrules.adx-mon.azure.com"])


if __name__ == "__main__":
    unittest.main()
