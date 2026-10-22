# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Synthetic receipt assertions only; no owners, providers or cluster access."""

import contextlib
import copy
import hashlib
import importlib.util
import io
import itertools
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "sandbox-platform-qualify.py"
FIXTURE = Path(__file__).parent / "fixtures/lifecycle_acceptance.json"
SPEC = importlib.util.spec_from_file_location("lifecycle_qualify", SCRIPT)
qualify = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(qualify)


def digest(value):
    return "sha256:" + hashlib.sha256(value.encode()).hexdigest()


def receipt(runtime="openclaw", execution="sandbox-native", model="shared",
            identity="none", helpers=()):
    value = json.loads(FIXTURE.read_text())
    value["case"].update(runtime=runtime, execution=execution, modelSource=model,
                         identity=identity, helpers=list(helpers))
    value["authorization"] = dict.fromkeys(value["authorization"], True)
    artifact = qualify.lifecycle_artifact_fingerprint(value["artifacts"], value["case"])
    sources = {"shared": ("shared",), "copilot-only": ("copilot",),
               "mixed": ("shared", "copilot")}[model]
    snapshot = {
        "artifactSet": artifact,
        "owner": {
            "path": "owner-rest", "authentication": "oidc",
            "subject": digest("owner"), "peerSubject": digest("peer"),
            "peerReadStatus": 403, "peerMutationStatus": 403,
            "nativeHttpStatus": 200, "nativeWebSocketStatus": 101,
        },
        "resources": {name: digest(name) for name in
                      ("agent", "namespace", "statefulSet", "homePvc", "homePv")},
        "home": {"path": "owner-application", "checksums": value["expectedHome"],
                 "readWrite": True},
        "accounting": {"key": digest("key"), "budget": "2.00",
                       "spend": "0.10", "spendAfterCall": "0.11"},
        "workload": {"pod": digest("pod-0"), "node": digest("node-a"),
                     "generation": 1, "ready": True},
        "models": {source: {"reply": True, "expectedToolChecksum": digest(source),
                            "observedToolChecksum": digest(source)} for source in sources},
        "peer": {"subject": digest("peer"), "agent": digest("peer-agent"),
                 "pod": digest("peer-pod"), "home": digest("peer-home"),
                 "key": digest("peer-key"), "budget": "3.00", "spend": "0.05",
                 "ready": True},
        "extensions": dict.fromkeys(qualify.lifecycle_extension_checks(value["case"]), True),
    }
    value["before"] = copy.deepcopy(snapshot)
    operations = ["restart", "suspend-resume"]
    if execution == "sandbox-native":
        operations += ["migrate", "migrate"]
    for index, operation in enumerate(operations, 1):
        previous_node = snapshot["workload"]["node"]
        snapshot = copy.deepcopy(snapshot)
        snapshot["workload"].update(pod=digest(f"pod-{index}"), generation=index + 1)
        if operation == "migrate":
            snapshot["workload"]["node"] = digest("node-b" if index == 3 else "node-a")
        snapshot["accounting"].update(spend=f"0.{index + 1}0",
                                       spendAfterCall=f"0.{index + 1}1")
        value["operations"].append({
            "operation": operation,
            "path": "admin-kubernetes" if operation == "migrate" else "owner-rest",
            "requestAccepted": True, "stopRequested": True, "stopObserved": True,
            "sourceHealthy": True, "sourceTerminated": True,
            "sourceNode": previous_node, "csiReleased": True, "csiAttached": True,
            "currentWorkloadReady": True, "elapsedSeconds": 2, "limitSeconds": 60,
            "snapshot": snapshot,
        })
    outcomes = {
        "duplicate": ("accepted", "completed"),
        "stale-uid": ("rejected", "rejected"),
        "stale-version": ("rejected", "rejected"),
        "inflight-replacement": ("rejected", "rejected"),
        "lost-response": ("unknown", "pending"),
        "controller-takeover": ("accepted", "completed"),
        "destination-failure": ("accepted", "failed"),
        "suspend-precedence": ("accepted", "suspended"),
        "delete-precedence": ("accepted", "deleted"),
    }
    for name in qualify.lifecycle_request_cases(execution):
        response, outcome = outcomes[name]
        value["requests"].append({
            "case": name, "artifactSet": artifact, "response": response,
            "outcome": outcome, "reportedState": outcome, "observedState": outcome,
            "authoritativeObservation": True,
            "intentBefore": digest(name), "intentAfter": digest(name),
            "appliesBefore": 1, "appliesAfter": 1,
            "elapsedSeconds": 2, "limitSeconds": 60,
        })
    value["cleanup"] = {
        "artifactSet": artifact, "path": "owner-rest", "authentication": "oidc",
        "subject": digest("owner"), "ordinary": True, "forced": False,
        "remaining": dict.fromkeys(
            ("agent", "namespace", "homePvc", "homePv", "attachments", "controlRecords",
             "reservations", "gatewayKey"), 0),
        "peer": copy.deepcopy(snapshot["peer"]),
        "elapsedSeconds": 2, "limitSeconds": 60,
    }
    return value


