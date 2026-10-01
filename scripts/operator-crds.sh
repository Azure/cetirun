#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Operator CRD handling for the deployment helpers. Sourced by
# scripts/cetirun-platform-deploy.sh and app/cetirun-operator/deploy/deploy.sh;
# it only defines functions.
#
# Helm creates the CRDs under a chart's crds/ directory on the first
# installation only and never upgrades them. The helpers apply them around
# `helm upgrade` in the order the operator's schema upgrade rules require
# (app/cetirun-operator/README.md, "Schema and binary upgrades"):
#   1. Before the upgrade, create the CRDs that do not exist yet, so a new kind
#      is served before the new operator uses it. Nothing running reads a kind
#      that did not exist.
#   2. After the upgrade, wait until every operator replica and, when the
#      release includes Sandbox admission, every admission replica runs the new
#      binary. Only then apply changes to existing CRDs, so admission
#      understands a schema before the API server serves it.
#   3. The callers wait for the Portal last: it takes no part in admission, and
#      a new Portal may write fields only the new schema has.
# A rollout that fails or times out stops the helper before step 2 applies
# anything, so the existing CRDs stay unchanged. Fix the rollout and run the
# helper again; every step can be repeated.

# manifest_has_deployment MANIFEST NAME succeeds when the rendered manifest
# contains a Deployment of that name.
manifest_has_deployment() {
  awk -v name="$2" '
    $0 == "---" { kind = ""; next }
    $1 == "kind:" { kind = $2; next }
    kind == "Deployment" && $1 == "name:" && $2 == name { found = 1; exit }
    END { exit !found }
  ' <<< "$1"
}

# create_missing_crds CRDS creates the CRDs in the CRDS manifest that the
# cluster does not have yet. It leaves existing CRDs untouched.
create_missing_crds() {
  local crds="$1" output=""
  grep -q '^kind: CustomResourceDefinition' <<< "${crds}" || return 0
  if ! output="$(kubectl create -f - <<< "${crds}" 2>&1)"; then
    if grep -v -e 'AlreadyExists' -e ' created$' -e '^Warning:' <<< "${output}" | grep -q .; then
      printf '%s\n' "${output}" >&2
      return 1
    fi
  fi
  grep ' created$' <<< "${output}" || true
}

# finish_crd_upgrade NAMESPACE MANIFEST CRDS FIELD_MANAGER waits for the
# operator and, when MANIFEST renders it, Sandbox admission to finish rolling
# out in NAMESPACE, then applies the CRDS manifest with server-side apply.
finish_crd_upgrade() {
  local namespace="$1" manifest="$2" crds="$3" manager="$4" deployment
  for deployment in cetirun-operator cetirun-sandbox-admission; do
    if manifest_has_deployment "${manifest}" "${deployment}" &&
      ! kubectl rollout status "deployment/${deployment}" -n "${namespace}" --timeout=180s; then
      echo "ERROR: ${deployment} did not finish rolling out, so the existing CRDs were left unchanged." >&2
      echo "Fix the rollout and run this helper again." >&2
      return 1
    fi
  done
  grep -q '^kind: CustomResourceDefinition' <<< "${crds}" || return 0
  kubectl apply --server-side --force-conflicts --field-manager="${manager}" -f - <<< "${crds}"
}
