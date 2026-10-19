#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-agent-broker/chart}"
app_version="$(awk -F': *' '$1=="appVersion" {gsub(/"/, "", $2); print $2; exit}' "${chart_dir}/Chart.yaml")"
if [ -z "${app_version}" ]; then
  echo "FAIL: Broker Chart.yaml has no appVersion" >&2
  exit 1
fi
values=(
  --set-string agentIdentity.tenantId=11111111-1111-1111-1111-111111111111
  --set-string agentIdentity.blueprintClientId=22222222-2222-2222-2222-222222222222
  --set-string agentIdentity.authorizationClientId=33333333-3333-3333-3333-333333333333
  --set-string agentIdentity.encryptionClientId=44444444-4444-4444-4444-444444444444
  --set-string agentIdentity.keyVaultKeyUrl=https://example.vault.azure.net/keys/cache/version
  --set-string agentIdentity.redirectUri=https://portal.example.invalid/api/v1/connections/callback
  --set-string agentIdentity.portalOrigin=https://portal.example.invalid
  --set-string agentIdentity.portalBaseUrl=https://portal.example.invalid
  --set-string istio.revision=asm-example
  --set istio.sidecarInjection=true
)

output="$(helm template broker-test "${chart_dir}" "${values[@]}")"
grep -Fq 'automountServiceAccountToken: false' <<<"${output}"
grep -Fq 'istio.io/rev: "asm-example"' <<<"${output}"
grep -Fq "image: \"ghcr.io/azure/cetirun-agent-broker:${app_version}\"" <<<"${output}"
grep -Fq 'image: "mcr.microsoft.com/entra-sdk/auth-sidecar:1.1.2-azurelinux3.0-distroless@sha256:73a8763100c93c3da1a3ed1ba3af2c6122c0517687354dceded30537a1aee97f"' <<<"${output}"
grep -Fq 'name: AzureAd__ClientCredentials__0__SignedAssertionFileDiskPath' <<<"${output}"
grep -Fq 'value: /var/run/secrets/azure/tokens/azure-identity-token' <<<"${output}"
if grep -Fq 'value: $(AZURE_FEDERATED_TOKEN_FILE)' <<<"${output}"; then
  echo 'SDK assertion path must not depend on a later webhook-injected variable' >&2
  exit 1
fi
grep -Fq 'name: Sidecar__AllowOverrides__GetAuthorizationHeader' <<<"${output}"
oauth_output="$(helm template broker-test "${chart_dir}" "${values[@]}" --set-string providers.github.credentialsSecretName=github-oauth-example)"
grep -Fq 'name: GITHUB_OAUTH_CLIENT_ID' <<<"${oauth_output}"
grep -Fq 'name: GITHUB_OAUTH_CLIENT_SECRET' <<<"${oauth_output}"
grep -Fq 'name: "github-oauth-example"' <<<"${oauth_output}"
if grep -Fq 'name: GITHUB_APP_' <<<"${oauth_output}"; then
  echo 'FAIL: personal GitHub OAuth must not configure GitHub App credentials' >&2
  exit 1
fi
google_output="$(helm template broker-test "${chart_dir}" "${values[@]}" --set-string providers.google.credentialsSecretName=google-oauth-example --set-string providers.google.clientIDKey=google-client-id --set-string providers.google.clientSecretKey=google-client-secret)"
grep -A4 -F 'name: GOOGLE_OAUTH_CLIENT_ID' <<<"${google_output}" | grep -Fq 'name: "google-oauth-example"'
grep -A4 -F 'name: GOOGLE_OAUTH_CLIENT_ID' <<<"${google_output}" | grep -Fq 'key: "google-client-id"'
grep -A4 -F 'name: GOOGLE_OAUTH_CLIENT_SECRET' <<<"${google_output}" | grep -Fq 'name: "google-oauth-example"'
grep -A4 -F 'name: GOOGLE_OAUTH_CLIENT_SECRET' <<<"${google_output}" | grep -Fq 'key: "google-client-secret"'
if grep -Fq 'name: GOOGLE_OAUTH_' <<<"${output}"; then
  echo 'FAIL: Google OAuth credentials must require an explicit Secret reference' >&2
  exit 1
