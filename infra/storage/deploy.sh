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
python3 "${SCRIPT_DIR}/../../scripts/bicep_inputs.py" \
  --template "$BICEP_FILE" --parameters "$PARAM_FILE" --output-dir "$DEPLOYMENT_INPUT_DIR"
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
STORAGE_BACKEND="${STORAGE_BACKEND:-blob}"
SHARE_NAME="${SHARE_NAME:-aks-skills}"
SHARE_QUOTA_GIB="${SHARE_QUOTA_GIB:-100}"
case "$STORAGE_BACKEND" in
  blob|azure-files-nfs) ;;
  *) echo "ERROR: STORAGE_BACKEND must be blob or azure-files-nfs." >&2; exit 1 ;;
esac
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
if [[ "$STORAGE_BACKEND" == azure-files-nfs ]]; then
  if [[ "$PUBLIC_NETWORK_ACCESS" != Disabled ]]; then
    echo "ERROR: azure-files-nfs requires PUBLIC_NETWORK_ACCESS=Disabled." >&2
    exit 1
  fi
  if [[ -z "$PRIVATE_ENDPOINT_SUBNET_ID" || -z "$PRIVATE_DNS_ZONE_ID" ]]; then
    echo "ERROR: azure-files-nfs requires PRIVATE_ENDPOINT_SUBNET_ID and PRIVATE_DNS_ZONE_ID." >&2
    exit 1
  fi
  if [[ "$PRIVATE_DNS_ZONE_ID" != *privatelink.file.core.windows.net* ]]; then
    echo "ERROR: azure-files-nfs requires the privatelink.file.core.windows.net private DNS zone." >&2
    exit 1
  fi
  python3 - "$PRIVATE_DNS_ZONE_ID" <<'PYDNS'
import re, sys
if not re.fullmatch(
    r'/subscriptions/[^/\s]+/resourceGroups/[^/\s]+/providers/Microsoft\.Network/privateDnsZones/privatelink\.file\.core\.windows\.net',
    sys.argv[1], re.IGNORECASE):
    raise SystemExit('Error: PRIVATE_DNS_ZONE_ID must be the exact privatelink.file.core.windows.net private DNS zone ARM ID.')
PYDNS
  if ! [[ "$SHARE_QUOTA_GIB" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: SHARE_QUOTA_GIB must be a positive integer." >&2
    exit 1
  fi
fi
if [[ "$PUBLIC_NETWORK_ACCESS" == Disabled && -z "$PRIVATE_ENDPOINT_SUBNET_ID" ]]; then
  echo "ERROR: PUBLIC_NETWORK_ACCESS=Disabled requires PRIVATE_ENDPOINT_SUBNET_ID: without a private endpoint, no client can reach the account." >&2
  exit 1
fi

PARAMETERS=(
  --parameters "@${DEPLOYMENT_INPUT_DIR}/parameters.json"
  --parameters publicNetworkAccess="$PUBLIC_NETWORK_ACCESS"
  --parameters storageBackend="$STORAGE_BACKEND"
)

if [[ "$STORAGE_BACKEND" == azure-files-nfs ]]; then
  PARAMETERS+=(--parameters shareName="$SHARE_NAME" shareQuotaGiB="$SHARE_QUOTA_GIB")
fi

if [[ -n "$PRIVATE_ENDPOINT_SUBNET_ID" ]]; then
  echo "Using a private endpoint in subnet: $PRIVATE_ENDPOINT_SUBNET_ID"
  python3 - "$PRIVATE_ENDPOINT_SUBNET_ID" "$AZURE_SUBSCRIPTION_ID" <<'PYSUBNET'
import re, sys
match = re.fullmatch(r'/subscriptions/([^/\s]+)/resourceGroups/([^/\s]+)/providers/Microsoft\.Network/virtualNetworks/([^/\s]+)/subnets/([^/\s]+)', sys.argv[1], re.IGNORECASE)
if not match or match[1].lower() != sys.argv[2].lower():
    raise SystemExit('Error: PRIVATE_ENDPOINT_SUBNET_ID must be a subnet ARM ID in the explicit target subscription.')
PYSUBNET
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

SHARE_OUTPUT="$(az_target deployment group show \
  --resource-group "$RG_NAME" \
  --name "$DEPLOYMENT_NAME" \
  --query 'properties.outputs.shareName.value' -o tsv)"

# Verify
az_target storage account show --name "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" -o table
# The workstation is outside the NFS subnet allowlist even when public network
# access is Enabled. Verify the container through ARM, not the blocked data plane.
if [[ "$STORAGE_BACKEND" == azure-files-nfs ]]; then
  # Verify the security properties through the control plane. No account keys
  # or data-plane credentials are requested.
  verify_value() {
    local label="$1" expected="$2" actual="$3"
    if [[ "$actual" != "$expected" ]]; then
      echo "ERROR: $label must be '$expected'; got '$actual'." >&2
      exit 1
    fi
  }
  ACCOUNT_ID="$(az_target storage account show --name "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" --query id -o tsv)"
  ARM_API_VERSION="2025-01-01"
  HTTPS_ONLY="$(az_target resource show --ids "$ACCOUNT_ID" --api-version "$ARM_API_VERSION" --query 'properties.supportsHttpsTrafficOnly' -o tsv)"
  PUBLIC_ACCESS="$(az_target resource show --ids "$ACCOUNT_ID" --api-version "$ARM_API_VERSION" --query 'properties.publicNetworkAccess' -o tsv)"
  SHARED_KEY="$(az_target resource show --ids "$ACCOUNT_ID" --api-version "$ARM_API_VERSION" --query 'properties.allowSharedKeyAccess' -o tsv)"
  verify_value "supportsHttpsTrafficOnly" true "$HTTPS_ONLY"
  verify_value "publicNetworkAccess" Disabled "$PUBLIC_ACCESS"
  verify_value "allowSharedKeyAccess" false "$SHARED_KEY"
  NFS_ENCRYPTION="$(az_target resource show --ids "${ACCOUNT_ID}/fileServices/default" --api-version "$ARM_API_VERSION" --query 'properties.protocolSettings.nfs.encryptionInTransit.required' -o tsv)"
  verify_value "NFS encryptionInTransit.required" true "$NFS_ENCRYPTION"
  SHARE_ROOT_SQUASH="$(az_target storage share-rm show --name "$SHARE_OUTPUT" --storage-account "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" --query 'rootSquash' -o tsv)"
  SHARE_PROTOCOL="$(az_target storage share-rm show --name "$SHARE_OUTPUT" --storage-account "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" --query 'enabledProtocols' -o tsv)"
  verify_value "share rootSquash" RootSquash "$SHARE_ROOT_SQUASH"
  verify_value "share enabledProtocols" NFS "$SHARE_PROTOCOL"
else
  az_target storage container-rm show --name "$CONTAINER_NAME" --storage-account "$STORAGE_ACCOUNT" --resource-group "$RG_NAME" -o table
fi

echo ""
echo "=== Deployment Summary ==="
echo "Storage Account: $STORAGE_ACCOUNT"
if [[ "$STORAGE_BACKEND" == azure-files-nfs ]]; then
  echo "Backend:         Azure Files NFS (TLS in transit)"
  echo "Share:           $SHARE_OUTPUT"
else
  echo "Backend:         Blob NFS"
  echo "Container:       $CONTAINER_NAME"
  echo "Blob Endpoint:   $BLOB_ENDPOINT"
fi
echo "Resource Group:  $RG_NAME"
