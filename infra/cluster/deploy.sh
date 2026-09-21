#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BICEP_FILE="${SCRIPT_DIR}/main.bicep"
PARAM_FILE="${PARAM_FILE:-${SCRIPT_DIR}/main.bicepparam}"
DEPLOYMENT_NAME="cetirun-cluster-$(date +%Y%m%d%H%M%S)"
# Set to true only for an explicitly authorized bootstrap environment where
# local AKS admin accounts remain enabled. Default user credentials preserve
# the normal Azure RBAC path.
AKS_ADMIN_CREDENTIALS="${AKS_ADMIN_CREDENTIALS:-false}"

RG_NAME="${RESOURCE_GROUP:?RESOURCE_GROUP is required}"
LOCATION="${LOCATION:?LOCATION is required}"

# Pre-flight: ensure aks-preview extension is installed and up-to-date
# --enable-gateway-api requires aks-preview >= 19.0.0b4
az extension add --name aks-preview --upgrade --yes 2>/dev/null \
  || { echo "Error: failed to install/update aks-preview extension"; exit 1; }

# Pre-flight: register Gateway API feature flag if needed
GATEWAY_STATE="$(az feature show --namespace Microsoft.ContainerService \
  --name ManagedGatewayAPIPreview --query 'properties.state' -o tsv 2>/dev/null || echo 'NotRegistered')"
if [[ "$GATEWAY_STATE" != "Registered" ]]; then
  echo "Registering ManagedGatewayAPIPreview feature flag..."
  az feature register --namespace Microsoft.ContainerService --name ManagedGatewayAPIPreview
  az provider register --namespace Microsoft.ContainerService
  echo "Waiting for feature registration..."
  while [[ "$(az feature show --namespace Microsoft.ContainerService \
    --name ManagedGatewayAPIPreview --query 'properties.state' -o tsv)" != "Registered" ]]; do
    sleep 30
  done
fi

# Create resource group (use existing location if RG already exists)
RG_LOCATION=$(az group show --name "$RG_NAME" --query location -o tsv 2>/dev/null || echo "$LOCATION")
az group create --name "$RG_NAME" --location "$RG_LOCATION" --tags project=cetirun

# Validate
az deployment group validate \
  --resource-group "$RG_NAME" \
  --template-file "$BICEP_FILE" \
  --parameters "$PARAM_FILE" \
  --name "${DEPLOYMENT_NAME}-validate"

# Deploy
az deployment group create \
  --resource-group "$RG_NAME" \
  --template-file "$BICEP_FILE" \
  --parameters "$PARAM_FILE" \
  --name "$DEPLOYMENT_NAME"

# Read cluster name from outputs
CLUSTER_NAME="$(az deployment group show \
  --resource-group "$RG_NAME" \
  --name "$DEPLOYMENT_NAME" \
  --query 'properties.outputs.clusterName.value' -o tsv)"

# Enable Gateway API
az aks update \
  --resource-group "$RG_NAME" \
  --name "$CLUSTER_NAME" \
  --enable-gateway-api \
  --yes

# Fetch kubeconfig
credential_args=()
if [[ "${AKS_ADMIN_CREDENTIALS}" == "true" ]]; then
  credential_args+=(--admin)
fi
az aks get-credentials \
  --resource-group "$RG_NAME" \
  --name "$CLUSTER_NAME" \
  "${credential_args[@]}" \
  --overwrite-existing

# Verify
kubectl get nodes -o wide
kubectl get pods -n aks-istio-system
kubectl get pods -n aks-istio-ingress
az aks show --resource-group "$RG_NAME" --name "$CLUSTER_NAME" \
  --query 'oidcIssuerProfile.issuerURL' -o tsv
