#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-chart}"
release_name="${RELEASE_NAME:-cetirun}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

# The chart deliberately has no image-embedded model default. Every rendering
# scenario, including expected-failure cases, must therefore supply a model so
# that each test exercises its intended validation path.
helm_template() {
  helm template "$@" --set-string secrets.model=lint-model
}

values_files=(
  "values.yaml"
)

for values_file in "${values_files[@]}"; do
  if [ -f "${chart_dir}/${values_file}" ]; then
    echo "==> helm template ${release_name} ${chart_dir} -f ${values_file} (agentType=openclaw)"
    helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/${values_file}" \
      --set agentType=openclaw \
      --set secrets.openclawGatewayToken=lint-token > /dev/null

    echo "==> helm template ${release_name} ${chart_dir} -f ${values_file} (agentType=hermes)"
    helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/${values_file}" \
      --set agentType=hermes > /dev/null
  fi
done

# Test variant: Hermes renders API port, native config, and no OpenClaw doctor init.
echo "==> helm template ${release_name} ${chart_dir} (agentType=hermes, rendered surface)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set agentType=hermes)
grep -q 'hermes-config.yaml' <<< "$output" || { echo "FAIL: hermes-config.yaml not rendered" >&2; exit 1; }
grep -q 'name: api' <<< "$output" || { echo "FAIL: Hermes API service/container port not found" >&2; exit 1; }
grep -q 'API_SERVER_KEY' <<< "$output" || { echo "FAIL: API_SERVER_KEY not rendered" >&2; exit 1; }
if grep -q '^  hermes-copilot-auth.py: |' <<< "$output"; then
  echo "FAIL: Hermes Copilot Python helper should be packaged in the image, not rendered in ConfigMap" >&2; exit 1
fi
if grep -q 'command: \["openclaw", "doctor"' <<< "$output"; then
  echo "FAIL: OpenClaw doctor init should not render for Hermes" >&2; exit 1
fi

# Test variant: existing secret
echo "==> helm template ${release_name} ${chart_dir} (secrets.existingSecret=my-secret)"
helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.existingSecret=my-secret > /dev/null

# Test variant: ttyd enabled
echo "==> helm template ${release_name} ${chart_dir} (ttyd.enabled=true)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set ttyd.enabled=true)
# Use here-strings instead of "echo | grep" to avoid SIGPIPE/broken-pipe errors
# under "set -eo pipefail". grep -q exits immediately after the first match,
# closing the pipe while echo is still writing, causing a non-zero exit code
# that pipefail treats as a failure.
grep -q 'name: ttyd' <<< "$output" || { echo "FAIL: ttyd port not found when enabled" >&2; exit 1; }
grep -q 'TTYD_ENABLED' <<< "$output" || { echo "FAIL: TTYD_ENABLED env var not found" >&2; exit 1; }

# Test variant: ttyd disabled (default)
echo "==> helm template ${release_name} ${chart_dir} (ttyd.enabled=false)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set ttyd.enabled=false)
if grep -q 'containerPort: 7681' <<< "$output"; then
  echo "FAIL: ttyd containerPort should not appear when disabled" >&2; exit 1
fi

# Test variant: ttyd port collision with noVNC (should fail)
echo "==> helm template ${release_name} ${chart_dir} (ttyd.port=6080, expect failure)"
if helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set ttyd.enabled=true \
  --set ttyd.port=6080 2>/dev/null; then
  echo "FAIL: ttyd.port=6080 should have been rejected (noVNC collision)" >&2; exit 1
fi

# Test variant: ttyd port collision with gateway (should fail)
echo "==> helm template ${release_name} ${chart_dir} (ttyd.port=18789, expect failure)"
if helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set ttyd.enabled=true \
  --set ttyd.port=18789 2>/dev/null; then
  echo "FAIL: ttyd.port=18789 should have been rejected (gateway collision)" >&2; exit 1
fi

# Test variant: Hermes ttyd port collision with API (should fail)
echo "==> helm template ${release_name} ${chart_dir} (agentType=hermes, ttyd.port=8642, expect failure)"
if helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set agentType=hermes \
  --set ttyd.enabled=true \
  --set ttyd.port=8642 2>/dev/null; then
  echo "FAIL: Hermes ttyd.port=8642 should have been rejected (API collision)" >&2; exit 1
fi


# ---------------------------------------------------------------------------
# Desktop sidecar tests
# ---------------------------------------------------------------------------

