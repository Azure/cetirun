---
title: "Infrastructure"
description: "Provision AKS and Azure Blob NFS with Bicep templates and deployment scripts."
weight: 10
---

CetiRun supplies Bicep templates for AKS and Azure Blob NFS. The cluster
template contains the monitoring configuration. There is no separate
`infra/monitoring` stack or Terraform deployment. Before you provision
resources, make sure that your Azure subscription and region support the
selected Kubernetes version and VM sizes. Also make sure that they support the
zones, preview features, and API version.

## Source map

| Source | Role |
| --- | --- |
| [infra/cluster/main.bicep]({{< repo "infra/cluster/main.bicep" >}}) | Cluster parameters and outputs |
| [AKS module]({{< repo "infra/cluster/modules/aks-cluster.bicep" >}}) | Node pools, networking, identity, CSI, Istio, monitoring |
| [cluster deploy helper]({{< repo "infra/cluster/deploy.sh" >}}) | Azure preflight, deployment, Gateway API, kubeconfig |
| [storage sources]({{< repo "infra/storage" "tree" >}}) | Shared Blob NFS account and container |
| [Istio chart]({{< repo "infra/istio" "tree" >}}) | Authentication and egress configuration |
| [cert-manager support]({{< repo "infra/cert-manager" "tree" >}}) | Pinned upstream controller support for optional public DNS and cert-manager HTTP-01 TLS |

## Cluster parameter file

By default, the helper expects `main.bicepparam`. But the repository
intentionally does not supply that file. Create a parameter file outside the
repository. In this file, put a `using` declaration that refers to
`infra/cluster/main.bicep` in your clone.

You must set these inputs: `resourceGroupName`, `location`, `clusterName`,
`dnsPrefix`, and `nodeResourceGroup`. Keep `resourceGroupName` the same as the
helper's `RESOURCE_GROUP`. The actual deployment scope comes from the helper's
value.

| Setting | Default |
| --- | --- |
| Kubernetes / AKS SKU | `1.37` / `Premium` |
| System pool | `Standard_D8s_v6`, 2–10 nodes, Ubuntu |
| Agent pool | `Standard_D16s_v6`, 15–150 nodes, AzureLinux |
| Agent2 pool | `Standard_D32s_v6`, 3–10 nodes, AzureLinux |
| Managed Istio revision | Empty; AKS selects its default revision |
| Availability zones | `1`, `2`, `3` on all three pools |
| Networking | Azure CNI overlay, Cilium, load-balancer outbound |
| Pod / Service / DNS ranges | `10.244.0.0/16`, `172.16.0.0/16`, `172.16.0.10` |
| Monitoring | Container Insights and Prometheus enabled |

