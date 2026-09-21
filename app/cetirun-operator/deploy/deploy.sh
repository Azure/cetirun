#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# deploy.sh — One-command deploy for CetiRun Operator.
#
# Checks cluster-level prerequisites (StorageClass + Istio Gateway) and then
# installs the operator from the OCI chart published in ACR. The chart installs
# CetiRun CRDs on its first installation.
# Every step is idempotent and safe to re-run.
#
# Requires: kubectl and helm configured to target the correct cluster,
#           with pull access to the ACR registry.
#
# Environment variables:
#   REGISTRY            — OCI registry          (default: cetirun.azurecr.io)
#   CHART_VERSION       — Chart version          (default: see below)
#   NAMESPACE           — Target namespace       (default: cetirun-system)
#   RELEASE_NAME        — Helm release name      (default: cetirun-operator)
#   LOCAL_CHART         — If non-empty, use local chart at ../chart instead of OCI
#   IMAGE_REPO          — Override image.repository Helm value
#   IMAGE_TAG           — Override image.tag Helm value
#   IMAGE_DIGEST        — Override image.digest Helm value (e.g. "sha256:abc123...")
#   DOMAIN              — Override domainSuffix Helm value (e.g. "example.com")
#   ISTIO_GATEWAY        — Override the instance VirtualService gateway as namespace/name
#   CETIRUN_REPO_URL    — Override cetirun.chart.repoURL (default: oci://$REGISTRY/helm/cetirun)
#   CETIRUN_CHART_VERSION — Override cetirun.chart.version (the agent chart the operator deploys)
#   HELM_SET_ARGS       — Space-separated extra key=value pairs passed as --set flags
#                         (e.g. "cetirun.chart.version=0.1.0 image.tag=latest")

set -euo pipefail

for cmd in kubectl helm; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: ${cmd} is not installed or not found in PATH." >&2
    exit 1
  fi
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# OCI chart / deploy defaults (override via environment variables)
REGISTRY="${REGISTRY:-cetirun.azurecr.io}"
CHART_REF="oci://${REGISTRY}/helm/cetirun-operator"
CHART_VERSION="${CHART_VERSION:-0.1.0}"
NAMESPACE="${NAMESPACE:-cetirun-system}"
RELEASE_NAME="${RELEASE_NAME:-cetirun-operator}"
IMAGE_REPO="${IMAGE_REPO:-${REGISTRY}/cetirun-operator}"
CETIRUN_REPO_URL="${CETIRUN_REPO_URL:-oci://${REGISTRY}/helm/cetirun}"
CETIRUN_CHART_VERSION="${CETIRUN_CHART_VERSION:-}"
ISTIO_GATEWAY="${ISTIO_GATEWAY:-aks-istio-ingress/cetirun-gateway}"
ISTIO_GATEWAY_NAMESPACE="${ISTIO_GATEWAY%%/*}"
ISTIO_GATEWAY_NAME="${ISTIO_GATEWAY#*/}"

if [ "${ISTIO_GATEWAY_NAMESPACE}" = "${ISTIO_GATEWAY}" ] || [ -z "${ISTIO_GATEWAY_NAME}" ]; then
  echo "ERROR: ISTIO_GATEWAY must use namespace/name format." >&2
  exit 1
fi

echo "==> Checking cluster connectivity..."
kubectl cluster-info --request-timeout=5s > /dev/null

# ── StorageClass ──────────────────────────────────────────────────────────────
echo ""
if kubectl get storageclass azureblob-nfs-premium &>/dev/null; then
  echo "==> StorageClass azureblob-nfs-premium already exists — skipping."
else
  echo "ERROR: StorageClass azureblob-nfs-premium not found."
  echo "The Azure Blob CSI driver must be enabled on the AKS cluster:"
  echo ""
  echo "    az aks update -g <resource-group> -n <cluster-name> --enable-blob-driver"
  echo ""
  echo "Once enabled, AKS will automatically create the azureblob-nfs-premium StorageClass."
  exit 1
fi

# ── Istio prerequisite check ──────────────────────────────────────────────────
echo ""
echo "==> Checking Istio prerequisites..."
if ! kubectl get crd virtualservices.networking.istio.io &>/dev/null; then
  echo "ERROR: Istio CRDs not found. Install Istio first:"
  echo ""
  echo "    az aks mesh enable --name <cluster> --resource-group <rg> --revision asm-1-27"
  echo "    az aks mesh enable-ingress-gateway --name <cluster> --resource-group <rg> --ingress-gateway-type External"
  echo ""
  exit 1
fi
if ! kubectl get gateway.networking.istio.io "${ISTIO_GATEWAY_NAME}" -n "${ISTIO_GATEWAY_NAMESPACE}" &>/dev/null; then
  echo "ERROR: ${ISTIO_GATEWAY} Gateway not found."
  echo "Deploy shared Istio infrastructure first:"
  echo ""
  echo "    cd infra/istio && ./deploy.sh"
  echo ""
  exit 1
fi
echo "    Istio CRDs and ${ISTIO_GATEWAY} found."

# ── Helm install ─────────────────────────────────────────────────────────────
echo ""
echo "==> Creating namespace ${NAMESPACE} (if not exists)..."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

echo ""
HELM_ARGS=()
if [ -n "${LOCAL_CHART:-}" ]; then
  CHART_SOURCE="$SCRIPT_DIR/../chart"
  echo "==> Installing/upgrading Helm release '${RELEASE_NAME}' from local chart ${CHART_SOURCE}..."
  HELM_ARGS+=("$CHART_SOURCE")
else
  echo "==> Installing/upgrading Helm release '${RELEASE_NAME}' from ${CHART_REF}:${CHART_VERSION}..."
  HELM_ARGS+=("$CHART_REF" --version "$CHART_VERSION")
fi
HELM_ARGS+=(--set "image.repository=${IMAGE_REPO}")
HELM_ARGS+=(--set-string "istioGateway=${ISTIO_GATEWAY}")
HELM_ARGS+=(--set "cetirun.chart.repoURL=${CETIRUN_REPO_URL}")
[ -n "${CETIRUN_CHART_VERSION}" ] && HELM_ARGS+=(--set-string "cetirun.chart.version=${CETIRUN_CHART_VERSION}")
[ -n "${IMAGE_TAG:-}" ]      && HELM_ARGS+=(--set "image.tag=${IMAGE_TAG}")
[ -n "${IMAGE_DIGEST:-}" ]   && HELM_ARGS+=(--set "image.digest=${IMAGE_DIGEST}")
[ -n "${DOMAIN:-}" ]         && HELM_ARGS+=(--set "domainSuffix=${DOMAIN}")
for kv in ${HELM_SET_ARGS:-}; do
  HELM_ARGS+=(--set "$kv")
done
helm upgrade --install "$RELEASE_NAME" "${HELM_ARGS[@]}" \
  --namespace "$NAMESPACE"

echo ""
echo "==> Deploy complete."
