---
title: "Install CetiRun"
description: "Prepare AKS, identity, networking, and storage, then install the operator and portal."
weight: 30
---

Install CetiRun on an AKS cluster with identity, networking, storage, and
registry access configured for your organization.

> **Warning:** Deployment scripts create or update cloud and cluster resources.
> Review the scripts, target subscription, Kubernetes context, costs, and
> access policies before running them.

For local chart validation without deploying resources, use
[Local checks](../local-checks/).

To run only the AI Gateway, for agents you operate yourself, install its chart
on its own instead: the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#install)
covers publishing its image and chart, installing it, smart-routing models and
a client's first call.

## Prepare the environment

| Requirement | Why it matters |
| --- | --- |
| AKS with managed Istio and an external ingress gateway | Shared authentication and instance routing |
| OIDC issuer and Workload Identity | Federated identity for OAuth2 Proxy and Agent Identity |
| Azure Disk CSI and the `default` StorageClass | Default persistent HOME volumes |
| Blob CSI and `azureblob-nfs-premium` StorageClass, when shared skills are enabled | Optional shared skills volumes |
| A compatible `kata-vm-isolation` RuntimeClass | Default agent chart sandbox; do not silently remove it |
| Kubernetes 1.37 or later, Kata node pools and Azure CNI powered by Cilium | Agents of Teams created in the portal run in a Sandbox; see [Policy-bound Teams](../../operations/policy-teams/) |
| Enforced Kubernetes NetworkPolicy and required admission APIs | Runtime isolation rules and the default exec-denial policy |
| Accessible images and versioned OCI agent chart | Operator fetches the agent chart, then nodes pull images |
| Entra ID app, allowed groups, federated credential | Portal login and group-based admission |
| Apex DNS and a matching TLS certificate; wildcard names if used | Portal, instance routes, and apex `/oauth2/callback` |
| Helm, kubectl, Azure CLI/Bicep, jq, OpenSSL | Deployment helpers |

The chart does not declare a general minimum Kubernetes version, but its
default `execRestriction.enabled: true` renders
`admissionregistration.k8s.io/v1` `ValidatingAdmissionPolicy` and binding
resources. The source documents Kubernetes 1.30+ for that feature. Check the
actual API availability and authorization to create these cluster-scoped
resources, plus Istio `VirtualService` and `Sidecar` APIs. Do not infer support
for an arbitrary Kubernetes distribution from a successful render.

Sources: [runtime values]({{< repo "app/cetirun-chart/values.yaml" >}}),
[exec policy]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}}),
and [operator RBAC]({{< repo "app/cetirun-operator/chart/templates/clusterrole.yaml" >}}).
The chart does not create the RuntimeClass, CSI driver, or Istio control plane.

## 1. Review infrastructure inputs

[Infrastructure](../../operations/infrastructure/) covers the
Bicep inputs and helper side effects. The cluster and storage helpers expect
operator-authored `.bicepparam` files, which are not included in the repository.
Set `PARAM_FILE` explicitly to your reviewed file.

The cluster helper installs or updates an Azure CLI extension, registers a
preview feature, provisions infrastructure, enables Gateway API, and overwrites
the selected kubeconfig entry. Node-pool defaults are not sized for a small
demonstration environment.

## 2. Configure authentication and storage

Follow [Istio authentication](../../operations/istio/) and
[persistent storage](../../operations/storage/). The identity redirect URI is
`https://<domain>/oauth2/callback`. Portal and native-instance routes
use paths on the apex domain. The Gateway also accepts wildcard hosts, but
that does not create a VirtualService for each subdomain. Configure DNS and
certificates for the hosts you actually serve; a wildcard certificate alone
does not cover the apex.
The Gateway references `cetirun-tls` in its configured namespace
(`aks-istio-ingress` by default). Setting
`tls.enabled: false` only disables chart-managed Secret creation, not TLS.
Configure the `oauth2-proxy` external authorization provider in the managed
Istio shared ConfigMap; direct Helm installation does not do that patch.

Do not expose the portal until authentication, ownership checks, and namespace
sidecar injection are verified. The shared filter calls the portal on port 8081;
that port is exempt from mTLS and must be restricted by its NetworkPolicy.

