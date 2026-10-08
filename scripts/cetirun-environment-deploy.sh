#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Upgrade an already-bootstrapped CetiRun environment from immutable OCI
# artifacts. Entra applications, workload identities, Key Vault, Redis, and the
# operator's ACR pull credential are deliberate one-time prerequisites and are
# never created or rotated by this continuous deployment entry point.
set -Eeuo pipefail

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: missing required command: $1" >&2
    exit 1
  }
}

require_variable() {
  [[ -n "${!1:-}" ]] || {
    echo "ERROR: $1 is required" >&2
    exit 1
  }
}

for command in az helm jq kubectl python3; do
  require_command "${command}"
done
python3 -c 'import yaml' >/dev/null 2>&1 || {
  echo "ERROR: Python PyYAML is required to reconcile the managed Istio configuration" >&2
  exit 1
}

required_variables=(
  ACR_DOCKER_SECRET_NAME
  ACR_NAME
  ACR_REGISTRY
  AGENT_ID_AUTHORIZATION_CLIENT_ID
  AGENT_ID_BLUEPRINT_CLIENT_ID
  AGENT_ID_ENCRYPTION_CLIENT_ID
  AGENT_ID_KEY_VAULT_KEY_URL
  AGENT_ID_TENANT_ID
  AGENT_PERSISTENCE_ACCESS_MODE
  AGENT_PERSISTENCE_SIZE
  AGENT_PERSISTENCE_STORAGE_CLASS
  AI_GATEWAY_IMAGE_DIGEST
  AKS_CLUSTER_NAME
  AKS_RESOURCE_GROUP
  AZURE_SUBSCRIPTION_ID
  BROKER_IMAGE_DIGEST
  CETIRUN_DOMAIN
  DESKTOP_IMAGE_DIGEST
  DESKTOP_IMAGE_TAG
  HERMES_IMAGE_DIGEST
  HERMES_IMAGE_TAG
  OPENCLAW_IMAGE_DIGEST
  OPENCLAW_IMAGE_TAG
  OPERATOR_IMAGE_DIGEST
  PORTAL_IMAGE_DIGEST
  PORTAL_ADMIN_ALIASES
  PORTAL_OIDC_CLIENT_ID
  REDIS_CONNECTION_URL
  REDIS_SECRET_NAME
  RELEASE_VERSION
  TERMINAL_IMAGE_DIGEST
)
for variable in "${required_variables[@]}"; do
  require_variable "${variable}"
done
export "${required_variables[@]}"

