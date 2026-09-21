#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Install the platform umbrella only after separately managed Istio is ready.
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-platform}"
namespace="${NAMESPACE:-cetirun-system}"
release_name="${RELEASE_NAME:-cetirun-platform}"
values_file="${VALUES_FILE:-}"
domain="${DOMAIN:?Set DOMAIN to the public CetiRun hostname}"
gateway_name="${ISTIO_GATEWAY_NAME:-cetirun-gateway}"
gateway_namespace="${ISTIO_GATEWAY_NAMESPACE:-aks-istio-ingress}"
gateway_service_account="${ISTIO_GATEWAY_SERVICE_ACCOUNT:-aks-istio-ingressgateway-external}"
oauth2_proxy_name="${OAUTH2_PROXY_NAME:-oauth2-proxy}"
oauth2_proxy_namespace="${OAUTH2_PROXY_NAMESPACE:-cetirun-istio}"

for command in helm kubectl; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "ERROR: Missing required command: ${command}" >&2
    exit 1
  fi
done
if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi
if [ -n "${values_file}" ] && [ ! -f "${values_file}" ]; then
  echo "ERROR: VALUES_FILE does not exist: ${values_file}" >&2
  exit 1
fi

kubectl cluster-info --request-timeout=5s >/dev/null
crd_dir="${repo_root}/app/cetirun-operator/config/crd/bases"
for crd_file in \
  "${crd_dir}/cetirun.microsoft.com_openclawinstances.yaml" \
  "${crd_dir}/cetirun.microsoft.com_openclawteams.yaml"; do
  if [ ! -f "${crd_file}" ]; then
    echo "ERROR: Required CRD manifest does not exist: ${crd_file}" >&2
    exit 1
  fi
  kubectl apply -f "${crd_file}" >/dev/null
done
kubectl wait --for=condition=Established \
  crd/openclawinstances.cetirun.microsoft.com \
  crd/openclawteams.cetirun.microsoft.com \
  --timeout=30s >/dev/null

kubectl get crd virtualservices.networking.istio.io >/dev/null
kubectl get gateway.networking.istio.io "${gateway_name}" -n "${gateway_namespace}" >/dev/null
kubectl get serviceaccount "${gateway_service_account}" -n "${gateway_namespace}" >/dev/null
kubectl get deployment "${oauth2_proxy_name}" -n "${oauth2_proxy_namespace}" >/dev/null
kubectl rollout status "deployment/${oauth2_proxy_name}" -n "${oauth2_proxy_namespace}" --timeout=120s

istio_revision="${ISTIO_REVISION:-$(kubectl get pods -n aks-istio-system -l app=istiod \
  -o jsonpath='{.items[0].metadata.labels.istio\.io/rev}')}"
if [ -z "${istio_revision}" ]; then
  echo "ERROR: cannot determine the Istio revision; set ISTIO_REVISION explicitly" >&2
  exit 1
fi

helm dependency build --skip-refresh "${chart_dir}" >/dev/null
kubectl create namespace "${namespace}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl label namespace "${namespace}" "istio.io/rev=${istio_revision}" --overwrite >/dev/null

helm_args=(
  upgrade --install "${release_name}" "${chart_dir}"
  --namespace "${namespace}"
  --set istio.enabled=true
  --set-string "istio.oauth2Proxy.name=${oauth2_proxy_name}"
  --set-string "istio.oauth2Proxy.namespace=${oauth2_proxy_namespace}"
  --set-string "cetirun-operator.namespace=${namespace}"
  --set-string "cetirun-operator.domainSuffix=${domain}"
  --set-string "cetirun-operator.istioRevision=${istio_revision}"
  --set-string "cetirun-portal.istio.host=${domain}"
  --set-string "cetirun-portal.istio.gateway=${gateway_namespace}/${gateway_name}"
  --set-string "cetirun-portal.istio.gatewayNamespace=${gateway_namespace}"
  --set-string "cetirun-portal.istio.gatewayServiceAccount=${gateway_service_account}"
)
if [ -n "${values_file}" ]; then
  helm_args+=(--values "${values_file}")
fi
helm "${helm_args[@]}"

kubectl rollout status deployment/cetirun-operator -n "${namespace}" --timeout=180s
kubectl rollout status deployment/cetirun-portal -n "${namespace}" --timeout=180s
