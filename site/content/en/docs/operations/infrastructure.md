---
title: "Infrastructure"
description: "Provision AKS and Azure Blob NFS with Bicep templates and deployment scripts."
weight: 10
---

CetiRun supplies Bicep templates for AKS and Azure Blob NFS. The cluster
template also creates the Azure Data Explorer resources of the ADX-Mon
collection. There is no separate `infra/monitoring` stack or Terraform
deployment. Before you provision resources, make sure that your Azure
subscription and region support the selected Kubernetes version and VM sizes.
Also make sure that they support the zones, preview features, and API version.

## Source map

| Source | Role |
| --- | --- |
| [infra/cluster/main.bicep]({{< repo "infra/cluster/main.bicep" >}}) | Cluster parameters and outputs |
| [AKS module]({{< repo "infra/cluster/modules/aks-cluster.bicep" >}}) | Node pools, networking, identity, CSI, Istio; Container Insights and managed Prometheus off |
| [ADX module]({{< repo "infra/cluster/modules/adx.bicep" >}}) | Kusto cluster, databases, query functions, and roles of the ADX-Mon collection |
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
| Monitoring | ADX-Mon into a new Kusto cluster (`adxMode: create`); Container Insights and managed Prometheus off |

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
the Bicep CLI on `PATH`, kubectl, and kubelogin. You must also have an Azure
session that is already authorized. `az aks install-cli` installs kubectl and
kubelogin. On this cluster, kubectl uses kubelogin to get its Entra tokens.

You must also set `RESOURCE_GROUP` and `LOCATION`. `LOCATION` sets only the
location of a new resource group. The cluster region comes from `location` in
the parameter file. `PARAM_FILE` selects the parameters that you examined. The
helper uses the current Azure subscription and does not select one. Confirm
that scope independently.

The helper does these steps:

1. It compiles Bicep locally (`--no-restore`), reads the compiled or supplied
   JSON parameters, and checks the effective Kubernetes version. It stops on
   compilation or version errors before any Azure operation. It saves the
   validated inputs in a private temporary directory, uses those same inputs
   for validation and deployment, and removes the directory on exit.
2. It installs or updates `aks-preview`. If necessary, it registers the managed
   Gateway API preview feature. Then it waits for the registration without a
   time limit.
3. It creates or updates the resource group.
4. It validates and creates the Bicep deployment.
5. It fetches credentials with `--overwrite-existing`.
6. It creates the Container Insights agent settings in `kube-system`, for the
   case that Container Insights is turned on later; the template turns it
   off. These settings are in `infra/cluster/container-azm-ms-agentconfig.yaml`.
   If it cannot create them, it stops with an error. If the cluster already
   has agent settings, the helper does not change them. In that case, it stops
   unless these settings disable environment variable collection.
7. It enables Gateway API and runs connected checks.

By default, the helper requests user credentials. `AKS_ADMIN_CREDENTIALS=true`
selects local admin credentials. This setting is only for an explicitly
authorized bootstrap environment that has local accounts enabled. It is not a
fallback for an Azure RBAC failure. Step 6 writes to `kube-system`. As a
result, the identity must have write access there, not only read access.

Before you run the helper, give your identity a Kubernetes role that can write
to `kube-system`, for example **Azure Kubernetes Service RBAC Cluster Admin**.
The cluster does not exist yet, so assign the role at the scope of the resource
group. **Connected, mutating:**

1. If the resource group does not exist, create it:

   ```bash
   az group create --name example-rg --location your-approved-region --output none
   ```

2. Assign the role at the scope of the resource group:

   ```bash
   RESOURCE_GROUP_ID=$(az group show --name example-rg --query id --output tsv)
   az role assignment create --role 'Azure Kubernetes Service RBAC Cluster Admin' \
     --assignee-object-id "$(az ad signed-in-user show --query id --output tsv)" \
     --assignee-principal-type User --scope "$RESOURCE_GROUP_ID" --output none
   ```

A new role assignment can take some minutes to apply. If step 6 stops with an
access error, examine the access with
`kubectl auth can-i create configmaps --namespace kube-system`. When the answer
is `yes`, run the helper again. A new run does all the steps again for the same
resource group and cluster.

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
assignment can take some minutes to apply.

If you assigned the role to yourself at the scope of the resource group before
the helper ran, it already applies to this cluster. For other administrators,
assign the role at the scope of the cluster. **Connected, mutating:**

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