# Test variant: desktop enabled (default)
echo "==> helm template ${release_name} ${chart_dir} (desktop.enabled=true)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set desktop.enabled=true)
grep -q 'name: desktop' <<< "$output" || { echo "FAIL: desktop container not found when enabled" >&2; exit 1; }
grep -q 'containerPort: 6080' <<< "$output" || { echo "FAIL: noVNC port not found on desktop container" >&2; exit 1; }
grep -q 'containerPort: 18800' <<< "$output" || { echo "FAIL: Chrome CDP port not found on desktop container" >&2; exit 1; }
grep -q 'tmp-desktop' <<< "$output" || { echo "FAIL: tmp-desktop volume not found when desktop enabled" >&2; exit 1; }

# Test variant: desktop disabled
echo "==> helm template ${release_name} ${chart_dir} (desktop.enabled=false)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set desktop.enabled=false \
  --set openclaw.config.browser.attachOnly=false)
if grep -q 'name: desktop' <<< "$output"; then
  echo "FAIL: desktop container should not appear when disabled" >&2; exit 1
fi
if grep -q 'tmp-desktop' <<< "$output"; then
  echo "FAIL: tmp-desktop volume should not appear when desktop disabled" >&2; exit 1
fi
# When desktop is disabled, noVNC ports should not appear anywhere
if grep -q 'containerPort: 6080' <<< "$output"; then
  echo "FAIL: noVNC container port should not appear when desktop disabled" >&2; exit 1
fi

# ---------------------------------------------------------------------------
# Tailscale enablement tests
# ---------------------------------------------------------------------------

# Test variant: tailscale enabled via secrets.tailscaleAuthKey (no existingSecret)
echo "==> helm template ${release_name} ${chart_dir} (tailscaleAuthKey set, no existingSecret)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set secrets.tailscaleAuthKey=tskey-test-key)
grep -q 'TAILSCALE_ENABLED' <<< "$output" || { echo "FAIL: TAILSCALE_ENABLED not found when tailscaleAuthKey is set" >&2; exit 1; }
grep -q 'TS_AUTHKEY' <<< "$output" || { echo "FAIL: TS_AUTHKEY env var not found when tailscaleAuthKey is set" >&2; exit 1; }
grep -q 'TAILSCALE_HOSTNAME' <<< "$output" || { echo "FAIL: TAILSCALE_HOSTNAME not found when tailscaleAuthKey is set" >&2; exit 1; }
grep -q 'tailscale-state' <<< "$output" || { echo "FAIL: tailscale-state volume not found when tailscaleAuthKey is set" >&2; exit 1; }

# Test variant: tailscale disabled (no tailscaleAuthKey, no existingSecret)
echo "==> helm template ${release_name} ${chart_dir} (no tailscaleAuthKey, no existingSecret)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token)
if grep -q 'TAILSCALE_ENABLED' <<< "$output"; then
  echo "FAIL: TAILSCALE_ENABLED should not appear when tailscaleAuthKey is not set" >&2; exit 1
fi
if grep -q 'tailscale-state' <<< "$output"; then
  echo "FAIL: tailscale-state volume should not appear when tailscale is disabled" >&2; exit 1
fi

# Test variant: tailscale enabled via secrets.tailscaleAuthKey + existingSecret
echo "==> helm template ${release_name} ${chart_dir} (tailscaleAuthKey set + existingSecret)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.existingSecret=gateway-token \
  --set secrets.tailscaleAuthKey=tskey-test-key)
grep -q 'TAILSCALE_ENABLED' <<< "$output" || { echo "FAIL: TAILSCALE_ENABLED not found with existingSecret + tailscaleAuthKey" >&2; exit 1; }
grep -q 'TS_AUTHKEY' <<< "$output" || { echo "FAIL: TS_AUTHKEY not found with existingSecret + tailscaleAuthKey" >&2; exit 1; }

# Test variant: existingSecret set, no tailscaleAuthKey (tailscale depends on Secret lookup at deploy time)
# helm template cannot do live lookup, so tailscale should NOT be enabled in dry-run
echo "==> helm template ${release_name} ${chart_dir} (existingSecret, no tailscaleAuthKey — dry-run)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.existingSecret=gateway-token)
if grep -q 'TAILSCALE_ENABLED' <<< "$output"; then
  echo "FAIL: TAILSCALE_ENABLED should not appear in dry-run without tailscaleAuthKey (lookup unavailable)" >&2; exit 1
fi

