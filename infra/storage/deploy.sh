#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BICEP_FILE="${SCRIPT_DIR}/main.bicep"
PARAM_FILE="${PARAM_FILE:-${SCRIPT_DIR}/main.bicepparam}"
DEPLOYMENT_NAME="cetirun-storage-$(date +%Y%m%d%H%M%S)"

RG_NAME="${RESOURCE_GROUP:?RESOURCE_GROUP is required}"
AKS_CLUSTER_NAME="${AKS_CLUSTER_NAME:?AKS_CLUSTER_NAME is required}"
AZURE_SUBSCRIPTION_ID="${AZURE_SUBSCRIPTION_ID:?AZURE_SUBSCRIPTION_ID is required}"
AZURE_TENANT_ID="${AZURE_TENANT_ID:?AZURE_TENANT_ID is required}"
export AZURE_SUBSCRIPTION_ID AZURE_TENANT_ID
# Compile the exact inputs before changing the subnet. The Azure CLI then uses
# only JSON snapshots, so it cannot download or select a different Bicep tool.
DEPLOYMENT_INPUT_DIR="$(mktemp -d)"
trap 'rm -rf -- "$DEPLOYMENT_INPUT_DIR"' EXIT
python3 - "$BICEP_FILE" "$PARAM_FILE" "$DEPLOYMENT_INPUT_DIR" <<'PYINPUTS'
import json, pathlib, subprocess, sys
source, parameters, destination = map(pathlib.Path, sys.argv[1:])
def compile_bicep(*args):
    return json.loads(subprocess.check_output(['bicep', *map(str, args), '--stdout', '--no-restore'], text=True))
if parameters.suffix == '.bicepparam':
    bundle = compile_bicep('build-params', parameters, '--bicep-file', source)
    template = json.loads(bundle['templateJson'])
    values = json.loads(bundle['parametersJson'])
else:
    template = compile_bicep('build', source)
    values = json.loads(parameters.read_text())
if not isinstance(values.get('parameters'), dict):
    raise SystemExit('Error: storage parameters must contain a parameters object.')
for name, document in [('template', template), ('parameters', values)]:
    (destination / (name + '.json')).write_text(json.dumps(document))
PYINPUTS
python3 - <<'PYTARGET'
import json, os, subprocess
account = json.loads(subprocess.check_output([
    'az', 'account', 'show', '--subscription', os.environ['AZURE_SUBSCRIPTION_ID'], '-o', 'json']))
if (account.get('id', '').lower() != os.environ['AZURE_SUBSCRIPTION_ID'].lower()
        or account.get('tenantId', '').lower() != os.environ['AZURE_TENANT_ID'].lower()):
    raise SystemExit('Error: Azure subscription/tenant does not match the explicit target.')
PYTARGET
az_target() {
  az "$@" --subscription "$AZURE_SUBSCRIPTION_ID"
}

# Optional private endpoint for the blob service. With one, the account takes no
# subnet service-endpoint rule from this helper.
PRIVATE_ENDPOINT_SUBNET_ID="${PRIVATE_ENDPOINT_SUBNET_ID:-}"
PRIVATE_DNS_ZONE_ID="${PRIVATE_DNS_ZONE_ID:-}"
PUBLIC_NETWORK_ACCESS="${PUBLIC_NETWORK_ACCESS:-Enabled}"
case "$PUBLIC_NETWORK_ACCESS" in
  Enabled|Disabled) ;;
  *)
    echo "ERROR: PUBLIC_NETWORK_ACCESS must be Enabled or Disabled." >&2
    exit 1
    ;;
esac
if [[ -n "$PRIVATE_DNS_ZONE_ID" && -z "$PRIVATE_ENDPOINT_SUBNET_ID" ]]; then
  echo "ERROR: PRIVATE_DNS_ZONE_ID requires PRIVATE_ENDPOINT_SUBNET_ID." >&2
  exit 1
fi
if [[ "$PUBLIC_NETWORK_ACCESS" == Disabled && -z "$PRIVATE_ENDPOINT_SUBNET_ID" ]]; then
  echo "ERROR: PUBLIC_NETWORK_ACCESS=Disabled requires PRIVATE_ENDPOINT_SUBNET_ID: without a private endpoint, no client can reach the account." >&2
  exit 1
fi

PARAMETERS=(
  --parameters "@${DEPLOYMENT_INPUT_DIR}/parameters.json"
  --parameters publicNetworkAccess="$PUBLIC_NETWORK_ACCESS"
)

if [[ -n "$PRIVATE_ENDPOINT_SUBNET_ID" ]]; then
  echo "Using a private endpoint in subnet: $PRIVATE_ENDPOINT_SUBNET_ID"
  PARAMETERS+=(--parameters privateEndpointSubnetId="$PRIVATE_ENDPOINT_SUBNET_ID")
  if [[ -n "$PRIVATE_DNS_ZONE_ID" ]]; then
    PARAMETERS+=(--parameters privateDnsZoneId="$PRIVATE_DNS_ZONE_ID")
  fi
