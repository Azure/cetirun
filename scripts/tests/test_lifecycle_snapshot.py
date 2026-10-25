# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Synthetic lifecycle observations; kubectl is always replaced by a local Python process."""

import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from urllib.parse import urlencode
import uuid


SCRIPT = Path(__file__).resolve().parents[1] / "sandbox-platform-qualify.py"
SPEC = importlib.util.spec_from_file_location("lifecycle_snapshot", SCRIPT)
qualify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(qualify)
REAL_POPEN = subprocess.Popen
SENTINEL = "sk-dummy-sensitive-synthetic-value.example.com"
NAMESPACE = "snapshot-example"
LABELS = {"app.kubernetes.io/name": "cetirun", "app.kubernetes.io/instance": "selected-runtime"}
IMAGE = "registry.example.com/runtime@sha256:" + "a" * 64


def scope():
    receipt = json.loads((SCRIPT.parent / "tests/fixtures/lifecycle_acceptance.json").read_text())
    resources = {key: {"name": name, "uid": key + "-uid"} for key, name in (
        ("agent", "selected-agent"), ("namespace", NAMESPACE), ("statefulSet", "selected-runtime"),
        ("pod", "selected-runtime-0"), ("homePvc", "selected-home"), ("homePv", "selected-pv"),
        ("attachment", "selected-attachment"),
    )}
    resources["agent"].update(resourceVersion="1", generation=3)
    return {"version": "v1", "run": "selected-run", "case": receipt["case"],
            "artifacts": receipt["artifacts"], "resources": resources}


def owner(key):
    pin = scope()["resources"][key]
    return {"apiVersion": "apps/v1" if key == "statefulSet" else "cetirun.microsoft.com/v1alpha1",
            "kind": "StatefulSet" if key == "statefulSet" else "Agent",
            "name": pin["name"], "uid": pin["uid"], "controller": True}


def paths():
    pins = {"cluster": {"name": "kube-system"}, **scope()["resources"]}
    selected = {key: base.format(namespace=NAMESPACE) + "/" + pins[key]["name"]
                for key, (_, _, base) in qualify.SNAPSHOT_RESOURCES.items()}
    selected["statefulSetList"] = qualify.SNAPSHOT_RESOURCES["statefulSet"][2].format(
        namespace=NAMESPACE) + "?limit=2"
    selected["podList"] = qualify.SNAPSHOT_RESOURCES["pod"][2].format(namespace=NAMESPACE) + "?" + urlencode({
        "limit": 2, "labelSelector": ",".join(name + "=" + LABELS[name] for name in sorted(LABELS)),
    })
    return selected


PATHS = paths()


