#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# e2e-test.sh — End-to-end test for CetiRun.
#
# Deploys a test OpenClawInstance, waits for it to become Ready,
# verifies key resources (namespace, secret, StatefulSet, pod, VirtualService),
# validates gateway routing and OAuth2 redirect to Entra ID, optionally checks
# the openclaw image tag, and cleans up.
#
# Prerequisites:
#   - kubectl and helm configured to target the AKS cluster
#   - OpenClawInstance CRD installed
#   - Operator, portal, and OAuth2 Proxy pods running
#
# Environment variables:
#   TEST_ALIAS            — Instance alias           (default: johnreese)
#   AGENT_TYPE            — Agent type               (default: openclaw)
#   TEST_NAMESPACE        — Operator namespace        (default: cetirun-system)
#   PORTAL_NAMESPACE      — Portal namespace          (default: TEST_NAMESPACE)
#   READY_TIMEOUT         — Seconds to wait for Ready (default: 600)
#   DOMAIN                — Base domain for routing   (auto-detected from operator Helm values)
#   EXPECTED_IMAGE_TAG    — If set, verify agent pod uses this image tag
#   SKIP_CLEANUP          — If non-empty, skip cleanup on success

set -euo pipefail

TEST_ALIAS="${TEST_ALIAS:-johnreese}"
AGENT_TYPE="${AGENT_TYPE:-openclaw}"
TEST_NAMESPACE="${TEST_NAMESPACE:-cetirun-system}"
PORTAL_NAMESPACE="${PORTAL_NAMESPACE:-${TEST_NAMESPACE}}"
READY_TIMEOUT="${READY_TIMEOUT:-600}"
INSTANCE_NS="cetirun-${TEST_ALIAS}"

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1" >&2; FAILURES=$((FAILURES + 1)); }
FAILURES=0

cleanup() {
  echo ""
  echo "==> Cleanup: deleting test instance '${TEST_ALIAS}'..."
  kubectl delete openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" --ignore-not-found --timeout=120s 2>/dev/null || true

  # Wait for finalizer to complete namespace deletion
  local deadline=$((SECONDS + 180))
  while kubectl get namespace "${INSTANCE_NS}" &>/dev/null; do
    if (( SECONDS > deadline )); then
      echo "WARNING: Namespace ${INSTANCE_NS} still exists after 180s — giving up." >&2
      break
    fi
    sleep 5
  done
  echo "    Cleanup done."
}

echo "==========================================="
echo " CetiRun E2E Test"
echo "==========================================="
echo ""
echo "Test alias:    ${TEST_ALIAS}"
echo "Agent type:    ${AGENT_TYPE}"
echo "Operator NS:   ${TEST_NAMESPACE}"
echo "Portal NS:     ${PORTAL_NAMESPACE}"
echo "Instance NS:   ${INSTANCE_NS}"
echo "Ready timeout: ${READY_TIMEOUT}s"
echo ""

if [[ "${AGENT_TYPE}" != "openclaw" && "${AGENT_TYPE}" != "hermes" ]]; then
  echo "ERROR: AGENT_TYPE must be 'openclaw' or 'hermes', got '${AGENT_TYPE}'." >&2
  exit 1
fi

# ── Prerequisite checks ──────────────────────────────────────────────────────
echo "==> Checking prerequisites..."
for cmd in kubectl helm jq curl base64; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: ${cmd} is required but not installed." >&2
    exit 1
  fi
done

kubectl cluster-info --request-timeout=5s > /dev/null
if ! kubectl get crd openclawinstances.cetirun.microsoft.com &>/dev/null; then
  echo "ERROR: OpenClawInstance CRD not found. Is the operator deployed?" >&2
  exit 1
fi

# Check operator is running
OPERATOR_PODS=$(kubectl get pods -n "${TEST_NAMESPACE}" -l app.kubernetes.io/name=cetirun-operator --field-selector=status.phase=Running -o name 2>/dev/null | wc -l || echo 0)
if (( OPERATOR_PODS == 0 )); then
  echo "ERROR: No running operator pods found in ${TEST_NAMESPACE}." >&2
  exit 1
fi
echo "    Operator running (${OPERATOR_PODS} pod(s))."

