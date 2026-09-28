#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/infra/istio}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

base_args=(
  --set-string clientId=lint-client
  --set-string tenantId=lint-tenant
  --set-string allowedGroups=lint-group
  --set-string cookieSecret=lint-cookie-secret
)

assert_ownership_target() {
  local output="$1"
  local host="$2"
  grep -Fq "address: \"${host}\"" <<< "${output}" || {
    echo "FAIL: ownership cluster did not target ${host}" >&2
    exit 1
  }
  grep -Fq "[\":authority\"] = \"${host}\"" <<< "${output}" || {
    echo "FAIL: ownership request authority did not target ${host}" >&2
    exit 1
  }
}

echo "==> helm template cetirun-istio (standalone portal default)"
output=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}")
assert_ownership_target "${output}" "cetirun-portal.cetirun-portal.svc.cluster.local"
grep -Fq 'local EXEMPT_EXACT_PATHS = {"/healthz", "/readyz", "/favicon.ico"}' <<< "${output}" || {
  echo "FAIL: health and readiness exemptions must be exact paths" >&2
  exit 1
}
grep -Fq 'local EXEMPT_PATH_PREFIXES = {"/oauth2", "/api", "/internal", "/assets", "/instances", "/admin"}' <<< "${output}" || {
  echo "FAIL: Portal subtree exemptions are missing" >&2
  exit 1
}
grep -Fq 'return path == prefix or path:sub(1, #prefix + 1) == prefix .. "/"' <<< "${output}" || {
  echo "FAIL: Portal subtree exemptions do not enforce a path-segment boundary" >&2
  exit 1
}
if grep -Fq 'local EXEMPT_PREFIXES =' <<< "${output}"; then
  echo "FAIL: legacy string-prefix exemptions can shadow valid instance names" >&2
  exit 1
fi
grep -Fq 'local PORTAL_COOKIE_NAME = "_cetirun_oauth2"' <<< "${output}" || {
  echo "FAIL: ownership filter did not identify the Portal session cookie" >&2
  exit 1
}
grep -Fq 'name:match("^" .. PORTAL_COOKIE_NAME .. "_%d+$")' <<< "${output}" || {
  echo "FAIL: ownership filter did not handle chunked Portal session cookies" >&2
  exit 1
}
[[ "$(grep -Ec '^[[:space:]]+strip_portal_session_cookie\(request_handle\)$' <<< "${output}")" == 2 ]] || {
  echo "FAIL: Portal cookie stripping must run on both owned Agent and webhook routes" >&2
  exit 1
}
grep -Fq 'headers:replace("cookie", table.concat(retained, "; "))' <<< "${output}" || {
  echo "FAIL: ownership filter did not preserve Agent-owned cookies" >&2
  exit 1
}
grep -Fq 'local CETIRUN_AUTH_KEY_HEADER = "x-cetirun-auth-key"' <<< "${output}" || {
  echo "FAIL: ownership filter did not define the instance auth transport header" >&2
  exit 1
}
grep -Fq 'return path:match("^/[^/]+/cetirun%-auth/") ~= nil' <<< "${output}" || {
  echo "FAIL: ownership filter did not restrict instance auth forwarding by route" >&2
  exit 1
}
grep -Fq 'headers:replace("authorization", "Bearer " .. key)' <<< "${output}" || {
  echo "FAIL: ownership filter did not restore the instance bearer credential" >&2
  exit 1
}
grep -Fq 'key:match("^[A-Za-z0-9_+/=-]+$")' <<< "${output}" || {
  echo "FAIL: ownership filter did not accept existing standard Base64 auth keys" >&2
  exit 1
}
[[ "$(grep -Fc 'request_handle:headers():remove(CETIRUN_AUTH_KEY_HEADER)' <<< "${output}")" == 3 ]] || {
  echo "FAIL: exempt Portal, webhook, and shared routes must remove the instance auth header" >&2
  exit 1
}
grep -Fq -- '- --scope=openid email profile' <<< "${output}" || {
  echo "FAIL: cookie-only oauth2-proxy requested an unexpected scope" >&2
  exit 1
}
grep -Fq -- '- --oidc-email-claim=preferred_username' <<< "${output}" || {
  echo "FAIL: oauth2-proxy did not use the tenant-facing username as its email claim" >&2
  exit 1
}
grep -Fq -- '- --cookie-expire=45m' <<< "${output}" || {
  echo "FAIL: cookie-only oauth2-proxy did not expire before the ID token" >&2
  exit 1
}
if grep -Fq -- 'offline_access' <<< "${output}" ||
  grep -Fq -- '--session-store-type=redis' <<< "${output}" ||
  grep -Fq -- 'OAUTH2_PROXY_REDIS_PASSWORD' <<< "${output}" ||
  grep -Fq -- 'checksum/redis-session-credentials:' <<< "${output}"; then
  echo "FAIL: cookie-only oauth2-proxy rendered Redis or refresh-token configuration" >&2
  exit 1
fi

echo "==> helm template cetirun-istio (Redis session store)"
redis_output=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-string oauth2Proxy.sessionStore.type=redis \
  --set-string oauth2Proxy.sessionStore.redis.connectionURL=redis://oauth2-proxy-redis.example.svc.cluster.local:6379/0 \
  --set-string oauth2Proxy.sessionStore.redis.existingSecret=oauth2-proxy-redis-credentials)
