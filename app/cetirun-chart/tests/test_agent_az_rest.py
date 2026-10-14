# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline tests for the delegated az rest wrapper; never call Azure."""

import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from contextlib import redirect_stderr, redirect_stdout
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock


SCRIPT = Path(__file__).parents[1] / "configs" / "cetirun-agent"
loader = importlib.machinery.SourceFileLoader("cetirun_agent_rest_cli", str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)

SUBSCRIPTION = "11111111-1111-1111-1111-111111111111"
SCOPE = f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example"
RESOURCE = SCOPE + "/providers/Microsoft.Network/loadBalancers/example"
URL = RESOURCE + "?api-version=2025-01-01"
TOKEN = "synthetic-cetirun-obo-token"


def arguments(*extra):
    return module.parser().parse_args(["az", "rest", "--url", URL, *extra])


class AgentAZRestTests(unittest.TestCase):
    def test_aks_and_node_resource_group_use_the_same_arm_credential(self):
        cluster = SCOPE + "/providers/Microsoft.ContainerService/managedClusters/example"
        node_group = f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example-nodes"
        for resource, scope in (
            (cluster, cluster), (cluster + "/agentPools", cluster),
            (node_group + "/resources", node_group),
            (node_group + "/providers/Microsoft.Network/loadBalancers/example", node_group),
            (node_group + "/providers/Microsoft.Compute/virtualMachines/example", node_group),
            (node_group + "/providers/Microsoft.Compute/virtualMachineScaleSets/example/virtualMachines", node_group),
        ):
            with self.subTest(resource=resource):
                args = arguments("--url", resource + "?api-version=2025-01-01", "--scope", scope)
                module.validate_arm_rest(args)
                self.assertEqual(args.url, module.ARM_BASE_URL + resource + "?api-version=2025-01-01")
                self.assertEqual(args.method, "get")

    def invoke_failure(self, *extra):
        with mock.patch.object(sys, "argv", ["cetirun-agent", "az", "rest", "--url", URL, *extra]), \
                mock.patch.object(module, "issue_arm_token") as issue, \
                mock.patch.object(module, "run_az") as run, \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            with self.assertRaises(SystemExit) as error:
                module.main()
        self.assertEqual(error.exception.code, 2)
        self.assertEqual(stdout.getvalue(), "")
        issue.assert_not_called()
        run.assert_not_called()
        return json.loads(stderr.getvalue())["error"]

    def test_main_uses_the_existing_broker_token_and_returns_only_response_data(self):
        with mock.patch.object(sys, "argv", ["cetirun-agent", "az", "rest", "--url", URL]), \
                mock.patch.object(module, "issue_arm_token", return_value=TOKEN) as issue, \
                mock.patch.object(module, "az_rest", return_value={"response": {"name": "example"}}) as rest, \
                redirect_stdout(io.StringIO()) as stdout:
            self.assertEqual(module.main(), 0)
        issue.assert_called_once_with()
        args, supplied_token = rest.call_args.args
        self.assertEqual(args.url, module.ARM_BASE_URL + URL)
        self.assertEqual(supplied_token, TOKEN)
        self.assertEqual(json.loads(stdout.getvalue()), {"response": {"name": "example"}})
        self.assertNotIn(TOKEN, stdout.getvalue())

    def test_all_mutating_methods_require_opt_in_before_credential_issuance(self):
        for method in ("post", "put", "patch", "delete"):
            with self.subTest(method=method):
                self.assertEqual(self.invoke_failure("--method", method)["code"], "write_not_enabled")
                args = arguments("--method", method, "--allow-write")
                module.validate_arm_rest(args)

    def test_read_requests_and_argument_injection_are_rejected_before_token_issuance(self):
        for extra in (
            ("--body", "{}"), ("--method", "head", "--body", "{}"),
            ("--headers", "Authorization=untrusted"), ("--debug",), ("--verbose",),
            ("--output-file", "/tmp/not-written"), ("--resource", "https://example.invalid"),
            ("--subscription", SUBSCRIPTION), ("--output", "tsv"),
            ("--skip-authorization-header",), ("--allow-w",),
        ):
            with self.subTest(extra=extra):
                self.assertEqual(self.invoke_failure(*extra)["code"], "invalid_request")

    def test_urls_are_confined_to_public_arm_subscription_paths(self):
        for url in (
            "https://example.invalid" + URL,
            "http://management.azure.com" + URL,
            "https://management.azure.com.example.invalid" + URL,
            "https://user@management.azure.com" + URL,
            "https://management.azure.com:443" + URL,
            "//management.azure.com" + URL,
            " /subscriptions?api-version=2022-12-01",
            URL + "#fragment", URL + "\n",
            "/subscriptions/not-a-uuid/resources?api-version=2025-01-01",
            "/tenants?api-version=2025-01-01",
            "/subscriptions/{subscriptionId}/resources?api-version=2025-01-01",
            SCOPE + "/../other?api-version=2025-01-01",
            SCOPE + "/%2e%2e/other?api-version=2025-01-01",
            SCOPE + "/%252e%252e/other?api-version=2025-01-01",
            SCOPE + "/%2fother?api-version=2025-01-01",
            SCOPE + "/%5cother?api-version=2025-01-01",
            SCOPE + "/%00other?api-version=2025-01-01",
            SCOPE + "//other?api-version=2025-01-01",
            SCOPE + "/other/?api-version=2025-01-01",
        ):
            with self.subTest(url=url):
                self.assertEqual(self.invoke_failure("--url", url)["code"], "invalid_request")

    def test_api_version_must_be_unambiguous_and_explicit(self):
        for query in (
            "", "?api-version=", "?api-version=latest",
            "?api-version=2025-01-01&api-version=2025-02-01",
            "?api-version=2025-01-01&Api-Version=2025-02-01",
            "?api-version=2025-01-01&access_token=not-accepted",
            "?api-version=2025-01-01&sig=not-accepted",
        ):
            with self.subTest(query=query):
                self.assertEqual(self.invoke_failure("--url", RESOURCE + query)["code"], "invalid_request")
        for suffix in ("", "-preview"):
            args = arguments("--url", RESOURCE + "?api-version=2025-01-01" + suffix)
            module.validate_arm_rest(args)

    def test_scope_guards_use_case_insensitive_segments_not_string_prefixes(self):
        args = arguments("--scope", SCOPE.upper())
        module.validate_arm_rest(args)
        for scope in (SCOPE + "-other", RESOURCE + "-other", "/subscriptions", "https://management.azure.com" + SCOPE):
            with self.subTest(scope=scope):
                self.assertEqual(self.invoke_failure("--scope", scope)["code"], "invalid_request")
        self.assertEqual(
            self.invoke_failure("--scope", SCOPE, "--url", URL.replace("example/", "example-other/", 1))["code"],
            "invalid_request",
        )
        args = arguments("--scope", SCOPE + "-other", "--scope", SCOPE)
        module.validate_arm_rest(args)

    def test_credential_and_remote_command_endpoints_remain_unavailable(self):
        for suffix in ("listKeys", "listSecrets", "accessProfiles/admin", "runCommand", "runCommands/example", "li%73tClusterAdminCredential"):
            for method in ("get", "post"):
                with self.subTest(suffix=suffix, method=method):
                    self.assertEqual(self.invoke_failure(
                        "--url", RESOURCE + "/" + suffix + "?api-version=2025-01-01",
                        "--method", method, "--allow-write",
                    )["code"], "invalid_request")
        for path in (
            SCOPE + "/providers/Microsoft.Authorization/roleAssignments/example",
            SCOPE + "/providers/Microsoft.Resources/deployments/example",
            SCOPE + "/moveResources",
        ):
            with self.subTest(path=path):
                self.assertEqual(self.invoke_failure(
                    "--url", path + "?api-version=2025-01-01", "--method", "put", "--allow-write",
                )["code"], "invalid_request")

    def test_resource_names_are_not_confused_with_privileged_operations(self):
        for name in ("runCommand", "listKeys", "Microsoft.Authorization", "deployments"):
            with self.subTest(name=name):
                url = SCOPE + "/providers/Microsoft.Compute/virtualMachines/" + name + "?api-version=2025-01-01"
                args = arguments("--url", url, "--method", "patch", "--allow-write", "--body", "{}")
                module.validate_arm_rest(args)

    def test_body_validation_happens_before_token_issuance(self):
        for value in ("[]", "null", "NaN", '{"value": NaN}', "{broken", "@/does-not-exist", "@/dev/null"):
            with self.subTest(value=value):
                self.assertEqual(self.invoke_failure(
                    "--method", "patch", "--allow-write", "--body", value,
                )["code"], "invalid_request")
        with mock.patch.object(module, "MAX_REQUEST_BYTES", 32):
            self.assertEqual(self.invoke_failure(
                "--method", "patch", "--allow-write", "--body", '{"x":"' + "x" * 50 + '"}',
            )["code"], "invalid_request")
        with tempfile.TemporaryDirectory(prefix="cetirun-body-test-") as directory:
            source = Path(directory) / "body.json"
            source.write_text('{"tags":{"team":"example"}}')
            args = arguments("--method", "patch", "--allow-write", "--body", "@" + str(source))
            module.validate_arm_rest(args)
            self.assertEqual(json.loads(args.body), {"tags": {"team": "example"}})

    def test_header_and_body_stay_out_of_child_argv_environment_and_disk(self):
        args = arguments("--method", "patch", "--allow-write", "--body", '{"tags":{"team":"example"}}')
        module.validate_arm_rest(args)
        config_dirs = []

        def run(command, headers, environment, pass_fds):
            self.assertEqual(command[:2], ["/usr/bin/az", "rest"])
            self.assertIn("@/dev/stdin", command)
            self.assertIn("--skip-authorization-header", command)
            self.assertEqual(json.loads(headers), {"Authorization": "Bearer " + TOKEN})
            self.assertNotIn(TOKEN, json.dumps(command) + json.dumps(environment))
            self.assertNotIn("team", json.dumps(command))
            self.assertEqual(len(pass_fds), 1)
            self.assertEqual(os.read(pass_fds[0], module.MAX_REQUEST_BYTES), args.body)
            self.assertIn(f"@/proc/self/fd/{pass_fds[0]}", command)
            for inherited in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "PYTHONPATH", "AZURE_CLIENT_SECRET", "HOME"):
                self.assertNotIn(inherited, environment)
            self.assertEqual(environment["NETRC"], os.devnull)
            self.assertEqual(environment["AZURE_CORE_COLLECT_TELEMETRY"], "false")
            self.assertEqual(environment["AZURE_LOGGING_ENABLE_LOG_FILE"], "false")
            config = Path(environment["AZURE_CONFIG_DIR"])
            self.assertEqual(list(config.iterdir()), [])
            self.assertEqual(config.stat().st_mode & 0o777, 0o700)
            config_dirs.append(config)
            return 0, b'{"properties":{"provisioningState":"Updating"}}', b""

        with mock.patch.object(module, "run_az", side_effect=run):
            result = module.az_rest(args, TOKEN)
        self.assertEqual(result["completion"], "not_tracked")
        self.assertEqual(result["response"]["properties"]["provisioningState"], "Updating")
        self.assertTrue(all(not path.exists() for path in config_dirs))

    def test_empty_successful_responses_do_not_imply_write_completion(self):
        for method in ("head", "post"):
            args = arguments("--method", method, "--allow-write")
            module.validate_arm_rest(args)
            with mock.patch.object(module, "run_az", return_value=(0, b"", b"")):
                result = module.az_rest(args, TOKEN)
            self.assertIsNone(result["response"])
            self.assertEqual(result.get("completion"), "not_tracked" if method == "post" else None)

    def test_nested_sensitive_fields_are_redacted_without_resource_schemas(self):
        document = {"name": "example", "properties": {"client_secret": "hidden", "items": [{"Password": "hidden"}]}}
        args = arguments()
        module.validate_arm_rest(args)
        with mock.patch.object(module, "run_az", return_value=(0, json.dumps(document).encode(), b"")):
            result = module.az_rest(args, TOKEN)
        self.assertEqual(result["response"]["name"], "example")
        self.assertNotIn("hidden", json.dumps(result))

    def test_formatted_response_is_also_bounded(self):
        args = arguments()
        module.validate_arm_rest(args)
        with mock.patch.object(module, "MAX_RESPONSE_BYTES", 32), \
                mock.patch.object(module, "run_az", return_value=(0, b'{"items":[1,2,3,4,5]}', b"")), \
                redirect_stderr(io.StringIO()) as stderr:
            with self.assertRaises(SystemExit):
                module.az_rest(args, TOKEN)
        self.assertEqual(json.loads(stderr.getvalue())["error"]["code"], "dependency_unavailable")

    def test_failures_do_not_echo_diagnostics_or_credentials_or_retry(self):
        for method in ("get", "patch"):
            for outcome in (
                (1, b"", b"ERROR private diagnostic"),
                (0, b"not JSON", b""),
                (0, json.dumps({"value": TOKEN}).encode(), b""),
                (0, b"{}", TOKEN.encode()),
                FileNotFoundError("private path"),
                module.AzureCLIError("private detail"),
            ):
                with self.subTest(method=method, outcome=type(outcome).__name__):
                    args = arguments("--method", method, "--allow-write")
                    module.validate_arm_rest(args)
                    setup = {"side_effect": outcome} if isinstance(outcome, Exception) else {"return_value": outcome}
                    with mock.patch.object(module, "run_az", **setup) as run, redirect_stderr(io.StringIO()) as stderr:
                        with self.assertRaises(SystemExit):
                            module.az_rest(args, TOKEN)
                    run.assert_called_once()
                    self.assertNotIn("private", stderr.getvalue())
                    self.assertNotIn(TOKEN, stderr.getvalue())
                    expected = "write_outcome_unknown" if method == "patch" else "dependency_unavailable"
                    self.assertEqual(json.loads(stderr.getvalue())["error"]["code"], expected)

    def test_read_errors_classify_known_codes_without_returning_azure_diagnostics(self):
        for diagnostic, expected in (
            (b"ERROR: (AuthorizationFailed) private diagnostic", "forbidden"),
            (b"ERROR: (TooManyRequests) private diagnostic", "throttled"),
            (b"ERROR: (ResourceNotFound) private diagnostic", "not_found"),
        ):
            with self.subTest(expected=expected):
                args = arguments()
                module.validate_arm_rest(args)
                with mock.patch.object(module, "run_az", return_value=(1, b"", diagnostic)), \
                        redirect_stderr(io.StringIO()) as stderr:
                    with self.assertRaises(SystemExit):
                        module.az_rest(args, TOKEN)
                self.assertEqual(json.loads(stderr.getvalue())["error"]["code"], expected)
                self.assertNotIn("private", stderr.getvalue())


