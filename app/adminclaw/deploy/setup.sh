#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Enroll CetiAdmin as a suspended governed Agent. The caller creates the
# referenced Team, admin AgentPolicy, and Sandbox first. This script does not
# create the protected runtime namespace, credentials, RBAC, or workload input.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for command_name in kubectl envsubst; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "ERROR: ${command_name} is required but not installed." >&2
    exit 1
  fi
done

required_variables=(
  ADMIN_TEAM
  ADMIN_SANDBOX
  ADMIN_OWNER_TENANT_ID
  ADMIN_OWNER_OBJECT_ID
  ADMIN_MODEL
)
for variable_name in "${required_variables[@]}"; do
  if [[ -z "${!variable_name:-}" ]]; then
    echo "ERROR: ${variable_name} is required." >&2
    exit 1
  fi
done

uuid_pattern='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
if [[ ! "${ADMIN_OWNER_TENANT_ID}" =~ ${uuid_pattern} ]] ||
   [[ ! "${ADMIN_OWNER_OBJECT_ID}" =~ ${uuid_pattern} ]]; then
  echo "ERROR: ADMIN_OWNER_TENANT_ID and ADMIN_OWNER_OBJECT_ID must be UUIDs." >&2
  exit 1
fi

ADMIN_GATEWAY_BUDGET_USD="${ADMIN_GATEWAY_BUDGET_USD:-20.00}"
ADMIN_AUTHORIZED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "Enrolling suspended governed CetiAdmin."
echo "Team: ${ADMIN_TEAM}; Sandbox: ${ADMIN_SANDBOX}; Model: ${ADMIN_MODEL}"

export ADMIN_TEAM ADMIN_SANDBOX ADMIN_MODEL
export ADMIN_OWNER_TENANT_ID ADMIN_OWNER_OBJECT_ID ADMIN_GATEWAY_BUDGET_USD
export ADMIN_AUTHORIZED_AT
envsubst '${ADMIN_TEAM} ${ADMIN_SANDBOX} ${ADMIN_MODEL} ${ADMIN_OWNER_TENANT_ID} ${ADMIN_OWNER_OBJECT_ID} ${ADMIN_GATEWAY_BUDGET_USD} ${ADMIN_AUTHORIZED_AT}' \
  < "${SCRIPT_DIR}/agent-admin.yaml" | kubectl apply -f -

echo "CetiAdmin Agent submitted in suspended state."
echo "Do not resume it until the typed Portal-client Sandbox entitlement and qualified execution adapter are installed."
