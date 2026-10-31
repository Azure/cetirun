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
| Kubernetes / AKS SKU | `1.34` / `Premium` |
| System pool | `Standard_D8s_v6`, 2–10 nodes, Ubuntu |
| Agent pool | `Standard_D16s_v6`, 15–150 nodes, AzureLinux |
| Agent2 pool | `Standard_D32s_v6`, 3–10 nodes, AzureLinux |
| Availability zones | `1`, `2`, `3` on all three pools |
| Networking | Azure CNI overlay, Cilium, load-balancer outbound |
| Pod / Service / DNS ranges | `10.244.0.0/16`, `172.16.0.0/16`, `172.16.0.10` |
| Monitoring | Container Insights and Prometheus enabled |

Agents of Teams that are created in the Portal run in a Sandbox. Kubernetes
1.37 or later is necessary for a Sandbox. In your parameter file, set
`kubernetesVersion` to 1.37 or later. See [Policy-bound Teams](../policy-teams/).

The initial pool counts total **20 nodes**. This is not a small demonstration
cluster. The system pool has `CriticalAddonsOnly=true:NoSchedule`. Both agent
pools request `workloadRuntime: KataVmIsolation`.

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

To run `infra/cluster/deploy.sh`, you must have Bash, Azure CLI, Bicep, kubectl,
and an Azure session that is already authorized. You must also set
`RESOURCE_GROUP` and `LOCATION`. `PARAM_FILE` selects the parameters that you
examined. The helper uses the current Azure subscription and does not select
one. Confirm that scope independently. The helper does these steps:

1. It installs or updates `aks-preview`. If necessary, it registers the managed
   Gateway API preview feature.
2. It creates or updates the resource group.
3. It validates and creates the Bicep deployment.
4. It fetches credentials with `--overwrite-existing`.
5. It creates the Container Insights agent settings in `kube-system`. These
   settings are in `infra/cluster/container-azm-ms-agentconfig.yaml`. If it
   cannot create them, it stops with an error. If the cluster already has agent
   settings, the helper does not change them. In that case, it stops unless
   these settings disable environment variable collection.
6. It enables Gateway API and runs connected checks.

By default, the helper requests user credentials. `AKS_ADMIN_CREDENTIALS=true`
selects local admin credentials. This setting is only for an explicitly
authorized bootstrap environment that has local accounts enabled. It is not a
fallback for an Azure RBAC failure. Step 5 writes to `kube-system`. As a
result, the identity must have write access there, not only read access.

**Connected, mutating example:** use it only after you examine the script and
the parameters:

```bash
RESOURCE_GROUP=example-rg LOCATION=your-approved-region \
PARAM_FILE=/absolute/path/cluster.bicepparam \
  bash infra/cluster/deploy.sh
```

This is not a dry run. The helper changes cloud resources, the subscription
feature registration, and the local Kubernetes configuration. Its extension,
feature, and resource-group writes occur **before** template validation. As a
result, if the parameter file is not valid or does not exist, this does not
mean that nothing changed. Gateway API enablement is a side effect of the
helper. CetiRun's routing templates use Istio `Gateway` and `VirtualService`
resources.

## Identity, images, and collection

If you only provision the cluster, that step does not publish images, create
all Entra registrations, issue TLS certificates, or prepare end-user provider
accounts. Complete the [platform checklist](../../getting-started/platform/).
For Agent connections, follow [Agent Identity setup](../agent-identity/). Use
it for the encryption identity and Key Vault key, provider applications,
certificates, and the coordinated Broker/MCP/Portal configuration.

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
