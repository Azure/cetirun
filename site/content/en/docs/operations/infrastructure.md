---
title: "Infrastructure"
description: "Provision AKS and Azure Blob NFS with Bicep templates and deployment scripts."
weight: 10
---

CetiRun provides Bicep templates for AKS and Azure Blob NFS. Monitoring is
configured in the cluster template; there is no separate `infra/monitoring`
stack or Terraform deployment. Before provisioning, verify that your Azure
subscription and region support the selected Kubernetes version, VM sizes,
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

The helper expects `main.bicepparam` by default, but that file is intentionally
not supplied in the repository. Create a parameter file outside the repository
with a `using` declaration pointing to `infra/cluster/main.bicep` in your clone.

Required inputs are `resourceGroupName`, `location`, `clusterName`, `dnsPrefix`,
and `nodeResourceGroup`. Keep `resourceGroupName` aligned with the helper's
`RESOURCE_GROUP`; the actual deployment scope comes from the latter.

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

The initial pool counts total **20 nodes**, not a small demonstration cluster.
The system pool has `CriticalAddonsOnly=true:NoSchedule`; both agent pools
request `workloadRuntime: KataVmIsolation`. Check the installed
`kata-vm-isolation` RuntimeClass and its scheduling requirements before creating
instances. The runtime chart does not create that class.
Its `nodeSelector` and `tolerations` default to empty; the chart does not
explicitly choose the Bicep `agent` or `agent2` pool. Review the RuntimeClass's
scheduling configuration and set workload placement deliberately when needed.

The module enables managed Entra/Azure RBAC, OIDC, Workload Identity, disk/file/
Blob CSI and the snapshot controller. It also leaves local accounts enabled
and `apiServerAccessProfile.authorizedIPRanges` empty. Review API-server
exposure and administration policy before deployment; these defaults are not a
private-cluster configuration.

## Separate compilation from deployment

**Offline syntax check**, with the Bicep executable already installed:

```bash
bicep build infra/cluster/main.bicep --stdout > /dev/null
```

This compiles the template only. It does not validate your parameter file,
Azure permissions, quota, region capabilities, or resulting cluster.
An Azure deployment validation or what-if is a separate connected operation.
The helper below has no render-only or dry-run mode.

## Helper side effects

`infra/cluster/deploy.sh` requires Bash, Azure CLI, Bicep, kubectl, an
already-authorized Azure session, `RESOURCE_GROUP`, and `LOCATION`; `PARAM_FILE`
selects the reviewed parameters. It uses the current Azure subscription rather
than selecting one. Confirm that scope independently. It:

1. Installs or updates `aks-preview` and registers the managed Gateway API
   preview feature if necessary.
2. Creates or updates the resource group.
3. Validates and creates the Bicep deployment.
4. Enables Gateway API.
5. Fetches credentials with `--overwrite-existing` and runs connected checks.

The helper requests user credentials by default. `AKS_ADMIN_CREDENTIALS=true`
opts into local admin credentials only for an explicitly authorized bootstrap
environment with local accounts enabled; it is not a fallback for an Azure
RBAC failure.

**Connected, mutating example:** only after reviewing the script and parameters:

```bash
RESOURCE_GROUP=example-rg LOCATION=your-approved-region \
PARAM_FILE=/absolute/path/cluster.bicepparam \
  bash infra/cluster/deploy.sh
```

This is not a dry run. The helper modifies subscription feature registration
and local Kubernetes configuration as well as cloud resources. Its extension,
feature, and resource-group writes occur **before** template validation, so an
invalid or missing parameter file does not imply that nothing changed.
Gateway API enablement is a helper side effect; CetiRun's routing
templates use Istio `Gateway` and `VirtualService` resources.

## Identity, images, and collection

Cluster provisioning alone does not publish images, create all Entra
registrations, issue TLS certificates, or establish end-user provider accounts.
Complete the [platform checklist](../../getting-started/platform/).

Cluster parameters enable Container Insights and Prometheus metrics by default.
Inspect the workspace and data-collection resources, retention, access roles,
and costs. A Log Analytics workspace with 30-day retention is created whenever
`logAnalyticsWorkspaceId` is empty, even if `enableContainerInsights` is false.
The agent chart’s session logger is also enabled by default; see
[Security](../../security/) before onboarding users.