grep -Fq -- '- --scope=openid email profile offline_access' <<< "${redis_output}" || {
  echo "FAIL: Redis oauth2-proxy did not request a refresh token" >&2
  exit 1
}
grep -Fq -- '- --session-store-type=redis' <<< "${redis_output}" || {
  echo "FAIL: Redis oauth2-proxy did not enable the Redis session store" >&2
  exit 1
}
grep -Fq -- '- --redis-connection-url=redis://oauth2-proxy-redis.example.svc.cluster.local:6379/0' <<< "${redis_output}" || {
  echo "FAIL: Redis oauth2-proxy did not render the configured connection URL" >&2
  exit 1
}
grep -Fq -- 'name: OAUTH2_PROXY_REDIS_PASSWORD' <<< "${redis_output}" || {
  echo "FAIL: Redis oauth2-proxy did not load its password from a Secret" >&2
  exit 1
}
grep -Fq -- 'checksum/redis-session-credentials:' <<< "${redis_output}" || {
  echo "FAIL: Redis Secret changes cannot trigger an oauth2-proxy rollout" >&2
  exit 1
}
grep -Fq -- '- --cookie-expire=8h' <<< "${redis_output}" || {
  echo "FAIL: Redis oauth2-proxy did not render the configured cookie lifetime" >&2
  exit 1
}
grep -Fq -- '- --cookie-refresh=30m' <<< "${redis_output}" || {
  echo "FAIL: Redis oauth2-proxy did not refresh the Portal session before ID-token expiry" >&2
  exit 1
}
policy_output=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --show-only templates/ingress/authorization-policy.yaml)
callback_count="$(grep -Fc '/api/v1/agent-authorization/callback' <<< "${policy_output}")"
[[ "${callback_count}" == 2 ]] || {
  echo "FAIL: apex callback bypass leaked into a legacy instance-subdomain rule" >&2
  exit 1
}
grep -Fq -- '- "/{*}/api/messages"' <<< "${output}" || {
  echo "FAIL: apex webhook bypass did not use a one-segment URI template" >&2
  exit 1
}
grep -Fq 'path:match("^/[^/]+/api/messages$")' <<< "${output}" || {
  echo "FAIL: ownership filter did not restrict apex webhooks to one instance segment" >&2
  exit 1
}
grep -Fq 'instance_name = legacy_instance_name(authority)' <<< "${output}" || {
  echo "FAIL: legacy subdomain routes did not derive ownership exclusively from authority" >&2
  exit 1
}
grep -Fq 'if not host then host = value:match("^([^:]+):%d+$") end' <<< "${output}" || {
  echo "FAIL: ownership filter did not normalize the request host" >&2
  exit 1
}
grep -Fq 'if host:sub(-1) == "." then host = host:sub(1, -2) end' <<< "${output}" || {
  echo "FAIL: ownership filter did not normalize a DNS root label" >&2
  exit 1
}
grep -Fq 'request_handle:respond({[":status"] = "403"}, "Forbidden: invalid instance host")' <<< "${output}" || {
  echo "FAIL: invalid legacy hosts did not fail closed" >&2
  exit 1
}
grep -Fq 'if not is_anonymous_apex_bypass(path_only, method, authority) then' <<< "${output}" || {
  echo "FAIL: anonymous apex bypasses could be promoted to a Portal identity" >&2
  exit 1
}
grep -Fq 'if request_host(authority) ~= PORTAL_DOMAIN then' <<< "${output}" || {
  echo "FAIL: Portal path exemptions leaked to instance subdomains" >&2
  exit 1
}
for header in x-auth-request-user x-auth-request-email x-auth-request-preferred-username; do
  grep -Fq "headers():remove(\"${header}\")" <<< "${output}" || {
    echo "FAIL: anonymous webhook did not strip spoofable ${header}" >&2
    exit 1
  }
done

echo "==> helm template cetirun-istio (legacy webhook bypass only)"
legacy_only_policy=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --show-only templates/ingress/authorization-policy.yaml \
  --set-json 'authPolicy.webhookBypassPaths=[]' \
  --set-json 'authPolicy.apexPostBypassPaths=[]')
[[ "$(grep -Ec '^[[:space:]]+paths:$' <<< "${legacy_only_policy}")" == 1 ]] || {
  echo "FAIL: legacy-only webhook configuration rendered an empty apex method guard" >&2
  exit 1
}
[[ "$(grep -Fc 'notMethods:' <<< "${legacy_only_policy}")" == 1 ]] || {
  echo "FAIL: legacy-only webhook configuration rendered the wrong method guards" >&2
  exit 1
}

echo "==> reject broad webhook suffix bypass"
if helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-json 'authPolicy.webhookBypassPaths=["*/api/messages"]' >/dev/null 2>&1; then
  echo "FAIL: broad webhook suffix bypass rendered successfully" >&2
  exit 1
fi

echo "==> helm template cetirun-istio (umbrella portal override)"
output=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-string portalAuth.clusterHost=cetirun-portal.cetirun-system.svc.cluster.local)
assert_ownership_target "${output}" "cetirun-portal.cetirun-system.svc.cluster.local"

echo "==> reject incomplete and invalid Redis session configuration"
if helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-string oauth2Proxy.sessionStore.type=redis >/dev/null 2>&1; then
  echo "FAIL: Redis session store rendered without a connection URL and Secret" >&2
  exit 1
fi
if helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-string oauth2Proxy.sessionStore.type=invalid >/dev/null 2>&1; then
  echo "FAIL: invalid session store type rendered successfully" >&2
  exit 1
fi
if helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-string oauth2Proxy.emailClaim=unsupported >/dev/null 2>&1; then
  echo "FAIL: invalid OIDC email claim rendered successfully" >&2
  exit 1
fi
