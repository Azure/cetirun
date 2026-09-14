#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# setup.sh — Deploy AdminClaw via OpenClawInstance CR (operator-managed).
#
# This script handles pre-requisites that the operator cannot manage:
#   1. Create the namespace (needed before SA and Secret can be created)
#   2. Apply RBAC (BYO ServiceAccount + ClusterRole + ClusterRoleBinding)
#   3. Create the gateway token Secret
#   4. Apply the OpenClawInstance CR
#
# The operator then takes over and:
#   - Labels the namespace with Istio sidecar revision
#   - Deploys the openclaw Helm chart
#   - Creates the VirtualService
#
# Required env vars:
#   MODEL_API_BASE             — model gateway base URL
#   MODEL_API_KEY                — API key for the model gateway (e.g. gateway master key or virtual key)
#
# Optional env vars:
#   WORKLOAD_IDENTITY_CLIENT_ID  — Azure Workload Identity MSI client ID (default: empty)
#
# Options:
#   --skills      Enable shared skills volume (default: disabled)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

#-----------------------------------------------------------------------
# Parse arguments
#-----------------------------------------------------------------------
ENABLE_SKILLS=false
for arg in "$@"; do
  case "$arg" in
    --skills) ENABLE_SKILLS=true ;;
    --no-skills) ENABLE_SKILLS=false ;;
    *) echo "Unknown option: $arg" >&2; exit 1 ;;
  esac
done

#-----------------------------------------------------------------------
# Validate required environment variables and tools
#-----------------------------------------------------------------------
for CMD in kubectl envsubst; do
  if ! command -v "$CMD" &>/dev/null; then
    echo "ERROR: ${CMD} is required but not installed." >&2
    exit 1
  fi
done

if [[ -z "${MODEL_API_BASE:-}" ]]; then
  echo "ERROR: MODEL_API_BASE is required." >&2
  exit 1
fi
if [[ -z "${MODEL_API_KEY:-}" ]]; then
  echo "ERROR: MODEL_API_KEY is required (gateway master key or virtual key)." >&2
  exit 1
fi

#-----------------------------------------------------------------------
# Constants
#-----------------------------------------------------------------------
NAMESPACE="cetirun-admin"
HELM_RELEASE="openclaw-admin"
GATEWAY_SECRET_NAME="gateway-token"

echo "==========================================="
echo " AdminClaw — Setup (operator-managed)"
echo "==========================================="
echo ""
echo "Shared skills:     ${ENABLE_SKILLS}"
echo "Model API base:    ${MODEL_API_BASE}"
echo "Model API key:     (set)"
echo "Workload Identity: ${WORKLOAD_IDENTITY_CLIENT_ID:-(disabled)}"
echo ""

#-----------------------------------------------------------------------
# Step 1: Create namespace
#-----------------------------------------------------------------------
echo "--- Creating namespace ${NAMESPACE} ---"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
echo "Namespace ${NAMESPACE} ready."

#-----------------------------------------------------------------------
# Step 2: Apply RBAC (ServiceAccount + ClusterRole + ClusterRoleBinding)
#-----------------------------------------------------------------------
echo ""
echo "--- Applying RBAC ---"
export NAMESPACE
envsubst '${NAMESPACE}' \
  < "${SCRIPT_DIR}/rbac.yaml" \
  | kubectl apply -f -
echo "RBAC applied (openclaw-admin-cetirun SA + adminclaw-admin ClusterRole)."

# Annotate SA for Azure Workload Identity when client ID is provided;
# remove the annotation on re-runs where the env var is unset.
if [[ -n "${WORKLOAD_IDENTITY_CLIENT_ID:-}" ]]; then
  echo "  Annotating SA with workload identity client-id."
  kubectl annotate serviceaccount openclaw-admin-cetirun \
    --namespace "${NAMESPACE}" \
    --overwrite \
    "azure.workload.identity/client-id=${WORKLOAD_IDENTITY_CLIENT_ID}"