if [[ ! "${RELEASE_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]]; then
  echo "ERROR: RELEASE_VERSION must be an immutable SemVer-compatible version" >&2
  exit 1
fi
if [[ ! "${AGENT_PERSISTENCE_ACCESS_MODE}" =~ ^(ReadWriteOnce|ReadWriteMany|ReadOnlyMany|ReadWriteOncePod)$ ]]; then
  echo "ERROR: AGENT_PERSISTENCE_ACCESS_MODE is invalid" >&2
  exit 1
fi
for variable in \
  AI_GATEWAY_IMAGE_DIGEST BROKER_IMAGE_DIGEST DESKTOP_IMAGE_DIGEST \
  HERMES_IMAGE_DIGEST OPENCLAW_IMAGE_DIGEST OPERATOR_IMAGE_DIGEST \
  PORTAL_IMAGE_DIGEST TERMINAL_IMAGE_DIGEST; do
  if [[ ! "${!variable}" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "ERROR: ${variable} is not a sha256 image digest" >&2
    exit 1
  fi
done
if [[ "${ACR_REGISTRY}" != "${ACR_NAME}.azurecr.io" ]]; then
  echo "ERROR: ACR_REGISTRY does not match ACR_NAME" >&2
  exit 1
fi

# The required-variable loop above validates this externally supplied value.
# shellcheck disable=SC2153
portal_admin_aliases="$(jq -cn --arg aliases "${PORTAL_ADMIN_ALIASES}" '
  $aliases | split(",") | map(gsub("^\\s+|\\s+$"; ""))
')"
if ! jq -e '
  length > 0 and
  all(.[]; test("^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$")) and
  length == (unique | length)
' <<<"${portal_admin_aliases}" >/dev/null; then
  echo "ERROR: PORTAL_ADMIN_ALIASES must be a unique comma-separated list of lowercase DNS aliases" >&2
  exit 1
fi

platform_namespace="${PLATFORM_NAMESPACE:-cetirun-system}"
istio_namespace="${ISTIO_NAMESPACE:-cetirun-istio}"
credential_namespace="${CREDENTIAL_NAMESPACE:-cetirun-credentials}"
platform_release="${PLATFORM_RELEASE:-cetirun-platform}"
istio_release="${ISTIO_RELEASE:-cetirun-istio}"
broker_release="${BROKER_RELEASE:-cetirun-agent-broker}"
broker_service_account="${BROKER_SERVICE_ACCOUNT:-cetirun-agent-broker}"
operator_service_account="${OPERATOR_SERVICE_ACCOUNT:-cetirun-operator-controller-manager}"
redis_deployment="${REDIS_DEPLOYMENT:-oauth2-proxy-redis}"
broker_url="http://${broker_release}.${credential_namespace}.svc.cluster.local:8080"
portal_origin="https://${CETIRUN_DOMAIN}"
export BROKER_SERVICE_ACCOUNT="${broker_service_account}"
export BROKER_URL="${broker_url}"
export ISTIO_REVISION
export OPERATOR_SERVICE_ACCOUNT="${operator_service_account}"
export PLATFORM_NAMESPACE="${platform_namespace}"
export PORTAL_ORIGIN="${portal_origin}"

require_variable KUBECONFIG
if [[ "${KUBECONFIG}" != /* || "${KUBECONFIG}" == "${HOME}/.kube/config" ]]; then
  echo "ERROR: KUBECONFIG must be an isolated absolute path" >&2
  exit 1
fi
install -d -m 700 "$(dirname "${KUBECONFIG}")"

# Never change the Azure CLI default subscription or the global kube context.
az account show --subscription "${AZURE_SUBSCRIPTION_ID}" --output none
target_id="$(az aks show \
  --subscription "${AZURE_SUBSCRIPTION_ID}" \
  --resource-group "${AKS_RESOURCE_GROUP}" \
  --name "${AKS_CLUSTER_NAME}" \
  --query id --output tsv --only-show-errors)"
expected_prefix="/subscriptions/${AZURE_SUBSCRIPTION_ID}/resourceGroups/${AKS_RESOURCE_GROUP}/providers/Microsoft.ContainerService/managedClusters/${AKS_CLUSTER_NAME}"
if [[ "${target_id,,}" != "${expected_prefix,,}" ]]; then
  echo "ERROR: AKS resource identity did not match the requested target" >&2
  exit 1
fi
az aks get-credentials --admin --overwrite-existing \
  --subscription "${AZURE_SUBSCRIPTION_ID}" \
  --resource-group "${AKS_RESOURCE_GROUP}" \
  --name "${AKS_CLUSTER_NAME}" \
  --file "${KUBECONFIG}" >/dev/null
chmod 600 "${KUBECONFIG}"
kubectl cluster-info --request-timeout=10s >/dev/null

# Continuous deployment fails closed if the one-time bootstrap is incomplete.
helm status "${platform_release}" --namespace "${platform_namespace}" >/dev/null
helm status "${istio_release}" --namespace "${istio_namespace}" >/dev/null
kubectl get deployment "${redis_deployment}" --namespace "${istio_namespace}" >/dev/null
kubectl get secret "${REDIS_SECRET_NAME}" --namespace "${istio_namespace}" >/dev/null
kubectl get secret "${ACR_DOCKER_SECRET_NAME}" --namespace "${platform_namespace}" >/dev/null

istio_revision="$(kubectl get pods --namespace aks-istio-system --selector app=istiod \
  --output jsonpath='{.items[0].metadata.labels.istio\.io/rev}')"
if [[ -z "${istio_revision}" ]]; then
  echo "ERROR: unable to discover the managed Istio revision" >&2
  exit 1
fi
export ISTIO_REVISION="${istio_revision}"

temporary_directory="$(mktemp -d)"
cleanup() {
  rm -rf "${temporary_directory}"
}
trap cleanup EXIT
istio_values="${temporary_directory}/istio.json"
broker_values="${temporary_directory}/broker.json"
platform_values="${temporary_directory}/platform.json"
agents_before="${temporary_directory}/agents-before.json"
agents_after="${temporary_directory}/agents-after.json"
chmod 700 "${temporary_directory}"

snapshot_agents() {
  kubectl get agents.cetirun.microsoft.com --output json | jq -S '[
    .items[] | {
      name: .metadata.name,
      uid: .metadata.uid,
      generation: .metadata.generation,
      observedGeneration: (.status.observedGeneration // 0),
      ready: (([.status.conditions[]? | select(.type == "Ready")] | last | .status) // "False"),
      agentIdentityEnabled: (.spec.agentIdentity.enabled // false),
      agentType: (.spec.agent.type // null),
      agentImage: (.spec.agent.image // null),
      agentPort: (.spec.agent.port // null),
      helmVersion: (.spec.helm.version // null),
      specInvariant: (.spec | del(.agent.image, .agent.port, .helm.version))
    }
  ] | sort_by(.name)'
}

agents_are_converged() {
  jq -e 'all(.[]; .ready == "True" and .observedGeneration == .generation)' "$1" >/dev/null
}

agents_are_preserved() {
  jq -e --slurp '
    def expected_image($agent):
      if $agent.agentType == "hermes" then
        env.ACR_REGISTRY + "/hermes-gateway:" + env.HERMES_IMAGE_TAG + "@" + env.HERMES_IMAGE_DIGEST
      elif $agent.agentType == "openclaw" then
        env.ACR_REGISTRY + "/openclaw-gateway:" + env.OPENCLAW_IMAGE_TAG + "@" + env.OPENCLAW_IMAGE_DIGEST
      else null
      end;
    def expected_port($agent):
      if $agent.agentType == "hermes" then 8642
      elif $agent.agentType == "openclaw" then 18789
      else null
      end;
    .[0] as $before | .[1] as $after |
    ($before | length) == ($after | length) and
    all(range(0; $before | length); . as $index |
      $before[$index] as $old | $after[$index] as $new |
      ($new.agentImage != $old.agentImage or $new.agentPort != $old.agentPort) as $runtimeChanged |
      $new.name == $old.name and
      $new.uid == $old.uid and
      $new.agentIdentityEnabled == $old.agentIdentityEnabled and
      $new.specInvariant == $old.specInvariant and
      $new.ready == "True" and
      $new.observedGeneration == $new.generation and
      $new.helmVersion == $old.helmVersion and
      (if $old.agentImage == null then
         ($new.agentImage == null or $new.agentImage == expected_image($old))
       else $new.agentImage == $old.agentImage
       end) and
      (if $old.agentPort == null then
         ($new.agentPort == null or $new.agentPort == expected_port($old))
       else $new.agentPort == $old.agentPort
       end) and
      $new.generation == ($old.generation + (if $runtimeChanged then 1 else 0 end))
    )
  ' "$1" "$2" >/dev/null
}

snapshot_agents >"${agents_before}"
if ! agents_are_converged "${agents_before}"; then
  echo "ERROR: every existing Agent must be Ready and fully reconciled before deployment" >&2
  exit 1
fi
helm_revision() {
  helm status "$1" --namespace "$2" --output json 2>/dev/null |
    jq -r '.version // 0'
}

istio_previous_revision="$(helm_revision "${istio_release}" "${istio_namespace}")"
platform_previous_revision="$(helm_revision "${platform_release}" "${platform_namespace}")"
broker_previous_revision="$(helm_revision "${broker_release}" "${credential_namespace}" || true)"
broker_previous_revision="${broker_previous_revision:-0}"
if [[ ! "${istio_previous_revision}" =~ ^[1-9][0-9]*$ ]] ||
   [[ ! "${platform_previous_revision}" =~ ^[1-9][0-9]*$ ]] ||
   [[ ! "${broker_previous_revision}" =~ ^[0-9]+$ ]]; then
  echo "ERROR: unable to establish Helm rollback revisions" >&2
  exit 1
fi

deployment_started=false
istio_upgraded=false
broker_upgraded=false
platform_upgraded=false

rollback_deployment() {
  local status=$?
  local line=$1
  trap - ERR
  set +e
  if [[ "${deployment_started}" == "true" ]]; then
    echo "ERROR: deployment failed near line ${line}; restoring prior Helm releases" >&2
    if [[ "${platform_upgraded}" == "true" ]]; then
      helm rollback "${platform_release}" "${platform_previous_revision}" \
        --namespace "${platform_namespace}" --wait --timeout 15m >&2
    fi
    if [[ "${broker_upgraded}" == "true" ]]; then
      if [[ "${broker_previous_revision}" == "0" ]]; then
        helm uninstall "${broker_release}" --namespace "${credential_namespace}" \
          --wait --timeout 10m >&2
      else
        helm rollback "${broker_release}" "${broker_previous_revision}" \
          --namespace "${credential_namespace}" --wait --timeout 10m >&2
      fi
    fi
    if [[ "${istio_upgraded}" == "true" ]]; then
      helm rollback "${istio_release}" "${istio_previous_revision}" \
        --namespace "${istio_namespace}" --wait --timeout 10m >&2
    fi
  fi
  exit "${status}"
}
trap 'rollback_deployment ${LINENO}' ERR

jq -n --argjson adminAliases "${portal_admin_aliases}" '{
  authPolicy: {
    adminAliases: $adminAliases,
    webhookBypassPaths: ["/{*}/api/messages"],
    legacySubdomainWebhookBypassPaths: ["/api/messages"],
    apexPostBypassPaths: ["/api/v1/agent-authorization/callback"]
  },
  oauth2Proxy: {
    sessionStore: {
      type: "redis",
      redis: {
        connectionURL: env.REDIS_CONNECTION_URL,
        existingSecret: env.REDIS_SECRET_NAME
      }
    }
  }
}' >"${istio_values}"

jq -n '{
  image: {
    repository: (env.ACR_REGISTRY + "/cetirun-agent-broker"),
    tag: env.RELEASE_VERSION,
    digest: env.BROKER_IMAGE_DIGEST
  },
  serviceAccount: {name: env.BROKER_SERVICE_ACCOUNT},
  agentIdentity: {
    tenantId: env.AGENT_ID_TENANT_ID,
    blueprintClientId: env.AGENT_ID_BLUEPRINT_CLIENT_ID,
    authorizationClientId: env.AGENT_ID_AUTHORIZATION_CLIENT_ID,
    encryptionClientId: env.AGENT_ID_ENCRYPTION_CLIENT_ID,
    keyVaultKeyUrl: env.AGENT_ID_KEY_VAULT_KEY_URL,
    redirectUri: (env.PORTAL_ORIGIN + "/api/v1/agent-authorization/callback"),
    portalOrigin: env.PORTAL_ORIGIN,
    portalBaseUrl: env.PORTAL_ORIGIN
  },
  portal: {
    namespace: env.PLATFORM_NAMESPACE,
    serviceAccount: "cetirun-portal"
  },
  operator: {
    namespace: env.PLATFORM_NAMESPACE,
    serviceAccount: env.OPERATOR_SERVICE_ACCOUNT
  },
  istio: {
    enabled: true,
    sidecarInjection: true,
    revision: env.ISTIO_REVISION
  }
}' >"${broker_values}"

jq -n --argjson adminAliases "${portal_admin_aliases}" '{
  global: {
    registry: env.ACR_REGISTRY,
    version: env.RELEASE_VERSION
  },
  "cetirun-operator": {
    image: {digest: env.OPERATOR_IMAGE_DIGEST},
    terminal: {image: {digest: env.TERMINAL_IMAGE_DIGEST}},
    acrDockerSecret: env.ACR_DOCKER_SECRET_NAME,
    cetirun: {
      chart: {
        repoURL: ("oci://" + env.ACR_REGISTRY + "/helm/cetirun"),
        version: env.RELEASE_VERSION
      },
      helmValues: {
        persistence: {
          storageClass: env.AGENT_PERSISTENCE_STORAGE_CLASS,
          accessMode: env.AGENT_PERSISTENCE_ACCESS_MODE,
          size: env.AGENT_PERSISTENCE_SIZE
        },
        agentIdentity: {
          brokerURL: env.BROKER_URL
        },
        openclaw: {
          image: {
            repository: (env.ACR_REGISTRY + "/openclaw-gateway"),
            tag: env.OPENCLAW_IMAGE_TAG,
            digest: env.OPENCLAW_IMAGE_DIGEST
          }
        },
        hermes: {
          image: {
            repository: (env.ACR_REGISTRY + "/hermes-gateway"),
            tag: env.HERMES_IMAGE_TAG,
            digest: env.HERMES_IMAGE_DIGEST
          }
        },
        desktop: {
          image: {
            repository: (env.ACR_REGISTRY + "/cetirun-desktop"),
            tag: env.DESKTOP_IMAGE_TAG,
            digest: env.DESKTOP_IMAGE_DIGEST
          }
        }
      }
    },
    agentIdentity: {
      enabled: true,
      cleanupEnabled: true,
      tenantId: env.AGENT_ID_TENANT_ID,
      blueprintClientId: env.AGENT_ID_BLUEPRINT_CLIENT_ID,
      namePrefix: "cetirun-agent",
      brokerURL: env.BROKER_URL
    },
    aiGateway: {
      copilotProxy: {image: {digest: env.AI_GATEWAY_IMAGE_DIGEST}}
    }
  },
  "cetirun-portal": {
    image: {digest: env.PORTAL_IMAGE_DIGEST},
    admins: {aliases: $adminAliases},
    features: {agentIdentityEnabled: true},
    agentIdentity: {
      tenantId: env.AGENT_ID_TENANT_ID,
      portalClientId: env.PORTAL_OIDC_CLIENT_ID,
      brokerURL: env.BROKER_URL
    }
  },
  "cetirun-ai-gateway": {
    image: {digest: env.AI_GATEWAY_IMAGE_DIGEST}
  }
}' >"${platform_values}"
: >"${agents_after}"
chmod 600 "${istio_values}" "${broker_values}" "${platform_values}" \
  "${agents_before}" "${agents_after}"

# Upgrade the authentication edge before exposing the Portal authorization UI.
deployment_started=true
helm upgrade "${istio_release}" \
  "oci://${ACR_REGISTRY}/helm/cetirun-istio" \
  --version "${RELEASE_VERSION}" \
  --namespace "${istio_namespace}" \
  --reset-then-reuse-values \
  --values "${istio_values}" \
  --atomic --wait --timeout 10m
istio_upgraded=true

# The managed Istio extension provider lives outside the Helm release. Keep
# its upstream response-header contract in sync with the Portal ID-token
# handoff after every authentication-edge upgrade.
python3 scripts/ensure-oauth2-proxy-id-token-header.py \
  --namespace aks-istio-system \
  --name "istio-shared-configmap-${istio_revision}"

kubectl create namespace "${credential_namespace}" --dry-run=client --output yaml |
  kubectl apply --filename - >/dev/null
kubectl label namespace "${credential_namespace}" \
  "istio.io/rev=${istio_revision}" --overwrite >/dev/null

helm upgrade --install "${broker_release}" \
    "oci://${ACR_REGISTRY}/helm/cetirun-agent-broker" \
    --version "${RELEASE_VERSION}" \
    --namespace "${credential_namespace}" \
    --values "${broker_values}" \
    --atomic --wait --timeout 10m
broker_upgraded=true

kubectl rollout status "deployment/${broker_release}" \
  --namespace "${credential_namespace}" --timeout=5m
kubectl get pods --namespace "${credential_namespace}" \
  --selector "app.kubernetes.io/name=${broker_release}" --output json |
  jq -e '
    (.items | length) > 0 and
    all(.items[];
      .metadata.labels["azure.workload.identity/use"] == "true" and
      (any(.spec.containers[]?; .name == "istio-proxy") or
       any(.spec.initContainers[]?; .name == "istio-proxy")) and
      all(.status.containerStatuses[]?; .ready == true) and
      all(.status.initContainerStatuses[]?;
        if .name == "istio-proxy" then .ready == true
        else .state.terminated.reason == "Completed" end))
  ' >/dev/null
kubectl get peerauthentication "${broker_release}" \
  --namespace "${credential_namespace}" --output json |
  jq -e '.spec.mtls.mode == "STRICT"' >/dev/null
kubectl get networkpolicy "${broker_release}" \
  --namespace "${credential_namespace}" >/dev/null

helm upgrade "${platform_release}" \
    "oci://${ACR_REGISTRY}/helm/cetirun-platform" \
    --version "${RELEASE_VERSION}" \
    --namespace "${platform_namespace}" \
    --reset-then-reuse-values \
    --values "${platform_values}" \
    --atomic --wait --timeout 15m
platform_upgraded=true

kubectl rollout status deployment/cetirun-operator \
  --namespace "${platform_namespace}" --timeout=5m
kubectl rollout status deployment/cetirun-portal \
  --namespace "${platform_namespace}" --timeout=5m

# Fail closed if the requested feature did not reach the live workloads.
helm get values "${platform_release}" --namespace "${platform_namespace}" --output json |
  jq -e '
    .["cetirun-operator"].agentIdentity.enabled == true and
    .["cetirun-portal"].features.agentIdentityEnabled == true
  ' >/dev/null
helm status "${broker_release}" --namespace "${credential_namespace}" >/dev/null

agents_preserved=false
preservation_attempts="${AGENT_PRESERVATION_ATTEMPTS:-60}"
preservation_interval="${AGENT_PRESERVATION_INTERVAL_SECONDS:-5}"
if [[ ! "${preservation_attempts}" =~ ^[1-9][0-9]*$ ]] ||
   [[ ! "${preservation_interval}" =~ ^[0-9]+$ ]]; then
  echo "ERROR: Agent preservation retry settings are invalid" >&2
  false
fi
for _ in $(seq 1 "${preservation_attempts}"); do
  snapshot_agents >"${agents_after}"
  if agents_are_converged "${agents_after}" && agents_are_preserved "${agents_before}" "${agents_after}"; then
    agents_preserved=true
    break
  fi
  sleep "${preservation_interval}"
done
if [[ "${agents_preserved}" != "true" ]]; then
  echo "ERROR: the existing Agent collection, identity settings, or Ready state changed" >&2
  false
fi

deployment_started=false

echo "Environment deployment completed and Agent Identity is enabled."
