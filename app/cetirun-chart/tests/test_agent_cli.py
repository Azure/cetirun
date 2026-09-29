# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.machinery
import importlib.util
import io
import json
import os
import pathlib
import sys
import unittest
import urllib.error
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock


SCRIPT = pathlib.Path(__file__).parents[1] / "configs" / "cetirun-agent"
loader = importlib.machinery.SourceFileLoader("cetirun_agent_cli", str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)

SUBSCRIPTION = "11111111-1111-1111-1111-111111111111"
BROKER_URL = "http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"


class Response(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *_args):
        self.close()


class AgentCLITest(unittest.TestCase):
    def invoke_failure(self, arguments, environment=None):
        with mock.patch.object(sys, "argv", ["cetirun-agent", *arguments]), mock.patch.dict(
            os.environ, environment or {}, clear=True
        ), redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            with self.assertRaises(SystemExit) as result:
                module.main()
        self.assertEqual(result.exception.code, 2)
        self.assertEqual(stdout.getvalue(), "")
        return json.loads(stderr.getvalue())

    def test_broker_url_requires_in_cluster_http_service(self):
        with mock.patch.dict(
            os.environ, {"CETIRUN_AGENT_BROKER_URL": BROKER_URL}, clear=True
        ):
            self.assertEqual(module.broker_url(), BROKER_URL)
        for value in (
            "https://broker.default.svc.cluster.local",
            "http://broker.example.com",
            "http://broker.default.svc.cluster.local/path",
            "http://broker.default.svc.cluster.local:65536",
        ):
            with self.subTest(value=value), mock.patch.dict(
                os.environ, {"CETIRUN_AGENT_BROKER_URL": value}, clear=True
            ):
                with self.assertRaises(module.BrokerConfigurationError):
                    module.broker_url()

    def test_auth_commands_do_not_issue_a_credential(self):
        for arguments, expected in (
            (["auth", "status"], ("GET", "/v1/auth/status")),
            (["auth", "start"], ("POST", "/v1/auth/start-link")),
        ):
            with self.subTest(arguments=arguments), mock.patch.object(
                sys, "argv", ["cetirun-agent", *arguments]
            ), mock.patch.object(
                module, "broker_call", return_value={"status": "connected"}
            ) as broker, redirect_stdout(io.StringIO()) as stdout:
                self.assertEqual(module.main(), 0)
                self.assertEqual(json.loads(stdout.getvalue()), {"status": "connected"})
                broker.assert_called_once_with(*expected)

    def test_azure_command_issues_fixed_credential_and_keeps_token_out_of_output(self):
        secret = "synthetic-secret-token"
        with mock.patch.object(
            sys, "argv", ["cetirun-agent", "azure", "subscriptions", "list"]
        ), mock.patch.object(
            module,
            "broker_call",
            return_value={"tokenType": "Bearer", "accessToken": secret},
        ) as broker, mock.patch.object(
            module,
            "subscriptions",
            return_value={"items": [{"displayName": "example"}], "truncated": False},
        ) as azure, redirect_stdout(io.StringIO()) as stdout, redirect_stderr(
            io.StringIO()
        ) as stderr:
            self.assertEqual(module.main(), 0)
        broker.assert_called_once_with(
            "POST", "/v1/credentials/issue", {"service": "azure-resource-manager"}
        )
        azure.assert_called_once_with(secret)
        self.assertNotIn(secret, stdout.getvalue())
        self.assertNotIn(secret, stderr.getvalue())

    def test_invalid_credential_is_not_echoed(self):
        secret = "secret with whitespace"
        with mock.patch.object(
            module,
            "broker_call",
            return_value={"tokenType": "Bearer", "accessToken": secret},
        ), redirect_stderr(io.StringIO()) as stderr:
            with self.assertRaises(SystemExit):
                module.issue_arm_token()
        self.assertNotIn(secret, stderr.getvalue())
        self.assertEqual(
            json.loads(stderr.getvalue())["error"]["code"], "dependency_unavailable"
        )

    def test_broker_contract_rejects_arbitrary_service_and_body(self):
        with self.assertRaises(module.InvalidRequestError):
            module.broker_call(
                "POST", "/v1/credentials/issue", {"service": "microsoft-graph"}
            )
        with self.assertRaises(module.InvalidRequestError):
            module.broker_call("GET", "/v1/auth/status", {"scope": "arbitrary"})
        with self.assertRaises(module.InvalidRequestError):
            module.broker_call("POST", "/v1/arbitrary", {})

    def test_resource_id_and_filters_reject_query_injection(self):
        canonical = f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example"
        self.assertEqual(module.validate_resource_id(canonical), canonical)
        for value in (
            "https://management.azure.com/anything",
            f"/subscriptions/{SUBSCRIPTION}/../secrets",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/a?api-version=x",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/a\\' | union Resources",
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/a\nResources",
        ):
            with self.subTest(value=value), self.assertRaises(
                module.InvalidRequestError
            ):
                module.validate_resource_id(value)
        for value in ("x' | project secret", "a" * 257):
            with self.subTest(value=value), self.assertRaises(
                module.InvalidRequestError
            ):
                module.validate_filters([SUBSCRIPTION], [value])

    def test_azure_call_uses_fixed_https_host_and_authorization_header(self):
        response = Response(b'{"value":[]}')
        with mock.patch.object(
            module.AZURE_OPENER, "open", return_value=response
        ) as request:
            result = module.azure_call(
                "GET",
                "https://management.azure.com/subscriptions?api-version=2022-12-01",
                "secret-token",
            )
        self.assertEqual(result, {"value": []})
        sent = request.call_args.args[0]
        self.assertEqual(sent.get_header("Authorization"), "Bearer secret-token")
        for endpoint in (
            "http://management.azure.com/subscriptions",
            "https://example.com/subscriptions",
            "https://user@management.azure.com/subscriptions",
            "https://management.azure.com:444/subscriptions",
        ):
            with self.subTest(endpoint=endpoint), self.assertRaises(
                module.InvalidRequestError
            ):
                module.azure_call("GET", endpoint, "secret-token")

    def test_redirect_is_never_followed(self):
        redirect = urllib.error.HTTPError(
            "https://management.azure.com/subscriptions",
            302,
            "Found",
            {"Location": "https://example.com/steal"},
            Response(b""),
        )
        with mock.patch.object(module.AZURE_OPENER, "open", side_effect=redirect), redirect_stderr(
            io.StringIO()
        ) as stderr:
            with self.assertRaises(SystemExit):
                module.azure_call(
                    "GET",
                    "https://management.azure.com/subscriptions?api-version=2022-12-01",
                    "secret-token",
                )
        self.assertEqual(
            json.loads(stderr.getvalue())["error"]["code"], "dependency_unavailable"
        )

    def test_subscriptions_are_bounded_and_filtered(self):
        raw = {
            "value": [
                {
                    "subscriptionId": SUBSCRIPTION,
                    "displayName": "example",
                    "state": "Enabled",
                    "authorizationSource": "RoleBased",
                }
            ]
        }
        with mock.patch.object(module, "azure_call", return_value=raw):
            result = module.subscriptions("secret-token")
        self.assertEqual(
            result,
            {
                "items": [
                    {
                        "subscriptionId": SUBSCRIPTION,
                        "displayName": "example",
                        "state": "Enabled",
                    }
                ],
                "truncated": False,
            },
        )
        self.assertNotIn("authorizationSource", result["items"][0])

    def test_broker_calls_ignore_process_proxy_settings(self):
        response = Response(b'{"status":"connected"}')
        with mock.patch.dict(
            os.environ,
            {
                "CETIRUN_AGENT_BROKER_URL": BROKER_URL,
                "HTTP_PROXY": "http://proxy.example:3128",
                "NO_PROXY": "",
            },
            clear=True,
        ), mock.patch.object(
            module.BROKER_OPENER, "open", return_value=response
        ) as request:
            self.assertEqual(
                module.broker_call("GET", "/v1/auth/status"),
                {"status": "connected"},
            )
        proxy_handlers = [
            handler
            for handler in module.BROKER_OPENER.handlers
            if isinstance(handler, urllib.request.ProxyHandler)
        ]
        self.assertEqual(proxy_handlers, [])
        self.assertEqual(request.call_args.args[0].full_url, BROKER_URL + "/v1/auth/status")

    def test_azure_calls_ignore_process_proxy_settings(self):
        with mock.patch.dict(
            os.environ,
            {
                "HTTP_PROXY": "http://proxy.example:3128",
                "HTTPS_PROXY": "http://proxy.example:3128",
                "NO_PROXY": "",
            },
            clear=True,
        ):
            opener = module.no_proxy_opener()
        proxy_handlers = [
            handler
            for handler in opener.handlers
            if isinstance(handler, urllib.request.ProxyHandler)
        ]
        self.assertEqual(proxy_handlers, [])

    def test_exact_page_boundary_is_not_reported_as_truncated_without_next_link(self):
        values = [
            {"subscriptionId": f"{index:08x}-1111-1111-1111-111111111111"}
            for index in range(module.MAX_RESULT_ITEMS)
        ]
        with mock.patch.object(
            module, "azure_call", return_value={"value": values}
        ):
            result = module.subscriptions("secret-token")
        self.assertEqual(len(result["items"]), module.MAX_RESULT_ITEMS)
        self.assertFalse(result["truncated"])

        with mock.patch.object(
            module,
            "azure_call",
            return_value={"value": values, "nextLink": (
                "https://management.azure.com/subscriptions?api-version=2022-12-01&$skiptoken=opaque"
            )},
        ):
            result = module.subscriptions("secret-token")
        self.assertTrue(result["truncated"])

    def test_pagination_accepts_only_same_host_and_path(self):
        valid = (
            "https://management.azure.com/subscriptions"
            "?api-version=2022-12-01&$skiptoken=opaque"
        )
        self.assertEqual(module.valid_next_link(valid, "/subscriptions"), valid)
        for value in (
            "https://example.com/subscriptions?skip=1",
            "https://management.azure.com/tenants?skip=1",
            "http://management.azure.com/subscriptions?skip=1",
        ):
            with self.subTest(value=value), self.assertRaises(ValueError):
                module.valid_next_link(value, "/subscriptions")

    def test_resource_graph_query_is_generated_internally(self):
        with mock.patch.object(
            module, "azure_call", return_value={"data": []}
        ) as call:
            result = module.resources(
                "secret-token",
                [SUBSCRIPTION],
                resource_group="example",
                resource_type="Microsoft.Compute/virtualMachines",
            )
        self.assertEqual(result, {"items": [], "truncated": False})
        request_body = call.call_args.args[3]
        self.assertEqual(request_body["subscriptions"], [SUBSCRIPTION])
        self.assertIn("resourceGroup =~ 'example'", request_body["query"])
        self.assertIn(
            "type =~ 'Microsoft.Compute/virtualMachines'", request_body["query"]
        )
        self.assertEqual(request_body["options"]["$top"], 101)

    def test_exact_resource_lookup_uses_its_subscription(self):
        resource_id = (
            f"/subscriptions/{SUBSCRIPTION}/resourceGroups/example/"
            "providers/Microsoft.Compute/virtualMachines/vm1"
        )
        with mock.patch.object(
            sys,
            "argv",
            ["cetirun-agent", "azure", "resource", "show", "--id", resource_id],
        ), mock.patch.object(
            module, "issue_arm_token", return_value="secret-token"
        ), mock.patch.object(
            module, "resources", return_value={"items": [], "truncated": False}
        ) as resources, redirect_stdout(io.StringIO()):
            self.assertEqual(module.main(), 0)
        resources.assert_called_once_with(
            "secret-token", [SUBSCRIPTION], resource_id=resource_id
        )

    def test_safe_broker_error_drops_unknown_fields(self):
        result = module.safe_broker_error(
            {
                "error": {
                    "code": "authorization_required",
                    "message": "Connect Microsoft Entra",
                    "correlationId": "test-correlation",
                    "token": "must-not-leak",
                }
            }
        )
        self.assertEqual(
            result,
            {
                "code": "authorization_required",
                "message": "Connect Microsoft Entra",
                "correlationId": "test-correlation",
            },
        )

    def test_argument_and_configuration_errors_are_structured(self):
        error = self.invoke_failure(
            ["azure", "resources", "search", "--subscription", "not-a-uuid"]
        )
        self.assertEqual(error["error"]["code"], "invalid_request")
        error = self.invoke_failure(["auth", "status"])
        self.assertEqual(error["error"]["code"], "dependency_unavailable")

    def test_parser_does_not_expose_generic_request_or_token_options(self):
        help_text = module.parser().format_help()
        self.assertNotIn("request", help_text)
        self.assertNotIn("token", help_text)


if __name__ == "__main__":
    unittest.main()