# Check portal is running
PORTAL_PODS=$(kubectl get pods -n "${PORTAL_NAMESPACE}" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l || echo 0)
if (( PORTAL_PODS == 0 )); then
  echo "ERROR: No running portal pods found in ${PORTAL_NAMESPACE}." >&2
  exit 1
fi
echo "    Portal running (${PORTAL_PODS} pod(s))."

# Check OAuth2 Proxy is running
OAUTH2_PODS=$(kubectl get pods -n cetirun-istio -l app=oauth2-proxy --field-selector=status.phase=Running -o name 2>/dev/null | wc -l || echo 0)
if (( OAUTH2_PODS == 0 )); then
  echo "ERROR: No running oauth2-proxy pods found in cetirun-istio namespace." >&2
  exit 1
fi
echo "    OAuth2 Proxy running (${OAUTH2_PODS} pod(s))."

echo "    Prerequisites OK."

# ── Deploy test instance ─────────────────────────────────────────────────────
echo ""
echo "==> Cleaning up any leftover instance '${TEST_ALIAS}'..."

# Clean up any leftover from a previous cancelled/failed run
if kubectl get openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" &>/dev/null; then
  echo "    Found leftover CR — deleting..."
  kubectl delete openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" --ignore-not-found --timeout=120s 2>/dev/null || true
fi
# Wait for namespace to be fully removed (finalizer may still be running)
if kubectl get namespace "${INSTANCE_NS}" &>/dev/null; then
  echo "    Waiting for leftover namespace ${INSTANCE_NS} to be deleted..."
  local_deadline=$((SECONDS + 180))
  while kubectl get namespace "${INSTANCE_NS}" &>/dev/null; do
    if (( SECONDS > local_deadline )); then
      echo "WARNING: Namespace ${INSTANCE_NS} still exists after 180s — force-deleting." >&2
      kubectl delete namespace "${INSTANCE_NS}" --ignore-not-found --timeout=60s 2>/dev/null || true
      sleep 10
      break
    fi
    sleep 5
  done
fi
echo "    Clean."

echo ""
echo "==> Creating test OpenClawInstance '${TEST_ALIAS}'..."

kubectl apply -n "${TEST_NAMESPACE}" -f - <<EOF
apiVersion: cetirun.microsoft.com/v1alpha1
kind: OpenClawInstance
metadata:
  name: ${TEST_ALIAS}
spec:
  agentType: ${AGENT_TYPE}
  gatewayTokenSecretRef: "gateway-token"
  user:
    alias: ${TEST_ALIAS}
    displayName: "E2E Test User"
    team: e2e-testing
    email: e2e-test@example.com
  helm:
    values:
      skills:
        shared: false
EOF

echo "    Instance CR applied."

# Set up cleanup trap after CR is created
trap cleanup EXIT

# ── Wait for Ready ────────────────────────────────────────────────────────────
echo ""
echo "==> Waiting for instance to reach Ready phase (timeout: ${READY_TIMEOUT}s)..."
DEADLINE=$((SECONDS + READY_TIMEOUT))
LAST_PHASE=""
while true; do
  PHASE=$(kubectl get openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" \
    -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")

  if [[ "${PHASE}" != "${LAST_PHASE}" ]]; then
    echo "    Phase: ${PHASE}"
    LAST_PHASE="${PHASE}"
  fi

  if [[ "${PHASE}" == "Ready" ]]; then
    echo "    Instance is Ready!"
    break
  fi

  if [[ "${PHASE}" == "Failed" ]]; then
    echo "ERROR: Instance entered Failed phase." >&2
    MSG=$(kubectl get openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null || echo "")
    [[ -n "$MSG" ]] && echo "  Message: ${MSG}" >&2
    exit 1
  fi

  if (( SECONDS > DEADLINE )); then
    echo "ERROR: Timed out waiting for Ready phase after ${READY_TIMEOUT}s." >&2
    echo "  Last phase: ${PHASE}" >&2
    kubectl describe openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" 2>/dev/null || true
    exit 1
  fi

  sleep 10
done

# ── Verify resources ─────────────────────────────────────────────────────────
echo ""
echo "==> Verifying resources..."

# Namespace
if kubectl get namespace "${INSTANCE_NS}" &>/dev/null; then
  pass "Namespace ${INSTANCE_NS} exists"