Shared skills storage is optional and separate from instance home PVCs. Review
`networkPolicy.allowedInternalCIDRs` rather than inheriting its
environment-specific exception. If npm skills are enabled, set
`skills.npmRegistry` to your approved reachable registry. Istio ServiceEntries
are not an outbound allowlist. Configure AgentPolicy network controls and
follow the [retired-filter cleanup procedure](../../operations/istio/#retired-shared-blacklist)
when upgrading an installation that used the old shared blacklist.

## 3. Install the operator and portal

For Agent connections, complete [Agent Identity setup](../../operations/agent-identity/)
before enabling the Portal feature. It covers the separate Broker, Key Vault,
MCP certificates and trust, provider registrations, and optional Entra lifecycle
configuration. The platform chart does not provision these external resources.

For a new installation, the platform chart packages the operator, the portal
and the AI Gateway into one release. Independent component charts are also available. Do not
install both over the same resources: the umbrella does not adopt resources
owned by standalone Helm releases.

Supply your own accessible registry and tested artifact versions. Registry
defaults in source are deployment configuration, not a public artifact catalog.
Check each independent artifact path:

- The workstation's Helm client must be able to fetch operator/portal OCI
  charts unless you select their local charts.
- The running operator must be able to fetch the runtime OCI chart. For a
  private registry, `acrDockerSecret` mounts a `kubernetes.io/dockerconfigjson`
  Secret from the operator namespace into its Helm client.
- Nodes must be able to pull operator, portal, selected agent, desktop, and
  OAuth2 Proxy images. Node registry permissions do not supply credentials to
  the operator's Helm client. For operator-managed Agents, Portal accepts
  per-Agent credentials and Operator creates the namespace-local pull Secret;
  see [private registries](../../reference/agent-harness/#private-registries).
  Standalone Agent Helm releases still require any referenced `imagePullSecrets`
  to exist in their release namespace.

The operator chart declares `imagePullSecrets` but does not render
it into its Deployment. That value cannot supply operator image-pull
credentials; verify the actual node/service-account pull path separately.

### Publish the images and charts

Build the images and charts from the revision you install, and push them to
your registry. On a machine with Docker Buildx, Helm, Python 3 and Bash, signed
in to the registry with `docker login` and `helm registry login`, from a clone
of the repository:

```bash
make publish REGISTRY=registry.example.com
```

It builds and pushes every CetiRun image, the operator, Portal, AI Gateway,
OpenClaw and Hermes ones among them, and every chart, the platform and Agent
(`helm/cetirun`) charts among them, all under the repository's release version
from `app/cetirun-chart/Chart.yaml`. It then pulls each chart back to check
it. The platform chart takes its images and the Agent chart from the registry
in `global.registry`, at that version, so the values below need no image tags.
The OpenClaw image's build runs an installer that needs the `openat2` system
call, which the builders of Azure Container Registry Tasks refuse: build on a
Docker host.

### Example: Azure Container Registry

With the artifacts in an Azure Container Registry, these steps cover the
nodes' image pulls and the operator's chart pulls. They assume the runtime
chart is `helm/cetirun` in the registry. Replace the example values first:

```bash
ACR_NAME=exampleregistry
CLUSTER_RESOURCE_GROUP=example-rg
CLUSTER_NAME=example-aks
CHART_VERSION=0.1.0
```

The registry's host is its login server: `<registry>.azurecr.io`, or a name
with a hash when the registry has domain name label protection. Read it rather
than build it, and use it wherever the registry's host appears, as in
`cetirun.chart.repoURL` (`oci://<login server>/helm/cetirun`):

```bash
ACR_LOGIN_SERVER=$(az acr show --name "$ACR_NAME" --query loginServer -o tsv)
```

1. Let the nodes pull images by giving the cluster's kubelet identity a role
   on the registry, which needs permission to assign roles there. The role
   depends on the registry's role assignment permissions mode:

   ```bash
   az acr show --name "$ACR_NAME" --query roleAssignmentMode -o tsv
   ```

   With `LegacyRegistryPermissions` (RBAC registry permissions), attach the
   registry, which assigns `AcrPull`:

   ```bash
   az aks update --resource-group "$CLUSTER_RESOURCE_GROUP" \
     --name "$CLUSTER_NAME" --attach-acr "$ACR_NAME"
   ```

   With `AbacRepositoryPermissions` (RBAC registry and ABAC repository
   permissions), the registry does not honor `AcrPull`, and AKS does not
   support attaching it. Assign `Container Registry Repository Reader`
   instead:

   ```bash
   KUBELET_OBJECT_ID=$(az aks show --resource-group "$CLUSTER_RESOURCE_GROUP" \
     --name "$CLUSTER_NAME" --query identityProfile.kubeletidentity.objectId -o tsv)
   az role assignment create --role "Container Registry Repository Reader" \
     --assignee-object-id "$KUBELET_OBJECT_ID" --assignee-principal-type ServicePrincipal \
     --scope "$(az acr show --name "$ACR_NAME" --query id -o tsv)"
   ```

   Without a condition, the assignment covers every repository in the
   registry, as `AcrPull` does; see
   [AKS and Azure Container Registry](https://learn.microsoft.com/azure/aks/cluster-container-registry-integration).

2. Give the operator's Helm client a token that can only read the runtime
   chart. The token is created without passwords and one is generated
   separately, so no command prints it, and the registry configuration is
   written without the password on a command line. The Secret goes in the
   operator namespace, `cetirun-system` by default; create it first if needed:

   ```bash
   az acr scope-map create --registry "$ACR_NAME" --name cetirun-chart-read \
     --repository helm/cetirun content/read metadata/read
   az acr token create --registry "$ACR_NAME" --name cetirun-operator \
     --scope-map cetirun-chart-read --no-passwords
   password=$(az acr token credential generate --registry "$ACR_NAME" \
     --name cetirun-operator --password1 --expiration-in-days 90 \
     --query 'passwords[0].value' -o tsv)
   auth=$(printf '%s:%s' cetirun-operator "$password" | base64 | tr -d '\n')
   (umask 077 && printf '{"auths":{"%s":{"auth":"%s"}}}\n' \
     "$ACR_LOGIN_SERVER" "$auth" > registry-config.json)
   unset password auth
   kubectl create secret generic acr-helm-pull --namespace cetirun-system \
     --type=kubernetes.io/dockerconfigjson \
     --from-file=.dockerconfigjson=registry-config.json
   ```

   Use `kubectl create`, not `kubectl apply`, which would also copy the
   credential into the `last-applied-configuration` annotation. Set
   `acrDockerSecret: acr-helm-pull` (`cetirun-operator.acrDockerSecret` in the
   platform chart). Generate a new password and replace the Secret before the
   current one expires.

3. Fetch the chart with the same file, as the operator does, then delete it:

   ```bash
   helm show chart "oci://$ACR_LOGIN_SERVER/helm/cetirun" \
     --version "$CHART_VERSION" --registry-config registry-config.json
   rm registry-config.json
   ```

Pushing the images and charts into the registry also depends on the role
assignment permissions mode that step 1 reads. With
`LegacyRegistryPermissions`, `AcrPush` lets an identity push, and so do `Owner`
and `Contributor` on the registry. With `AbacRepositoryPermissions`, the
registry honors neither `AcrPush` nor the repository access of `Owner` and
`Contributor`, which then manage only the registry itself: the identity that
pushes needs `Container Registry Repository Writer`, assigned on the registry
like the role in step 1. A new assignment can take a few minutes to apply.
Copying the artifacts in from another registry with `az acr import` is an
operation on the registry itself, which `Owner` and `Contributor` still allow,
so it needs no repository role there. See
[ABAC repository permissions](https://learn.microsoft.com/azure/container-registry/container-registry-rbac-abac-repository-permissions).

### Upgrading runtime registry configuration

Operator-managed Agents reject raw `imagePullSecrets` overrides, including an
empty list. Before upgrading, remove `cetirun.helmValues.imagePullSecrets` from
the Operator's saved values (`cetirun-operator.cetirun.helmValues.imagePullSecrets`
in the platform chart) and any instance `spec.helm.values.imagePullSecrets`.
Keeping the deployment-level key makes built-in Agents report `AgentConfigInvalid`
or `RegistryRuntimeConfigRejected`, even when their images are public. Helm
`--reuse-values` retains that key; replace the saved values with a reviewed file
that omits it.

Keep a working node image-pull identity while migrating private images, or
arrange a maintenance window. After the Operator, Portal, and CRDs are upgraded,
upgrade each affected Agent's pinned runtime chart to a version that advertises
managed-registry support, then configure its per-Agent credentials through the
[Registry API](../../reference/agent-harness/#installation-and-api). Existing
Agent credentials can also be replaced through Portal. A raw pull Secret is not
automatically adopted or copied into the managed source namespace.

The managed credential covers one registry host derived from the Agent image;
it does not supply another registry's credentials for Desktop, Terminal, or other
platform images. Those images need their own working node or platform pull path.
Check fresh pulls, not only already-running or cached images, before retiring
the old access method. Standalone Agent chart `imagePullSecrets` and Portal
Deployment `imagePullSecrets` are separate settings and remain supported.

### Platform chart

The umbrella installs its components in `cetirun-system` by default. OpenClaw
and Hermes Agents take their models from the AI Gateway, which the umbrella
installs with PostgreSQL and Valkey unless one is installed separately. See the
[platform chart README]({{< repo "app/cetirun-platform/README.md" >}}) for shared
models, the Gateway console and a separately installed Gateway. Configure
the independent Istio release to reach that portal before installation, using
`PORTAL_AUTH_NAMESPACE=cetirun-system` with its deployment helper. See
[portal ownership endpoint](../../operations/istio/#portal-ownership-endpoint).
The Operator subchart creates the Registry source namespace, retained on
uninstall; Portal and Operator manage its per-Agent credentials at runtime.
The platform chart does not install Istio, certificates, external credentials,
Agents, or retained Agent data.

Prepare a private values file. With the artifacts published as above, for
example:

```yaml
global:
  # Images and the Agent chart, at the chart's own version.
  registry: registry.example.com
cetirun-operator:
  # The operator's Helm client credential; see the registry example above.
  acrDockerSecret: acr-helm-pull
cetirun-portal:
  noticeBanner: ""
  admins:
    aliases:
      - alice
cetirun-ai-gateway:
  # The Secret created below.
  providerCredentials:
    existingSecret: gateway-provider-keys  # holds AZURE_OPENAI_KEY
  models:
    - model_name: gpt-main
      litellm_params:
        model: azure/my-gpt-deployment
        api_base: https://example.openai.azure.com/
        api_key: os.environ/AZURE_OPENAI_KEY
        # Must support the Responses API: agents' tool calls are sent there.
        api_version: "2025-04-01-preview"
      model_info:
        base_model: azure/gpt-4o
```

These are placeholders, not published artifacts. `admins.aliases` names the
Portal administrators, who create Teams, Agent Policies and Sandboxes; the
models are the shared models Agents can use, see the
[platform chart README]({{< repo "app/cetirun-platform/README.md" >}}#ai-gateway).
The Gateway reads the models' provider keys from the Secret the values name.
Create it in the release namespace before the install:

```bash
kubectl create namespace cetirun-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic gateway-provider-keys -n cetirun-system \
  --from-literal=AZURE_OPENAI_KEY='<key>'
```

Add the storage and runtime settings for your environment. Agents of a Team
created in the Portal run in a Sandbox, which needs the additional setup in
[Policy-bound Teams](../../operations/policy-teams/). After completing the
prerequisites:

```bash
DOMAIN=example.com VALUES_FILE=/absolute/path/platform-values.yaml \
  bash scripts/cetirun-platform-deploy.sh
```

The helper checks Istio resources and OAuth2 Proxy readiness, builds the local
chart dependencies, creates and labels the target namespace for sidecar
injection, installs the release, and waits for the rendered operator/portal
Deployments. It does not verify end-to-end login or agent readiness.
`ISTIO_REVISION` overrides revision detection.

For a custom gateway, use `ISTIO_GATEWAY_NAMESPACE` and `ISTIO_GATEWAY_NAME`
for its resource, and `ISTIO_INGRESS_NAMESPACE` and
`ISTIO_GATEWAY_SERVICE_ACCOUNT` for the gateway workload. The helper aligns
the portal route, ownership policy, and agent NetworkPolicy with these inputs.
See [platform values](../../reference/helm/#platform-chart).

### Independent component charts

Render the operator chart locally with example values:

```bash
helm template cetirun-operator app/cetirun-operator/chart \
  --namespace cetirun-system \
  --set-string domainSuffix=example.com \
  --set-string image.repository=registry.example.com/cetirun-operator \
  --set-string image.tag=example-build \
  --set-string cetirun.chart.repoURL=oci://registry.example.com/helm/cetirun \
  --set-string cetirun.chart.version=0.1.0 > /dev/null
```

This render runs without cluster or registry access. Check artifact availability
and rollout status during installation. See
[runtime rendering](../../reference/helm/#offline-rendering) for the agent chart.

After completing the prerequisites, run the deployment scripts from the
repository root. Replace all example values before running these commands:

```bash
REGISTRY=registry.example.com CHART_VERSION=0.1.0 IMAGE_TAG=example-build \
CETIRUN_CHART_VERSION=0.1.0 DOMAIN=example.com \
  bash app/cetirun-operator/deploy/deploy.sh

REGISTRY=registry.example.com CHART_VERSION=0.1.0 IMAGE_TAG=example-build \
ISTIO_HOST=example.com ISTIO_REV=your-installed-istio-revision \
NOTICE_BANNER="" \
  bash app/cetirun-portal/deploy/deploy.sh
```

Replace every example value. `0.1.0` is the chart's source version, not a
claim that an artifact exists at your registry; `example-build` is not a
published image tag. Prefer verified image digests through `IMAGE_DIGEST`.
`CHART_VERSION` selects the component chart; `CETIRUN_CHART_VERSION` selects the
runtime chart; `IMAGE_TAG`/`IMAGE_DIGEST` pin only the component being installed,
not the runtime images.

The [operator helper]({{< repo "app/cetirun-operator/deploy/deploy.sh" >}})
checks the StorageClass and shared gateway before installing the operator.
The operator chart bundles all four CRDs as ordinary templates. Helm upgrades the CRDs
with the release, and their retention annotation keeps them on uninstall.
The default compatible admission mode tolerates newer CR fields without
disabling existing validation or authorization. No external CRD apply script is
needed for compatible schema changes. Review
[API compatibility](../../reference/crds/#existing-installations) before
upgrading an existing installation. The
[portal helper]({{< repo "app/cetirun-portal/deploy/deploy.sh" >}})
expects the operator/CRDs to be prepared separately and labels its namespace
for Istio injection. Set `ISTIO_REV` to the actual installed revision instead of
its built-in `asm-1-27`. Supply `ISTIO_HOST` explicitly: the helper also reads
it in its final summary. Neither helper waits for the operator or portal rollout.

`LOCAL_CHART=1` selects the local operator or portal chart. It does **not** make
the installation offline: images and the operator-managed agent chart still
need to be available.

Review [chart configuration](../../reference/helm/) for image pins, OCI pull
credentials, deployment-wide Helm values, default model, admin aliases, and
portal features. Configure these explicitly before serving users. The operator
helper supports simple extra `HELM_SET_ARGS`; the portal helper does not.
Use a separately reviewed direct Helm install with a private values file for
settings not exposed by a helper, retaining all CRD, gateway, namespace-label,
and authentication prerequisites.

The default namespaces and resource names are cross-component contracts.
Changing them also requires reviewing the shared chart's `portalAuth` service
address, gateway namespace/service-account policies, and controller routing.
An available `NAMESPACE` or `RELEASE_NAME` override does not establish that
every consumer is reconfigured automatically.
In particular, the operator helper's `NAMESPACE` does not override the chart's
`namespace` value. A non-default installation must set both consistently.
The Portal's [Upgrade action](../../guides/lifecycle/#pin-and-update-a-chart) also
looks up the fixed `cetirun-system/cetirun-operator` Deployment; changing those
Helm settings does not reconfigure this lookup.

## 4. Verify the deployment

Check the installed resources using the platform chart's default namespace:

```bash
kubectl get runtimeclass kata-vm-isolation
kubectl get storageclass default
kubectl get csidriver disk.csi.azure.com
kubectl get crd agents.cetirun.microsoft.com teams.cetirun.microsoft.com
kubectl get gateway.networking.istio.io cetirun-gateway -n aks-istio-ingress
kubectl rollout status deployment/cetirun-operator -n cetirun-system --timeout=120s
kubectl rollout status deployment/cetirun-portal -n cetirun-system --timeout=120s
kubectl rollout status deployment/cetirun-ai-gateway -n cetirun-system --timeout=300s
```

New agents need a Team, which an administrator creates in the portal's
administration pages with an Agent Policy and a Sandbox, once
[Policy-bound Teams](../../operations/policy-teams/) are set up. To give a
Team's agents a model that sends each request to a cheaper or a stronger model,
continue with [Smart routing](../../operations/smart-routing/).

When shared skills are enabled, also check `azureblob-nfs-premium` and the
`blob.csi.azure.com` CSI driver. HOME storage does not require Blob CSI.

The standalone portal helper defaults to namespace `cetirun-portal`; use that
namespace instead when verifying an independent installation.

Check pod sidecar injection as well as namespace labels. With permission to
create a test instance, verify TLS coverage, Entra login and sign-out, rejection
of cross-owner access, persistent storage, and provider login. Review the
[same-origin sign-out checks](../../operations/istio/#prepare-identity-dns-and-tls).
Test both successful and denied requests, and inspect `IngressReady` independently of
`Ready`. Then give users the portal address and
[first-instance guide](../first-instance/).

Test Tailscale, shared storage, and cloud log collection separately before
enabling them for users.