def fixture():
    pins = {"cluster": {"name": "kube-system", "uid": "cluster-uid"}, **scope()["resources"]}
    objects = {"config": {
        "current-context": "selected-context",
        "contexts": [{"name": "selected-context", "context": {"cluster": "selected-cluster", "user": "selected-user"}}],
        "users": [{"name": "selected-user", "user": {"token": "REDACTED"}}],
        "clusters": [{"name": "selected-cluster", "cluster": {"server": "https://cluster.example.com"}}],
    }}
    for key, (api, kind, base) in qualify.SNAPSHOT_RESOURCES.items():
        metadata = {**pins[key], "resourceVersion": "1", "annotations": {"synthetic-unused": SENTINEL}}
        if "{namespace}" in base:
            metadata["namespace"] = NAMESPACE
        if key in ("agent", "namespace"):
            metadata["labels"] = {qualify.RUN_LABEL: "selected-run"}
        objects[PATHS[key]] = {"apiVersion": api, "kind": kind, "metadata": metadata}
    objects[PATHS["agent"]].update(
        spec={"agent": {"type": "openclaw"}, "user": {"username": SENTINEL}},
        status={"namespace": NAMESPACE, "phase": "Ready", "observedGeneration": 3, "accessUrl": SENTINEL},
    )
    namespace = objects[PATHS["namespace"]]
    namespace["metadata"]["ownerReferences"] = [owner("agent")]
    namespace["status"] = {"phase": "Active"}
    runtime = {
        "containers": [{"name": "cetirun", "image": IMAGE, "env": [{"name": "UNUSED", "value": SENTINEL}],
                        "volumeMounts": [{"name": "data", "mountPath": "/home/vibe"}]}],
        "volumes": [{"name": "data", "persistentVolumeClaim": {"claimName": "selected-home"}},
                    {"name": "unused-secret-reference", "secret": {"secretName": SENTINEL}}],
    }
    stateful_set = objects[PATHS["statefulSet"]]
    stateful_set["metadata"].update(ownerReferences=[owner("agent")], generation=2)
    stateful_set.update(
        spec={"replicas": 1, "selector": {"matchLabels": dict(LABELS)},
              "template": {"metadata": {"labels": dict(LABELS)}, "spec": copy.deepcopy(runtime)}},
        status={"observedGeneration": 2, "replicas": 1, "readyReplicas": 1, "currentReplicas": 1,
                "updatedReplicas": 1, "currentRevision": "selected-revision", "updateRevision": "selected-revision"},
    )
    pod = objects[PATHS["pod"]]
    pod["metadata"].update(ownerReferences=[owner("statefulSet")],
                           labels={**LABELS, "controller-revision-hash": "selected-revision"})
    pod.update(
        spec={**copy.deepcopy(runtime), "nodeName": "selected-node"},
        status={"phase": "Running", "conditions": [{"type": "Ready", "status": "True", "message": SENTINEL}],
                "containerStatuses": [{"name": "cetirun", "ready": True, "imageID": IMAGE,
                                       "state": {"running": {"startedAt": "2026-10-23T00:00:00Z"}}}]},
    )
    objects[PATHS["homePvc"]].update(spec={"volumeName": "selected-pv"}, status={"phase": "Bound"})
    objects[PATHS["homePv"]].update(
        spec={"claimRef": {"kind": "PersistentVolumeClaim", "name": "selected-home",
                          "namespace": NAMESPACE, "uid": pins["homePvc"]["uid"]},
              "csi": {"driver": "csi.example.com", "volumeHandle": SENTINEL, "volumeAttributes": {"unused": SENTINEL}}},
        status={"phase": "Bound"},
    )
    objects[PATHS["attachment"]].update(
        spec={"attacher": "csi.example.com", "nodeName": "selected-node",
              "source": {"persistentVolumeName": "selected-pv"}},
        status={"attached": True, "attachmentMetadata": {"unused": SENTINEL}},
    )
    for key in ("statefulSet", "pod"):
        api, kind, _ = qualify.SNAPSHOT_RESOURCES[key]
        objects[PATHS[key + "List"]] = {
            "apiVersion": api, "kind": kind + "List", "metadata": {"resourceVersion": "10"},
            "items": [objects[PATHS[key]]],
        }
    return objects


class KubectlDouble:
    def __init__(self, kubeconfig):
        self.kubeconfig = str(kubeconfig)
        self.responses = fixture()
        self.calls, self.processes, self.counts, self.scripts = [], [], {}, {}
        self.change = lambda key, count, value: value

    def spawn(self, command, **options):
        assert command[:7] == [
            "kubectl", "--kubeconfig", self.kubeconfig, "--context", "selected-context",
            "--request-timeout=5s", "--v=0",
        ], command
        assert options["stdin"] == subprocess.DEVNULL
        assert options["stdout"] == subprocess.PIPE and options["stderr"] == subprocess.DEVNULL
        assert options["start_new_session"] is True and options["env"]["KUBECTL_KUBERC"] == "false"
        if command[7:] == qualify.DeploymentKubernetes.CONFIG_COMMAND:
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


