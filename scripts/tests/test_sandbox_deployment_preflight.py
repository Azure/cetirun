# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline deployed-Sandbox checks; every kubectl process is replaced by Python."""

import base64
import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "sandbox-platform-qualify.py"
SPEC = importlib.util.spec_from_file_location("deployment_preflight", SCRIPT)
qualify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(qualify)
REAL_POPEN = subprocess.Popen
NAMESPACE = "preflight-test"
CORE = f"/api/v1/namespaces/{NAMESPACE}/"
APPS = f"/apis/apps/v1/namespaces/{NAMESPACE}/"
ADMISSION = "/apis/admissionregistration.k8s.io/v1/"
OPERATOR = APPS + "deployments/selected-controller"
SERVER = APPS + "deployments/selected-admission"
SERVICE = CORE + "services/cetirun-sandbox-admission"
ENDPOINTS = CORE + "endpoints/cetirun-sandbox-admission"
PLATFORM = CORE + "configmaps/selected-platform"
PLACEMENT = CORE + "configmaps/selected-placement"
POD = CORE + "pods/admission-a"
REPLICA_SET = APPS + "replicasets/admission-rs"
CLUSTER = "/api/v1/namespaces/kube-system"
IMAGE = "registry.example.com/operator@sha256:" + "a" * 64
SERVER_IMAGE = "registry.example.com/operator@sha256:" + "b" * 64
CA = base64.b64encode(b"-----BEGIN CERTIFICATE-----\nZml4dHVyZQ==\n-----END CERTIFICATE-----\n").decode()
SENTINEL = "sensitive-synthetic-value.example.com"


def arguments():
    return [
        "deployment-preflight", "--kubeconfig", "/example/explicit-config", "--context", "selected-context",
        "--cluster-uid", "cluster-uid", "--namespace", NAMESPACE,
        "--operator-deployment", "selected-controller", "--operator-uid", "controller-uid",
        "--operator-image", IMAGE, "--operator-service-account", "selected-controller-sa",
        "--operator-service-account-uid", "controller-sa-uid",
        "--admission-deployment", "selected-admission", "--admission-uid", "admission-uid",
        "--admission-image", SERVER_IMAGE, "--admission-service-account", "selected-admission-sa",
        "--admission-service-account-uid", "admission-sa-uid",
    ]


