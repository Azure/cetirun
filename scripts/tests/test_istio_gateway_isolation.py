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


class IstioGatewayIsolationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = TEMPLATE.read_text(encoding="utf-8")

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
            "/admin": True,
            "/admin/users": True,
            "/admin-tools/term/": False,
            "/api/v1/instance": True,
            "/api-demo/term/": False,
        }
        for path, expected in cases.items():
            with self.subTest(path=path):
                self.assertEqual(is_exempt_path(path), expected)

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
            "if is_exempt(path_only, method, authority) then", token_removal
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
        helper_auth = self.source.index(
            "forward_instance_key(request_handle, path_only, authority)", cookie_removal
        )

        self.assertLess(token_removal, exempt_branch)
        self.assertLess(exempt_branch, instance_branch)
        self.assertLess(instance_branch, ownership_check)
        self.assertLess(ownership_check, fail_closed)
        self.assertLess(fail_closed, cookie_removal)
        self.assertLess(cookie_removal, helper_auth)

    def test_helper_and_native_api_credentials_use_separate_route_bound_headers(self):
        self.assertIn(
            'local CETIRUN_AUTH_KEY_HEADER = "x-cetirun-auth-key"', self.source
        )
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
            "if is_cetirun_auth_path(path) and valid_instance_key(auth_key) then\n"
            '                  headers:replace("authorization", "Bearer " .. auth_key)\n'
            "                elseif is_native_agent_api_path(path, authority) and valid_instance_key(api_key) then\n"
            '                  headers:replace("authorization", "Bearer " .. api_key)',
            self.source,
        )
        self.assertEqual(
            self.source.count(
                'request_handle:headers():remove(AGENT_API_KEY_HEADER)'
            ),
            4,
        )
        self.assertIn(
            'if not instance_name or instance_name == "" then\n'
            '                  -- Do not let credentials reach a fallback route',
            self.source,
        )


if __name__ == "__main__":
    unittest.main()