The cluster template turns Container Insights and managed Prometheus off. By
default, it creates the Azure Data Explorer resources of the
[ADX-Mon collection](../observability/), which `make deploy` installs. Refer
to [Azure Data Explorer](#azure-data-explorer). On Kata node pools with the
node image `AKSAzureLinux-V3katagen2-202610.30.1` or later, the monitoring
add-on collects no container logs or metrics from those nodes: its pods there
restart in a loop, because the node kernel has no legacy iptables `nat` table.
The platform and the Agents run on these nodes.

A deployment over a cluster that an earlier template created also turns the
monitoring add-on and managed Prometheus off. It does not delete the Log
Analytics workspace, the Azure Monitor workspace, and the data collection rules
that the earlier template created. When you no longer need their data, delete
them. The commands use the names that the earlier template gave them. The
`az monitor data-collection` commands install the Azure CLI extension
`monitor-control-service` the first time. **Connected, mutating:**

```bash
CLUSTER_ID=$(az aks show --resource-group example-rg --name example-cluster \
  --query id --output tsv)
az monitor data-collection rule association delete --yes \
  --name ContainerInsightsExtension --resource "$CLUSTER_ID"
az monitor data-collection rule association delete --yes \
  --name MSProm-your-approved-region-example-cluster --resource "$CLUSTER_ID"
az monitor data-collection rule delete --yes --resource-group example-rg \
  --name MSCI-your-approved-region-example-cluster
az monitor data-collection rule delete --yes --resource-group example-rg \
  --name example-cluster-prometheus
az monitor log-analytics workspace delete --yes --resource-group example-rg \
  --workspace-name example-cluster-logs
az monitor account delete --yes --resource-group example-rg \
  --name example-cluster-prometheus
```

The earlier template created no workspace when `logAnalyticsWorkspaceId` named
an existing one, and no resources for Container Insights or managed Prometheus
that its parameters turned off. A parameter file that still sets
`enableContainerInsights`, `enablePrometheusMetrics`, or
`logAnalyticsWorkspaceId` does not compile with this template, so the helper
stops before any Azure call. Remove these parameters from the file.

If you turn Container Insights on yourself, examine its collection, its access
roles, and its costs first. By default, the add-on copies the environment
variables of every container into the `ContainerInventory` table one time each
minute. This copy includes values from Secrets. CetiRun components keep
credentials in environment variables.

The helper creates the settings that are in
`infra/cluster/container-azm-ms-agentconfig.yaml`. These settings disable that
copy. They also join log lines longer than 16 KiB that the container runtime
splits. The agent restarts itself within minutes to apply the settings.
CetiRun's own containers that hold credentials also disable the collection for
themselves, but other workloads do not. To create the settings on a cluster
that the helper did not create:

```bash
kubectl create -f infra/cluster/container-azm-ms-agentconfig.yaml
```

If `kube-system` already has a `container-azm-ms-agentconfig` ConfigMap, the
create command fails. In this case, add the two settings from the file to the
`log-data-collection-settings` of that ConfigMap. Do not apply the file over
the ConfigMap. If you do, the file replaces those settings as a whole. This
also replaces any namespaces that they exclude.

The agent chart’s session logger is also enabled by default. Before you onboard
users, see [Security](../../security/).

## Azure Data Explorer

The template creates the Azure resources of the
[ADX-Mon collection](../observability/), which `make deploy` requires. Set
`adxMode`:

| `adxMode` | Effect |
|---|---|
| `create` | The default: a new Kusto cluster `adxClusterName` in `adxResourceGroupName`, with the three databases |
| `existing` | The three databases in the existing Kusto cluster `adxClusterName` |
| `none` | No Azure Data Explorer resources. `make deploy` then needs a Kusto cluster, its databases, and the ingestor identity that exist; refer to [the prerequisites](../observability/#prerequisites) |

The deployment registers the `Microsoft.Kusto` resource provider in the
subscription of the Kusto cluster if it is not registered. This needs the
`Microsoft.Kusto/register/action` permission, which the Contributor and Owner
roles include.

A new Kusto cluster goes to the AKS region unless `adxLocation` names
another. Not every region offers every Kusto SKU; the default Dev SKU, for
example, is not offered in every region.

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

With `adxContent` set to `true`, the template also creates the database of AI
Gateway prompts and responses:

- The database `CetiRunContent` with 7 days of retention, its table `Content`,
  the ingestion mapping `ContentMapping`, and the function `GatewayContent()`.
- The user-assigned managed identity `<clusterName>-gateway-content`, with a
  federated credential for the Gateway's service account `cetirun-ai-gateway`
  in `platformNamespace`, and the roles `Ingestor` and `Viewer` on that
  database only. The ADX-Mon ingestor gets no role on it.
- The role `Viewer` for the principals in `adxContentViewers`. Every prompt and
  response is in this database: keep the list short.

The outputs `adxContentDatabase` and `adxContentClientId` are the AI Gateway's
`content.adx.database` and `content.clientId`; see "Content in Azure Data
Explorer" in the
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}).

The default SKU, `Dev(No SLA)_Standard_E2a_v4`, has one engine node and no SLA.
Use it for evaluation. A cluster with no ingestion or queries for five days
stops, and you must start it again. The outputs `adxClusterUri` and
`adxIngestorClientId` are the inputs of the `observability` block of the
`make deploy` environment file.

The resource group of an existing or a new Kusto cluster must exist before the
deployment, unless it is the cluster resource group. Kusto cluster names are
unique in Azure.