class BoundedChildTests(unittest.TestCase):
    def run_child(self, program, payload=b"{}"):
        return module.run_az([sys.executable, "-B", "-c", program], payload, module.az_environment("/tmp/not-used"), ())

    def test_input_and_both_output_streams_are_drained(self):
        code, output, diagnostics = self.run_child(
            'import sys; data=sys.stdin.buffer.read(); sys.stdout.buffer.write(data); sys.stderr.write("diagnostic")',
            b"x" * 100000,
        )
        self.assertEqual(code, 0)
        self.assertEqual(output, b"x" * 100000)
        self.assertEqual(diagnostics, b"diagnostic")

    def test_child_can_exit_without_reading_input(self):
        code, _output, _diagnostics = self.run_child("raise SystemExit(7)", b"x" * 100000)
        self.assertEqual(code, 7)

    def test_output_limits_terminate_the_child(self):
        for field, stream in (("MAX_RESPONSE_BYTES", "stdout"), ("MAX_STDERR_BYTES", "stderr")):
            with self.subTest(stream=stream), mock.patch.object(module, field, 16):
                with self.assertRaises(module.AzureCLIError):
                    self.run_child(f'import sys; sys.{stream}.write("x" * 10000)')

    def test_timeout_terminates_the_child(self):
        with mock.patch.object(module, "AZ_TIMEOUT_SECONDS", 0.15):
            with self.assertRaises(module.AzureCLIError):
                self.run_child("import time; time.sleep(10)")