else
  # Resolve AKS node subnet ID
  if [[ -z "${AKS_SUBNET_ID:-}" ]]; then
    echo "AKS_SUBNET_ID not set, detecting from cluster '$AKS_CLUSTER_NAME'..."

    # Try agentPoolProfiles first (works for custom VNet clusters)
    AKS_SUBNET_ID="$(az_target aks show \
      --resource-group "$RG_NAME" \
      --name "$AKS_CLUSTER_NAME" \
      --query 'agentPoolProfiles[0].vnetSubnetId' -o tsv 2>/dev/null)"

    # Fallback for CNI Overlay: find subnet in node resource group
    if [[ -z "$AKS_SUBNET_ID" || "$AKS_SUBNET_ID" == "None" || "$AKS_SUBNET_ID" == "null" ]]; then
      echo "vnetSubnetId not set (CNI Overlay), looking up from node resource group..."
      NODE_RG="$(az_target aks show -g "$RG_NAME" -n "$AKS_CLUSTER_NAME" --query nodeResourceGroup -o tsv 2>/dev/null)"
      AKS_SUBNET_ID="$(az_target network vnet list -g "$NODE_RG" \
        --query '[0].subnets[?name==`aks-subnet`].id | [0]' -o tsv 2>/dev/null)"
    fi

    if [[ -z "$AKS_SUBNET_ID" || "$AKS_SUBNET_ID" == "None" ]]; then
      echo "ERROR: Could not detect subnet ID. Set AKS_SUBNET_ID manually." >&2
      exit 1
    fi
    echo "Detected subnet: $AKS_SUBNET_ID"
  fi

  # Validate the full ARM identity and use the same ID for the subnet update
  # and storage deployment. Do not reinterpret it as separate CLI name arguments.
  python3 - "$AKS_SUBNET_ID" "$AZURE_SUBSCRIPTION_ID" <<'PYSUBNET'
import re, sys
match = re.fullmatch(r'/subscriptions/([^/\s]+)/resourceGroups/([^/\s]+)/providers/Microsoft\.Network/virtualNetworks/([^/\s]+)/subnets/([^/\s]+)', sys.argv[1], re.IGNORECASE)
if not match or match[1].lower() != sys.argv[2].lower():
    raise SystemExit('Error: AKS_SUBNET_ID must be a subnet ARM ID in the explicit target subscription.')
PYSUBNET

  # Ensure Microsoft.Storage service endpoint is enabled on the subnet (required for NFS v3)
  echo "Ensuring Microsoft.Storage service endpoint on subnet..."
  az network vnet subnet update \
    --ids "$AKS_SUBNET_ID" \
    --service-endpoints Microsoft.Storage \
    -o none

  PARAMETERS+=(--parameters allowedSubnetIds="[\"${AKS_SUBNET_ID}\"]")
fi

# Validate
az_target deployment group validate \
  --resource-group "$RG_NAME" \
  --template-file "${DEPLOYMENT_INPUT_DIR}/template.json" \
  "${PARAMETERS[@]}" \
  --name "${DEPLOYMENT_NAME}-validate"

# Deploy
az_target deployment group create \
  --resource-group "$RG_NAME" \
  --template-file "${DEPLOYMENT_INPUT_DIR}/template.json" \
  "${PARAMETERS[@]}" \
  --name "$DEPLOYMENT_NAME"

# Read outputs
STORAGE_ACCOUNT="$(az_target deployment group show \
  --resource-group "$RG_NAME" \
  --name "$DEPLOYMENT_NAME" \
  --query 'properties.outputs.storageAccountName.value' -o tsv)"

CONTAINER_NAME="$(az_target deployment group show \
  --resource-group "$RG_NAME" \
  --name "$DEPLOYMENT_NAME" \
  --query 'properties.outputs.containerName.value' -o tsv)"

BLOB_ENDPOINT="$(az_target deployment group show \
  --resource-group "$RG_NAME" \
  --name "$DEPLOYMENT_NAME" \
  --query 'properties.outputs.primaryBlobEndpoint.value' -o tsv)"

# Verify
az_target storage account show --name "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" -o table
# The workstation is outside the NFS subnet allowlist even when public network
# access is Enabled. Verify the container through ARM, not the blocked data plane.
az_target storage container-rm show --name "$CONTAINER_NAME" --storage-account "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" -o table

echo ""
echo "=== Deployment Summary ==="
echo "Storage Account: $STORAGE_ACCOUNT"
echo "Container:       $CONTAINER_NAME"
echo "Blob Endpoint:   $BLOB_ENDPOINT"
echo "Resource Group:  $RG_NAME"
