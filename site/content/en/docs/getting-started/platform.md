---
title: "Install CetiRun"
description: "Prepare AKS, identity, networking, and storage, and then install the operator and portal."
weight: 30
---

Install CetiRun on an AKS cluster with identity, networking, storage, and
registry access configured for your organization.

> **Warning:** Before you run the deployment scripts, review the scripts,
> target subscription, Kubernetes context, costs, and access policies. The
> scripts create or update cloud and cluster resources.

To validate the chart locally without a deployment of resources, use
[Local checks](../local-checks/).

To run only the AI Gateway for agents that you operate yourself, install its
chart on its own instead. The
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#install)
tells you how to publish its image and chart and how to install it. It also
covers smart routing of models and the first call from a client.

## Prepare the environment

| Requirement | Why it matters |
| --- | --- |
| AKS with managed Istio and an external ingress gateway | Shared authentication and instance routes |
| OIDC issuer and Workload Identity | Federated identity for OAuth2 Proxy and Agent Identity |
| Azure Disk CSI and the `default` StorageClass | Default persistent HOME volumes |
| Blob CSI and `azureblob-nfs-premium` StorageClass, when shared skills are enabled | Optional shared skills volumes |
| A compatible `kata-vm-isolation` RuntimeClass | Default agent chart sandbox. Do not silently remove it |
| Kubernetes 1.37 or later | `make deploy` refuses an older version. Sandbox admission must have this version |
| Kata node pools and Azure CNI powered by Cilium | Agents of Teams created in the portal run in a Sandbox. Refer to [Policy-bound Teams](../../operations/policy-teams/) |
| Enforced Kubernetes NetworkPolicy and necessary admission APIs | Runtime isolation rules and the default exec-denial policy |
| Accessible images and versioned OCI agent chart | Operator fetches the agent chart, and then nodes pull images |
| Entra ID app, allowed groups, federated credential | Portal login and group-based admission |
| Apex DNS and a TLS certificate that matches it. Wildcard names if you use them | Portal, instance routes, and apex `/oauth2/callback`. Optional `publicTLS` in [Public DNS and TLS](../../operations/public-ingress/) can prepare one exact hostname during `make deploy` |
| Python 3 with PyYAML, Helm, kubectl, OpenSSL, Docker Buildx, Azure CLI/Bicep, jq | `make deploy` and the deployment helpers |

The chart does not declare a general minimum Kubernetes version, but its
default `execRestriction.enabled: true` renders
`admissionregistration.k8s.io/v1` `ValidatingAdmissionPolicy` and binding
resources. The source documents Kubernetes 1.30+ for that feature. Examine the
actual API availability and the authorization to create these cluster-scoped
resources. Also examine the Istio `VirtualService` and `Sidecar` APIs. Do not
infer support for an arbitrary Kubernetes distribution from a successful render.

Sources: [runtime values]({{< repo "app/cetirun-chart/values.yaml" >}}),
[exec policy]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}}),
and [operator RBAC]({{< repo "app/cetirun-operator/chart/templates/clusterrole.yaml" >}}).
The chart does not create the RuntimeClass, CSI driver, or Istio control plane.

## 1. Review infrastructure inputs

[Infrastructure](../../operations/infrastructure/) covers the
Bicep inputs and helper side effects. The cluster and storage helpers expect
operator-authored `.bicepparam` files. The repository does not include these
files. Set `PARAM_FILE` explicitly to your reviewed file.

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
certificates for the hosts that you serve. A wildcard certificate alone does
not cover the apex.

The Gateway refers to `cetirun-tls` in its configured namespace
(`aks-istio-ingress` by default). The setting `tls.enabled: false` disables
only chart-managed Secret creation, not TLS. Configure the `oauth2-proxy`
external authorization provider in the managed Istio shared ConfigMap. A direct
Helm installation does not do that patch.

For a single exact hostname, configure `publicTLS` in the same environment file
before deployment. `make deploy` can configure Azure Public IP DNS or validate
a user-managed record, then issue the Gateway certificate with cert-manager
HTTP-01 before it deploys the complete platform. The normal environment file
still needs all identity, Broker, admission, model and runtime configuration.

Do not expose the portal before you make sure that authentication, ownership
checks, and namespace sidecar injection work. The shared filter calls the
portal on port 8081. That port is exempt from mTLS, and its NetworkPolicy must
restrict it.

