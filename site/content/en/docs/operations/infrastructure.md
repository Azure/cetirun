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

## Select the Azure target

Use an Azure CLI session authenticated to the intended tenant. Set the target
before running the resource and permission examples on this page:

```bash
export AZURE_SUBSCRIPTION_ID="<subscription-id>"
export AZURE_TENANT_ID="<tenant-id>"
az account show --subscription "$AZURE_SUBSCRIPTION_ID" \
  --query '{subscription:id,tenant:tenantId}' --output json
```

Confirm that the returned subscription and tenant match these values. Every
Azure resource command below passes the subscription explicitly. The `az ad`
commands use the tenant of the signed-in session. These variables do not
change the default Azure subscription.

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
| Managed Istio traffic redirection | `InitContainers`. Kata VM-isolated Agent Pods do not start with CNI chaining |
| Availability zones | `1`, `2`, `3` on all three pools |
| Networking | Azure CNI overlay, Cilium, load-balancer outbound |
| DNS forwarding | LocalDNS disabled on every pool; workloads use CoreDNS |
| Pod / Service / DNS ranges | `10.244.0.0/16`, `172.16.0.0/16`, `172.16.0.10` |
| Monitoring | ADX-Mon into a new Kusto cluster (`adxMode: create`); Container Insights and managed Prometheus off |
| Automatic upgrades | `patch` channel for Kubernetes, `NodeImage` channel for the node OS |
| Add-ons | Key Vault Secrets Provider, with rotation of the mounted secrets every 2 minutes |

The template explicitly disables AKS LocalDNS. CetiRun's network policies
allow DNS to CoreDNS Pods and deny unapproved link-local destinations. AKS can
otherwise enable LocalDNS by default, which redirects queries to a node-local
address and prevents protected workloads, including Connections MCP, from
starting. Keep this setting on new pools as well. Changing LocalDNS on an
existing pool can roll its nodes; inspect its current settings and plan that
operation separately. Do not relax Agent isolation to work around DNS failures.

With the automatic upgrade channels, AKS installs Kubernetes patch versions
and new node images without a manual step. An upgrade drains each node, so the
Agent Pods on that node start again on another node. The template configures
no maintenance window. To control the time of these upgrades, configure a
maintenance window for the cluster.