else
  echo "  Ensuring SA has no workload identity annotation."
  kubectl annotate serviceaccount openclaw-admin-cetirun \
    --namespace "${NAMESPACE}" \
    "azure.workload.identity/client-id-" 2>/dev/null || true
fi

#-----------------------------------------------------------------------
# Step 3: Create Gateway Token Secret (with optional GATEWAY_TOKEN)
#
# Pre-create the gateway-token Secret so the operator skips its own
# creation (ensureGatewayToken is a no-op when the Secret exists).
# Operators may optionally provide a fixed gateway token via the
# GATEWAY_TOKEN environment variable; if unset, an existing token is
# reused when present, otherwise a new OPENCLAW_GATEWAY_TOKEN is generated.
#-----------------------------------------------------------------------
echo ""
echo "--- Ensuring Gateway Token Secret ---"

# Preserve existing gateway token on re-run; only generate on first deploy.
EXISTING_TOKEN=$(kubectl get secret "${GATEWAY_SECRET_NAME}" \
  --namespace "${NAMESPACE}" \
  -o jsonpath='{.data.OPENCLAW_GATEWAY_TOKEN}' 2>/dev/null | base64 -d) || true

if [[ -n "${GATEWAY_TOKEN:-}" ]]; then
  echo "  Using GATEWAY_TOKEN from environment."
elif [[ -n "${EXISTING_TOKEN}" ]]; then
  GATEWAY_TOKEN="${EXISTING_TOKEN}"
  echo "  Reusing existing OPENCLAW_GATEWAY_TOKEN."
else
  GATEWAY_TOKEN=$(openssl rand -base64 32)
  echo "  Generated new OPENCLAW_GATEWAY_TOKEN."
fi

kubectl create secret generic "${GATEWAY_SECRET_NAME}" \
  --namespace "${NAMESPACE}" \
  --from-literal="OPENCLAW_GATEWAY_TOKEN=${GATEWAY_TOKEN}" \
  --from-literal="MODEL_API_KEY=${MODEL_API_KEY}" \
  --dry-run=client -o yaml \
  | kubectl apply -f -
echo "Gateway Token Secret ${GATEWAY_SECRET_NAME} ready."

#-----------------------------------------------------------------------
# Step 4: Apply OpenClawInstance CR
#-----------------------------------------------------------------------
echo ""
echo "--- Applying OpenClawInstance CR ---"
export ENABLE_SKILLS
export MODEL_API_BASE
export WORKLOAD_IDENTITY_CLIENT_ID="${WORKLOAD_IDENTITY_CLIENT_ID:-}"
if [[ -n "${WORKLOAD_IDENTITY_CLIENT_ID}" ]]; then
  export WORKLOAD_IDENTITY_ENABLED=true
else
  export WORKLOAD_IDENTITY_ENABLED=false
fi
envsubst '${ENABLE_SKILLS} ${MODEL_API_BASE} ${WORKLOAD_IDENTITY_ENABLED} ${WORKLOAD_IDENTITY_CLIENT_ID}' \
  < "${SCRIPT_DIR}/openclawinstance-admin.yaml" \
  | kubectl apply -f -
echo "OpenClawInstance 'admin' applied (skills=${ENABLE_SKILLS}, apiBase=${MODEL_API_BASE}, workloadIdentity=${WORKLOAD_IDENTITY_ENABLED})."

#-----------------------------------------------------------------------
# Done
#-----------------------------------------------------------------------
echo ""
echo "==========================================="
echo " Pre-requisites applied. Operator will now reconcile."
echo "==========================================="
echo ""
echo "Watch progress:"
echo "  kubectl get openclawinstance admin -w"
echo ""
echo "Verify (after Ready):"
echo "  kubectl get sts openclaw-admin-cetirun -n ${NAMESPACE}"
echo "  kubectl get vs cetirun-admin -n ${NAMESPACE}"
echo "  kubectl auth can-i '*' '*' --as=system:serviceaccount:${NAMESPACE}:openclaw-admin-cetirun"
echo "  curl https://admin.<your-domain>"