Shared skills storage is optional and separate from instance home PVCs. Review
`networkPolicy.allowedInternalCIDRs` rather than inherit its
environment-specific exception. If npm skills are enabled, set
`skills.npmRegistry` to your approved reachable registry. Istio ServiceEntries
are not an outbound allowlist. Configure AgentPolicy network controls and
follow the [retired-filter cleanup procedure](../../operations/istio/#retired-shared-blacklist)
when you upgrade installations that used the old shared blacklist.

## 3. Install the operator and portal

`make deploy` installs the platform with Agent connections and Agent Identity.
Before you run it, complete [Agent Identity setup](../../operations/agent-identity/).
It covers the separate Broker, Key Vault, MCP certificates and trust, provider
registrations, and Entra lifecycle configuration. `make deploy` does not
provision these external resources.

`make deploy` installs the Broker with Microsoft delegated access, and it
enables the Agent Identity lifecycle of the operator. If the environment file
turns off one of these features, the run stops. As a result, the Entra parts of
that setup are not optional with `make deploy`. These parts are the Agent
Identity Blueprint, the Authorization application, the encryption identity, and
the Key Vault key. GitHub and Google connections stay optional. Refer to
[What `make deploy` requires](../../operations/agent-identity/#what-make-deploy-requires).

For a new installation, `make deploy` installs three Helm releases: the
CetiRun Istio release, the Agent Broker, and the platform release. The platform
chart packages the operator, the portal, the AI Gateway, and Connections MCP.
Independent component charts are also available. Do not install both over the
same resources, because the umbrella does not adopt resources that standalone
Helm releases own.

Supply your own accessible registry and tested artifact versions. Registry
defaults in source are deployment configuration, not a public artifact catalog.
Examine each independent artifact path:

- The workstation's Helm client must have access to the operator/portal OCI
  charts, unless you select their local charts.
- The operator must have access to the runtime OCI chart when it runs. For a
  private registry, `acrDockerSecret` mounts a `kubernetes.io/dockerconfigjson`
  Secret from the operator namespace into its Helm client.
- Nodes must have pull access to the operator, portal, selected agent, desktop,
  and OAuth2 Proxy images. Node registry permissions do not supply credentials
  to the operator's Helm client. For operator-managed Agents, Portal accepts
  per-Agent credentials, and Operator creates the namespace-local pull Secret.
  Refer to [private registries](../../reference/agent-harness/#private-registries).
  For Standalone Agent Helm releases, any referenced `imagePullSecrets` must
  still exist in their release namespace.

The operator chart declares `imagePullSecrets`, but it does not render
it into its Deployment. That value cannot supply operator image-pull
credentials. Examine the actual node/service-account pull path separately.

### Publish the images and charts

Build the images and charts from the revision that you install, and push them
to your registry. Use a machine with Docker Buildx, Helm, Python 3, and Bash.
On this machine, sign in to the registry with `docker login` and
`helm registry login`. Then run this command from a clone of the repository:

```bash
make publish REGISTRY=registry.example.com VERSION=0.1.1 \
  GIT_SHA="$(git rev-parse HEAD)"
```

The command builds and pushes the 14 images and 9 charts of
[`scripts/release-artifacts.json`]({{< repo "scripts/release-artifacts.json" >}}).
These include the operator, Portal, AI Gateway, Broker, OpenClaw, Hermes, and
VS Code Server images, and the platform, Istio, and Agent (`helm/cetirun`)
charts. Then the command pulls each chart back to examine it. Refer to the
[unified release inventory]({{< repo "DEVELOPMENT.md" >}}#unified-release-inventory).

`ACCEPT_VSCODE_SERVER_LICENSE_TERMS` defaults to `true`, so builds accept the
[VS Code Server license](https://code.visualstudio.com/license/server) without
a separate flag or prompt. An explicit `false` stops builds that include VS Code
Server. The command also stops if a tag already exists.

If you do not accept that license yet, publish the remaining artifacts with the
release CLI and exclude the declared optional VS Code image explicitly:

```bash
python3 -B scripts/cetirun-release.py publish \
  --registry registry.example.com --version 0.1.1 \
  --git-sha "$(git rev-parse HEAD)" \
  --exclude-image cetirun-vscode
```

This selection publishes 13 images and all 9 charts. The normal `make publish`
path deliberately keeps the complete inventory and still requires the license
acceptance. Use the same selection in the environment file:

```yaml
release:
  excludeImages:
    - cetirun-vscode
```

The deployment helper rejects unknown, duplicate, or required image exclusions.
It also rejects any configuration that still references an excluded image,
including a Custom runtime `approvedImages` digest. Remove that reference and
do not create or retain a VS Code Server Agent for this deployment. When the
Agent CRD exists, the helper also stops before a Helm write if a retained Custom
Agent still references that image. This does not bypass the VS Code builder's
own license check.

`VERSION` must be the same as the version in the source. To publish a
different version, for example a test build of a branch, first run
`make set-version VERSION=<version>`. This command changes the version in the
charts and in the other source files. Commit the change, so that `GIT_SHA`
names the source of the build.

Use the same registry, version, Git SHA, and optional image selection in the
`release` section of the environment file for `make deploy`. Before it writes,
`make deploy` makes sure that every selected image and every chart of this
version is in the registry. It sets the
registry and the version of the platform images and of the Agent chart. As a
result, image tags are not necessary in the environment file.

The build of the OpenClaw image runs an installer. The `openat2` system call is
necessary for this installer. The Dockerfile checks the actual builder before
the plugin downloads, and stops with guidance if that builder blocks the call.
Use a Linux Docker Buildx builder that permits `openat2`; remote builders that
block the syscall, including Azure Container Registry Tasks, are unsupported.

### Example: Azure Container Registry

When the artifacts are in an Azure Container Registry, these steps cover the
nodes' image pulls and the operator's chart pulls. In these steps, the runtime
chart is `helm/cetirun` in the registry. First, replace the example values:

```bash
ACR_NAME=exampleregistry
CLUSTER_RESOURCE_GROUP=example-rg
CLUSTER_NAME=example-aks
CHART_VERSION=0.1.0
```

The registry's host is its login server. This is `<registry>.azurecr.io`, or a
name with a hash when the registry has domain name label protection. Read the
login server rather than build it. Use it wherever the registry's host appears,
as in `cetirun.chart.repoURL` (`oci://<login server>/helm/cetirun`):

```bash
ACR_LOGIN_SERVER=$(az acr show --name "$ACR_NAME" --query loginServer -o tsv)
```

1. To let the nodes pull images, give the cluster's kubelet identity a role on
   the registry. You must have permission to assign roles on the registry. The
   role depends on the registry's role assignment permissions mode:

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
   permissions), the registry does not honor `AcrPull`. Also, AKS does not
   support the attachment of the registry. Assign
   `Container Registry Repository Reader` instead:

   ```bash
   KUBELET_OBJECT_ID=$(az aks show --resource-group "$CLUSTER_RESOURCE_GROUP" \
     --name "$CLUSTER_NAME" --query identityProfile.kubeletidentity.objectId -o tsv)
   az role assignment create --role "Container Registry Repository Reader" \
     --assignee-object-id "$KUBELET_OBJECT_ID" --assignee-principal-type ServicePrincipal \
     --scope "$(az acr show --name "$ACR_NAME" --query id -o tsv)"
   ```

   Without a condition, the assignment covers every repository in the
   registry, as `AcrPull` does. Refer to
   [AKS and Azure Container Registry](https://learn.microsoft.com/azure/aks/cluster-container-registry-integration).

2. Give the operator's Helm client a token that can only read the runtime
   chart. The commands create the token without passwords and generate one
   separately. As a result, no command prints the password. The commands also
   write the registry configuration without the password on a command line.
   The Secret goes in the operator namespace, which is `cetirun-system` by
   default. If necessary, create the namespace first:

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

   Use `kubectl create`, not `kubectl apply`. The apply command also copies the
   credential into the `last-applied-configuration` annotation. Set
   `acrDockerSecret: acr-helm-pull` (`cetirun-operator.acrDockerSecret` in the
   platform chart). In the environment file of `make deploy`, the field is
   `platform.values.cetirun-operator.acrDockerSecret`. Before the current one
   expires, generate a new password and replace the Secret.

3. Fetch the chart with the same file, as the operator does. Then delete the
   file:

   ```bash
   helm show chart "oci://$ACR_LOGIN_SERVER/helm/cetirun" \
     --version "$CHART_VERSION" --registry-config registry-config.json
   rm registry-config.json
   ```

The push of the images and charts into the registry also depends on the role
assignment permissions mode that step 1 reads. With
`LegacyRegistryPermissions`, `AcrPush` lets an identity push. `Owner` and
`Contributor` on the registry also let an identity push. With
`AbacRepositoryPermissions`, the registry honors neither `AcrPush` nor the
repository access of `Owner` and `Contributor`. In this mode, these roles
manage only the registry itself.

The identity that pushes must have `Container Registry Repository Writer`,
assigned on the registry like the role in step 1. A new assignment can take a
few minutes to apply. If you copy the artifacts in from another registry with
`az acr import`, this is an operation on the registry itself. `Owner` and
`Contributor` still allow this operation. As a result, a repository role on the
registry is not necessary for the copy. Refer to
[ABAC repository permissions](https://learn.microsoft.com/azure/container-registry/container-registry-rbac-abac-repository-permissions).

### Upgrading runtime registry configuration

Operator-managed Agents reject raw `imagePullSecrets` overrides. This also
applies to an empty list. Before you upgrade, remove
`cetirun.helmValues.imagePullSecrets` from the Operator's saved values
(`cetirun-operator.cetirun.helmValues.imagePullSecrets` in the platform chart)
and any instance `spec.helm.values.imagePullSecrets`.

If you keep the deployment-level key, built-in Agents report
`AgentConfigInvalid` or `RegistryRuntimeConfigRejected`, even when their images
are public. Helm `--reuse-values` keeps that key. Replace the saved values with
a reviewed file that omits it.

While you migrate private images, keep a node image-pull identity that works,
or arrange a maintenance window. After you upgrade the Operator, Portal, and
CRDs, upgrade the pinned runtime chart of each affected Agent. Use a version
that advertises managed-registry support. Then configure the per-Agent
credentials through the
[Registry API](../../reference/agent-harness/#installation-and-api). Agent
credentials that already exist can also be replaced through Portal. A raw pull
Secret is not automatically adopted or copied into the managed source
namespace.

The managed credential covers one registry host derived from the Agent image.
It does not supply the credentials of another registry for Desktop, Terminal,
or other platform images. Those images must have their own node or platform
pull path that works. Before you retire the old access method, examine fresh
pulls, not only images that are already in use or cached. Standalone Agent
chart `imagePullSecrets` and Portal Deployment `imagePullSecrets` are separate
settings, and they are still supported.

### Platform chart

`make deploy` installs the platform from the charts in your clone. It runs
`scripts/cetirun-platform-deploy.sh` with one environment file. The script no
longer reads `DOMAIN` or `VALUES_FILE`. It installs these Helm releases:

- The CetiRun Istio release, with OAuth2 Proxy
- The Agent Broker, in its own credential namespace
- The platform release, in `cetirun-system` by default. It contains the
  operator, the Sandbox admission server, the portal, the AI Gateway with
  PostgreSQL and Valkey, Connections MCP, and the ADX-Mon collection of logs
  and metrics.

OpenClaw and Hermes Agents take their models from the AI Gateway. For the
shared models and the Gateway console, refer to the
[platform chart README]({{< repo "app/cetirun-platform/README.md" >}}#ai-gateway).
The README also describes each input of the environment file, in
[Deploy the platform]({{< repo "app/cetirun-platform/README.md" >}}#deploy-the-platform).

Before the first run, make sure that these items exist:

- Managed Istio and its external ingress gateway. Supply the `cetirun-tls`
  Secret for your domain, or configure `publicTLS` to issue it during
  deployment. Refer to [Istio authentication](../../operations/istio/).
- The Agent Identity resources: the Entra registrations, the Key Vault key, and
  the Broker and MCP certificates. Refer to
  [Agent Identity setup](../../operations/agent-identity/).
- A client ID for `platform.values.cetirun-operator.azure.clientId`. Use the
  client ID of the Agent Identity Blueprint, unless another operator
  integration must use a different identity. `make deploy` stops if the value is
  empty. Refer to
  [Federate the AKS workloads to the Blueprint](../../operations/entra-agent-identity/#federate-the-aks-workloads-to-the-blueprint).
- The Sandbox admission certificate. Refer to
  [the admission certificate](../../operations/policy-teams/#the-admission-certificate).
- The StorageClasses for the HOME of Custom Agents, for the Gateway's
  PostgreSQL, and for shared skills. Also the Blob storage of shared skills.
  Refer to [Storage](../../operations/storage/).
- The Azure Data Explorer resources of the ADX-Mon collection: a Kusto cluster
  with the logs, metrics, and Agent logs databases, and the ingestor's managed
  identity with its roles and federated credential. By default, the cluster
  template creates them. Refer to
  [Observability with ADX-Mon](../../operations/observability/#prerequisites).
- The Secrets of the AI Gateway. The commands below create them.
- The Microsoft-Decision-1 deployment that the smart-routing model `auto`
  uses to classify requests. Deploy the model in an Azure AI Foundry resource,
  not in an Azure OpenAI resource. Record the deployment name, the endpoint of
  the resource, for example `https://<resource>.services.ai.azure.com`, and its
  key. Refer to
  [Foundry Models sold by Azure](https://learn.microsoft.com/azure/foundry/foundry-models/concepts/models-sold-directly-by-azure).

  A subscription can have no quota for this model. Before you deploy it, find
  the entries of Microsoft-Decision-1 in the output of
  `az cognitiveservices usage list --location <region>`. If their limit is 0,
  request more quota. To see if a region offers the model, use
  `az cognitiveservices model list --location <region>`.

  With the default `modelRouting.mode: auto`, the `decision-1` entry is
  necessary, but `make deploy`
  does not call the deployment. If the deployment does not work, `auto` sends
  each request to its default model. Each request to `auto` can cause a
  classifier call of up to 32,768 tokens. Size the capacity of the deployment
  for this traffic. Refer to
  [Choose a classifier](../../operations/smart-routing/#choose-a-classifier).
  To deploy before classifier quota is available, set `modelRouting.mode: direct`
  in the environment file. Configure callable shared models without `auto`,
  `decision-1`, or other classifier/router entries. Set enabled CetiAdmin's
  `model.default` to one of the configured models. This explicit mode preserves
  the other deployment checks and leaves automatic model routing unavailable.
- The digests of the Custom images that you approve. Examples are the Pi and
  VS Code Server images that `make publish` pushed. To read a digest, use
  `docker buildx imagetools inspect <image>:<version>`. After each
  `make publish` of a new version, read the digests again and update
  `approvedImages`. `make deploy` makes sure only that each digest is in the
  registry, so an old digest also passes.

`make deploy` examines the objects of this list in the cluster, and the images
and charts in the registry. If one of them does not exist, the run stops before
it writes. `make deploy` does not call Azure or Microsoft Graph. As a result,
it does not find a wrong Entra ID, a missing Key Vault key or Kusto database,
or a Decision-1 deployment that does not work. These problems occur only when
a component uses the resource.

The AI Gateway reads the provider keys of its models from a Secret. It also
uses a Secret with its master key, salt key, and console password, and a
Secret with the password of its Valkey. Create the namespace and these Secrets
in the namespace of `platform.namespace`. The commands use the default
namespace, `cetirun-system`.

The commands use the current context of your kubeconfig. First make sure that
`kubectl config current-context` shows the value of `target.context`. Refer to
[Connect to the cluster](../../operations/infrastructure/#connect-to-the-cluster).
Give the master key the `sk-` prefix, as the chart does when it generates one:

```bash
kubectl get namespace cetirun-system >/dev/null 2>&1 ||
  kubectl create namespace cetirun-system
kubectl create secret generic gateway-provider-keys -n cetirun-system \
  --from-literal=AZURE_OPENAI_KEY='<key>' \
  --from-literal=AZURE_DECISION_KEY='<key>'
kubectl create secret generic cetirun-ai-gateway-auth -n cetirun-system \
  --from-literal=master-key="sk-$(openssl rand -hex 24)" \
  --from-literal=salt-key="$(openssl rand -hex 32)" \
  --from-literal=ui-password="$(openssl rand -hex 16)"
kubectl create secret generic gateway-valkey-auth -n cetirun-system \
  --from-literal=redis-password="$(openssl rand -hex 24)"
```

Do not change the salt key later. If it changes, the models that were added in
the Gateway's console disappear. Refer to the Gateway chart's
[Credentials]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#credentials).

The `--from-literal` options put the provider keys in your shell history. To
prevent this, write each key to a file outside the repository. Then use
`--from-file=AZURE_OPENAI_KEY=<file>` instead, and delete the files after the
command.

Each `api_key: os.environ/<NAME>` in the models reads the key `<NAME>` of the
provider-key Secret. The example environment file uses `AZURE_OPENAI_KEY` for
the chat models and `AZURE_DECISION_KEY` for `decision-1`. If you rename a key,
rename it in both places.

Write the environment file outside the repository. Start from the
[synthetic example]({{< repo "app/cetirun-platform/ci/deployment-example.yaml" >}}),
and replace every value in it. The example uses the Secret names of this page
and of the pages that the list above links to. If you use other names, change
them in the file.

Put the Helm values of each release under its `values` field, for
example `platform.values.cetirun-ai-gateway.models`. `make deploy` sets some
values itself, for example the registry, the version, the namespaces, the
domain, and the Istio Gateway. Do not set these values in the file, because a
different value stops the run.

The `istio` section names the managed Istio resources that already exist. AKS
adds the Istio revision to the names of the control plane Deployment, the
ingress gateway Deployment, and the shared mesh ConfigMap. The name of the
ingress gateway ServiceAccount has no revision. Read the revision and the names
from the cluster:

```bash
kubectl get pods -n aks-istio-system -l app=istiod \
  -o jsonpath='{.items[0].metadata.labels.istio\.io/rev}'
kubectl get deployments,configmaps -n aks-istio-system
kubectl get deployments,serviceaccounts -n aks-istio-ingress
```

| Input | Name on AKS |
| --- | --- |
| `istio.revision` | The revision, for example `asm-1-30` |
| `istio.controlPlaneDeployment` | `istiod-<revision>` |
| `istio.ingressDeployment` | `aks-istio-ingressgateway-external-<revision>` |
| `istio.ingressServiceAccount` | `aks-istio-ingressgateway-external` |
| `istio.meshConfigMap` | `istio-shared-configmap-<revision>`. With `istio.mode: manage`, `make deploy` creates it if it does not exist |

On a new cluster, `istio-shared-configmap-<revision>` does not exist yet. The
listing of `aks-istio-system` shows the ConfigMap `istio-<revision>` instead.
AKS manages that ConfigMap. Do not put its name in `istio.meshConfigMap`.

Before it writes, `make deploy` makes sure that the two Deployments exist. If a
Deployment does not exist, the run stops and names the input. If the name with
the revision exists, the message also gives that name. The run also stops if
`istio.meshConfigMap` does not exist but `istio-shared-configmap-<revision>`
exists.

Make a decision about these inputs before the first run:

- `ownerAlias` is the portal administrator. Portal administrators create Teams,
  Agent Policies, and Sandboxes. To add other administrators, list them in
  `platform.values.cetirun-portal.admins.aliases`.
- `platform.values.cetirun-operator.sandboxAdmission` names the admission
  certificate. It also names the administrators who can write policy-bound
  configuration with `kubectl`. A later run cannot change these administrators.
  Refer to
  [the admission certificate](../../operations/policy-teams/#the-admission-certificate).
- `platform.values.cetirun-operator.cetiadmin` configures CetiAdmin, the Hermes
  administration Agent that the operator deploys. `make deploy` enables it by
  default. Set its owner, its initial monthly budget, and its access to the
  Kubernetes API, or set `enabled: false`. Its `owner.alias` must be the same as
  `ownerAlias`. The default model of CetiAdmin is `auto`. Refer to
  [Configure CetiAdmin]({{< repo "app/cetirun-platform/README.md" >}}#configure-cetiadmin).
- `platform.values.cetirun-ai-gateway.models` holds the shared models. It must
  contain callable models. The default `modelRouting.mode: auto` also requires
  the smart-routing model `auto`, which uses the decision model
  `decision-1` as its classifier. The `base_model` of `decision-1` must be
  `azure_ai/Microsoft-Decision-1`. Refer to
  [Smart routing](../../operations/smart-routing/).
- `platform.values.cetirun-ai-gateway.postgresql.bundled.persistence` sets the
  StorageClass and the size of the Gateway's database. A later run cannot
  change them. `make deploy` always installs the Gateway with its bundled
  PostgreSQL and Valkey. It refuses `bundled.enabled: false` for either of
  them. For managed databases, install the platform chart directly. Refer to
  the Gateway chart's
  [Bundled or your own]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#bundled-or-your-own).
- `platform.values.cetirun-ai-gateway.resources` reserves CPU and memory for
  each Gateway pod. The chart reserves none. Set it, and the resources of the
  bundled PostgreSQL and Valkey, before you serve users. Refer to the Gateway
  chart's [Resources]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#resources).
- LiteLLM's spend log in the Gateway's database keeps the prompt and the
  response of each call. By default, it keeps them for seven days. To keep
  only the metadata of each call, set
  `platform.values.cetirun-ai-gateway.spendLogs.storePrompts: false`. Refer to
  [the platform values](../../reference/helm/#platform-chart).

Run the offline plan first. Then run the deployment:

```bash
make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
```

The plan validates the inputs and renders the charts locally. It does not
connect to the cluster or to the registry. If a chart refuses its values, the
plan stops and shows the message of the chart. An example is a router tier that
is not in `models`.

Before it writes, `make deploy` examines the target cluster, the references,
the certificates, and the published artifacts. With `publicTLS`, it first
prepares DNS/cert-manager/certificates through a restricted umbrella-chart
revision, then reruns the full preflight. It installs the three normal releases
and waits for their workloads. On a new installation, it starts the
admission server before it enforces admission. At the end, it does a
server-side dry run of a Team. It does not create Agents or send a request to a
model.

If a step fails, the message names the command of that step.

Run `make deploy` again after each change to the environment file. A run does
not change the admission administrators, and it does not remove the Sandbox
settings that are in effect. If the file tries to do this, the run stops.
Sandbox execution stays disabled until you enable it, as
[Policy-bound Teams](../../operations/policy-teams/) shows. The
[Skill Marketplace](../../operations/skill-marketplace/) is optional. A later
`make deploy` run enables it, together with Sandbox execution or after it.

A change to the models replaces the Gateway's Pods. When a shared model is
added or removed, or when its limits change, the Agents that use shared models
restart one time. Refer to
[Choose a model](../../guides/models/#runtime-configuration).

Other changes also replace the Gateway's Pods, for example a new release
version. With the rollout-stable DNS and Gateway selectors in
[Peer labels](../../operations/policy-teams/#peer-labels), replacement Pods keep
their network authorization without manual labels. Existing IP/peer-UID
registrations still require current backend IPs and labels until they migrate.
Upgrade all readers and follow the coordinated configuration-change workflow
before adopting the new registration.

With `istio.mode: external`, `make deploy` does not upgrade the CetiRun Istio
release. Configure that release to reach the portal: use
`PORTAL_AUTH_NAMESPACE=cetirun-system` with its deployment helper. Refer to
[portal ownership endpoint](../../operations/istio/#portal-ownership-endpoint).

The Operator subchart creates the Registry source namespace, which is kept on
uninstall. Portal and Operator manage its per-Agent credentials at runtime.
`make deploy` does not install managed Istio, external credentials, Agents, or
Agent data that is kept. When `publicTLS` is enabled it can install the pinned
cert-manager controller and issue the one configured public certificate. To install the platform chart
directly with Helm instead, refer to
[Use the component chart directly]({{< repo "app/cetirun-platform/README.md" >}}#use-the-component-chart-directly)
and to the [platform values](../../reference/helm/#platform-chart).

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

This render runs without cluster or registry access. During installation,
examine artifact availability and rollout status. For the agent chart, refer to
[runtime render](../../reference/helm/#offline-rendering).

After you complete the prerequisites, run the deployment scripts from the
repository root. Before you run these commands, replace all example values:

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
claim that an artifact exists at your registry. `example-build` is not a
published image tag. We recommend that you use verified image digests through
`IMAGE_DIGEST`. `CHART_VERSION` selects the component chart, and
`CETIRUN_CHART_VERSION` selects the runtime chart. `IMAGE_TAG`/`IMAGE_DIGEST`
pin only the component that you install, not the runtime images.

The [operator helper]({{< repo "app/cetirun-operator/deploy/deploy.sh" >}})
examines the StorageClass and shared gateway before it installs the operator.
The operator chart bundles all four CRDs as ordinary templates.
Helm upgrades the CRDs with the release, and their retention annotation keeps
them on uninstall.

The default compatible admission mode tolerates newer CR fields. It does not
disable the validation or authorization that already exists. No external CRD
apply script is necessary for compatible schema changes. Before you upgrade an
installation that already exists, review
[API compatibility](../../reference/crds/#existing-installations).

The [portal helper]({{< repo "app/cetirun-portal/deploy/deploy.sh" >}})
expects that the operator/CRDs are prepared separately. It also labels its
namespace for Istio injection. Set `ISTIO_REV` to the actual installed revision
instead of its built-in `asm-1-27`. Supply `ISTIO_HOST` explicitly, because the
helper also reads it in its final summary. Neither helper waits for the
operator or portal rollout.

`LOCAL_CHART=1` selects the local operator or portal chart. It does **not** make
the installation offline. Images and the operator-managed agent chart must
still be available.

Review [chart configuration](../../reference/helm/) for image pins, OCI pull
credentials, deployment-wide Helm values, default model, admin aliases, and
portal features. Before you serve users, configure these explicitly. The
operator helper supports simple extra `HELM_SET_ARGS`, but the portal helper
does not. For settings that a helper does not expose, use a separately reviewed
direct Helm install with a private values file. With this install, keep all
CRD, gateway, namespace-label, and authentication prerequisites.

The default namespaces and resource names are cross-component contracts. If
you change them, you must also review the shared chart's `portalAuth` service
address, gateway namespace/service-account policies, and controller routes. An
available `NAMESPACE` or `RELEASE_NAME` override does not establish that every
consumer is reconfigured automatically.

In particular, the operator helper's `NAMESPACE` does not override the chart's
`namespace` value. A non-default installation must set both consistently. The
Portal's [Upgrade action](../../guides/lifecycle/#pin-and-update-a-chart) also
finds the fixed `cetirun-system/cetirun-operator` Deployment. A change to
those Helm settings does not reconfigure this lookup.

## 4. Verify the deployment

Examine the installed resources. These commands use the platform chart's
default namespace:

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

`make deploy` sends no request to a model. Send one to the AI Gateway with its
master key: list the models, and ask a configured model a question. The plan
reports the selected mode in `modelRouting.mode`. For `auto` mode, use the
request below. For `direct` mode, replace `"model": "auto"` with a configured
model name, such as `"model": "gpt-main"`. These commands use the default
namespace and do not print the key:

```bash
kubectl port-forward -n cetirun-system svc/cetirun-ai-gateway 4000:4000 &
MASTER_KEY="$(kubectl get secret cetirun-ai-gateway-auth -n cetirun-system \
  -o jsonpath='{.data.master-key}' | base64 -d)"
curl -s http://localhost:4000/v1/models -H "Authorization: Bearer ${MASTER_KEY}"
curl -si http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer ${MASTER_KEY}" -H 'Content-Type: application/json' \
  -d '{"model": "auto", "messages": [{"role": "user", "content": "What is 2+2?"}]}'
```

The list contains the names of `models`. In direct mode, verify that the
selected model returns a normal response; there is no classifier or routing
header to verify. Stop the port-forward after the test.

In auto mode, Agents do not see `decision-1`, because the Gateway lists it only
for keys that the operator did not issue. The answer from `auto` has the header `x-litellm-complexity-router-tier`, and
`x-litellm-complexity-router-cause` is `jev_classifier`. Any other cause shows
that the classifier did not decide. Then examine the `decision-1` entry, its
key, and the Gateway's log. Stop the port-forward after the test.

A Team is optional for a new agent. An agent without a Team can use every
shared conversational model, including `auto` when automatic routing is
configured. An administrator
creates Teams in the portal's administration pages, with an Agent Policy and a
Sandbox. The agents of such a Team run in its Sandbox, after you configure
[Policy-bound Teams](../../operations/policy-teams/). A Team can also give its
agents its own smart-routing models. To configure them, continue with
[Smart routing](../../operations/smart-routing/).

When shared skills are enabled, also examine `azureblob-nfs-premium` and the
`blob.csi.azure.com` CSI driver. Blob CSI is not necessary for HOME storage.

By default, the standalone portal helper uses the namespace `cetirun-portal`.
When you examine an independent installation, use that namespace instead.

Examine pod sidecar injection as well as namespace labels. If you have
permission to create a test instance, use it to make sure that these items
work:

- TLS coverage
- Entra login and sign-out
- Rejection of cross-owner access
- Persistent storage
- Provider login

Review the
[same-origin sign-out checks](../../operations/istio/#prepare-identity-dns-and-tls).
Test both successful and denied requests. Examine `IngressReady` independently
of `Ready`. Then give users the portal address and the
[first-instance guide](../first-instance/).

Before you enable Tailscale, shared storage, and cloud log collection for
users, test them separately.