class LifecycleAcceptanceTests(unittest.TestCase):
    def assess(self, value):
        with patch.object(qualify.subprocess, "run",
                          side_effect=AssertionError("offline assertions must not run commands")):
            result = qualify.assess_lifecycle_receipt(value)
        self.assertLess(len(json.dumps(result).encode("utf-8")), 4096)
        return result

    def assert_check(self, value, name, status, reason=None):
        result = self.assess(value)
        check = next(item for item in result["checks"] if item["check"] == name)
        self.assertEqual(check["status"], status, check)
        if reason is not None:
            self.assertEqual(check["reason"], reason)
        self.assertFalse(result["qualifiesExecution"])
        self.assertEqual(result["liveAcceptance"], "not-established")
        return result

    def test_core_runtime_execution_model_matrix(self):
        for runtime, execution, model in itertools.product(
                ("openclaw", "hermes"), ("ordinary", "sandbox-native"),
                ("shared", "copilot-only", "mixed")):
            with self.subTest(runtime=runtime, execution=execution, model=model):
                result = self.assess(receipt(runtime, execution, model))
                self.assertEqual(result["status"], "matched", result)
                self.assertEqual(result["evidenceKind"], "synthetic")
                self.assertFalse(result["qualifiesExecution"])
                self.assertEqual(result["liveAcceptance"], "not-established")

    def test_optional_identity_and_helper_assertions(self):
        for runtime, execution, model, identity, helper in itertools.product(
                ("openclaw", "hermes"), ("ordinary", "sandbox-native"),
                ("shared", "copilot-only", "mixed"),
                ("none", "entra-obo", "azure-app-only"),
                ("skills", "memory", "doctor", "logger", "desktop")):
            with self.subTest(runtime=runtime, execution=execution, model=model, identity=identity, helper=helper):
                value = receipt(runtime, execution, model, identity, (helper,))
                self.assertEqual(self.assess(value)["status"], "matched")
                value["operations"][-1]["snapshot"]["extensions"][
                    next(iter(value["before"]["extensions"]))] = False
                self.assert_check(value, "extensions", "failed")

    def test_blocked_fixture_is_not_a_live_pass(self):
        result = self.assess(json.loads(FIXTURE.read_text()))
        self.assertEqual(result["status"], "blocked")
        self.assert_check(json.loads(FIXTURE.read_text()), "authorization", "blocked")

    def test_home_manifest_pins_known_synthetic_bytes(self):
        value = json.loads(FIXTURE.read_text())
        self.assertEqual(value["expectedHome"], {"witness": digest("CetiRun synthetic HOME witness\n")})

    def test_reported_live_evidence_still_requires_independent_review(self):
        value = receipt()
        value["evidenceKind"] = "live"
        result = self.assess(value)
        self.assertEqual(result["status"], "matched")
        self.assertEqual(result["liveAcceptance"], "not-established")
        self.assertFalse(result["qualifiesExecution"])

    def test_artifact_drift_cannot_be_hidden_by_a_run_label(self):
        for section in ("before", "operation", "request", "cleanup", "source", "chart"):
            with self.subTest(section=section):
                value = receipt()
                if section == "operation":
                    value["operations"][0]["snapshot"]["artifactSet"] = digest("other")
                elif section == "request":
                    value["requests"][0]["artifactSet"] = digest("other")
                elif section == "source":
                    value["artifacts"]["sourceTree"] = "3" * 40
                elif section == "chart":
                    value["artifacts"]["charts"]["runtime"] = digest("other")
                else:
                    value[section]["artifactSet"] = digest("other")
                self.assert_check(value, "artifacts", "failed")

    def test_tags_missing_pins_and_unknown_tuple_fail(self):
        for field, replacement in (
                ("images", {"operator": "example:latest"}),
                ("charts", {}), ("sourceTree", "main"), ("configuration", None)):
            value = receipt()
            value["artifacts"][field] = replacement
            self.assert_check(value, "artifacts", "failed")
        for field, replacement in (("runtime", "custom"), ("execution", "gvisor"),
                                   ("helpers", ["unknown"]), ("identity", "fabricated")):
            value = receipt()
            value["case"][field] = replacement
            self.assertNotEqual(self.assess(value)["status"], "matched")

    def test_real_owner_path_and_second_owner_are_required(self):
        for field, replacement in (
                ("path", "admin-kubernetes"), ("authentication", "synthetic-headers"),
                ("peerSubject", digest("owner")), ("peerReadStatus", 200),
                ("peerMutationStatus", 200), ("nativeHttpStatus", 302),
                ("nativeWebSocketStatus", 200)):
            value = receipt()
            value["operations"][0]["snapshot"]["owner"][field] = replacement
            self.assert_check(value, "owner-access", "failed")

    def test_every_authorization_prerequisite_is_explicit(self):
        for key in receipt()["authorization"]:
            for replacement in (False, None, "true", 1):
                with self.subTest(key=key, value=replacement):
                    value = receipt()
                    value["authorization"][key] = replacement
                    self.assert_check(value, "authorization", "blocked")

    def test_home_identity_is_not_a_checksum_or_write_witness(self):
        for section, field, replacement in (
                ("home", "checksums", {}), ("home", "checksums", {"witness": digest("corrupt")}),
                ("home", "readWrite", False), ("home", "path", "protected-pod-exec"),
                ("resources", "homePv", digest("replacement"))):
            value = receipt()
            value["operations"][0]["snapshot"][section][field] = replacement
            self.assert_check(value, "home", "failed")

    def test_nonzero_spend_retention_and_new_accumulation(self):
        mutations = (
            ("before", "spend", "0"), ("before", "spend", "-1"),
            ("before", "spend", "NaN"), ("before", "spend", "Infinity"),
            ("before", "spend", True), ("after", "spend", "0.10"),
            ("after", "spendAfterCall", "0.20"), ("after", "budget", "4.00"),
            ("after", "key", digest("replacement")),
        )
        for point, field, replacement in mutations:
            with self.subTest(point=point, field=field, value=replacement):
                value = receipt()
                target = value["before"] if point == "before" else value["operations"][0]["snapshot"]
                target["accounting"][field] = replacement
                self.assert_check(value, "accounting", "failed")

    def test_ready_generation_and_acceptance_are_not_completion(self):
        for key in ("requestAccepted", "stopRequested", "stopObserved",
                    "sourceTerminated", "currentWorkloadReady"):
            value = receipt()
            value["operations"][0][key] = False
            self.assert_check(value, "recovery", "failed")
        for field, replacement in (("ready", False), ("generation", 1),
                                   ("pod", digest("pod-0"))):
            value = receipt()
            value["operations"][0]["snapshot"]["workload"][field] = replacement
            self.assert_check(value, "recovery", "failed")

    def test_model_source_and_independent_tool_effect_are_required(self):
        for model in ("shared", "copilot-only", "mixed"):
            value = receipt(model=model)
            source = next(iter(value["before"]["models"]))
            value["operations"][0]["snapshot"]["models"][source]["observedToolChecksum"] = digest("wrong")
            self.assert_check(value, "recovery", "failed")
        value = receipt(model="copilot-only")
        value["operations"][0]["snapshot"]["models"] = receipt()["before"]["models"]
        self.assert_check(value, "recovery", "failed")

    def test_repeated_operations_cannot_reuse_an_earlier_pod(self):
        value = receipt()
        value["operations"][-1]["snapshot"]["workload"]["pod"] = value["before"]["workload"]["pod"]
        self.assert_check(value, "recovery", "failed", "fresh-execution-required")

    def test_migration_is_admin_only_healthy_repeated_and_csi_observed(self):
        for key, replacement in (
                ("path", "owner-rest"), ("sourceHealthy", False),
                ("sourceTerminated", False), ("csiReleased", False),
                ("csiAttached", False), ("sourceNode", digest("other"))):
            value = receipt()
            value["operations"][2][key] = replacement
            self.assert_check(value, "recovery", "failed")
        value = receipt()
        value["operations"].pop()
        self.assert_check(value, "recovery", "blocked")

    def test_peers_remain_ready_isolated_and_unchanged_through_cleanup(self):
        for point in ("before", "after", "cleanup"):
            for field, replacement in (
                    ("subject", digest("owner")), ("key", digest("key")),
                    ("pod", digest("changed")), ("ready", False), ("spend", "-1")):
                value = receipt()
                target = value["operations"][0]["snapshot"] if point == "after" else value[point]
                target["peer"][field] = replacement
                self.assert_check(value, "peers", "failed")
        value = receipt()
        value["operations"][0]["snapshot"]["peer"]["spend"] = "0"
        self.assert_check(value, "peers", "failed")

    def test_an_unused_peer_need_not_have_nonzero_spend(self):
        value = receipt()
        for snapshot in [value["before"]] + [item["snapshot"] for item in value["operations"]]:
            snapshot["peer"]["spend"] = "0"
        value["cleanup"]["peer"]["spend"] = "0"
        self.assertEqual(self.assess(value)["status"], "matched")

    def test_all_request_cases_are_required_and_duplicate_rows_rejected(self):
        for request in receipt()["requests"]:
            value = receipt()
            value["requests"] = [item for item in value["requests"] if item["case"] != request["case"]]
            self.assert_check(value, "requests", "blocked")
        value = receipt()
        value["requests"].append(copy.deepcopy(value["requests"][0]))
        self.assert_check(value, "requests", "failed")

    def test_unknown_response_is_not_rejection_or_cancellation_from_absence(self):
        for outcome in ("rejected", "cancelled", "deleted"):
            value = receipt()
            item = next(item for item in value["requests"] if item["case"] == "lost-response")
            item.update(outcome=outcome, reportedState=outcome, observedState=outcome)
            self.assert_check(value, "requests", "failed")
        value = receipt()
        value["requests"][0]["authoritativeObservation"] = False
        self.assert_check(value, "requests", "blocked")

    def test_intent_status_apply_counts_and_bounded_failure_are_checked(self):
        for field, replacement in (
                ("intentAfter", digest("rebased")), ("reportedState", "pending"),
                ("appliesAfter", 2), ("appliesBefore", True),
                ("limitSeconds", 0), ("elapsedSeconds", 61)):
            value = receipt()
            value["requests"][0][field] = replacement
            self.assert_check(value, "requests", "failed")

    def test_reported_suspension_cannot_replace_observed_shutdown(self):
        value = receipt()
        item = next(item for item in value["requests"] if item["case"] == "suspend-precedence")
        item["observedState"] = "pending"
        self.assert_check(value, "requests", "failed", "request-outcome-mismatch")
        item.update(outcome="pending", reportedState="pending")
        self.assert_check(value, "requests", "matched")
        value["operations"][1]["stopObserved"] = False
        self.assert_check(value, "recovery", "failed", "acceptance-stop-and-ready-observations-required")

    def test_combined_helpers_require_every_witness_and_image_pin(self):
        value = receipt(model="mixed", identity="azure-app-only",
                        helpers=("skills", "memory", "doctor", "logger", "desktop"))
        self.assertEqual(self.assess(value)["status"], "matched")
        for component in ("copilot", "broker", "mcp", "memory", "doctor", "logger", "desktop"):
            changed = copy.deepcopy(value)
            del changed["artifacts"]["images"][component]
            self.assert_check(changed, "artifacts", "failed")

    def test_cleanup_requires_absence_not_retention_or_forced_removal(self):
        for field, replacement in (
                ("ordinary", False), ("forced", True), ("path", "admin-kubernetes"),
                ("subject", digest("peer")), ("limitSeconds", 0)):
            value = receipt()
            value["cleanup"][field] = replacement
            self.assert_check(value, "cleanup", "failed")
        for name in receipt()["cleanup"]["remaining"]:
            for remaining in (1, True):
                value = receipt()
                value["cleanup"]["remaining"][name] = remaining
                self.assert_check(value, "cleanup", "failed")

    def test_invalid_and_private_input_never_appears_in_output(self):
        for replacement in (None, [], "do-not-print-this", 1):
            value = receipt()
            value["before"] = replacement
            result = self.assess(value)
            self.assertNotEqual(result["status"], "matched")
            self.assertNotIn("do-not-print-this", json.dumps(result))
        value = receipt()
        value["private-notes"] = "do-not-print-this"
        self.assertNotIn("do-not-print-this", json.dumps(self.assess(value)))
        self.assertNotEqual(self.assess(value)["status"], "matched")

    def test_nested_invalid_shapes_fail_without_raw_diagnostics(self):
        for path in (
                ("authorization",), ("artifacts", "images"), ("artifacts", "charts"),
                ("before", "owner"), ("before", "resources"), ("before", "home"),
                ("before", "accounting"), ("before", "workload"), ("before", "models"),
                ("before", "models", "shared"), ("before", "peer"), ("before", "extensions"),
                ("operations",), ("operations", 0), ("operations", 0, "snapshot"),
                ("requests",), ("requests", 0), ("cleanup", "remaining"), ("cleanup", "peer")):
            for replacement in (None, [], {}, True, "do-not-print-this"):
                with self.subTest(path=path, value=replacement):
                    value = receipt(helpers=("skills",))
                    target = value
                    for key in path[:-1]:
                        target = target[key]
                    target[path[-1]] = replacement
                    result = self.assess(value)
                    self.assertNotEqual(result["status"], "matched")
                    self.assertNotIn("do-not-print-this", json.dumps(result))

    def test_cli_is_offline_sanitized_and_preserves_live_target_requirements(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "receipt.json"
            path.write_text(json.dumps(receipt()))
            output = io.StringIO()
            with contextlib.redirect_stdout(output), patch.object(
                    qualify.subprocess, "run", side_effect=AssertionError("no external calls")):
                self.assertEqual(qualify.main(["acceptance-check", "--receipt", str(path)]), 0)
            result = json.loads(output.getvalue())
            self.assertEqual(result["status"], "matched")
            self.assertNotIn(str(path), output.getvalue())
        for args in (["preflight"], ["network-smoke"], ["deployment-preflight"],
                     ["acceptance-check", "--kubeconfig", "do-not-print-this"]):
            with self.subTest(args=args), contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(qualify.main(args), 2)
                self.assertNotIn("do-not-print-this", output.getvalue())

    def test_cli_rejects_duplicate_json_nonfinite_oversize_and_unreadable_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "receipt.json"
            for raw in ('{"version":"v1","version":"v1"}', '{"number":NaN}',
                        "{" + " " * (1024 * 1024), "\ud800"):
                path.write_bytes(raw.encode("utf-8", errors="surrogatepass"))
                with contextlib.redirect_stdout(io.StringIO()) as output:
                    self.assertEqual(qualify.main(["acceptance-check", "--receipt", str(path)]), 1)
                    self.assertFalse(json.loads(output.getvalue())["qualifiesExecution"])
            with contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(qualify.main(["acceptance-check", "--receipt", str(path / "missing")]), 1)
                self.assertNotIn(str(path), output.getvalue())

    def test_real_cli_blocked_fixture_exit_and_output(self):
        result = subprocess.run(
            [sys.executable, "-B", str(SCRIPT), "acceptance-check", "--receipt", str(FIXTURE)],
            capture_output=True, text=True, check=False, timeout=10,
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual(json.loads(result.stdout)["status"], "blocked")
        self.assertEqual(result.stderr, "")


if __name__ == "__main__":
    unittest.main()