else
  fail "Namespace ${INSTANCE_NS} not found"
fi

# Gateway token secret
if kubectl get secret gateway-token -n "${INSTANCE_NS}" &>/dev/null; then
  pass "Gateway token secret exists"
else
  fail "Gateway token secret not found"
fi

# Helm release (StatefulSet)
STS_COUNT=$(kubectl get statefulset -n "${INSTANCE_NS}" -o name 2>/dev/null | wc -l || echo 0)
if (( STS_COUNT > 0 )); then
  pass "StatefulSet found (${STS_COUNT})"
else
  fail "No StatefulSet found in ${INSTANCE_NS}"
fi

# Pod running
RUNNING_PODS=$(kubectl get pods -n "${INSTANCE_NS}" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l || echo 0)
if (( RUNNING_PODS > 0 )); then
  pass "Running pod(s) found (${RUNNING_PODS})"
else
  fail "No running pods in ${INSTANCE_NS}"
fi

POD_NAME=$(kubectl get pods -n "${INSTANCE_NS}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [[ "${AGENT_TYPE}" == "hermes" && -n "${POD_NAME}" ]]; then
  echo ""
  echo "==> Verifying Hermes services inside the pod..."

  API_SERVER_KEY=$(kubectl get secret gateway-token -n "${INSTANCE_NS}" \
    -o jsonpath='{.data.API_SERVER_KEY}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  if [[ -n "${API_SERVER_KEY}" ]]; then
    pass "Hermes API_SERVER_KEY secret exists"
  else
    fail "Hermes API_SERVER_KEY secret not found"
  fi

  HERMES_ENV=$(kubectl get secret gateway-token -n "${INSTANCE_NS}" \
    -o jsonpath='{.data.hermes\.env}' 2>/dev/null | base64 -d 2>/dev/null || echo "")
  if echo "${HERMES_ENV}" | grep -q '^API_SERVER_KEY='; then
    pass "Hermes hermes.env secret contains API_SERVER_KEY"
  else
    fail "Hermes hermes.env secret missing API_SERVER_KEY"
  fi

  wait_for_pod_http() {
    local label="$1"
    local url="$2"
    local deadline=$((SECONDS + 120))
    while true; do
      if kubectl exec -n "${INSTANCE_NS}" "${POD_NAME}" -- curl -fsS --max-time 5 "${url}" >/dev/null 2>&1; then
        pass "${label} reachable"
        return
      fi
      if (( SECONDS > deadline )); then
        fail "${label} not reachable at ${url}"
        return
      fi
      sleep 5
    done
  }

  wait_for_pod_http "Hermes API health" "http://127.0.0.1:8642/health"
fi

# VirtualService
if kubectl get virtualservice -n "${INSTANCE_NS}" -o name 2>/dev/null | grep -q .; then
  pass "VirtualService exists"
else
  fail "VirtualService not found"
fi

# ── Suspend / Unsuspend cycle ─────────────────────────────────────────────────
echo ""
echo "==> Testing suspend / unsuspend cycle..."

# Determine PVC name (StatefulSet volumeClaimTemplate "data" + pod ordinal 0)
mapfile -t STS_NAMES < <(kubectl get statefulset -n "${INSTANCE_NS}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)

if (( ${#STS_NAMES[@]} == 1 )) && [[ -n "${STS_NAMES[0]}" ]]; then
  STS_NAME="${STS_NAMES[0]}"
elif (( ${#STS_NAMES[@]} == 0 )); then
  fail "No StatefulSets found in namespace ${INSTANCE_NS}"
  STS_NAME=""
else
  fail "Expected 1 StatefulSet in namespace ${INSTANCE_NS}, found ${#STS_NAMES[@]}"
  STS_NAME=""
fi
PVC_NAME="data-${STS_NAME}-0"

if [[ -n "${STS_NAME}" ]]; then
  # Verify PVC exists and is Bound before suspend
  PVC_PHASE=$(kubectl get pvc "${PVC_NAME}" -n "${INSTANCE_NS}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
  if [[ "${PVC_PHASE}" == "Bound" ]]; then
    pass "PVC ${PVC_NAME} exists and is Bound (before suspend)"
  else
    fail "PVC ${PVC_NAME} phase is '${PVC_PHASE}', expected 'Bound' (before suspend)"
  fi
fi

# Suspend the instance
echo "    Patching suspended=true..."
kubectl patch openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" --type=merge -p '{"spec":{"suspended":true}}'

# Wait for Suspended phase
SUSPEND_TIMEOUT=300
SUSPEND_DEADLINE=$((SECONDS + SUSPEND_TIMEOUT))
while true; do
  PHASE=$(kubectl get openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
  if [[ "${PHASE}" == "Suspended" ]]; then
    pass "Phase transitioned to Suspended"
    break
  fi
  if (( SECONDS > SUSPEND_DEADLINE )); then
    fail "Timed out waiting for Suspended phase (last: ${PHASE})"
    break
  fi
  sleep 5
done

# Verify: StatefulSet replicas=0
STS_REPLICAS=$(kubectl get statefulset -n "${INSTANCE_NS}" -o jsonpath='{.items[0].spec.replicas}' 2>/dev/null || echo "?")
if [[ "${STS_REPLICAS}" == "0" ]]; then
  pass "StatefulSet replicas=0 when suspended"
else
  fail "StatefulSet replicas=${STS_REPLICAS}, expected 0"
fi

# Verify: no running pods (wait for termination — pods may still be shutting down)
POD_GONE_DEADLINE=$((SECONDS + 120))
while true; do
  RUNNING_PODS_SUSPENDED=$(kubectl get pods -n "${INSTANCE_NS}" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l || echo 0)
  if (( RUNNING_PODS_SUSPENDED == 0 )); then
    pass "No running pods when suspended"
    break
  fi
  if (( SECONDS > POD_GONE_DEADLINE )); then
    fail "Found ${RUNNING_PODS_SUSPENDED} running pod(s) when suspended after 120s, expected 0"
    break
  fi
  sleep 5
done

# Verify: VirtualService deleted
if kubectl get virtualservice -n "${INSTANCE_NS}" -o name 2>/dev/null | grep -q .; then
  fail "VirtualService still exists when suspended"
else
  pass "VirtualService deleted when suspended"
fi

# Verify: PVC preserved during suspend
PVC_PHASE_SUSPENDED=$(kubectl get pvc "${PVC_NAME}" -n "${INSTANCE_NS}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
if [[ "${PVC_PHASE_SUSPENDED}" == "Bound" ]]; then
  pass "PVC ${PVC_NAME} preserved and Bound during suspend"
else
  fail "PVC ${PVC_NAME} phase is '${PVC_PHASE_SUSPENDED}' during suspend, expected 'Bound'"
fi

# Unsuspend the instance
echo "    Patching suspended=false..."
kubectl patch openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" --type=merge -p '{"spec":{"suspended":false}}'

# Wait for Ready phase
UNSUSPEND_DEADLINE=$((SECONDS + READY_TIMEOUT))
LAST_PHASE_UNSUSPEND=""
while true; do
  PHASE=$(kubectl get openclawinstance "${TEST_ALIAS}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")
  if [[ "${PHASE}" != "${LAST_PHASE_UNSUSPEND}" ]]; then
    echo "    Phase: ${PHASE}"
    LAST_PHASE_UNSUSPEND="${PHASE}"
  fi
  if [[ "${PHASE}" == "Ready" ]]; then
    pass "Phase transitioned back to Ready after unsuspend"
    break
  fi
  if [[ "${PHASE}" == "Failed" ]]; then
    fail "Instance entered Failed phase during unsuspend"
    break
  fi
  if (( SECONDS > UNSUSPEND_DEADLINE )); then
    fail "Timed out waiting for Ready after unsuspend (last: ${PHASE})"
    break
  fi
  sleep 10
done

# Verify: StatefulSet replicas=1
STS_REPLICAS_AFTER=$(kubectl get statefulset -n "${INSTANCE_NS}" -o jsonpath='{.items[0].spec.replicas}' 2>/dev/null || echo "?")
if [[ "${STS_REPLICAS_AFTER}" == "1" ]]; then
  pass "StatefulSet replicas=1 after unsuspend"
else
  fail "StatefulSet replicas=${STS_REPLICAS_AFTER} after unsuspend, expected 1"
fi

# Verify: pod running again
RUNNING_PODS_AFTER=$(kubectl get pods -n "${INSTANCE_NS}" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l || echo 0)
if (( RUNNING_PODS_AFTER > 0 )); then
  pass "Running pod(s) restored after unsuspend (${RUNNING_PODS_AFTER})"
else
  fail "No running pods after unsuspend"
fi

# Verify: VirtualService restored
if kubectl get virtualservice -n "${INSTANCE_NS}" -o name 2>/dev/null | grep -q .; then
  pass "VirtualService restored after unsuspend"
else
  fail "VirtualService not found after unsuspend"
fi

# Verify: PVC still exists after full cycle
PVC_PHASE_AFTER=$(kubectl get pvc "${PVC_NAME}" -n "${INSTANCE_NS}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
if [[ "${PVC_PHASE_AFTER}" == "Bound" ]]; then
  pass "PVC ${PVC_NAME} preserved through full suspend/unsuspend cycle"
else
  fail "PVC ${PVC_NAME} phase is '${PVC_PHASE_AFTER}' after unsuspend, expected 'Bound'"
fi

# ── Verify routing ────────────────────────────────────────────────────────────
echo ""
echo "==> Verifying gateway routing..."

# Auto-detect the domain from the operator's Helm values if not overridden.
if [[ -z "${DOMAIN:-}" ]]; then
  DOMAIN=$(helm get values cetirun-operator -n "${TEST_NAMESPACE}" -o json 2>/dev/null \
    | jq -r '.domainSuffix // empty' 2>/dev/null || echo "")
  [[ -z "${DOMAIN}" ]] && DOMAIN="example.com"
fi
INSTANCE_URL="https://${TEST_ALIAS}.${DOMAIN}"

if [[ "${AGENT_TYPE}" == "openclaw" ]]; then
  GATEWAY_TOKEN=$(kubectl get secret gateway-token -n "${INSTANCE_NS}" \
    -o jsonpath='{.data.OPENCLAW_GATEWAY_TOKEN}' 2>/dev/null | base64 -d 2>/dev/null || echo "")

  if [[ -n "${GATEWAY_TOKEN}" ]]; then
    HTTP_CODE=$(curl -s -o /dev/null -w '%{http_code}' \
      --connect-timeout 10 --max-time 30 -k \
      "${INSTANCE_URL}/?tkn=${GATEWAY_TOKEN}" 2>/dev/null || echo "000")

    if [[ "${HTTP_CODE}" =~ ^(200|301|302|401|403|426)$ ]]; then
      pass "Authenticated routing works (HTTP ${HTTP_CODE} at ${INSTANCE_URL})"
    else
      fail "Authenticated routing returned unexpected HTTP ${HTTP_CODE} at ${INSTANCE_URL}"
    fi
  else
    fail "Could not retrieve gateway token"
  fi
else
  echo "    SKIP: Hermes external API is protected by OAuth2 Proxy plus API_SERVER_KEY."
fi

# Verify unauthenticated request gets 302 redirect to Entra ID
REDIRECT_HEADERS=$(curl -s -D - -o /dev/null --connect-timeout 10 --max-time 30 -k \
  "${INSTANCE_URL}/" 2>/dev/null || echo "")
REDIRECT_CODE=$(echo "$REDIRECT_HEADERS" | awk '/^HTTP\//{print $2}' | head -1)
LOCATION=$(echo "$REDIRECT_HEADERS" | grep -i '^location:' | tr -d '\r' || true)

if [[ "${REDIRECT_CODE}" == "302" ]]; then
  pass "Unauthenticated request returns 302 redirect"
else
  fail "Unauthenticated request returned HTTP ${REDIRECT_CODE:-???}, expected 302"
fi

if echo "${LOCATION}" | grep -q 'login.microsoftonline.com'; then
  pass "Redirect points to login.microsoftonline.com"
else
  fail "Redirect Location does not point to login.microsoftonline.com: ${LOCATION}"
fi

# Verify client_id in the redirect matches the configured Entra app
EXPECTED_CLIENT_ID=$(helm get values cetirun-istio -n cetirun-istio -o json 2>/dev/null \
  | jq -r '.clientId // empty' 2>/dev/null || echo "")
if [[ -n "${EXPECTED_CLIENT_ID}" ]]; then
  if echo "${LOCATION}" | grep -q "client_id=${EXPECTED_CLIENT_ID}"; then
    pass "Redirect uses correct client_id (${EXPECTED_CLIENT_ID})"
  else
    fail "Redirect client_id does not match expected '${EXPECTED_CLIENT_ID}'"
  fi
else
  echo "    SKIP: Could not determine expected client_id from Helm values."
fi

# ── Verify image tags ───────────────────────────────────────────────────────────────
if [[ -n "${EXPECTED_IMAGE_TAG:-}" ]]; then
  echo ""
  echo "==> Verifying ${AGENT_TYPE} image tag..."
  image_name="${AGENT_TYPE}-gateway"
  ACTUAL_IMAGE=$(kubectl get pods -n "${INSTANCE_NS}" \
    -o jsonpath='{.items[0].spec.containers[*].image}' 2>/dev/null | tr ' ' '\n' | grep "${image_name}" || echo "")

  if echo "${ACTUAL_IMAGE}" | grep -q ":${EXPECTED_IMAGE_TAG}"; then
    pass "${AGENT_TYPE} image uses expected tag (${EXPECTED_IMAGE_TAG})"
  else
    fail "${AGENT_TYPE} image tag mismatch: expected '${EXPECTED_IMAGE_TAG}', got '${ACTUAL_IMAGE}'"
  fi
fi

# ── Verify Istio EnvoyFilter alias check ─────────────────────────────────────
echo ""
echo "==> Verifying EnvoyFilter alias-check configuration..."

ENVOY_FILTER_LUA=$(kubectl get envoyfilter cetirun-alias-check -n aks-istio-ingress \
  -o jsonpath='{.spec.configPatches[0].patch.value.typed_config.inline_code}' 2>/dev/null || echo "")

if [[ -n "${ENVOY_FILTER_LUA}" ]]; then
  pass "EnvoyFilter cetirun-alias-check exists with Lua code"
else
  fail "EnvoyFilter cetirun-alias-check not found or has no Lua code"
fi

# Verify aliasExemptPrefixes are rendered
for prefix in oauth2 portal llmapi; do
  if echo "${ENVOY_FILTER_LUA}" | grep -Fq "^${prefix}%."; then
    pass "Alias exempt prefix '${prefix}' rendered in EnvoyFilter"
  else
    fail "Alias exempt prefix '${prefix}' not found in EnvoyFilter"
  fi
done

# Verify adminAliases are rendered into the portal admins ConfigMap
# (admin authorization moved from a per-host Envoy Lua check to the portal
# /admin route guard backed by the cetirun-portal-admins ConfigMap).
ADMIN_ALIASES=$(helm get values cetirun-istio -n cetirun-istio -o json 2>/dev/null \
  | jq -r '.authPolicy.adminAliases // [] | .[]' 2>/dev/null || echo "")
if [[ -n "${ADMIN_ALIASES}" ]]; then
  PORTAL_ADMINS_CM=$(kubectl get configmap cetirun-portal-admins -n "${PORTAL_NAMESPACE}" \
    -o jsonpath='{.data.aliases}' 2>/dev/null || echo "")
  if [[ -n "${PORTAL_ADMINS_CM}" ]]; then
    pass "cetirun-portal-admins ConfigMap exists"
  else
    fail "cetirun-portal-admins ConfigMap missing or empty (expected for /admin authorization)"
  fi
  while IFS= read -r admin; do
    if echo "${PORTAL_ADMINS_CM}" | grep -Fxqi "${admin}"; then
      pass "Admin alias '${admin}' rendered in portal admins ConfigMap"
    else
      fail "Admin alias '${admin}' not found in portal admins ConfigMap"
    fi
  done <<< "${ADMIN_ALIASES}"
else
  echo "    SKIP: No adminAliases configured — skipping admin check."
fi

# ── Result ────────────────────────────────────────────────────────────────────
echo ""
echo "==========================================="
if (( FAILURES > 0 )); then
  echo " E2E FAILED (${FAILURES} failure(s))"
  echo "==========================================="
  # cleanup runs via trap
  exit 1
else
  echo " E2E PASSED"
  echo "==========================================="
  if [[ -n "${SKIP_CLEANUP:-}" ]]; then
    echo "SKIP_CLEANUP set — leaving test instance in place."
    trap - EXIT
  fi
fi
