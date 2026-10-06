# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline tests; no Kubernetes or Azure calls."""

import contextlib
import http.client
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "sandbox-platform-qualify.py"
SPEC = importlib.util.spec_from_file_location("sandbox_platform_qualify", SCRIPT)
qualify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(qualify)
IMAGE = "registry.example.com/probe@sha256:" + "a" * 64


class FakeKubernetes:
    def __init__(self):
        self.created = []
        self.deleted = []
        self.gets = []
        self.target_checks = 0
        self.namespace = None
        self.version = "v1.37.0"
        self.handler = "kata"
        self.address = "192.0.2.10"
        self.responses = [[True, True, True], [False, True, False], [False, True, True]]
        self.probe_calls = 0
        self.crd_ready = True
        self.replace_namespace = False
        self.replace_probe = False
        self.fail_probe = False

    def check_target(self):
        self.target_checks += 1

    def get(self, resource, name, namespace=None, absent_ok=False):
        self.gets.append((resource, name, namespace))
        if resource == "namespace":
            if self.replace_namespace and self.namespace:
                return {"metadata": {**self.namespace["metadata"], "uid": "foreign-uid"}}
            return self.namespace
        if resource == "runtimeclass":
            return {"handler": self.handler}
        if resource == "storageclass":
            return {"provisioner": "csi.example.com"}
        if resource == "crd":
            return {"status": {"conditions": [{"type": "Established", "status": "True" if self.crd_ready else "False"}]}}
        if resource == "pod":
            return {"metadata": {"uid": "changed" if self.replace_probe else name},
                    "status": {"podIP": self.address, "phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}
        raise AssertionError(resource)

    def create(self, value):
        self.created.append(value)
        if value["kind"] == "Namespace":
            self.namespace = {"metadata": {**value["metadata"], "uid": "namespace-uid", "resourceVersion": "1"}}
            return self.namespace
        return value

    def run(self, args, document=None, timeout=30):
        if args[:2] == ["get", "--raw"]:
            return json.dumps({"gitVersion": self.version})
        if args[0] == "exec":
            self.probe_calls += 1
            if self.fail_probe:
                raise qualify.QualificationError("synthetic execution failure")
            return json.dumps(self.responses.pop(0))
        if args[0] == "delete":
            self.deleted.append((args, document))
            self.namespace = None
            return "{}"
        raise AssertionError(args)


class SandboxPlatformQualificationTests(unittest.TestCase):
    def test_every_command_has_explicit_target_and_deadline(self):
        client = qualify.Kubernetes("/example/test-config", "test-context", "expected-uid")
        with patch.object(qualify.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, '{"metadata":{"uid":"expected-uid"}}')) as run:
            client.check_target()
        args, kwargs = run.call_args
        self.assertEqual(args[0][:7], ["kubectl", "--kubeconfig", "/example/test-config", "--context", "test-context", "--request-timeout=15s", "get"])
        self.assertEqual(kwargs["timeout"], 30)
        self.assertFalse(kwargs["check"])

    def test_wrong_cluster_denies_before_other_reads(self):
        client = qualify.Kubernetes("/example/test-config", "test-context", "expected-uid")
        with patch.object(client, "get", return_value={"metadata": {"uid": "different"}}):
            with self.assertRaisesRegex(qualify.QualificationError, "Cluster UID mismatch"):
                qualify.preflight(client, "kata-vm-isolation", "kata", "home")

    def test_upstream_credentials_and_endpoints_not_echoed(self):
        client = qualify.Kubernetes("/example/test-config", "test-context", "expected")
        with patch.object(qualify.subprocess, "run", return_value=subprocess.CompletedProcess([], 1, "", "sensitive-upstream-value")):
            with self.assertRaises(qualify.QualificationError) as raised:
                client.get("namespace", "kube-system")
        self.assertNotIn("sensitive-upstream-value", str(raised.exception))

    def test_timeout_is_explicit_failure(self):
        client = qualify.Kubernetes("/example/test-config", "test-context", "expected")
        with patch.object(qualify.subprocess, "run", side_effect=subprocess.TimeoutExpired([], 1)):
            with self.assertRaisesRegex(qualify.QualificationError, "timed out"):
                client.get("namespace", "kube-system")

    def test_preflight_is_read_only(self):
        kube = FakeKubernetes()
        result = qualify.preflight(kube, "kata-vm-isolation", "kata", "home")
        self.assertTrue(result["ciliumAPIsEstablished"])
        self.assertEqual(kube.created, [])
        self.assertEqual(kube.deleted, [])

    def test_preflight_rejects_incompatible_prerequisites(self):
        for field, value in (("version", "v1.36.9"), ("version", "unknown"), ("handler", "runc"), ("crd_ready", False)):
            kube = FakeKubernetes()
            setattr(kube, field, value)
            with self.subTest(field=field):
                with self.assertRaises(qualify.QualificationError):
                    qualify.preflight(kube, "kata-vm-isolation", "kata", "home")

    def test_pods_are_synthetic_without_credentials_or_host_access(self):
        for role in ("server", "probe"):
            value = qualify.pod("test-namespace", "run", role, IMAGE, "kata-vm-isolation")
            self.assertFalse(value["spec"]["automountServiceAccountToken"])
            self.assertEqual(value["spec"]["restartPolicy"], "Never")
            self.assertNotIn("volumes", value["spec"])
            self.assertNotIn("hostNetwork", value["spec"])
            if role == "probe":
                self.assertEqual(value["spec"]["runtimeClassName"], "kata-vm-isolation")

    def test_smoke_checks_allow_deny_and_cleans_exact_uid(self):
        kube = FakeKubernetes()
        result = {}
        qualify.network_smoke(kube, IMAGE, "kata-vm-isolation", result)
        self.assertTrue(result["cleanupComplete"])
        self.assertTrue(all(result["networkChecks"].values()))
        self.assertEqual([obj["kind"] for obj in kube.created],
                         ["Namespace", "Pod", "Pod", "CiliumNetworkPolicy", "NetworkPolicy"])
        _, options = kube.deleted[0]
        self.assertEqual(options["preconditions"], {"uid": "namespace-uid", "resourceVersion": "1"})
        self.assertEqual(kube.target_checks, 2)

    def test_precedence_waits_for_allow_policy_witness(self):
        kube = FakeKubernetes()
        kube.responses = [
            [True, True, True], [False, True, False],
            [False, True, False], [False, True, False], [False, True, True],
        ]
        result = {}
        with patch.object(qualify.time, "sleep"):
            qualify.network_smoke(kube, IMAGE, "kata-vm-isolation", result)
        self.assertEqual(kube.probe_calls, 5)
        self.assertTrue(result["networkChecks"]["overlappingAllowObserved"])
        self.assertTrue(result["networkChecks"]["denyOverOverlappingAllow"])

    def test_precedence_without_applied_allow_does_not_pass(self):
        kube = FakeKubernetes()
        kube.responses = [[False, True, False]]
        with patch.object(qualify.time, "monotonic", side_effect=[0, 0, 46]), patch.object(qualify.time, "sleep"):
            with self.assertRaisesRegex(qualify.QualificationError, "did not match"):
                qualify.wait_connectivity(kube, "ns", "192.0.2.1", [False, True, True])

    def test_allow_transition_within_probe_cannot_reuse_old_deny(self):
        state = {"allowApplied": False}
        calls = []

        class Connection:
            status = 200

            def __init__(self, address, port, **kwargs):
                self.port = port

            def connect(self):
                calls.append(self.port)
                if not state["allowApplied"] and self.port != 8081:
                    # Simulate an incorrectly permissive allow becoming active
                    # immediately after the first old-policy connection timeout.
                    state["allowApplied"] = True
                    raise TimeoutError

            def request(self, *args):
                pass

            def getresponse(self):
                return self

            def read(self):
                return b"qualification-ok"

            def close(self):
                pass

        results = []
        with patch.object(http.client, "HTTPConnection", Connection), \
             patch.object(qualify.sys, "argv", ["probe", "192.0.2.1"]):
            for _ in range(2):
                with contextlib.redirect_stdout(io.StringIO()) as output:
                    exec(compile(qualify.PROBE, "<synthetic-probe>", "exec"), {})
                results.append(json.loads(output.getvalue()))
        self.assertEqual(calls, [8082, 8080, 8081] * 2)
        self.assertEqual(results, [[True, True, False], [True, True, True]])
        self.assertNotIn([False, True, True], results)

    def test_probe_rejects_http_errors_after_successful_tcp_connect(self):
        class Connection:
            def __init__(self, *args, **kwargs):
                pass

            def connect(self):
                pass

            def request(self, *args):
                pass

            def getresponse(self):
                return self

            def read(self):
                return b"qualification-ok"

            def close(self):
                pass

        for response_status, body, failure in (
            (503, b"qualification-ok", None),
            (200, b"unexpected", None),
            (200, b"qualification-ok", http.client.BadStatusLine("synthetic")),
            (200, b"qualification-ok", TimeoutError("after connect")),
        ):
            with self.subTest(status=response_status, failure=type(failure).__name__):
                with patch.object(http.client, "HTTPConnection", Connection), \
                     patch.object(Connection, "status", response_status, create=True), \
                     patch.object(Connection, "read", return_value=body), \
                     patch.object(Connection, "getresponse", side_effect=failure, return_value=Connection()), \
                     patch.object(qualify.sys, "argv", ["probe", "192.0.2.1"]), \
                     contextlib.redirect_stdout(io.StringIO()) as output:
                    with self.assertRaises((SystemExit, http.client.HTTPException, TimeoutError)):
                        exec(compile(qualify.PROBE, "<synthetic-probe>", "exec"), {})
                self.assertEqual(output.getvalue(), "")

    def test_probe_counts_only_connection_timeout_as_blocked(self):
        class Connection:
            def __init__(self, *args, **kwargs):
                pass

            def connect(self):
                raise TimeoutError

            def close(self):
                pass

        with patch.object(http.client, "HTTPConnection", Connection), \
             patch.object(qualify.sys, "argv", ["probe", "192.0.2.1"]), \
             contextlib.redirect_stdout(io.StringIO()) as output:
            exec(compile(qualify.PROBE, "<synthetic-probe>", "exec"), {})
        self.assertEqual(json.loads(output.getvalue()), [False, False, False])
        with patch.object(http.client, "HTTPConnection", Connection), \
             patch.object(Connection, "connect", side_effect=ConnectionRefusedError), \
             patch.object(qualify.sys, "argv", ["probe", "192.0.2.1"]):
            with self.assertRaises(ConnectionRefusedError):
                exec(compile(qualify.PROBE, "<synthetic-probe>", "exec"), {})

    def test_probe_execution_error_is_not_network_denial(self):
        kube = FakeKubernetes()
        kube.fail_probe = True
        result = {}
        with self.assertRaisesRegex(qualify.QualificationError, "execution failure"):
            qualify.network_smoke(kube, IMAGE, "kata-vm-isolation", result)
        self.assertTrue(result["cleanupComplete"])
        self.assertNotIn("networkChecks", result)

    def test_malformed_probe_results_fail(self):
        kube = FakeKubernetes()
        for value in ([True], [0, 1], {"allowed": True}):
            kube.responses = [value]
            with self.assertRaises(qualify.QualificationError):
                qualify.connectivity(kube, "ns", "192.0.2.1")

    def test_foreign_namespace_is_never_deleted(self):
        kube = FakeKubernetes()
        kube.create({"kind": "Namespace", "metadata": {"name": "test", "labels": {qualify.RUN_LABEL: "run"}}})
        kube.replace_namespace = True
        with self.assertRaisesRegex(qualify.QualificationError, "ownership"):
            qualify.cleanup(kube, "test", "namespace-uid", "run")
        self.assertEqual(kube.deleted, [])

    def test_both_probe_and_cleanup_errors_remain_visible(self):
        kube = FakeKubernetes()
        kube.fail_probe = True
        kube.replace_namespace = True
        result = {}
        with self.assertRaisesRegex(qualify.QualificationError, "execution failure; cleanup failed: Cleanup ownership"):
            qualify.network_smoke(kube, IMAGE, "kata-vm-isolation", result)
        self.assertFalse(result["cleanupComplete"])
        self.assertEqual(kube.deleted, [])

    def test_mutable_probe_image_is_rejected_without_writes(self):
        kube = FakeKubernetes()
        with self.assertRaisesRegex(qualify.QualificationError, "digest-pinned"):
            qualify.network_smoke(kube, "registry.example.com/probe:latest", "kata-vm-isolation", {})
        self.assertEqual(kube.created, [])

    def test_cli_requires_mutation_opt_in_and_never_qualifies_execution(self):
        args = ["network-smoke", "--kubeconfig", "/example/test-config", "--context", "explicit",
                "--cluster-uid", "expected", "--storage-class", "home", "--probe-image", IMAGE]
        with patch.object(qualify, "Kubernetes") as client, contextlib.redirect_stdout(io.StringIO()) as output:
            code = qualify.main(args)
        self.assertEqual(code, 1)
        self.assertFalse(client.called)
        result = json.loads(output.getvalue())
        self.assertFalse(result["qualifiesExecution"])
        self.assertEqual(result["status"], "blocked")

    def test_successful_preflight_is_not_execution_evidence(self):
        args = ["preflight", "--kubeconfig", "/example/test-config", "--context", "explicit",
                "--cluster-uid", "expected", "--storage-class", "home"]
        with patch.object(qualify, "Kubernetes", return_value=FakeKubernetes()), contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(qualify.main(args), 0)
        result = json.loads(output.getvalue())
        self.assertEqual(result["status"], "completed")
        self.assertIs(result["qualifiesExecution"], False)


if __name__ == "__main__":
    unittest.main()
