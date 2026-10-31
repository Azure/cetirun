# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import re
import unittest
from pathlib import Path


TEMPLATE = (
    Path(__file__).resolve().parents[2]
    / "infra"
    / "istio"
    / "templates"
    / "ingress"
    / "envoy-filter-alias.yaml"
)
SECURITY_HEADERS_TEMPLATE = TEMPLATE.with_name("envoy-filter-security-headers.yaml")


class IstioGatewayIsolationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = TEMPLATE.read_text(encoding="utf-8")
        cls.security_headers = SECURITY_HEADERS_TEMPLATE.read_text(encoding="utf-8")

    def _lua_string_list(self, name):
        match = re.search(rf"local {name} = \{{([^\n]*)\}}", self.source)
        self.assertIsNotNone(match, f"missing Lua list {name}")
        return re.findall(r'"([^"]+)"', match.group(1))

    def test_portal_exempt_paths_use_exact_and_segment_matches(self):
        exact_paths = self._lua_string_list("EXEMPT_EXACT_PATHS")
        path_prefixes = self._lua_string_list("EXEMPT_PATH_PREFIXES")

        self.assertIn("/healthz", exact_paths)
        self.assertIn("/readyz", exact_paths)
        self.assertNotIn("/healthz", path_prefixes)
        self.assertNotIn("/readyz", path_prefixes)
        self.assertIn(
            "for _, exact_path in ipairs(EXEMPT_EXACT_PATHS) do\n"
            "                  if path == exact_path then return true end",
            self.source,
        )
        self.assertIn(
            'return path == prefix or path:sub(1, #prefix + 1) == prefix .. "/"',
            self.source,
        )
        self.assertIn(
            "for _, prefix in ipairs(EXEMPT_PATH_PREFIXES) do\n"
            "                  if has_path_segment_prefix(path, prefix) then return true end",
            self.source,
        )

        def is_exempt_path(path):
            return path in exact_paths or any(
                path == prefix or path.startswith(prefix + "/")
                for prefix in path_prefixes
            )

        cases = {
            "/healthz": True,
            "/readyz": True,
            "/healthz/live": False,
            "/readyz/live": False,
            "/healthz-demo/term/": False,
            "/readyz-demo/term/": False,
            "/connections": True,
            "/connections/": True,
            "/connections/term/": False,
            "/connections/desktop/": False,
            "/connections/cetirun-auth/status": False,
            "/connections/api/": False,
            "/connections-demo/term/": False,
            "/connectionsdemo": False,
            "/ui/connections/": False,
            "/admin": True,
            "/admin/users": True,
            "/admin-tools/term/": False,
            "/api/v1/instance": True,
            "/api-demo/term/": False,
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(is_exempt_path(path), expected)

    def test_portal_spa_routes_are_exempt_and_reserved(self):
        # A new Portal page must not be interpreted as an Agent name after
        # sign-in, or be shadowed by an Agent created with the same bare name.
        root = TEMPLATE.parents[4]
        app = (root / "app/cetirun-portal/web/src/App.tsx").read_text()
        handler = (root / "app/cetirun-portal/internal/handler/instance.go").read_text()
        reserved_block = re.search(
            r"var reservedInstanceNames = map\[string\]struct\{\}\{(.*?)\n\}",
            handler,
            re.DOTALL,
        )
        self.assertIsNotNone(reserved_block)
        reserved = set(re.findall(r'"([^"/]+)":', reserved_block.group(1)))
        exemptions = self._lua_string_list("EXEMPT_PATH_PREFIXES") + self._lua_string_list("EXEMPT_EXACT_PATHS")
        routes = set(re.findall(r'path="/([^/":]+)', app))
        self.assertIn("connections", routes)
        for route in routes:
            with self.subTest(route=route):
                self.assertIn("/" + route, exemptions)
                self.assertIn(route, reserved)

    def test_portal_sign_in_cookies_never_reach_agents(self):
        # OAuth2 Proxy names a CSRF cookie per sign-in when
        # --cookie-csrf-per-request is on: <session cookie>_<8 characters of
        # the state hash>_csrf, or <session cookie>_csrf otherwise.
        for line in (
            'return name == PORTAL_COOKIE_NAME or',
            'name:match("^" .. PORTAL_COOKIE_NAME .. "_%d+$") ~= nil or',
            'name:match("^" .. PORTAL_COOKIE_NAME .. "_[%w_%-]*csrf$") ~= nil',
        ):
            with self.subTest(line=line):
                self.assertIn(line, self.source)

        # The Lua patterns above, as Python regular expressions.
        def is_portal_cookie_name(name):
            return (
                name == "_cetirun_oauth2"
                or re.fullmatch(r"_cetirun_oauth2_\d+", name) is not None
                or re.fullmatch(r"_cetirun_oauth2_[A-Za-z0-9_-]*csrf", name) is not None
            )

        cases = {
            "_cetirun_oauth2": True,
            "_cetirun_oauth2_0": True,
            "_cetirun_oauth2_12": True,
            "_cetirun_oauth2_csrf": True,
            "_cetirun_oauth2_D2Wd31uG_csrf": True,
            "_cetirun_oauth2_-j84Ob9O_csrf": True,
            "_cetirun_oauth2_6Ys_ZHE7_csrf": True,
            "_cetirun_oauth2_csrf_extra": False,
            "_cetirun_oauth2x": False,
            "agent_session": False,
            "openclaw_csrf": False,
        }
        for name, expected in cases.items():
            with self.subTest(name=name):
                self.assertEqual(is_portal_cookie_name(name), expected)

    def test_instance_branch_preserves_ownership_and_credential_isolation(self):
        token_removal = self.source.index(
            'request_handle:headers():remove("x-cetirun-portal-id-token")'
        )
        exempt_branch = self.source.index(
            "if is_exempt(path_only, authority) then", token_removal
        )
        instance_branch = self.source.index(
            "-- Native agent UIs are namespaced", exempt_branch
        )
        ownership_check = self.source.index(
            "request_handle:httpCall(", instance_branch
        )
        fail_closed = self.source.index(
            'if status ~= "200" then', ownership_check
        )
        cookie_removal = self.source.index(
            "strip_portal_session_cookie(request_handle)", fail_closed
        )
        native_api_auth = self.source.index(
            "forward_native_agent_api_key(request_handle, path_only, authority)", cookie_removal
        )

        self.assertLess(token_removal, exempt_branch)
        self.assertLess(exempt_branch, instance_branch)
        self.assertLess(instance_branch, ownership_check)
        self.assertLess(ownership_check, fail_closed)
        self.assertLess(fail_closed, cookie_removal)
        self.assertLess(cookie_removal, native_api_auth)

    def test_native_api_credentials_use_a_route_bound_header(self):
        self.assertIn(
            'local AGENT_API_KEY_HEADER = "x-cetirun-agent-api-key"', self.source
        )
        self.assertIn(
            'path:match("^/ui/[^/]+/v1/models$") ~= nil', self.source
        )
        self.assertIn(
            'path:match("^/ui/[^/]+/v1/chat/completions$") ~= nil', self.source
        )
        self.assertIn(
            'return path == "/v1/models" or path == "/v1/chat/completions"',
            self.source,
        )
        self.assertIn(
            "if is_native_agent_api_path(path, authority) and valid_instance_key(api_key) then\n"
            '                  headers:replace("authorization", "Bearer " .. api_key)',
            self.source,
        )
        self.assertNotIn("CETIRUN_AUTH_KEY_HEADER", self.source)
        self.assertNotIn("is_cetirun_auth_path", self.source)
        self.assertEqual(
            self.source.count(
                'request_handle:headers():remove(AGENT_API_KEY_HEADER)'
            ),
            3,
        )
        self.assertIn(
            'if not instance_name or instance_name == "" then\n'
            '                  -- Do not let credentials reach a fallback route',
            self.source,
        )

    def test_native_ui_routes_restore_only_the_browser_authorization(self):
        # The security headers filter runs before ext-auth and records the
        # client's Authorization in dynamic metadata, never in a header.
        self.assertIn("name: envoy.filters.http.ext_authz", self.security_headers)
        self.assertIn("operation: INSERT_BEFORE", self.security_headers)
        self.assertIn(
            'local authorization = handle:headers():get("authorization")\n'
            '                if authorization and authorization ~= "" then\n'
            '                  handle:streamInfo():dynamicMetadata():set("cetirun", "client_authorization", authorization)',
            self.security_headers,
        )
        self.assertNotIn("client_authorization", self.security_headers.split("function envoy_on_response")[1])

        # The ownership filter restores it only after the dedicated route
        # credentials, only on owned /ui/<instance>/ routes of the Portal
        # host other than the native Agent API, and never when OAuth2 Proxy
        # accepted it as the identity.
        self.assertIn(
            '                  headers:replace("authorization", "Bearer " .. api_key)\n'
            "                elseif is_native_ui_path(path, authority) and not is_native_agent_api_path(path, authority) then\n",
            self.source,
        )
        self.assertIn(
            "                  if client and not same_credential(client, identity_token) then\n"
            '                    headers:replace("authorization", client)',
            self.source,
        )
        self.assertIn(
            "return request_host(authority) == PORTAL_DOMAIN and\n"
            '                  path:match("^/ui/[^/]+/") ~= nil',
            self.source,
        )
        identity = self.source.index('local identity_token = headers:get("authorization")')
        removal = self.source.index('headers:remove("authorization")', identity)
        restore = self.source.index('headers:replace("authorization", client)', removal)
        self.assertLess(identity, removal)
        self.assertLess(removal, restore)
        self.assertEqual(self.source.count('headers:replace("authorization", client)'), 1)

        # same_credential and bearer_token, as Python.
        def bearer_token(value):
            match = re.fullmatch(r"\s*[Bb][Ee][Aa][Rr][Ee][Rr]\s+(\S+)\s*", value or "")
            return match.group(1) if match else None

        def same_credential(a, b):
            if not a or not b:
                return False
            if a == b:
                return True
            token = bearer_token(a)
            return token is not None and token == bearer_token(b)

        self.assertIn(
            'return value:match("^%s*[Bb][Ee][Aa][Rr][Ee][Rr]%s+(%S+)%s*$")',
            self.source,
        )
        cases = {
            ("Bearer agent-token", "Bearer id-token"): False,
            ("Bearer id-token", "Bearer id-token"): True,
            ("bearer id-token", "Bearer id-token"): True,
            ("Basic dXNlcjpwYXNz", "Bearer id-token"): False,
            (None, "Bearer id-token"): False,
            ("Bearer agent-token", None): False,
        }
        for (client, identity), expected in cases.items():
            with self.subTest(client=client, identity=identity):
                self.assertEqual(same_credential(client, identity), expected)


if __name__ == "__main__":
    unittest.main()