# ---------------------------------------------------------------------------
# Shared skills Azure Blob CSI tests
# ---------------------------------------------------------------------------

# Test variant: Blobfuse2 shared skills render the CSI protocol, staging Secret,
# and static mount options.
echo "==> helm template ${release_name} ${chart_dir} (skills.shared=true, protocol=fuse2)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set skills.shared=true \
  --set skills.resourceGroup=lint-rg \
  --set skills.storageAccount=lintstorage \
  --set skills.containerName=lint-skills \
  --set skills.storageSize=10Gi \
  --set skills.protocol=fuse2 \
  --set skills.secretName=blobfuse-staging \
  --set 'skills.mountOptions[0]=-o allow_other')
grep -q 'protocol: fuse2' <<< "$output" || { echo "FAIL: Fuse2 protocol not rendered for shared skills" >&2; exit 1; }
grep -q 'name: blobfuse-staging' <<< "$output" || { echo "FAIL: Blobfuse staging Secret not rendered" >&2; exit 1; }
grep -q -- '- -o allow_other' <<< "$output" || { echo "FAIL: Blobfuse mount option not rendered" >&2; exit 1; }

# Test variant: Blobfuse shared skills require their node-stage Secret.
echo "==> helm template ${release_name} ${chart_dir} (skills.protocol=fuse2 without secretName, expect failure)"
if helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set skills.shared=true \
  --set skills.resourceGroup=lint-rg \
  --set skills.storageAccount=lintstorage \
  --set skills.containerName=lint-skills \
  --set skills.storageSize=10Gi \
  --set skills.protocol=fuse2 2>/dev/null; then
  echo "FAIL: Blobfuse shared skills without secretName should have been rejected" >&2; exit 1
fi

# ---------------------------------------------------------------------------
# RuntimeClass (Kata pod sandboxing) tests
# ---------------------------------------------------------------------------

# Test variant: runtimeClassName renders by default (kata-vm-isolation)
echo "==> helm template ${release_name} ${chart_dir} (runtimeClassName=kata-vm-isolation, default)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token)
grep -q 'runtimeClassName: kata-vm-isolation' <<< "$output" || { echo "FAIL: runtimeClassName not found in default render" >&2; exit 1; }

# Test variant: runtimeClassName disabled (empty string)
echo "==> helm template ${release_name} ${chart_dir} (runtimeClassName empty)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set secrets.openclawGatewayToken=lint-token \
  --set runtimeClassName="")
if grep -q 'runtimeClassName' <<< "$output"; then
  echo "FAIL: runtimeClassName should not appear when set to empty string" >&2; exit 1
fi

# ---------------------------------------------------------------------------
# Hermes + MS Teams (cetirun_teams plugin) tests
# ---------------------------------------------------------------------------

# Test variant: Hermes with Teams enabled renders cetirun_teams platform config
echo "==> helm template ${release_name} ${chart_dir} (agentType=hermes, msteamsWebhookPort=3978)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set agentType=hermes \
  --set msteamsWebhookPort=3978 \
  --set secrets.msteamsAppId=test-app-id \
  --set secrets.msteamsTenantId=test-tenant \
  --set secrets.msteamsAppPassword=test-secret)
grep -q 'cetirun_teams:' <<< "$output" || { echo "FAIL: cetirun_teams platform not found in hermes-config.yaml" >&2; exit 1; }
# Verify openclaw.json is NOT rendered for Hermes
if grep -q 'openclaw.json' <<< "$output"; then
  echo "FAIL: openclaw.json should not be rendered for Hermes" >&2; exit 1
fi

# Test variant: Hermes with port but no credentials does NOT render cetirun_teams
echo "==> helm template ${release_name} ${chart_dir} (agentType=hermes, msteamsWebhookPort=3978, no creds)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set agentType=hermes \
  --set msteamsWebhookPort=3978)
if grep -q 'cetirun_teams:' <<< "$output"; then
  echo "FAIL: cetirun_teams should not appear without credentials" >&2; exit 1
fi

# Test variant: Hermes without Teams does NOT render cetirun_teams
echo "==> helm template ${release_name} ${chart_dir} (agentType=hermes, no Teams)"
output=$(helm_template "${release_name}" "${chart_dir}" -f "${chart_dir}/values.yaml" \
  --set agentType=hermes)
if grep -q 'cetirun_teams:' <<< "$output"; then
  echo "FAIL: cetirun_teams should not appear when Teams is not enabled" >&2; exit 1
fi