def fixture():
    objects = {}

    def put(path, kind, name, *, uid=None, namespace=None, **fields):
        api = "apps/v1" if path.startswith("/apis/apps/") else (
            "admissionregistration.k8s.io/v1" if path.startswith(ADMISSION) else "v1")
        meta = {"name": name, "uid": uid or name + "-uid", "resourceVersion": "1"}
        if namespace is not None:
            meta["namespace"] = namespace
        objects[path] = {"apiVersion": api, "kind": kind, "metadata": meta, **fields}
        return objects[path]

    objects["config"] = {
        "current-context": "selected-context",
        "contexts": [{"name": "selected-context", "context": {"cluster": "selected-cluster", "user": "selected-user"}}],
        "users": [{"name": "selected-user", "user": {"token": "REDACTED"}}],
        "clusters": [{"name": "selected-cluster", "cluster": {"server": "https://cluster.example.com"}}],
    }
    objects["/version"] = {"gitVersion": "v1.37.0", "gitCommit": SENTINEL}
    put(CLUSTER, "Namespace", "kube-system", uid="cluster-uid")
    sources = {
        "enabled": True, "platformConfigMapName": "selected-platform", "placementConfigMapName": "selected-placement",
        "admissionServiceAccount": "selected-admission-sa", "homeClaimName": "home", "skillsClaimName": "skills",
        "helpers": {"doctor": False, "logger": False, "desktop": False},
    }
    config = {
        "controllerUsername": f"system:serviceaccount:{NAMESPACE}:selected-controller-sa",
        "validationMode": "strict", "adminUsers": ["admin@example.com"], "adminGroups": [],
        "platformNamespace": NAMESPACE, "platformConfigMapName": "selected-platform",
        "placementConfigMapName": "selected-placement",
    }
    operator = {
        "name": "manager", "image": IMAGE, "args": ["--leader-elect=true", "--domain-suffix=example.com"],
        "env": [
            {"name": "OPERATOR_NAMESPACE", "valueFrom": {"fieldRef": {"apiVersion": "v1", "fieldPath": "metadata.namespace"}}},
            {"name": "OPERATOR_SA_NAME", "value": "selected-controller-sa"},
            {"name": "CETIRUN_SANDBOX_SOURCES", "value": json.dumps(sources)},
        ],
    }
    admission = {
        "name": "admission", "image": SERVER_IMAGE,
        "args": ["--mode=sandbox-admission", "--admission-port=9443",
                 "--admission-cert-dir=/var/run/cetirun-admission", "--health-probe-bind-address=:8081",
                 f"--admission-pod-state-namespace={NAMESPACE}", "--admission-pod-timeout=4000ms"],
        "env": [{"name": "CETIRUN_SANDBOX_ADMISSION", "value": json.dumps(config)}],
        "ports": [{"name": "admission", "containerPort": 9443, "protocol": "TCP"},
                  {"name": "health", "containerPort": 8081, "protocol": "TCP"}],
        "readinessProbe": {"httpGet": {"path": "/readyz", "port": "health"}, "periodSeconds": 10},
        "volumeMounts": [{"name": "admission-tls", "mountPath": "/var/run/cetirun-admission", "readOnly": True}],
    }
    tls_volume = {"name": "admission-tls", "secret": {
        "secretName": "synthetic-tls-reference", "defaultMode": 288,
        "items": [{"key": "tls.crt", "path": "tls.crt"}, {"key": "tls.key", "path": "tls.key"}],
    }}
    for path, name, uid, account, account_uid, container, replicas in (
        (OPERATOR, "selected-controller", "controller-uid", "selected-controller-sa", "controller-sa-uid", operator, 1),
        (SERVER, "selected-admission", "admission-uid", "selected-admission-sa", "admission-sa-uid", admission, 2),
    ):
        spec = {"serviceAccountName": account, "containers": [container], "volumes": [tls_volume] if replicas == 2 else []}
        value = put(path, "Deployment", name, uid=uid, namespace=NAMESPACE, spec={
            "replicas": replicas, "selector": {"matchLabels": {"app": name}},
            "template": {"metadata": {"labels": {"app": name}}, "spec": spec},
        }, status={"observedGeneration": 3, **{key: replicas for key in
                                             ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas")}})
        value["metadata"]["generation"] = 3
        put(CORE + "serviceaccounts/" + account, "ServiceAccount", account, uid=account_uid, namespace=NAMESPACE)
    put(PLATFORM, "ConfigMap", "selected-platform", namespace=NAMESPACE, data={"platform.json": json.dumps({
        "version": "v1", "registrations": {}, "platforms": {}, "runtimeClasses": {}, "storageClasses": {},
    })})
    put(PLACEMENT, "ConfigMap", "selected-placement", namespace=NAMESPACE, data={"placement.json": json.dumps({
        "version": "v1", "nodes": [], "platformDaemonSets": [],
    })})
    put(SERVICE, "Service", "cetirun-sandbox-admission", namespace=NAMESPACE, spec={
        "type": "ClusterIP", "clusterIP": "192.0.2.100", "selector": {"app": "selected-admission"},
        "ports": [{"name": "admission", "port": 443, "targetPort": "admission", "protocol": "TCP"}],
    })
    replica_set = put(REPLICA_SET, "ReplicaSet", "admission-rs", namespace=NAMESPACE)
    replica_set["metadata"]["ownerReferences"] = [{
        "apiVersion": "apps/v1", "kind": "Deployment", "name": "selected-admission",
        "uid": "admission-uid", "controller": True,
    }]
    addresses = []
    for index, letter in enumerate(("a", "b"), 1):
        name = "admission-" + letter
        spec = copy.deepcopy(objects[SERVER]["spec"]["template"]["spec"])
        spec["nodeName"] = "node-" + letter
        spec["volumes"].append({"name": "kube-api-access-example", "projected": {"sources": [
            {"serviceAccountToken": {"expirationSeconds": 3600, "path": "token"}},
        ]}})
        spec["containers"][0]["volumeMounts"].append({
            "name": "kube-api-access-example", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": True,
        })
        pod = put(CORE + "pods/" + name, "Pod", name, namespace=NAMESPACE, spec=spec,
                  status={"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}],
                          "podIPs": [{"ip": f"192.0.2.{index}"}]})
        pod["metadata"].update(labels={"app": "selected-admission"}, ownerReferences=[{
            "apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "admission-rs",
            "uid": "admission-rs-uid", "controller": True,
        }])
        addresses.append({"ip": f"192.0.2.{index}", "targetRef": {
            "kind": "Pod", "namespace": NAMESPACE, "name": name, "uid": name + "-uid",
        }})
    put(ENDPOINTS, "Endpoints", "cetirun-sandbox-admission", namespace=NAMESPACE, subsets=[{
        "ports": [{"name": "admission", "port": 9443, "protocol": "TCP"}], "addresses": addresses,
    }])
    group, pods = ["cetirun.microsoft.com"], ["pods", "pods/ephemeralcontainers", "pods/resize"]
    for resource, kind, name, hook, route, rules in (
        ("validatingwebhookconfigurations", "ValidatingWebhookConfiguration", "cetirun-sandbox-admission",
         "sandbox-configuration", "validate-sandbox-configuration",
         [(group, ["v1alpha1"], ["CREATE", "UPDATE"], ["agents", "teams", "sandboxes", "agentpolicies"], "Cluster"),
          (group, ["v1alpha1"], ["DELETE"], ["teams", "sandboxes", "agentpolicies"], "Cluster")]),
        ("mutatingwebhookconfigurations", "MutatingWebhookConfiguration", "cetirun-sandbox-admission",
         "sandbox-agent", "mutate-sandbox-agent",
         [(group, ["v1alpha1"], ["CREATE", "UPDATE"], ["agents"], "Cluster")]),
        ("validatingwebhookconfigurations", "ValidatingWebhookConfiguration", "cetirun-sandbox-pods",
         "sandbox-pods", "validate-sandbox-pods",
         [([""], ["v1"], ["CREATE", "UPDATE"], pods, "Namespaced"),
          ([""], ["v1"], ["CONNECT"], ["pods/exec", "pods/attach", "pods/portforward"], "Namespaced")]),
        ("validatingwebhookconfigurations", "ValidatingWebhookConfiguration", "cetirun-sandbox-placement",
         "sandbox-placement", "validate-sandbox-placement",
         [([""], ["v1"], ["CREATE", "UPDATE"], pods, "Namespaced"),
          ([""], ["v1"], ["CREATE"], ["pods/binding", "bindings"], "Namespaced"),
          ([""], ["v1"], ["UPDATE", "DELETE"], ["nodes"], "Cluster")]),
    ):
        webhook = {
            "name": hook + ".cetirun.microsoft.com", "failurePolicy": "Fail", "sideEffects": "None",
            "matchPolicy": "Equivalent", "admissionReviewVersions": ["v1"], "timeoutSeconds": 5,
            "clientConfig": {"caBundle": CA, "service": {
                "name": "cetirun-sandbox-admission", "namespace": NAMESPACE, "port": 443, "path": "/" + route,
            }},
            "rules": [dict(zip(("apiGroups", "apiVersions", "operations", "resources", "scope"), rule)) for rule in rules],
            "matchConditions": [{"name": "synthetic-unverified-condition", "expression": "true"}],
        }
        if resource == "mutatingwebhookconfigurations":
            webhook["reinvocationPolicy"] = "Never"
        if name == "cetirun-sandbox-pods":
            webhook["namespaceSelector"] = {
                "matchExpressions": [{"key": "cetirun.microsoft.com/sandbox-protected", "operator": "Exists"}],
            }
        put(ADMISSION + resource + "/" + name, kind, name, webhooks=[webhook])
    return objects


class KubectlDouble:
    def __init__(self):
        self.responses = fixture()
        self.calls = []
        self.counts = {}
        self.processes = []
        self.scripts = {}
        self.change = lambda key, count, value: value

    def spawn(self, command, **options):
        assert command[:7] == [
            "kubectl", "--kubeconfig", "/example/explicit-config", "--context", "selected-context",
            "--request-timeout=5s", "--v=0",
        ], command
        assert options["stdin"] == subprocess.DEVNULL
        assert options["stdout"] == subprocess.PIPE and options["stderr"] == subprocess.DEVNULL
        assert options["start_new_session"] is True and options["env"]["KUBECTL_KUBERC"] == "false"
        if command[7:] == ["config", "view", "--minify", "-o", "json"]:
            key = "config"
        else:
            assert len(command) == 10 and command[7:9] == ["get", "--raw"], command
            key = command[9]
        assert key in self.responses, key
        self.calls.append(command)
        self.counts[key] = self.counts.get(key, 0) + 1
        value = self.change(key, self.counts[key], copy.deepcopy(self.responses[key]))
        raw = value if isinstance(value, str) else json.dumps(value)
        script = self.scripts.get(key, "import sys;sys.stdout.write(sys.argv[1])")
        process = REAL_POPEN([sys.executable, "-B", "-c", script, raw], **options)
        self.processes.append(process)
        return process


class DeploymentPreflightTests(unittest.TestCase):
    def run_preflight(self, double=None, args=None):
        double = double or KubectlDouble()
        with patch.object(qualify.subprocess, "Popen", side_effect=double.spawn), \
             contextlib.redirect_stdout(io.StringIO()) as stdout, contextlib.redirect_stderr(io.StringIO()) as stderr:
            code = qualify.main(arguments() if args is None else args)
        self.assertEqual(stderr.getvalue(), "")
        text = stdout.getvalue()
        self.assertLess(len(text.encode()), 4096)
        self.assertNotIn(SENTINEL, text)
        self.assertTrue(all(p.poll() is not None for p in double.processes), "subprocess was not reaped")
        result = json.loads(text)
        self.assertIs(result["qualifiesExecution"], False)
        return code, result, double

    def assert_blocked(self, double, reason=None):
        code, result, double = self.run_preflight(double)
        self.assertEqual(code, 1)
        self.assertEqual(result["status"], "blocked")
        if reason:
            self.assertIn(reason, [check["reason"] for check in result["checks"]])
        return result

    def test_exact_target_and_named_gets_complete_only_scoped_observations(self):
        code, result, double = self.run_preflight()
        self.assertEqual(code, 0)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["executionReadiness"], "unknown")
        self.assertEqual(result["stockExecutionHold"], "writer-exclusion-unavailable")
        self.assertEqual(set(result), {"version", "mode", "status", "qualifiesExecution", "executionReadiness",
                                       "stockExecutionHold", "checks", "unverified"})
        self.assertEqual(len(result["checks"]), 11)
        self.assertEqual(len(result["unverified"]), 5)
        self.assertTrue(all(check["status"] == "observed" for check in result["checks"]))
        self.assertTrue(all(check["status"] == "unknown" for check in result["unverified"]))
        self.assertEqual(set(double.counts), set(double.responses))
        self.assertTrue(all(count >= 2 for count in double.counts.values()))
        self.assertLessEqual(len(double.calls), qualify.DeploymentKubernetes.MAX_COMMANDS)
        self.assertEqual(double.calls[1][-1], CLUSTER)
        self.assertEqual(double.calls[2][-1], "/version")
        output = json.dumps(result)
        for value in (NAMESPACE, IMAGE, SERVER_IMAGE, "192.0.2.", "admin@example.com", "selected-context", "cluster-uid", CA):
            self.assertNotIn(value, output)

    def test_every_input_pin_is_mandatory_before_any_command(self):
        args = arguments()
        for index in range(1, len(args), 2):
            with self.subTest(option=args[index]):
                code, result, double = self.run_preflight(args=args[:index] + args[index + 2:])
                self.assertIn(code, (1, 2))
                self.assertEqual(result["status"], "blocked")
                self.assertEqual(double.calls, [])

    def test_invalid_arguments_and_mutation_options_are_sanitized_without_commands(self):
        for extra in (["--allow-test-resources"], ["--probe-image", IMAGE], ["--storage-class", "home"],
                      ["--runtime-class", "kata"], ["--execution-ready", SENTINEL]):
            with self.subTest(extra=extra):
                code, _, double = self.run_preflight(args=arguments() + extra)
                self.assertIn(code, (1, 2))
                self.assertEqual(double.calls, [])
        for option, value in (("--kubeconfig", "relative-config"), ("--namespace", "../other"),
                              ("--operator-image", "registry.example.com/operator:latest"),
                              ("--operator-service-account", "selected-admission-sa")):
            args = arguments()
            args[args.index(option) + 1] = value
            code, _, double = self.run_preflight(args=args)
            self.assertEqual(code, 1)
            self.assertEqual(double.calls, [])

    def test_auth_plugins_are_rejected_before_any_cluster_request(self):
        for key in ("exec", "auth-provider", "tokenFile", "as", "as-groups"):
            double = KubectlDouble()
            double.responses["config"]["users"][0]["user"][key] = SENTINEL
            self.assert_blocked(double, "credential-plugin-or-indirection-not-supported")
            self.assertEqual(set(double.counts), {"config"})

    def test_context_binding_and_https_must_match(self):
        for change in (
            lambda c: c.update({"current-context": SENTINEL}),
            lambda c: c["contexts"][0]["context"].update(user="foreign"),
            lambda c: c["clusters"][0]["cluster"].update({"insecure-skip-tls-verify": True}),
            lambda c: c["clusters"][0]["cluster"].update(server="http://cluster.example.com"),
        ):
            double = KubectlDouble()
            change(double.responses["config"])
            self.assert_blocked(double)
            self.assertEqual(set(double.counts), {"config"})

    def test_wrong_cluster_fails_before_version_or_workload_reads(self):
        double = KubectlDouble()
        double.responses[CLUSTER]["metadata"]["uid"] = "other-cluster"
        self.assert_blocked(double, "resource-uid-mismatch")
        self.assertEqual(set(double.counts), {"config", CLUSTER})

    def test_version_minimum_is_not_a_runtime_qualification(self):
        for version in ("v1.36.9", "v1.37.0-alpha.1", SENTINEL, None, {}, 137):
            double = KubectlDouble()
            double.responses["/version"]["gitVersion"] = version
            self.assert_blocked(double, "stable-kubernetes-1.37-or-newer-required")
            self.assertEqual(set(double.counts), {"config", CLUSTER, "/version"})
        double = KubectlDouble()
        double.responses["/version"]["gitVersion"] = "v1.38.1+vendor.1"
        self.assertEqual(self.run_preflight(double)[0], 0)

    def test_deployment_identity_image_mode_and_rollout_fail_closed(self):
        changes = (
            lambda d: d["metadata"].update(uid=SENTINEL),
            lambda d: d["metadata"].update(deletionTimestamp="2026-01-01T00:00:00Z"),
            lambda d: d["status"].update(observedGeneration=2),
            lambda d: d["status"].update(readyReplicas=1),
            lambda d: d["spec"].update(replicas=1),
            lambda d: d["spec"]["template"]["spec"].update(serviceAccountName="foreign"),
            lambda d: d["spec"]["template"]["spec"]["containers"][0].update(image=IMAGE),
            lambda d: d["spec"]["template"]["spec"]["containers"][0].update(command=["foreign"]),
            lambda d: d["spec"]["template"]["spec"]["containers"][0].update(envFrom=[{"secretRef": {"name": "never-read"}}]),
            lambda d: d["spec"]["template"]["spec"]["containers"][0]["args"].append("--mode=operator"),
        )
        for index, change in enumerate(changes):
            with self.subTest(change=index):
                double = KubectlDouble()
                change(double.responses[SERVER])
                self.assert_blocked(double)
                self.assertNotIn(SERVICE, double.counts)
        double = KubectlDouble()
        double.responses[CORE + "serviceaccounts/selected-admission-sa"]["metadata"]["uid"] = "replacement"
        self.assert_blocked(double, "resource-uid-mismatch")
        double = KubectlDouble()
        double.responses[OPERATOR]["status"]["readyReplicas"] = True
        self.assert_blocked(double, "deployment-not-fully-observed")

    def test_source_bindings_do_not_dereference_secrets_or_unknown_inputs(self):
        for path, env_name, update in (
            (OPERATOR, "CETIRUN_SANDBOX_SOURCES", {"enabled": False}),
            (OPERATOR, "CETIRUN_SANDBOX_SOURCES", {"placementConfigMapName": "other-placement"}),
            (SERVER, "CETIRUN_SANDBOX_ADMISSION", {"controllerUsername": SENTINEL}),
            (SERVER, "CETIRUN_SANDBOX_ADMISSION", {"platformNamespace": "other-namespace"}),
            (SERVER, "CETIRUN_SANDBOX_ADMISSION", {"validationMode": "compatible"}),
            (SERVER, "CETIRUN_SANDBOX_ADMISSION", {"adminGroups": ["system:authenticated"]}),
        ):
            with self.subTest(update=update):
                double = KubectlDouble()
                env = double.responses[path]["spec"]["template"]["spec"]["containers"][0]["env"]
                entry = next(e for e in env if e["name"] == env_name)
                value = json.loads(entry["value"])
                entry["value"] = json.dumps({**value, **update})
                self.assert_blocked(double)
                self.assertNotIn(SERVICE, double.counts)
        double = KubectlDouble()
        entry = double.responses[SERVER]["spec"]["template"]["spec"]["containers"][0]["env"][0]
        entry.pop("value")
        entry["valueFrom"] = {"secretKeyRef": {"name": "never-read", "key": "config"}}
        result = self.assert_blocked(double, "inline-configuration-required")
        self.assertEqual(next(c for c in result["checks"] if c["check"] == "source-bindings")["status"], "unknown")
        self.assertFalse(any("/secrets/" in c[-1] for c in double.calls))

    def test_source_document_versions_and_shapes_are_not_silently_accepted(self):
        for document in ('{"version":"v2"}', '{"version":"v1"}', '{"version":"v1","version":"v1"}',
                         "[]", "{", None, "\ud800"):
            double = KubectlDouble()
            double.responses[PLATFORM]["data"]["platform.json"] = document
            self.assert_blocked(double)
            self.assertNotIn(SERVICE, double.counts)

    def test_service_selectors_listener_probe_and_tls_references_are_checked(self):
        for path, change in (
            (SERVICE, lambda d: d["spec"].update(selector={"app": "foreign"})),
            (SERVICE, lambda d: d["spec"]["ports"][0].update(targetPort=8443)),
            (SERVICE, lambda d: d["spec"].update(type="ExternalName", externalName=SENTINEL)),
            (SERVER, lambda d: d["spec"]["template"]["spec"]["containers"][0]["readinessProbe"]["httpGet"].update(path="/not-ready")),
            (SERVER, lambda d: d["spec"]["template"]["spec"]["volumes"][0]["secret"].update(optional=True)),
        ):
            double = KubectlDouble()
            change(double.responses[path])
            self.assert_blocked(double)
            self.assertNotIn(ENDPOINTS, double.counts)

    def test_serving_endpoints_must_be_bounded_and_owned_by_selected_deployment(self):
        changes = (
            (ENDPOINTS, lambda d: d["subsets"][0]["addresses"].extend([copy.deepcopy(d["subsets"][0]["addresses"][0])] * 3)),
            (ENDPOINTS, lambda d: d["subsets"][0].update(notReadyAddresses=[{"ip": "192.0.2.9"}])),
            (ENDPOINTS, lambda d: d["subsets"][0]["addresses"][0]["targetRef"].update(namespace="other")),
            (POD, lambda d: d["metadata"].update(uid="replaced")),
            (POD, lambda d: d["spec"].update(nodeName="node-b")),
            (POD, lambda d: d["spec"]["containers"][0].update(image=IMAGE)),
            (POD, lambda d: d["status"]["conditions"][0].update(status="False")),
            (POD, lambda d: d["status"].update(podIPs=[{"ip": "192.0.2.200"}])),
            (REPLICA_SET, lambda d: d["metadata"]["ownerReferences"][0].update(uid="foreign-deployment")),
        )
        for index, (path, change) in enumerate(changes):
            with self.subTest(change=index):
                double = KubectlDouble()
                change(double.responses[path])
                self.assert_blocked(double)
                self.assertFalse(any("webhookconfigurations/" in c[-1] for c in double.calls))

    def test_webhook_wiring_errors_are_blockers_but_cel_evaluation_stays_unknown(self):
        path = ADMISSION + "validatingwebhookconfigurations/cetirun-sandbox-placement"
        for update in (
            lambda h: h.update(failurePolicy="Ignore"),
            lambda h: h.update(timeoutSeconds=4),
            lambda h: h.update(rules=[]),
            lambda h: h.update(namespaceSelector={"matchLabels": {"skip": "true"}}),
            lambda h: h["clientConfig"]["service"].update(namespace="foreign"),
            lambda h: h["clientConfig"].update(url="https://external.example.com"),
            lambda h: h["clientConfig"].update(caBundle=SENTINEL),
        ):
            double = KubectlDouble()
            update(double.responses[path]["webhooks"][0])
            self.assert_blocked(double)
        double = KubectlDouble()
        double.responses[path]["webhooks"][0]["matchConditions"][0]["expression"] = "false"
        _, result, _ = self.run_preflight(double)
        self.assertIn({"check": "webhook-match-conditions-and-namespace-policy", "status": "unknown"}, result["unverified"])
        self.assertEqual(result["executionReadiness"], "unknown")

    def test_source_capability_booleans_cannot_become_execution_evidence(self):
        double = KubectlDouble()
        platform = json.loads(double.responses[PLATFORM]["data"]["platform.json"])
        platform["registrations"]["kata"] = {"Available": True, "Capabilities": {"MicroVMIsolation": True}}
        double.responses[PLATFORM]["data"]["platform.json"] = json.dumps(platform)
        code, result, _ = self.run_preflight(double)
        self.assertEqual(code, 0)
        self.assertIs(result["qualifiesExecution"], False)
        self.assertEqual(result["executionReadiness"], "unknown")
        self.assertIn({"check": "runtime-network-storage-and-provider-evidence", "status": "unknown"}, result["unverified"])

    def test_second_reads_detect_uid_resource_version_and_api_drift(self):
        for selected, field in ((OPERATOR, "uid"), (SERVER, "resourceVersion"), (POD, "resourceVersion"),
                                (PLACEMENT, "resourceVersion"), (ENDPOINTS, "uid"), (CLUSTER, "uid")):
            double = KubectlDouble()

            def change(key, count, value):
                if key == selected and count > 1:
                    value["metadata"][field] = "changed"
                return value

            double.change = change
            self.assert_blocked(double)
        for selected in ("config", "/version"):
            double = KubectlDouble()

            def change(key, count, value):
                if key == selected and count > 1:
                    value["changed"] = SENTINEL
                return value

            double.change = change
            self.assert_blocked(double, "kubeconfig-drift" if selected == "config" else "api-version-drift")

    def test_get_allowlist_cannot_be_used_for_discovery_or_other_methods(self):
        kube = qualify.DeploymentKubernetes("/example/explicit-config", "selected-context", "cluster-uid")
        with patch.object(qualify.subprocess, "Popen") as popen:
            for command in (
                ["get", "pods", "-A"], ["get", "--raw", "/api/v1/namespaces/preflight-test/secrets/example"],
                ["get", "--raw", "/api"], ["get", "--raw", "/version?x=1"], ["create", "-f", "-"],
                ["exec", "pod"], ["attach", "pod"], ["port-forward", "pod"], ["delete", "pod"],
            ):
                with self.assertRaises(qualify.PreflightBlocked):
                    kube.run(command)
            for resource in ("secret", "node", "agent", "runtimeclass", "storageclass"):
                with self.assertRaises(qualify.PreflightBlocked):
                    kube.get(resource, "example")
            with self.assertRaises(qualify.PreflightBlocked):
                kube.get("namespace", "unrelated")
            with self.assertRaises(qualify.PreflightBlocked):
                kube.get("pod", "../escape", NAMESPACE)
            popen.assert_not_called()

    def test_command_errors_and_malformed_bodies_never_escape_to_output(self):
        for script in (
            f"import sys;sys.stdout.write('{SENTINEL}');sys.stderr.write('{SENTINEL}');sys.exit(1)",
            f"print('{SENTINEL}')",
            "print('[]')",
            "print('{\"metadata\": []}')",
            "import sys;sys.stdout.buffer.write(b'\\xff')",
        ):
            double = KubectlDouble()
            double.scripts[CLUSTER] = script
            self.assert_blocked(double)
        with patch.object(qualify.subprocess, "Popen", side_effect=OSError(SENTINEL)), \
             contextlib.redirect_stdout(io.StringIO()) as stdout:
            self.assertEqual(qualify.main(arguments()), 1)
        self.assertNotIn(SENTINEL, stdout.getvalue())

    def test_response_cap_includes_exact_boundary_and_reaps_oversized_writer(self):
        for extra in (0, 1):
            double = KubectlDouble()
            double.scripts["config"] = (
                f"import sys;sys.stdout.write('x'*{qualify.DeploymentKubernetes.MAX_BYTES + extra})"
            )
            kube = qualify.DeploymentKubernetes("/example/explicit-config", "selected-context", "cluster-uid")
            with patch.object(qualify.subprocess, "Popen", side_effect=double.spawn):
                if extra:
                    with self.assertRaisesRegex(qualify.PreflightBlocked, "response-too-large"):
                        kube.run(kube.CONFIG_COMMAND)
                else:
                    self.assertEqual(len(kube.run(kube.CONFIG_COMMAND)), kube.MAX_BYTES)
            self.assertTrue(all(p.poll() is not None for p in double.processes))

    def test_command_count_and_total_deadline_are_hard_limits(self):
        with patch.object(qualify.DeploymentKubernetes, "MAX_COMMANDS", 0):
            result = self.assert_blocked(KubectlDouble(), "command-budget-exhausted")
            self.assertEqual(result["checks"][1]["status"], "unknown")
        kube = qualify.DeploymentKubernetes("/example/explicit-config", "selected-context", "cluster-uid")
        kube.deadline = time.monotonic() - 1
        with patch.object(qualify.subprocess, "Popen") as popen, self.assertRaisesRegex(
                qualify.PreflightBlocked, "deadline-exhausted"):
            kube.run(kube.CONFIG_COMMAND)
        popen.assert_not_called()

    def test_real_subprocess_timeout_is_bounded_and_reaped(self):
        double = KubectlDouble()
        double.scripts["config"] = "import time;time.sleep(60)"
        start = time.monotonic()
        with patch.object(qualify.DeploymentKubernetes, "COMMAND_SECONDS", 0.1):
            self.assert_blocked(double, "request-timeout")
        self.assertLess(time.monotonic() - start, 3)

    def test_interrupt_and_sigterm_cancel_without_followup_requests(self):
        previous = signal.getsignal(signal.SIGTERM)

        def terminate(_timeout):
            os.kill(os.getpid(), signal.SIGTERM)

        for cancellation in (KeyboardInterrupt, terminate):
            double = KubectlDouble()
            double.scripts["config"] = "import time;time.sleep(60)"
            with patch.object(qualify.selectors.DefaultSelector, "select", side_effect=cancellation):
                code, result, double = self.run_preflight(double)
            self.assertEqual(code, 130)
            self.assertEqual(result["checks"][1]["reason"], "cancelled")
            self.assertEqual(len(double.calls), 1)
            self.assertEqual(signal.getsignal(signal.SIGTERM), previous)


if __name__ == "__main__":
    unittest.main()