The cluster template defaults to Kubernetes 1.37, the minimum for Sandbox and
CetiAdmin admission. The cluster helper checks the effective `kubernetesVersion`
before any Azure writes, including overrides in Bicep or JSON parameter files.
It rejects older versions, prereleases, and values it cannot resolve to a stable
`major.minor` or `major.minor.patch`. `make deploy` also refuses an older cluster.
See [Install CetiRun](../../getting-started/platform/#platform-chart) and
[Policy-bound Teams](../policy-teams/). To see the Kubernetes versions of a
region, run
`az aks get-versions --subscription "$AZURE_SUBSCRIPTION_ID" --location your-approved-region --output table`.
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
az aks mesh get-revisions --subscription "$AZURE_SUBSCRIPTION_ID" \
  --location your-approved-region --output table
```

After the deployment, read the installed revision. The names of the managed
Istio resources contain it, and it is the `istio.revision` input of
`make deploy`:

```bash
az aks show --subscription "$AZURE_SUBSCRIPTION_ID" \
  --resource-group example-rg --name example-cluster \
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

### Small test environment

By default, the platform workloads other than OAuth2 Proxy do not tolerate the
taint of the system pool. They run on the `agent` and `agent2` pools, so keep
at least one node in the `agent` pool. A pool with a minimum of 0 gets a node
only when a Pod must have one. That Pod then waits some minutes for the node.
Set maximum counts as well as minimums to bound test capacity. For example,
this configuration starts with two nodes and allows at most six:

```bicep
using '../cetirun/infra/cluster/main.bicep'

param resourceGroupName = 'example-rg'
param location = 'your-approved-region'
param clusterName = 'example-cluster'
param dnsPrefix = 'example-cluster'
param nodeResourceGroup = 'example-rg-nodes'
param kubernetesVersion = '1.37'
param skuTier = 'Standard'
param systemNodePoolVmSize = 'Standard_D4as_v5'
param agentNodePoolVmSize = 'Standard_D8as_v5'
param agent2NodePoolVmSize = 'Standard_D8as_v5'
param systemNodePoolMinCount = 1
param systemNodePoolMaxCount = 2
param agentNodePoolMinCount = 1
param agentNodePoolMaxCount = 3
param agent2NodePoolMinCount = 0
param agent2NodePoolMaxCount = 1
```

Save the parameter file outside the repository and adjust `using` relative to
that file's directory. Bicep does not accept an absolute `using` path. Verify
that the region offers the selected VM sizes and supports Kata on them.

The initial two nodes use 12 vCPUs. Admission replica placement can require
another Kata node even before user Agents are created; three nodes in this
example use 20 vCPUs. The steady-state maximum is 40 vCPUs across six nodes.

Each pool also configures `maxSurge: '33%'` for upgrades. AKS rounds the surge
node count up, so the pool maxima of 2, 3, and 1 each need one extra node:
4 vCPUs for `system`, 8 for `agent`, and 8 for `agent2`. At the steady-state
maximum, upgrading one pool at a time needs up to 48 vCPUs. If surge nodes
for all three pools overlap, the cluster needs 60 vCPUs. Reserve this upgrade
capacity in both the regional vCPU quota and the Dasv5 family quota, in
addition to other resources using those quotas. A quota of 40 vCPUs cannot
support a surge upgrade when the pools are at their maxima.

One system node and a Dev/Test ADX SKU are test choices, not a production
availability design.

Before you create Agents, examine the installed `kata-vm-isolation`
RuntimeClass and its scheduling rules. The agent chart does not create that
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
the parameter file. `PARAM_FILE` selects the parameters that you examined.
Set `AZURE_SUBSCRIPTION_ID` and `AZURE_TENANT_ID` to the intended target. The
helper verifies this pair and passes the subscription to every resource
command. It never changes the default Azure subscription. Set `KUBECONFIG` to
one absolute private file outside the default kubeconfig, in a directory that
already exists. The helper restricts that file to mode `0600` and checks its
API endpoint against the returned AKS resource before writing Kubernetes objects.

The helper does these steps:

1. It compiles Bicep locally (`--no-restore`), reads the compiled or supplied
   JSON parameters, and checks the effective Kubernetes version. It stops on
   compilation or version errors before any Azure operation. It saves the
   validated inputs in a private temporary directory, uses those same inputs
   for validation and deployment, and removes the directory on exit.
2. It checks that the installed CLI supports `--enable-gateway-api`; it does
   not install or upgrade extensions. If necessary, it registers the managed
   Gateway API preview feature and waits up to `GATEWAY_REGISTRATION_TIMEOUT`
   seconds (900 by default). A timeout leaves registration pending; inspect the
   actual state before rerunning. It re-registers the provider and waits with the
   same bound, including after a previous run registered the feature.
3. It creates or updates the resource group.
4. It validates and creates the Bicep deployment.
5. It fetches credentials into the explicit private `KUBECONFIG` with
   `--overwrite-existing` and verifies the target before any Kubernetes write.
6. It creates the Container Insights agent settings in `kube-system`, for the
   case that Container Insights is enabled later. The template disables it.
   These settings are in `infra/cluster/container-azm-ms-agentconfig.yaml`.
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
   az group create --subscription "$AZURE_SUBSCRIPTION_ID" \
     --name example-rg --location your-approved-region --output none
   ```

2. Assign the role at the scope of the resource group:

   ```bash
   RESOURCE_GROUP_ID=$(az group show --subscription "$AZURE_SUBSCRIPTION_ID" \
     --name example-rg --query id --output tsv)
   az role assignment create --subscription "$AZURE_SUBSCRIPTION_ID" \
     --role 'Azure Kubernetes Service RBAC Cluster Admin' \
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
AZURE_SUBSCRIPTION_ID="<subscription-id>" AZURE_TENANT_ID="<tenant-id>" \
KUBECONFIG=/absolute/private/kubeconfig \
RESOURCE_GROUP=example-rg LOCATION=your-approved-region \
PARAM_FILE=/absolute/path/cluster.bicepparam \
  bash infra/cluster/deploy.sh
```

This is not a dry run. The helper changes cloud resources, the subscription
feature registration, and the selected private Kubernetes configuration. The
local version preflight runs first, but feature and resource-group writes still
occur **before Azure deployment validation**. A later validation failure does
not mean that nothing changed. Gateway API enablement is a side effect of the
helper. CetiRun's routing templates use Istio `Gateway` and `VirtualService`
resources.

## Connect to the cluster

Use the subscription and tenant verified in
[Select the Azure target](#select-the-azure-target) for these commands.

The template enables Microsoft Entra authentication with Azure RBAC for
Kubernetes authorization. As a result, each person who runs kubectl, Helm, or
`make deploy` must have an Azure role for the Kubernetes API of the cluster.
An example is **Azure Kubernetes Service RBAC Cluster Admin**. A new role
assignment can take some minutes to apply.

If you assigned the role to yourself at the scope of the resource group before
the helper ran, it already applies to this cluster. For other platform
administrators, assign the role at the scope of the cluster.
**Connected, mutating:**

```bash
CLUSTER_ID=$(az aks show --subscription "$AZURE_SUBSCRIPTION_ID" \
  --resource-group example-rg --name example-cluster \
  --query id --output tsv)
az role assignment create --subscription "$AZURE_SUBSCRIPTION_ID" \
  --role 'Azure Kubernetes Service RBAC Cluster Admin' \
  --assignee-object-id "<administrator-object-id>" \
  --assignee-principal-type User --scope "$CLUSTER_ID" --output none
```

To read your own object ID, run
`az ad signed-in-user show --query id --output tsv`.

Get the user credentials into a separate file. Then convert them, so that
kubectl gets its tokens through kubelogin from the Azure CLI sign-in.
`az aks install-cli` installs kubectl and kubelogin:

```bash
az aks get-credentials --subscription "$AZURE_SUBSCRIPTION_ID" \
  --resource-group example-rg --name example-cluster \
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

If you set `target.kubeconfig`, `make deploy` uses that file for every command
and ignores the shell `KUBECONFIG`. Without it, `make deploy` uses the
kubeconfig of the shell. With `publicTLS`, `target.kubeconfig` is necessary.
Every command uses the context in `target.context`. Read the `target` values
from the intended context:

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
For Agent connections, follow [Prepare Agent connections](../agent-identity/). Use
it for the encryption identity and Key Vault key, provider applications,
certificates, and the coordinated configuration of the Agent Identity Broker,
MCP, and Portal.

For a single public Portal hostname, configure `publicTLS` in the environment
file after AKS managed Istio and Gateway API exist. The normal `make deploy`
path can configure an Azure Public IP DNS label or verify a user-managed A/CNAME
record. Then it issues the externally managed certificate of the ingress gateway.
It does not configure Entra, Broker/MCP or admission certificates, and it does
not change the cluster template.

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
CLUSTER_ID=$(az aks show --subscription "$AZURE_SUBSCRIPTION_ID" \
  --resource-group example-rg --name example-cluster \
  --query id --output tsv)
az monitor data-collection rule association delete --subscription "$AZURE_SUBSCRIPTION_ID" --yes \
  --name ContainerInsightsExtension --resource "$CLUSTER_ID"
az monitor data-collection rule association delete --subscription "$AZURE_SUBSCRIPTION_ID" --yes \
  --name MSProm-your-approved-region-example-cluster --resource "$CLUSTER_ID"
az monitor data-collection rule delete --subscription "$AZURE_SUBSCRIPTION_ID" \
  --yes --resource-group example-rg \
  --name MSCI-your-approved-region-example-cluster
az monitor data-collection rule delete --subscription "$AZURE_SUBSCRIPTION_ID" \
  --yes --resource-group example-rg \
  --name example-cluster-prometheus
az monitor log-analytics workspace delete --subscription "$AZURE_SUBSCRIPTION_ID" \
  --yes --resource-group example-rg \
  --workspace-name example-cluster-logs
az monitor account delete --subscription "$AZURE_SUBSCRIPTION_ID" \
  --yes --resource-group example-rg \
  --name example-cluster-prometheus
```

The earlier template created no workspace when `logAnalyticsWorkspaceId` named
an existing one, and no resources for Container Insights or managed Prometheus
that its parameters disabled. A parameter file that still sets
`enableContainerInsights`, `enablePrometheusMetrics`, or
`logAnalyticsWorkspaceId` does not compile with this template, so the helper
stops before any Azure call. Remove these parameters from the file.

A platform that an earlier version installed can hold the AI Gateway's
PodMonitor of managed Prometheus. This PodMonitor uses the API
`azmonitoring.coreos.com/v1`, which goes away when the template turns managed
Prometheus off. Before Helm upgrades a release, it builds the objects of the
installed release, so each upgrade of such a platform fails. `make deploy`
stops before it changes the cluster, and it names the release and the
PodMonitor.

To continue, create a stand-in definition of the API, run `make deploy`, and
then delete the definition. The upgrade removes the PodMonitor from the
release, so later upgrades need no stand-in. **Connected, mutating:**

```bash
kubectl create -f infra/cluster/managed-prometheus-podmonitor-crd.yaml
kubectl wait --for=condition=Established \
  customresourcedefinition/podmonitors.azmonitoring.coreos.com
make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
kubectl delete -f infra/cluster/managed-prometheus-podmonitor-crd.yaml
```

If the create command fails because the definition exists, stop. In this
case, the cluster serves the API, and the definition belongs to another tool.
A standalone AI Gateway release that an earlier chart installed with this
PodMonitor needs the same steps around its `helm upgrade`.

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

The agent chart's session logger is also enabled by default. It copies the
conversations of each Agent to the container log of the Agent. Before you
onboard users, see [Security](../../security/).

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

- The databases `CetiRunLogs`, `CetiRunMetrics` and `CetiRunAgents`. Their
  retention is `adxLogsRetentionDays`, `adxMetricsRetentionDays` and
  `adxAgentLogsRetentionDays`. The hot cache keeps 7 days, or the retention
  when it is shorter. Ingestion batching is 30 seconds.
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
  federated credential for the AI Gateway's service account
  `cetirun-ai-gateway` in `platformNamespace`. It has the roles `Ingestor` and
  `Viewer` on that database only. The ADX-Mon ingestor gets no role on it.
- The role `Viewer` for the principals in `adxContentViewers`. Every prompt and
  response is in this database: keep the list short.

The outputs `adxContentDatabase` and `adxContentClientId` are
`observability.content.database` and `observability.content.clientId` of the
`make deploy` environment file: the AI Gateway's `content.adx.database` and
`content.clientId`. See "Content in Azure Data Explorer" in the
[AI Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}).

The logs database also gets the materialized view `GatewayDailyUsageV2`. It
has the AI Gateway calls, tokens, and spend of each day per Agent, AI Gateway
user, owner, Team, and model. It shows separately the calls that GitHub
Copilot routes answered. It keeps `adxGatewayUsageRetentionDays` days, 400 by
default. This is longer than the request log in its source table. Refer to
[Query functions](../observability/#query-functions).

The view counts the calls from its creation. Kusto fills a new view with the
earlier rows of its table (backfill) only while these rows are in the hot
cache. The rows of a `Gateway` table older than the hot cache period are not,
so the template creates the view without backfill.

Earlier templates created the view `GatewayDailyUsage` instead. It groups only
by the name of the Agent and the alias of the owner. Kusto cannot change how a
materialized view groups its rows. When you deploy this template to existing
databases, it creates `GatewayDailyUsageV2` next to the earlier view. The
earlier view keeps its rows and continues to count new calls until you drop it.

To count the earlier days in `GatewayDailyUsageV2` as well, a database admin
can build a second view with backfill. The second view counts only the rows
that the `Gateway` table still keeps. The table deletes its rows about
`adxLogsRetentionDays` days after their ingestion, but the view keeps its days
for `adxGatewayUsageRetentionDays` days. The table deletes rows in batches by
the time of their ingestion, not by their timestamps. It can lose a part of the
calls of a day or of an Agent and keep the rest. A warm cache does not bring
back rows that the table has deleted.

Therefore, do not drop `GatewayDailyUsageV2`. Replace it only when the second
view has at least its counts and its spend in each group of the view. A group
is one day, cluster, Agent, user, Team, model, route, and call origin.

The steps use two names that no view, table, or function of the logs database
has yet. Here, the names have the date of the backfill. Use your own date:

- `GatewayDailyUsageV2Backfill20261126` for the second view.
- `GatewayDailyUsageV2Previous20261126` for the current view after the
  replacement.

To list the views of the database, run `.show materialized-views`. Do not
deploy the cluster template while you do the steps. Each deployment runs
`cetirun-gateway-usage.kql`, which creates `GatewayDailyUsageV2` again, without
backfill, when the name is free. Then do these steps:

1. Keep the `Gateway` table in the hot cache for its retention. For example,
   for 90 days:

   ```kusto
   .alter table Gateway policy caching hot = 90d
   ```

2. Create the second view. Run the first command of
   `infra/cluster/modules/adx/cetirun-gateway-usage.kql` in the logs database
   with these changes:

   - Replace `.create ifnotexists` with `.create async`. Without `ifnotexists`,
     the creation fails when the name exists.
   - Add `backfill = true` to the `with` list.
   - Name the view `GatewayDailyUsageV2Backfill20261126`.

   The command returns an operation ID. Run
   `.show operations <OperationId>` until `State` is not `InProgress`.
   Continue only when `State` is `Completed`. In another state, such as
   `Failed` or `Throttled`, the view does not exist. Then stop, or run the
   command again later.
3. Set the policies of the second view. Run the two `.alter` commands of the
   file with the name `GatewayDailyUsageV2Backfill20261126`, and replace
   `__RETENTION_DAYS__`.
4. Compare the views. The query must return no rows:

   ```kusto
   union
       (GatewayDailyUsageV2 | extend Sign = -1),
       (GatewayDailyUsageV2Backfill20261126 | extend Sign = 1)
   | summarize
       Calls = sum(Sign * Calls), FailedCalls = sum(Sign * FailedCalls),
       PromptTokens = sum(Sign * PromptTokens),
       CompletionTokens = sum(Sign * CompletionTokens),
       CachedTokens = sum(Sign * CachedTokens),
       CacheCreationTokens = sum(Sign * CacheCreationTokens),
       ReasoningTokens = sum(Sign * ReasoningTokens), Spend = sum(Sign * Spend)
       by Day, Cluster, AgentUid, Agent, UserId, Owner, Team, Model,
       ProviderModel, Provider, CopilotRoute, CallOrigin
   | where Calls < 0 or FailedCalls < 0 or PromptTokens < 0
       or CompletionTokens < 0 or CachedTokens < 0 or CacheCreationTokens < 0
       or ReasoningTokens < 0 or Spend < -0.000001
   ```

   Each value is the second view minus `GatewayDailyUsageV2`, in one group of
   the view. A row is a group in which the table has lost rows that
   `GatewayDailyUsageV2` counted. The value `0.000001` allows for rounding in
   the sums of the spend. If the query returns rows, keep
   `GatewayDailyUsageV2`, drop the second view, and continue with step 6:

   ```kusto
   .drop materialized-view GatewayDailyUsageV2Backfill20261126
   ```

5. Replace the view, and keep the current view under the other name. Run both
   renames in one script. Select the whole command before you run it:

   ```kusto
   .execute database script with (ThrowOnErrors = true) <|
   .rename materialized-view GatewayDailyUsageV2 to GatewayDailyUsageV2Previous20261126
   .rename materialized-view GatewayDailyUsageV2Backfill20261126 to GatewayDailyUsageV2
   ```

   The script stops at the first command that fails. It does not undo the
   commands before it.
6. Remove the caching policy of the table. The policy of the database applies
   again:

   ```kusto
   .delete table Gateway policy caching
   ```

If the script of step 5 fails, list the views before you give the name
`GatewayDailyUsageV2` to one of them:

```kusto
.show materialized-views
| where Name startswith "GatewayDailyUsageV2"
| project Name, EffectiveDateTime
```

- Without `GatewayDailyUsageV2Previous20261126`, the first rename failed, and
  nothing was replaced. Drop the second view as in step 4.
- With `GatewayDailyUsageV2Previous20261126`, the first rename ran. A
  `GatewayDailyUsageV2` next to it is a view that a deployment created without
  backfill. Its `EffectiveDateTime` is after the start of step 5. It counts
  only the calls since its creation, which the two other views also count.
  Drop it.

After the first rename, give the name to the second view to complete the
replacement, or to the previous view to undo it. For example, to undo the
replacement:

```kusto
.rename materialized-view GatewayDailyUsageV2Previous20261126 to GatewayDailyUsageV2
```

Later deployments of the template keep the replaced view, because the template
creates `GatewayDailyUsageV2` only when it is missing. They leave
`GatewayDailyUsageV2Previous20261126` as it is. When you no longer need it,
drop it:

```kusto
.drop materialized-view GatewayDailyUsageV2Previous20261126
```

If you do not need the days that are only in `GatewayDailyUsage`, a database
admin can drop it:

```kusto
.drop materialized-view GatewayDailyUsage
```

With `adxAlerter` set to `true`, the template also creates the user-assigned
managed identity `<clusterName>-adx-alerter` for the
[ADX-Mon alerter](../observability/#alerts). It has a federated credential for
the service account `cetirun-adx-mon-alerter` in `observabilityNamespace`. It
has the role `Viewer` on the logs and metrics databases only. The output
`adxAlerterClientId` is `observability.alerter.clientId` of the `make deploy`
environment file.

The default SKU, `Dev(No SLA)_Standard_E2a_v4`, has one engine node and no SLA.
Use it for evaluation; for production, refer to
[Size Azure Data Explorer](../observability/#size-azure-data-explorer). The
template enables auto-stop on a Kusto cluster that
it creates. Such a cluster stops after five days without ingestion or queries,
and you must start it again. The outputs `adxClusterUri` and
`adxIngestorClientId` are the inputs of the `observability` block of the
`make deploy` environment file.

The resource group of an existing or a new Kusto cluster must exist before the
deployment, unless it is the cluster resource group. Kusto cluster names are
unique in Azure.

### Kubernetes audit events

With `adxAuditLogs` set to `true`, the template sends the audit events of the
AKS API server to Azure Data Explorer. These are the events of the category
`kube-audit-admin`: every request except `get` and `list`. The template creates
these resources:

- In the cluster resource group, the Event Hubs namespace
  `auditEventHubNamespaceName`, Standard tier, with one throughput unit. Its hub
  `kube-audit` keeps each message for one day. The diagnostic setting
  `cetirun-kube-audit` of the AKS cluster sends the events there.
- The system-assigned managed identity of a new Kusto cluster, with the role
  `Azure Event Hubs Data Receiver` on that namespace. With `adxMode: existing`,
  enable the system-assigned identity of the Kusto cluster before the
  deployment.
- The database `CetiRunAudit`, which keeps `adxAuditRetentionDays` days, 90 by
  default. Its table `KubeAuditLogs` has one row for each Event Hubs message.
  Its function `AuditEvents()` gives one row for each audit event.
- An Event Hubs data connection of that database for the hub of this cluster,
  and the role `Viewer` for the principals in `adxAuditViewers`. Its name is
  `kube-audit-` and a hash of the resource ID of the hub. So CetiRun clusters
  that share the Kusto cluster each keep a connection of their own.

The first events arrive a few minutes after the deployment. On a test cluster
with four nodes and two Agents, AKS sent about 30,000 events an hour. Two
thirds of them renewed leases, and the messages had about 70 MB an hour. The
Free tier of AKS sends these events too. The Event Hubs namespace bills its
throughput unit by the hour. If the first deployment fails on the data
connection because the new role is not in effect yet, run it again.

An audit event names the user, the source address, and the object of a
request. For many kinds, such as Deployments, role bindings, and webhook
configurations, it also has the request and the response. For Secrets and
ConfigMaps, it has neither. Give `CetiRunAudit` its own readers.