fi
if grep -Fq ':latest' <<<"${output}"; then
  echo "FAIL: chart rendered a latest image tag" >&2
  exit 1
fi

digest_output="$(helm template broker-test "${chart_dir}" "${values[@]}" --set-string image.tag=test --set-string image.digest=sha256:1111111111111111111111111111111111111111111111111111111111111111 --set-string authSDK.tag=test --set-string authSDK.digest=sha256:2222222222222222222222222222222222222222222222222222222222222222)"
grep -Fq 'image: "ghcr.io/azure/cetirun-agent-broker:test@sha256:1111111111111111111111111111111111111111111111111111111111111111"' <<<"${digest_output}"
grep -Fq 'image: "mcr.microsoft.com/entra-sdk/auth-sidecar:test@sha256:2222222222222222222222222222222222222222222222222222222222222222"' <<<"${digest_output}"

# Only the Broker receives the short-lived Kubernetes API credential. The
# third-party Auth SDK container must not receive this mount.
deployment="$(awk '/^kind: Deployment$/{in_deployment=1} in_deployment{print} /^---$/{if(in_deployment) exit}' <<<"${output}")"
[[ "$(grep -Fc 'mountPath: /var/run/cetirun/kubernetes' <<<"${deployment}")" == 1 ]]
[[ "$(grep -Fc 'name: kubernetes-api-credential' <<<"${deployment}")" == 2 ]]
[[ "$(grep -Fc 'mountPath: /app/keys' <<<"${deployment}")" == 1 ]]
[[ "$(grep -Fc 'name: auth-sdk-keys' <<<"${deployment}")" == 2 ]]
grep -A4 -F 'name: auth-sdk-keys' <<<"${deployment}" | grep -Fq 'medium: Memory'
grep -A4 -F 'name: auth-sdk-keys' <<<"${deployment}" | grep -Fq 'sizeLimit: 16Mi'
[[ "$(grep -Fc 'mountPath: /tmp' <<<"${deployment}")" == 1 ]]
[[ "$(grep -Fc 'name: auth-sdk-tmp' <<<"${deployment}")" == 2 ]]
grep -A4 -F 'name: auth-sdk-tmp' <<<"${deployment}" | grep -Fq 'medium: Memory'
grep -A4 -F 'name: auth-sdk-tmp' <<<"${deployment}" | grep -Fq 'sizeLimit: 16Mi'

# The Auth SDK image declares the named user `app`. Kubernetes cannot prove a
# named image user is non-root, so the chart must pin the image's numeric UID
# and GID to satisfy runAsNonRoot before kubelet starts the container.
auth_sdk_security_context="$(awk '/- name: auth-sdk/{in_auth_sdk=1} in_auth_sdk && /securityContext:/{in_security_context=1} in_security_context{print} in_security_context && /resources:/{exit}' <<<"${deployment}")"
grep -Fq 'runAsNonRoot: true' <<<"${auth_sdk_security_context}"
grep -Fq 'runAsUser: 1654' <<<"${auth_sdk_security_context}"
grep -Fq 'runAsGroup: 1654' <<<"${auth_sdk_security_context}"

if helm template broker-test "${chart_dir}" "${values[@]}" --set istio.enabled=false >/dev/null 2>&1; then
  echo "FAIL: chart allowed Agent Identity Broker without Istio authorization" >&2
  exit 1
fi
if helm template broker-test "${chart_dir}" "${values[@]}" --set istio.sidecarInjection=false >/dev/null 2>&1; then
  echo "FAIL: chart allowed unconfirmed sidecar injection" >&2
  exit 1
fi