The cluster template defaults to Kubernetes 1.37, the minimum for Sandbox and
CetiAdmin admission. The cluster helper checks the effective `kubernetesVersion`
before any Azure writes, including overrides in Bicep or JSON parameter files.
It rejects older versions, prereleases, and values it cannot resolve to a stable
`major.minor` or `major.minor.patch`. `make deploy` also refuses an older cluster.
See [Install CetiRun](../../getting-started/platform/#platform-chart) and
[Policy-bound Teams](../policy-teams/). To see the Kubernetes versions of a
region, run
`az aks get-versions --location your-approved-region --output table`.
If the region does not offer a stable compatible version, stop and select a
region/version that does; do not fall back to an older version. This default
change does not upgrade existing clusters. Plan any existing-cluster upgrade
separately, following the upgrade path supported by AKS.

Leave `istioRevision` empty to let AKS select its default managed Istio
revision. To pin a revision, set it only after confirming that the region lists
it as compatible with the selected Kubernetes version. Kubernetes 1.37 needs a
revision that AKS lists as compatible with 1.37, such as `asm-1-30` or later.
Before you deploy, list the revisions of the region and their compatible
Kubernetes versions:

```bash
az aks mesh get-revisions --location your-approved-region --output table
```

After the deployment, read the installed revision. The names of the managed
Istio resources contain it, and it is the `istio.revision` input of
`make deploy`:

```bash
az aks show --resource-group example-rg --name example-cluster \
  --query serviceMeshProfile.istio.revisions --output tsv
```

The initial pool counts total **20 nodes**, with 352 vCPUs at the default VM
sizes. This is not a small demonstration cluster. The system pool has
`CriticalAddonsOnly=true:NoSchedule`. Both agent pools request
`workloadRuntime: KataVmIsolation`.

Each pool starts with its minimum count, and the autoscaler keeps it between
its minimum and maximum. For a small test cluster, set lower minimum counts in
the parameter file. You can also select smaller VM sizes from the allowed
values in `main.bicep`.

By default, the platform workloads other than OAuth2 Proxy do not tolerate the
taint of the system pool. They run on the `agent` and `agent2` pools, so keep
at least one node in the `agent` pool. A pool with a minimum of 0 gets a node
only when a Pod must have one. That Pod then waits some minutes for the node.
For example:

```bicep
using '<path-to-your-clone>/infra/cluster/main.bicep'

param resourceGroupName = 'example-rg'
param location = 'your-approved-region'
param clusterName = 'example-cluster'
param dnsPrefix = 'example-cluster'
param nodeResourceGroup = 'example-rg-nodes'
param kubernetesVersion = '1.37'
param istioRevision = 'asm-example'
param systemNodePoolVmSize = 'Standard_D4ds_v6'
param agentNodePoolVmSize = 'Standard_D8ds_v6'
param agent2NodePoolVmSize = 'Standard_D8ds_v6'
param systemNodePoolMinCount = 2
param agentNodePoolMinCount = 1
param agent2NodePoolMinCount = 0
```

Before you create instances, examine the installed `kata-vm-isolation`
RuntimeClass and its scheduling rules. The runtime chart does not create that
class. Its `nodeSelector` and `tolerations` are empty by default. The chart
does not explicitly select the Bicep `agent` or `agent2` pool. Examine the
scheduling configuration of the RuntimeClass. When necessary, set the workload
placement deliberately.

The module enables managed Entra/Azure RBAC, OIDC, Workload Identity,
disk/file/Blob CSI, and the snapshot controller. It also leaves local accounts
enabled and `apiServerAccessProfile.authorizedIPRanges` empty. Before you
deploy, examine the API-server exposure and the administration policy. These
defaults are not a private-cluster configuration.

## Separate compilation from deployment

**Offline syntax check**. If the Bicep executable is already installed, you can
run this command:

```bash
bicep build infra/cluster/main.bicep --stdout > /dev/null
```

This command compiles only the template. It does not validate your parameter
file, Azure permissions, quota, region capabilities, or the cluster that
results. An Azure deployment validation or what-if is a separate connected
operation. The helper below has no render-only or dry-run mode.

## Helper side effects

To run `infra/cluster/deploy.sh`, you must have Bash, Python 3, Azure CLI,
the Bicep CLI on `PATH`, kubectl, and an Azure session that is already authorized.
You must also set `RESOURCE_GROUP` and `LOCATION`. `PARAM_FILE` selects the parameters that you
examined. The helper uses the current Azure subscription and does not select
one. Confirm that scope independently. The helper does these steps:

1. It compiles Bicep locally (`--no-restore`), reads the compiled or supplied
   JSON parameters, and checks the effective Kubernetes version. It stops on
   compilation or version errors before any Azure operation. It saves the
   validated inputs in a private temporary directory, uses those same inputs
   for validation and deployment, and removes the directory on exit.
2. It installs or updates `aks-preview`. If necessary, it registers the managed
   Gateway API preview feature.
3. It creates or updates the resource group.
4. It validates and creates the Bicep deployment.
5. It fetches credentials with `--overwrite-existing`.
6. It creates the Container Insights agent settings in `kube-system`. These
   settings are in `infra/cluster/container-azm-ms-agentconfig.yaml`. If it
   cannot create them, it stops with an error. If the cluster already has agent
   settings, the helper does not change them. In that case, it stops unless
   these settings disable environment variable collection.
7. It enables Gateway API and runs connected checks.

By default, the helper requests user credentials. `AKS_ADMIN_CREDENTIALS=true`
selects local admin credentials. This setting is only for an explicitly
authorized bootstrap environment that has local accounts enabled. It is not a
fallback for an Azure RBAC failure. Step 6 writes to `kube-system`. As a
result, the identity must have write access there, not only read access.

**Connected, mutating example:** use it only after you examine the script and
the parameters:

```bash
RESOURCE_GROUP=example-rg LOCATION=your-approved-region \
PARAM_FILE=/absolute/path/cluster.bicepparam \
  bash infra/cluster/deploy.sh
```

This is not a dry run. The helper changes cloud resources, the subscription
feature registration, and the local Kubernetes configuration. The local version
preflight runs first, but extension, feature, and resource-group writes still
occur **before Azure deployment validation**. A later validation failure does
not mean that nothing changed. Gateway API enablement is a side effect of the
helper. CetiRun's routing templates use Istio `Gateway` and `VirtualService`
resources.

## Connect to the cluster

The template enables Microsoft Entra authentication with Azure RBAC for
Kubernetes authorization. As a result, each person who runs kubectl, Helm, or
`make deploy` must have an Azure role for the Kubernetes API of the cluster.
An example is **Azure Kubernetes Service RBAC Cluster Admin**. A new role
assignment can take some minutes to apply. **Connected, mutating:**

```bash
CLUSTER_ID=$(az aks show --resource-group example-rg --name example-cluster \
  --query id --output tsv)
az role assignment create --role 'Azure Kubernetes Service RBAC Cluster Admin' \
  --assignee-object-id "<administrator-object-id>" \
  --assignee-principal-type User --scope "$CLUSTER_ID" --output none
```

To read your own object ID, run
`az ad signed-in-user show --query id --output tsv`.

Get the user credentials into a separate file. Then convert them, so that
kubectl gets its tokens through kubelogin from the Azure CLI sign-in.
`az aks install-cli` installs kubectl and kubelogin:

```bash
az aks get-credentials --resource-group example-rg --name example-cluster \
  --file /absolute/private/kubeconfig
export KUBECONFIG=/absolute/private/kubeconfig
kubelogin convert-kubeconfig -l azurecli
kubectl auth whoami
```

`kubectl auth whoami` shows the user name and the groups that Kubernetes
receives. Use these values for the admission administrators of `make deploy`.
`AKS_ADMIN_CREDENTIALS=true`, or `az aks get-credentials --admin`, gives local
admin credentials instead. Use them only in an explicitly authorized bootstrap
environment, not as a fallback for an Azure RBAC failure.

When `publicTLS` is enabled, `make deploy` uses the required
`target.kubeconfig` and `target.context`; it does not use the shell
`KUBECONFIG`. Other deployments use the kubeconfig of the shell. In either
case, read the `target` values from the intended context:

```bash
kubectl config view --minify \
  -o jsonpath='{.contexts[0].name}{"\n"}{.contexts[0].context.cluster}{"\n"}{.clusters[0].cluster.server}{"\n"}'
kubectl get namespace kube-system -o jsonpath='{.metadata.uid}{"\n"}'
```

| Output | Environment file field |
| --- | --- |
| First line of the first command | `target.context` |
| Second line | `target.cluster`, the cluster entry of the kubeconfig. It is not always the AKS cluster name |
| Third line | `target.server`, exactly as printed |
| Second command | `target.clusterUID` |

## Identity, images, and collection

If you only provision the cluster, that step does not publish images, create
all Entra registrations, issue TLS certificates, or prepare end-user provider
accounts. Complete the [platform checklist](../../getting-started/platform/).
For Agent connections, follow [Agent Identity setup](../agent-identity/). Use
it for the encryption identity and Key Vault key, provider applications,
certificates, and the coordinated Broker/MCP/Portal configuration.

For a single public Portal hostname, configure `publicTLS` in the environment
file after AKS managed Istio and Gateway API exist. The normal `make deploy`
path can configure an Azure Public IP DNS label or verify a user-managed A/CNAME
record, then issue the externally managed Gateway certificate. It does not
configure Entra, Broker/MCP or admission certificates, and it does not change
the cluster template.

By default, the cluster parameters enable Container Insights and Prometheus
metrics. Examine the workspace and data-collection resources, the retention,
the access roles, and the costs. If `logAnalyticsWorkspaceId` is empty, the
template creates a Log Analytics workspace with 30-day retention, even if
`enableContainerInsights` is false. The template enables the cluster's
monitoring add-on with its data collection rule. Container Insights collects
this data through that add-on:

- Container stdout and stderr outside `kube-system`
- Kubernetes inventory and events
- Node and container performance data

By default, the add-on also copies the environment variables of every
container into the `ContainerInventory` table one time each minute. This copy
includes values from Secrets. CetiRun components keep credentials in
environment variables.

The helper creates the settings that are in
`infra/cluster/container-azm-ms-agentconfig.yaml`. These settings disable that
copy. They also join log lines longer than 16 KiB that the container runtime
splits. The agent restarts itself within minutes to apply the settings.
CetiRun's own containers that hold credentials also disable the collection for
themselves, but other workloads do not.

Clusters that were created from earlier templates have the rule but not the
add-on. These clusters collect no container logs. Create the settings first.
Then enable the add-on:

```bash
kubectl create -f infra/cluster/container-azm-ms-agentconfig.yaml
az aks enable-addons --addons monitoring --enable-msi-auth-for-monitoring \
  --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" \
  --workspace-resource-id "$LOG_ANALYTICS_WORKSPACE_ID"
```

If `kube-system` already has a `container-azm-ms-agentconfig` ConfigMap, the
create command fails. In this case, add the two settings from the file to the
`log-data-collection-settings` of that ConfigMap. Do not apply the file over
the ConfigMap. If you do, the file replaces those settings as a whole. This
also replaces any namespaces that they exclude.

On a cluster that already ran for some time, the agent also uploads the log
files that the containers already have. It does this one time. Log lines from
before the add-on, back to the oldest file that the node keeps, also reach the
workspace.

The agent chart’s session logger is also enabled by default. Before you onboard
users, see [Security](../../security/).

## Azure Data Explorer

The template can also create the Azure resources of the optional
[ADX-Mon collection](../observability/). Set `adxMode`:

| `adxMode` | Effect |
|---|---|
| `none` | The default: no Azure Data Explorer resources |
| `create` | A new Kusto cluster `adxClusterName` in `adxResourceGroupName`, with the three databases |
| `existing` | The three databases in the existing Kusto cluster `adxClusterName` |

With `create` or `existing`, the template also creates these resources:

- The databases `CetiRunLogs`, `CetiRunMetrics` and `CetiRunAgents`, with the
  retention of `adxLogsRetentionDays`, `adxMetricsRetentionDays` and
  `adxAgentLogsRetentionDays`, 7 days in the hot cache, and 30 seconds of
  ingestion batching.
- The query functions in `infra/cluster/modules/adx/*.kql`.
- The user-assigned managed identity `<clusterName>-adx-ingestor` in the
  cluster resource group, with a federated credential for the ingestor's
  service account in `observabilityNamespace`.
- The roles `User` and `Ingestor` of that identity on the three databases.
- The role `Viewer` for the principals in `adxViewers` on the logs and metrics
  databases, and for the principals in `adxAgentLogsViewers` on the Agent logs
  database.

The default SKU, `Dev(No SLA)_Standard_E2a_v4`, has one engine node and no SLA.
Use it for evaluation. A cluster with no ingestion or queries for five days
stops, and you must start it again. The outputs `adxClusterUri` and
`adxIngestorClientId` are the inputs of the `observability` block of the
`make deploy` environment file.

The resource group of an existing or a new Kusto cluster must exist before the
deployment, unless it is the cluster resource group. Kusto cluster names are
unique in Azure.