@unittest.skipUnless(Path("/usr/bin/az").is_file(), "Azure CLI is not installed; mock tests still run")
class LocalAzureCLITests(unittest.TestCase):
    def test_real_az_rest_with_synthetic_token_and_loopback_only(self):
        observed = []
        real_run = module.run_az

        def audited_run(command, headers, environment, pass_fds):
            self.assertNotIn(TOKEN, json.dumps(command) + json.dumps(environment))
            result = real_run(command, headers, environment, pass_fds)
            for path in Path(environment["AZURE_CONFIG_DIR"]).rglob("*"):
                if path.is_file():
                    self.assertNotIn(TOKEN.encode(), path.read_bytes())
                    self.assertFalse(path.name.startswith(("msal_token_cache", "accessTokens")))
            return result

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                observed.append(self.headers.get("Authorization") == "Bearer " + TOKEN)
                payload = b'{"name":"local-fixture"}'
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_PATCH(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                observed.append(
                    self.headers.get("Authorization") == "Bearer " + TOKEN
                    and json.loads(body) == {"tags": {"team": "example"}}
                )
                self.send_response(204)
                self.end_headers()

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            for method, body in (("get", None), ("patch", '{"tags":{"team":"example"}}')):
                extra = ["--method", method, "--allow-write"]
                if body:
                    extra.extend(["--body", body])
                args = arguments(*extra)
                module.validate_arm_rest(args)
                # Only this internal fixture substitutes loopback after public URL validation.
                args.url = f"http://127.0.0.1:{server.server_port}/fixture"
                with mock.patch.object(module, "run_az", side_effect=audited_run):
                    result = module.az_rest(args, TOKEN)
                if method == "get":
                    self.assertEqual(result, {"response": {"name": "local-fixture"}})
                else:
                    self.assertEqual(result, {"response": None, "completion": "not_tracked"})
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)
        self.assertEqual(observed, [True, True])


if __name__ == "__main__":
    unittest.main()