class LifecycleSnapshotTests(unittest.TestCase):
    def setUp(self):
        self.directory = Path.cwd() / (".lifecycle-snapshot-test-" + uuid.uuid4().hex)
        self.directory.mkdir()
        self.addCleanup(shutil.rmtree, self.directory)
        self.scope_path = self.directory / "scope.json"
        self.scope_path.write_text(json.dumps(scope()))
        self.kubeconfig = self.directory / "synthetic-config.json"
        self.kubeconfig.write_text(json.dumps(fixture()["config"]))

    def arguments(self):
        return ["lifecycle-snapshot", "--kubeconfig", str(self.kubeconfig), "--context", "selected-context",
                "--cluster-uid", "cluster-uid", "--scope", str(self.scope_path)]

    def run_snapshot(self, double=None, args=None):
        double = double or KubectlDouble(self.kubeconfig)
        with patch.object(qualify.subprocess, "Popen", side_effect=double.spawn), \
             patch.object(qualify.subprocess, "run", side_effect=AssertionError("unbounded runner is forbidden")), \
             contextlib.redirect_stdout(io.StringIO()) as stdout, contextlib.redirect_stderr(io.StringIO()) as stderr:
            code = qualify.main(self.arguments() if args is None else args)
        self.assertEqual(stderr.getvalue(), "")
        text = stdout.getvalue()
        self.assertLess(len(text.encode()), 8192)
        for private in (SENTINEL, NAMESPACE, IMAGE, "selected-context", "cluster-uid", "selected-agent",
                        "selected-node", str(self.directory), "REDACTED", "volumeHandle", "accessUrl"):
            self.assertNotIn(private, text)
        self.assertTrue(all(p.poll() is not None for p in double.processes), "subprocess was not reaped")
        result = json.loads(text)
        self.assertIs(result["qualifiesExecution"], False)
        self.assertEqual(result["liveAcceptance"], "not-established")
        return code, result, double

    def assert_blocked(self, double=None, reason=None, args=None):
        code, result, double = self.run_snapshot(double, args)
        self.assertEqual(code, 1)
        self.assertEqual(result["status"], "blocked")
        self.assertIsNone(result["snapshot"])
        if reason:
            self.assertIn(reason, [item["reason"] for item in result["checks"]])
        return result, double

    def test_positive_observation_is_read_only_pinned_and_not_acceptance(self):
        code, result, double = self.run_snapshot()
        self.assertEqual(code, 0)
        self.assertEqual(result["status"], "observed")
        self.assertTrue(all(item["status"] == "observed" for item in result["checks"]))
        self.assertTrue(all(item["status"] == "unknown" for item in result["unverified"]))
        self.assertEqual(set(double.counts), set(double.responses))
        self.assertTrue(all(count == 2 for count in double.counts.values()))
        self.assertEqual(double.calls[1][-1], PATHS["cluster"])
        self.assertLessEqual(len(double.calls), qualify.DeploymentKubernetes.MAX_COMMANDS)
        snapshot = result["snapshot"]
        self.assertEqual(set(snapshot), set(qualify.LIFECYCLE_SNAPSHOT_FIELDS))
        self.assertEqual(snapshot["artifactSet"],
                         qualify.lifecycle_artifact_fingerprint(scope()["artifacts"], scope()["case"]))
        self.assertTrue(all(qualify.lifecycle_digest(pin) for pin in snapshot["resources"].values()))
        self.assertEqual(snapshot["workload"]["agent"], snapshot["resources"]["agent"])
        self.assertEqual(snapshot["workload"]["pod"], snapshot["workload"]["observedPod"])
        self.assertIsNone(snapshot["workload"]["ready"])
        self.assertIsNone(snapshot["workload"]["observedGeneration"])
        self.assertEqual(result["reported"], {"agentObservedGeneration": 3, "podReady": True})
        for key in ("owner", "home", "accounting", "models", "peer", "extensions"):
            self.assertIsNone(snapshot[key])
        self.assertTrue(result["storage"]["csiAttached"])
        self.assertIsNone(result["storage"]["csiReleased"])
        self.assertNotEqual(qualify.assess_lifecycle_receipt(result)["status"], "matched")
        self.assertEqual(self.run_snapshot()[1]["snapshot"], snapshot)

    def test_unpinned_attachment_is_partial_not_absence_or_release(self):
        selected = scope()
        selected["resources"]["attachment"] = None
        self.scope_path.write_text(json.dumps(selected))
        code, result, double = self.run_snapshot()
        self.assertEqual(code, 1)
        self.assertEqual(result["status"], "blocked")
        self.assertIsNotNone(result["snapshot"])
        check = next(item for item in result["checks"] if item["check"] == "csi-attachment")
        self.assertEqual(check, {"check": "csi-attachment", "status": "unknown", "reason": "attachment-not-pinned"})
        self.assertIsNone(result["storage"]["attachment"])
        self.assertIsNone(result["storage"]["csiAttached"])
        self.assertIsNone(result["storage"]["csiReleased"])
        self.assertNotIn(PATHS["attachment"], double.counts)
        self.assertEqual(result["checks"][-1]["status"], "observed")

    def test_missing_source_artifact_or_resource_pins_precede_any_subprocess(self):
        mutations = [
            lambda s: s.pop("run"), lambda s: s["artifacts"].pop("sourceCommit"),
            lambda s: s["artifacts"].pop("sourceTree"),
            lambda s: s["artifacts"].update(sourceCommit="main"),
            lambda s: s["artifacts"]["images"].pop("runtime"),
            lambda s: s["artifacts"]["charts"].update(runtime="latest"),
            lambda s: s["artifacts"].pop("configuration"),
            lambda s: s["resources"]["agent"].pop("generation"),
            lambda s: s["resources"]["agent"].pop("resourceVersion"),
            lambda s: s["resources"]["agent"].update(generation=True),
            lambda s: s["resources"]["homePv"].pop("uid"),
            lambda s: s["resources"].pop("attachment"),
            lambda s: s["resources"]["namespace"].update(name="kube-system"),
            lambda s: s["resources"]["pod"].update(name="../escape"),
            lambda s: s.update(run="x,y=z"), lambda s: s.update(extra=SENTINEL),
        ]
        for index, mutate in enumerate(mutations):
            with self.subTest(index=index):
                selected = scope()
                mutate(selected)
                self.scope_path.write_text(json.dumps(selected))
                _, double = self.assert_blocked()
                self.assertEqual(double.calls, [])

    def test_bad_json_is_bounded_and_sanitized(self):
        for raw in (SENTINEL, "[]", '{"version":"v1","version":"v1"}', '{"run":NaN}',
                    '{"value":"' + SENTINEL * 4096 + '"}'):
            with self.subTest(size=len(raw)):
                self.scope_path.write_text(raw)
                _, double = self.assert_blocked()
                self.assertEqual(double.calls, [])

    def test_nonregular_inputs_never_start_kubectl_or_block_on_a_fifo(self):
        start = time.monotonic()
        directory = self.directory / "directory"
        directory.mkdir()
        fifo = self.directory / "fifo"
        os.mkfifo(fifo)
        symlink = self.directory / "symlink"
        symlink.symlink_to(self.scope_path)
        for option in ("--scope", "--kubeconfig"):
            for path in (directory, fifo, symlink):
                with self.subTest(option=option, path=path.name):
                    args = self.arguments()
                    args[args.index(option) + 1] = str(path)
                    _, double = self.assert_blocked(reason="input-not-regular-file", args=args)
                    self.assertEqual(double.calls, [])
        self.assertLess(time.monotonic() - start, 2)
        args = self.arguments()
        args[-1] = "\0" + SENTINEL
        self.assert_blocked(reason="invalid-input-path", args=args)

    def test_mutation_and_legacy_mode_flags_are_rejected_without_commands(self):
        for extra in (["--allow-test-resources"], ["--network-protocol", "TCP"], ["--receipt", SENTINEL],
                      ["--probe-image", IMAGE], ["--namespace", SENTINEL]):
            with self.subTest(extra=extra[0]):
                code, result, double = self.run_snapshot(args=self.arguments() + extra)
                self.assertEqual(code, 2)
                self.assertIn("invalid-command-line", result["error"])
                self.assertEqual(double.calls, [])
        for option in ("--kubeconfig", "--context", "--cluster-uid", "--scope"):
            args = self.arguments()
            index = args.index(option)
            del args[index:index + 2]
            self.assertEqual(self.run_snapshot(args=args)[0], 2)

    def test_auth_and_wrong_cluster_fail_before_workload_reads(self):
        for key in ("exec", "auth-provider", "tokenFile", "as", "as-groups", "client-key", "client-certificate"):
            double = KubectlDouble(self.kubeconfig)
            double.responses["config"]["users"][0]["user"][key] = SENTINEL
            self.assert_blocked(double)
            self.assertEqual(set(double.counts), {"config"})
        double = KubectlDouble(self.kubeconfig)
        double.responses[PATHS["cluster"]]["metadata"]["uid"] = "other-cluster"
        self.assert_blocked(double, "resource-uid-mismatch")
        self.assertEqual(set(double.counts), {"config", PATHS["cluster"]})

    def test_replaced_identity_and_stale_agent_are_never_adopted(self):
        for key in qualify.SNAPSHOT_RESOURCES:
            with self.subTest(resource=key):
                double = KubectlDouble(self.kubeconfig)
                double.responses[PATHS[key]]["metadata"]["uid"] = "replacement-uid"
                self.assert_blocked(double, "resource-uid-mismatch")
        for field, value in (("generation", 4), ("generation", True), ("resourceVersion", "2")):
            double = KubectlDouble(self.kubeconfig)
            double.responses[PATHS["agent"]]["metadata"][field] = value
            self.assert_blocked(double, "agent-version-mismatch")

    def test_run_labels_and_controller_owners_must_match(self):
        for key in qualify.SNAPSHOT_RESOURCES:
            if key == "cluster":
                continue
            with self.subTest(resource=key):
                double = KubectlDouble(self.kubeconfig)
                double.responses[PATHS[key]]["metadata"].setdefault("labels", {})[qualify.RUN_LABEL] = "another-run"
                self.assert_blocked(double, "run-label-mismatch")
        for key in ("agent", "namespace"):
            double = KubectlDouble(self.kubeconfig)
            double.responses[PATHS[key]]["metadata"]["labels"].clear()
            self.assert_blocked(double, "run-label-mismatch")
        for key in ("namespace", "statefulSet", "pod"):
            double = KubectlDouble(self.kubeconfig)
            double.responses[PATHS[key]]["metadata"]["ownerReferences"][0]["uid"] = "other-owner"
            self.assert_blocked(double, "resource-owner-mismatch")

    def test_multiple_missing_and_paginated_candidates_fail_without_selection(self):
        for key in ("statefulSetList", "podList"):
            for count in (0, 2):
                double = KubectlDouble(self.kubeconfig)
                original = double.responses[PATHS[key]]["items"][0]
                double.responses[PATHS[key]]["items"] = [copy.deepcopy(original) for _ in range(count)]
                self.assert_blocked(double, "candidate-set-not-singleton")
            for field, value in (("continue", SENTINEL), ("remainingItemCount", 1), ("resourceVersion", "")):
                double = KubectlDouble(self.kubeconfig)
                double.responses[PATHS[key]]["metadata"][field] = value
                self.assert_blocked(double, "candidate-list-incomplete")

    def test_typed_list_items_may_omit_type_metadata_but_not_change_identity(self):
        double = KubectlDouble(self.kubeconfig)
        for key in ("statefulSetList", "podList"):
            item = copy.deepcopy(double.responses[PATHS[key]]["items"][0])
            item.pop("apiVersion")
            item.pop("kind")
            double.responses[PATHS[key]]["items"] = [item]
        self.assertEqual(self.run_snapshot(double)[0], 0)
        double.responses[PATHS["podList"]]["items"][0]["metadata"]["uid"] = "other-pod"
        self.assert_blocked(double, "resource-uid-mismatch")

    def test_pending_terminating_and_unready_resources_are_not_ready_or_cleanup(self):
        for key in qualify.SNAPSHOT_RESOURCES:
            double = KubectlDouble(self.kubeconfig)
            double.responses[PATHS[key]]["metadata"]["deletionTimestamp"] = "2026-10-23T00:00:00Z"
            self.assert_blocked(double, "resource-terminating")
        for key, field, value, reason in (
            ("agent", "phase", "Pending", "agent-not-current-ready"),
            ("agent", "observedGeneration", 2, "agent-not-current-ready"),
            ("namespace", "phase", "Terminating", "namespace-not-active"),
            ("statefulSet", "readyReplicas", 0, "statefulset-not-current-ready"),
            ("statefulSet", "observedGeneration", 1, "statefulset-not-current-ready"),
            ("pod", "phase", "Pending", "pod-not-current-ready"),
            ("homePvc", "phase", "Pending", "home-storage-not-bound"),
            ("homePv", "phase", "Released", "home-storage-not-bound"),
            ("attachment", "attached", False, "csi-attachment-not-observed"),
        ):
            double = KubectlDouble(self.kubeconfig)
            double.responses[PATHS[key]]["status"][field] = value
            self.assert_blocked(double, reason)

    def test_home_and_csi_must_link_exactly_without_reading_any_contents(self):
        mutations = [
            ("homePvc", lambda v: v["spec"].update(volumeName="other-volume"), "home-storage-binding-mismatch"),
            ("homePv", lambda v: v["spec"]["claimRef"].update(uid="other-claim"), "home-storage-binding-mismatch"),
            ("pod", lambda v: v["spec"]["volumes"][0]["persistentVolumeClaim"].update(claimName="other-home"),
             "home-claim-mismatch"),
            ("pod", lambda v: v["spec"]["containers"][0]["volumeMounts"][0].update(subPath="other-home"),
             "home-mount-mismatch"),
            ("attachment", lambda v: v["spec"].update(nodeName="source-node"), "csi-attachment-binding-mismatch"),
            ("attachment", lambda v: v["spec"].update(attacher="other.example.com"), "csi-attachment-binding-mismatch"),
            ("attachment", lambda v: v["spec"]["source"].update(persistentVolumeName="other-pv"),
             "csi-attachment-binding-mismatch"),
            ("attachment", lambda v: v["status"].update(attachError={"message": SENTINEL}),
             "csi-attachment-not-observed"),
        ]
        for key, mutate, reason in mutations:
            double = KubectlDouble(self.kubeconfig)
            mutate(double.responses[PATHS[key]])
            self.assert_blocked(double, reason)

    def test_main_image_revisions_and_readiness_cannot_be_filled_from_agent_status(self):
        for replacement in ("registry.example.com/runtime:latest", IMAGE.replace("a" * 64, "b" * 64)):
            double = KubectlDouble(self.kubeconfig)
            double.responses[PATHS["pod"]]["spec"]["containers"][0]["image"] = replacement
            self.assert_blocked(double, "runtime-image-pin-mismatch")
        for mutate, reason in (
            (lambda p: p["metadata"]["labels"].update({"controller-revision-hash": "old-revision"}),
             "pod-revision-or-selector-mismatch"),
            (lambda p: p["status"]["conditions"][0].update(status="False"), "pod-not-current-ready"),
            (lambda p: p["status"]["containerStatuses"][0].update(ready=False), "main-container-not-running-ready"),
            (lambda p: p["status"]["containerStatuses"][0].update(imageID="unknown"), "runtime-image-id-unverified"),
        ):
            double = KubectlDouble(self.kubeconfig)
            mutate(double.responses[PATHS["pod"]])
            self.assert_blocked(double, reason)

    def test_stable_rechecks_detect_named_candidate_and_configuration_drift(self):
        for selected in PATHS.values():
            with self.subTest(path=selected):
                double = KubectlDouble(self.kubeconfig)

                def change(key, count, value):
                    if key == selected and count > 1:
                        if key in (PATHS["statefulSetList"], PATHS["podList"]):
                            value["items"][0]["metadata"]["resourceVersion"] = "new-version"
                        else:
                            value["metadata"]["resourceVersion"] = "new-version"
                    return value

                double.change = change
                self.assert_blocked(double, "agent-version-mismatch" if selected == PATHS["agent"] else "resource-version-drift")
        double = KubectlDouble(self.kubeconfig)
        double.change = lambda key, count, value: {**value, "changed": SENTINEL} if key == "config" and count > 1 else value
        self.assert_blocked(double, "kubeconfig-drift")
        double = KubectlDouble(self.kubeconfig)

        def replace_file(key, count, value):
            if key == PATHS["attachment"]:
                self.kubeconfig.write_text("changed-synthetic-config")
            return value

        double.change = replace_file
        self.assert_blocked(double, "kubeconfig-file-drift")

    def test_runner_allowlist_forbids_discovery_mutation_and_unpinned_gets(self):
        args = SimpleNamespace(kubeconfig=str(self.kubeconfig), context="selected-context", cluster_uid="cluster-uid")
        kube = qualify.SnapshotKubernetes(args, scope())
        with patch.object(qualify.subprocess, "Popen") as popen:
            for args in (
                ["get", "pods", "-A"], ["get", "--raw", "/api/v1/pods"],
                ["get", "--raw", "/apis/storage.k8s.io/v1/volumeattachments"],
                ["get", "--raw", f"/api/v1/namespaces/{NAMESPACE}/secrets/example"], ["get", "--raw", "/version"],
                ["get", "--raw", PATHS["pod"] + "/log"], ["config", "view", "--raw"],
                ["create", "-f", "-"], ["apply", "-f", "-"], ["patch", "agent"], ["delete", "pod"],
                ["exec", "pod"], ["port-forward", "pod"], ["attach", "pod"],
            ):
                with self.assertRaises(qualify.PreflightBlocked):
                    kube.run(args)
            for key in ("secret", "node", "other-agent", "../pod"):
                with self.assertRaises(qualify.PreflightBlocked):
                    kube.get(key)
            popen.assert_not_called()

    def test_command_failure_malformed_output_and_budgets_stay_unknown_sanitized(self):
        for script in (
            f"import sys;sys.stdout.write('{SENTINEL}');sys.stderr.write('{SENTINEL}');sys.exit(1)",
            "print('[]')", "print('{\"metadata\":[]}')", "import sys;sys.stdout.buffer.write(b'\\xff')",
            f"import sys;sys.stdout.write('x'*{qualify.DeploymentKubernetes.MAX_BYTES + 1})",
        ):
            double = KubectlDouble(self.kubeconfig)
            double.scripts[PATHS["pod"]] = script
            result, _ = self.assert_blocked(double)
            self.assertEqual(next(c for c in result["checks"] if c["check"] == "pod")["status"], "unknown")
        with patch.object(qualify.SnapshotKubernetes, "MAX_COMMANDS", 1):
            self.assert_blocked(reason="command-budget-exhausted")
        double = KubectlDouble(self.kubeconfig)
        double.scripts["config"] = "import time;time.sleep(60)"
        start = time.monotonic()
        with patch.object(qualify.SnapshotKubernetes, "COMMAND_SECONDS", 0.1):
            self.assert_blocked(double, "request-timeout")
        self.assertLess(time.monotonic() - start, 3)

    def test_cancellation_restores_signal_handler_and_never_accepts_partial_data(self):
        before = signal.getsignal(signal.SIGTERM)
        double = KubectlDouble(self.kubeconfig)
        double.change = lambda *args: (_ for _ in ()).throw(KeyboardInterrupt())
        code, result, _ = self.run_snapshot(double)
        self.assertEqual(code, 130)
        self.assertIsNone(result["snapshot"])
        self.assertEqual(signal.getsignal(signal.SIGTERM), before)
        self.assertIn("cancelled", [item["reason"] for item in result["checks"]])


if __name__ == "__main__":
    unittest.main()
