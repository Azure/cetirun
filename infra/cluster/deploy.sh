#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BICEP_FILE="${SCRIPT_DIR}/main.bicep"
PARAM_FILE="${PARAM_FILE:-${SCRIPT_DIR}/main.bicepparam}"
DEPLOYMENT_NAME="${DEPLOYMENT_NAME:-cetirun-cluster-$(date +%Y%m%d%H%M%S)}"
# Set to true only for an explicitly authorized bootstrap environment where
# local AKS admin accounts remain enabled. Default user credentials preserve
# the normal Azure RBAC path.
AKS_ADMIN_CREDENTIALS="${AKS_ADMIN_CREDENTIALS:-false}"

RG_NAME="${RESOURCE_GROUP:?RESOURCE_GROUP is required}"
LOCATION="${LOCATION:?LOCATION is required}"
AZURE_SUBSCRIPTION_ID="${AZURE_SUBSCRIPTION_ID:?AZURE_SUBSCRIPTION_ID is required}"
AZURE_TENANT_ID="${AZURE_TENANT_ID:?AZURE_TENANT_ID is required}"
export KUBECONFIG="${KUBECONFIG:?KUBECONFIG must name a private absolute file}"
export AZURE_SUBSCRIPTION_ID AZURE_TENANT_ID
GATEWAY_REGISTRATION_TIMEOUT="${GATEWAY_REGISTRATION_TIMEOUT:-900}"
if [[ ! "$GATEWAY_REGISTRATION_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
  echo "Error: GATEWAY_REGISTRATION_TIMEOUT must be a positive number of seconds." >&2
  exit 1
fi
python3 - <<'PYCONFIG'
import os
from pathlib import Path
p = Path(os.environ['KUBECONFIG'])
default = Path.home() / '.kube/config'
if (not p.is_absolute() or ':' in str(p) or p.is_symlink()
        or p.resolve() == default.resolve()
        or (p.exists() and default.exists() and p.samefile(default))
        or not p.parent.is_dir()
        or (p.exists() and not p.is_file())):
    raise SystemExit('Error: KUBECONFIG must name one private absolute file outside the default kubeconfig.')
if p.exists():
    p.chmod(0o600)
PYCONFIG

# Never select a global subscription. Every resource call carries this target.
az_target() {
  az "$@" --subscription "$AZURE_SUBSCRIPTION_ID"
}

# Check the effective version before extension, feature, or resource writes.
# Deploy these same compiled inputs even if the source changes while waiting
# for Azure feature registration. The private directory can contain secrets.
DEPLOYMENT_INPUT_DIR="$(mktemp -d)"
trap 'rm -rf -- "$DEPLOYMENT_INPUT_DIR"' EXIT
python3 "${SCRIPT_DIR}/../../scripts/validate-cluster-kubernetes.py" \
  --template "$BICEP_FILE" --parameters "$PARAM_FILE" --output-dir "$DEPLOYMENT_INPUT_DIR"

AGENT_SETTINGS_NAME="container-azm-ms-agentconfig"
AGENT_SETTINGS_FILE="${SCRIPT_DIR}/${AGENT_SETTINGS_NAME}.yaml"

# agent_setting_is SECTION VALUE: succeeds when the Container Insights agent
# settings on stdin set "enabled = VALUE" in [SECTION].
agent_setting_is() {
  awk -v want_section="$1" -v want_value="$2" '
    /^[[:space:]]*\[/ { section = $0; gsub(/[][[:space:]]/, "", section); next }
    section == want_section && /^[[:space:]]*enabled[[:space:]]*=/ {
      value = $0
      sub(/^[^=]*=[[:space:]]*/, "", value)
      sub(/[[:space:]]*(#.*)?$/, "", value)
      found = (value == want_value)
    }
    END { exit found ? 0 : 1 }
  '
}

# Read-only target and tool checks precede every Azure mutation.
python3 - <<'PYTARGET'
import json, os, subprocess
account = json.loads(subprocess.check_output([
    'az', 'account', 'show', '--subscription', os.environ['AZURE_SUBSCRIPTION_ID'], '-o', 'json']))
if (account.get('id', '').lower() != os.environ['AZURE_SUBSCRIPTION_ID'].lower()
        or account.get('tenantId', '').lower() != os.environ['AZURE_TENANT_ID'].lower()):
    raise SystemExit('Error: Azure subscription/tenant does not match the explicit target.')
PYTARGET
# Gateway API support must already be installed. Capability detection also
# supports Azure CLI installations whose extension metadata omits a version.
if ! aks_update_help="$(az aks update --help)" || [[ "$aks_update_help" != *"--enable-gateway-api"* ]]; then
  echo "Error: install a reviewed, pinned Azure CLI/aks-preview with --enable-gateway-api support before deployment." >&2
  exit 1
fi

GATEWAY_STATE="$(az_target feature show --namespace Microsoft.ContainerService \
  --name ManagedGatewayAPIPreview --query 'properties.state' -o tsv)"
if [[ "$GATEWAY_STATE" != "Registered" ]]; then
  echo "Registering ManagedGatewayAPIPreview feature flag..."
  az_target feature register --namespace Microsoft.ContainerService --name ManagedGatewayAPIPreview -o none
  registration_started=$SECONDS
  while [[ "$(az_target feature show --namespace Microsoft.ContainerService \
    --name ManagedGatewayAPIPreview --query 'properties.state' -o tsv)" != "Registered" ]]; do
    if (( SECONDS - registration_started >= GATEWAY_REGISTRATION_TIMEOUT )); then
      echo "Error: feature registration is still pending; inspect its state before rerunning." >&2
      exit 1
    fi
    sleep 15
  done
fi
# Registration can still need propagation when a prior run stopped just after
# the feature became Registered. Re-register idempotently on every run and
# wait for the provider before resource deployment.
az_target provider register --namespace Microsoft.ContainerService -o none
registration_started=$SECONDS
while [[ "$(az_target provider show --namespace Microsoft.ContainerService --query registrationState -o tsv)" != "Registered" ]]; do
  if (( SECONDS - registration_started >= GATEWAY_REGISTRATION_TIMEOUT )); then
    echo "Error: provider registration is still pending; inspect its state before rerunning." >&2
    exit 1
  fi
  sleep 15
done

# Create resource group (use existing location if RG already exists)
RG_LOCATION=$(az_target group show --name "$RG_NAME" --query location -o tsv 2>/dev/null || echo "$LOCATION")
az_target group create --name "$RG_NAME" --location "$RG_LOCATION" --tags project=cetirun

# Validate
az_target deployment group validate \
  --resource-group "$RG_NAME" \
  --template-file "${DEPLOYMENT_INPUT_DIR}/template.json" \
  --parameters "@${DEPLOYMENT_INPUT_DIR}/parameters.json" \
  --name "${DEPLOYMENT_NAME}-validate"

# Deploy
az_target deployment group create \
  --resource-group "$RG_NAME" \
  --template-file "${DEPLOYMENT_INPUT_DIR}/template.json" \
  --parameters "@${DEPLOYMENT_INPUT_DIR}/parameters.json" \
  --name "$DEPLOYMENT_NAME"

# Read cluster name from outputs
CLUSTER_NAME="$(az_target deployment group show \
  --resource-group "$RG_NAME" \
  --name "$DEPLOYMENT_NAME" \
  --query 'properties.outputs.clusterName.value' -o tsv)"

# Fetch kubeconfig
credential_args=()
if [[ "${AKS_ADMIN_CREDENTIALS}" == "true" ]]; then
  credential_args+=(--admin)
fi
az_target aks get-credentials \
  --resource-group "$RG_NAME" \
  --name "$CLUSTER_NAME" \
  "${credential_args[@]}" \
  --file "$KUBECONFIG" \
  --overwrite-existing
chmod 600 "$KUBECONFIG"

# Verify the credentials resolve to this exact AKS before the first API write.
python3 - "$RG_NAME" "$CLUSTER_NAME" <<'PYCLUSTER'
import json, os, subprocess, sys
from urllib.parse import urlsplit
cluster = json.loads(subprocess.check_output([
    'az', 'aks', 'show', '--resource-group', sys.argv[1], '--name', sys.argv[2],
    '--subscription', os.environ['AZURE_SUBSCRIPTION_ID'], '-o', 'json']))
expected = f"/subscriptions/{os.environ['AZURE_SUBSCRIPTION_ID']}/resourceGroups/{sys.argv[1]}/providers/Microsoft.ContainerService/managedClusters/{sys.argv[2]}"
config = json.loads(subprocess.check_output(['kubectl', 'config', 'view', '--minify', '-o', 'json']))
server = urlsplit(config['clusters'][0]['cluster']['server'])
hosts = {cluster.get('fqdn'), cluster.get('privateFqdn')}
if cluster.get('id', '').lower() != expected.lower() or server.scheme != 'https' or server.hostname not in hosts:
    raise SystemExit('Error: kubeconfig does not match the explicit AKS target.')
PYCLUSTER

# Container Insights agent settings, in place before anything else runs on the
# cluster. The cluster template turns Container Insights off, and ADX-Mon
# collects the logs. These settings are for a cluster where it is turned on
# anyway, by hand or by another tool: its agent then does not collect
# containers' environment variables, which hold credentials, and it joins log
# lines the container runtime split.
#
# A cluster that already has agent settings keeps them. They are checked, not
# replaced: replacing them would drop the cluster's own collection settings,
# such as namespaces it excludes, for every workload.
if ! existing_settings="$(kubectl get configmap "${AGENT_SETTINGS_NAME}" -n kube-system --ignore-not-found -o name)"; then
  echo "Error: could not read the Container Insights agent settings in kube-system." >&2
  exit 1
fi
if [[ -z "${existing_settings}" ]]; then
  if ! kubectl create -f "${AGENT_SETTINGS_FILE}"; then
    echo "Error: could not create the Container Insights agent settings in kube-system." >&2
    echo "Until they exist, a Container Insights agent that is turned on collects every container's environment, credentials included." >&2
    echo "Create them from ${AGENT_SETTINGS_FILE} with an identity that can write to kube-system before installing CetiRun." >&2
    exit 1
  fi
else
  if ! log_settings="$(kubectl get configmap "${AGENT_SETTINGS_NAME}" -n kube-system \
    -o 'jsonpath={.data.log-data-collection-settings}')"; then
    echo "Error: could not read kube-system/${AGENT_SETTINGS_NAME}." >&2
    exit 1
  fi
  if ! agent_setting_is log_collection_settings.env_var false <<< "${log_settings}"; then
    echo "Error: kube-system/${AGENT_SETTINGS_NAME} already exists and does not turn off environment variable collection." >&2
    echo "It was left unchanged, since replacing it would drop the cluster's own collection settings." >&2
    echo "Add these settings to its log-data-collection-settings, as in ${AGENT_SETTINGS_FILE}, then run this script again:" >&2
    echo "  [log_collection_settings.env_var]" >&2
    echo "     enabled = false" >&2
    echo "  [log_collection_settings.enable_multiline_logs]" >&2
    echo "     enabled = true" >&2
    exit 1
  fi
  echo "kube-system/${AGENT_SETTINGS_NAME} already exists and turns off environment variable collection; left unchanged."
  if ! agent_setting_is log_collection_settings.enable_multiline_logs true <<< "${log_settings}"; then
    echo "Note: it does not turn on multiline logs, so lines longer than 16 KiB are stored in pieces." >&2
  fi
fi

# Enable Gateway API
az_target aks update \
  --resource-group "$RG_NAME" \
  --name "$CLUSTER_NAME" \
  --enable-gateway-api \
  --yes

# Verify
kubectl get nodes -o wide
kubectl get pods -n aks-istio-system
kubectl get pods -n aks-istio-ingress
# The Azure CLI spells the property issuerUrl; the ARM name in the Bicep
# template is issuerURL. JMESPath is case-sensitive, so the ARM spelling
# prints an empty line.
az_target aks show --resource-group "$RG_NAME" --name "$CLUSTER_NAME" \
  --query 'oidcIssuerProfile.issuerUrl' -o tsv
