---
title: "Publish images and charts"
description: "Build and push the CetiRun images and charts, give the cluster access to them, and prepare a private registry or a restricted network."
weight: 12
---

`make deploy` installs the images and charts of one release version from one
registry. Build them from the revision that you install, and push them to your
registry before you run `make deploy`. This page is a step of
[Install CetiRun](../../getting-started/platform/).

Supply your own accessible registry and tested artifact versions. Registry
defaults in source are deployment configuration, not a public artifact catalog.
Examine each independent artifact path:

- The Helm client of the workstation must be able to read all charts of the
  release in the registry. `make deploy` examines them before it writes, but it
  installs the charts of your clone. The standalone helpers can use their
  local charts instead, with `LOCAL_CHART=1`.
- The operator must have access to the runtime OCI chart when it runs. For a
  private registry, `acrDockerSecret` mounts a `kubernetes.io/dockerconfigjson`
  Secret from the operator namespace into its Helm client.
- Nodes must have pull access to the platform images and to the selected agent
  and desktop images. They also need the images of other projects that
  [Private registries and restricted networks](#private-registries-and-restricted-networks)
  lists. Node registry permissions do not supply credentials to the operator's
  Helm client. For operator-managed Agents, Portal accepts per-Agent
  credentials, and Operator creates the namespace-local pull Secret. Refer to
  [private registries](../../reference/agent-harness/#private-registries).
  For Standalone Agent Helm releases, any referenced `imagePullSecrets` must
  still exist in their release namespace.
- The `imagePullSecrets` of the Operator chart apply to the admission server
  and the CetiAdmin hook Jobs, not to the Operator Deployment. For the Operator
  image, use a node pull path that works.

## Publish the images and charts

Use a machine with Docker Buildx, Helm, Python 3, and Bash. On this machine,
sign in to the registry with `docker login` and `helm registry login`. Then
run this command from a clone of the repository:

```bash
make publish REGISTRY=registry.example.com VERSION=0.1.1 \
  GIT_SHA="$(git rev-parse HEAD)"
```

The command builds and pushes the 13 images and 9 charts of
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

This selection publishes 12 images and all 9 charts. The normal `make publish`
path always selects the complete inventory. Use the same selection in the
environment file:

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

### Select the version

For an official version, first run `make set-version VERSION=<version>`. This
command changes the version in the charts and in the other source files.
Commit the change, so that `GIT_SHA` names the source of the build.

For a test build, for example of a branch, set another `VERSION` without a
change to the source. The release CLI copies the working tree to a temporary
directory, and it sets the version only in that copy. `GIT_SHA` names the
commit of the working tree. If the working tree has changes that are not
committed, the build includes them, and `GIT_SHA` does not fully identify the
source.

### The OpenClaw builder

The build of the OpenClaw image runs an installer. The `openat2` system call is
necessary for this installer. The Dockerfile checks the actual builder before
the plugin downloads, and stops with guidance if that builder blocks the call.
Use a Linux Docker Buildx builder that permits `openat2`; remote builders that
block the syscall, including Azure Container Registry Tasks, are unsupported.

## Use the artifacts with make deploy

Use the same registry, version, Git SHA, and optional image selection in the
`release` section of the environment file for `make deploy`. Before it writes,
`make deploy` makes sure that every selected image and every chart of this
version is in the registry. It sets the registry and the version of the
platform images and of the Agent chart. As a result, image tags are not
necessary in the environment file.

Run `make deploy` from the same commit that built the artifacts. `make deploy`
renders and installs the charts of your clone and sets only their version to
`release.version`. It does not compare the clone with the artifacts in the
registry. `release.gitSHA` must be a full 40-character commit ID. `make deploy`
records it in the plan, but it does not compare it with the clone. Before each
run, compare it yourself:

```bash
git rev-parse HEAD
git status --short
```

The first command must print `release.gitSHA`. The second command must print
nothing.

Also read the digests of the Custom images that you approve, for example the Pi
and VS Code Server images that `make publish` pushed:

```bash
docker buildx imagetools inspect registry.example.com/cetirun-pi-agent:0.1.1
```

Put the digests in `platform.values.cetirun-operator.customRuntime.approvedImages`.
After each `make publish` of a new version, read the digests again. `make deploy`
makes sure only that each digest is in the registry, so an old digest also
passes.

## Example: Azure Container Registry

When the artifacts are in an Azure Container Registry, these steps cover the
nodes' image pulls and the operator's chart pulls. In these steps, the runtime
chart is `helm/cetirun` in the registry. First, replace the example values.
`CHART_VERSION` is the version that you published:

```bash
ACR_NAME=exampleregistry
CLUSTER_RESOURCE_GROUP=example-rg
CLUSTER_NAME=example-aks
CHART_VERSION=0.1.1
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
   expires, generate a new password and replace the Secret. Refer to
   [Certificates and credentials](../certificates/).

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

## Private registries and restricted networks

`make publish` pushes only the artifacts of `scripts/release-artifacts.json`.
An installation also pulls the images of other projects in this table. To pull
them from a private registry, copy each image to that registry. Then set its
`repository`, `tag`, and `digest` fields under the value in the table. Set a
digest for each image, so that the reference does not change.

| Image | Default repository | Value |
| --- | --- | --- |
| OAuth2 Proxy | `quay.io/oauth2-proxy/oauth2-proxy` | `istio.values.oauth2Proxy.image` |
| Session store of OAuth2 Proxy | `mcr.microsoft.com/azurelinux/base/valkey` | `istio.values.oauth2Proxy.sessionStore.redis.bundled.image` |
| Entra auth sidecar of the Broker | `mcr.microsoft.com/entra-sdk/auth-sidecar` | `broker.values.authSDK` |
| AI Gateway PostgreSQL | `mcr.microsoft.com/azurelinux/base/postgres` | `platform.values.cetirun-ai-gateway.postgresql.bundled.image` |
| AI Gateway Valkey | `mcr.microsoft.com/azurelinux/base/valkey` | `platform.values.cetirun-ai-gateway.redis.bundled.image` |
| ADX-Mon collector of the [ADX-Mon collection](../observability/) | `mcr.microsoft.com/aks/ai-runtime/adx-mon/collector` | `platform.values.cetirun-observability.collector.image` |
| ADX-Mon ingestor | `mcr.microsoft.com/aks/ai-runtime/adx-mon/ingestor` | `platform.values.cetirun-observability.ingestor.image` |
| ADX-Mon alerter, with an alert receiver | `mcr.microsoft.com/aks/ai-runtime/adx-mon/alerter` | `platform.values.cetirun-observability.alerter.image` |
| kube-state-metrics of the ADX-Mon collection | `mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics` | `platform.values.cetirun-observability.kubeStateMetrics.image` |
| cert-manager, with `publicTLS` install mode | `quay.io/jetstack/cert-manager-*` | None |

`global.registry` does not change these images. The cert-manager images always
come from `quay.io`, because the deployment accepts no other registry for
them. Custom Agents use only the images in `customRuntime.approvedImages`.

You can lock the OpenClaw and Hermes image defaults of the agent chart to
digests in a new private registry prefix. To do this, set
`OPENCLAW_IMAGE_DIGEST` and `HERMES_IMAGE_DIGEST` when you publish. Refer to the
[scripts README]({{< repo "scripts/README.md" >}}#default-platform-deployment-and-release).

These components connect to services outside the cluster:

| Endpoint | Component | Change it with |
| --- | --- | --- |
| The release registry | `make deploy` reads the images and charts. Nodes pull the images. The Operator pulls the agent chart | `release.registry` |
| The provider endpoints of the models | AI Gateway | `api_base` of each model |
| LiteLLM's price table on GitHub | Each start of an AI Gateway Pod. If the download fails, the AI Gateway uses the table in its image | `platform.values.cetirun-ai-gateway.priceTable: bundled` |
| `https://registry.npmjs.org/` | Agents that request npm skills | `sharedSkills.npmRegistry` for all Agents |
| `login.microsoftonline.com` | OAuth2 Proxy sign-in and the Broker | No setting |
| `graph.microsoft.com` and `management.azure.com` | The Operator for Agent Identity, and Agent tools | No setting |
| The Key Vault of `identity.keyVaultKeyUrl` | The Broker, to encrypt connection records | `identity.keyVaultKeyUrl` |
| `github.com`, `api.github.com`, and `*.githubcopilot.com` | GitHub Copilot sign-in and models | No setting |
| The Kusto cluster | The ADX-Mon collection. With `content.enabled`, also the AI Gateway, at the query endpoint and at its `ingest-` endpoint | `observability.adx.endpoint`, and `platform.values.cetirun-ai-gateway.content.adx.endpoint` for the AI Gateway |
| The alert receiver | The ADX-Mon alerter, at `<endpoint>/alerts` | `observability.alerter.endpoint` |
| Let's Encrypt and two public DNS resolvers | `publicTLS` | No setting. Refer to [Public DNS and TLS](../public-ingress/#prerequisites) |
