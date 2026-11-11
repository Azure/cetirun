---
title: "Plan capacity and cost"
description: "Estimate the nodes, disks, and quota that CetiRun Agents need, and find the defaults that drive cost."
weight: 14
---

Use this page to estimate the size of a cluster before you deploy it. The
numbers are the defaults of the charts and of the cluster template. They do not
include the overhead of the Kata virtual machine or of the Istio sidecar.
Measure the actual use of your Agents before you size production.

## Resources of one Agent

An OpenClaw or Hermes Agent runs in one Pod. These are the default requests
and limits of its containers:

| Container | Request | Limit |
| --- | --- | --- |
| Agent | 4 CPU, 8 GiB | 8 CPU, 16 GiB |
| Desktop, on by default | 0.5 CPU, 1 GiB | 2 CPU, 4 GiB |
| Session logger, on by default | 0.01 CPU, 64 MiB | 0.1 CPU, 128 MiB |

The Pod requests about 4.5 CPU and 9 GiB, plus the Istio sidecar. The runtime
chart sets no resources for the sidecar, so AKS managed Istio applies its
defaults. Each OpenClaw or Hermes Agent also has a 100 GiB HOME disk of the
`default` StorageClass. Custom Agents use the same default requests. Their HOME
disk has the size and the StorageClass of `customRuntime.storageSize` and
`customRuntime.storageClass`, which `make deploy` requires. A Custom Agent also
has a terminal Deployment with a request of 0.05 CPU and 64 MiB.

To change the resources of all Agents, set `resources` in
`platform.values.cetirun-operator.cetirun.helmValues`. Policy-bound Agents use
the resources of their Sandbox instead.

The `kata-vm-isolation` RuntimeClass of AKS can add a fixed overhead to each
Pod. To read it, run this command:

```bash
kubectl get runtimeclass kata-vm-isolation -o jsonpath='{.overhead}'
```

## Agents for each node

The CPU request usually limits the number of Agents on a node:

| Pool | Default VM size | Agents for each node |
| --- | --- | --- |
| `agent` | `Standard_D16s_v6`, 16 vCPUs | About 3 |
| `agent2` | `Standard_D32s_v6`, 32 vCPUs | About 6 |

With the minimum node counts, the `agent` pool holds about 45 Agents and the
`agent2` pool about 18 Agents. CetiAdmin, which `make deploy` enables by
default, uses the default requests and counts as one of these Agents. The
platform workloads, other than OAuth2 Proxy and its session store, also run on
these pools. The autoscaler adds nodes up to the maximum count of each pool, if
the vCPU quota of the region allows it.

A node with 4 vCPUs cannot run an Agent with the default requests. If you
select a smaller VM size, also lower the `resources` of the Agents.

Each HOME disk is an Azure data disk. A VM can attach only a limited number of
data disks, and the limit depends on the VM size. This limit can be lower than
the CPU limit for small VM sizes.

## Check the vCPU quota

At their minimum counts, the default node pools have 20 nodes and 352 vCPUs of
the Dsv6 family:

- 2 `system` nodes with 8 vCPUs
- 15 `agent` nodes with 16 vCPUs
- 3 `agent2` nodes with 32 vCPUs

Before you deploy, compare these numbers with the vCPU quota of the region:

```bash
az vm list-usage --location your-approved-region --output table
```

Examine the total regional vCPUs and the vCPUs of the Dsv6 family. For a
smaller cluster, set lower minimum counts or smaller VM sizes in the parameter
file. Refer to [Infrastructure](../infrastructure/#cluster-parameter-file).

## Platform workloads

| Workload | Replicas | Request of each replica |
| --- | --- | --- |
| Operator | 2 | 0.1 CPU, 128 MiB |
| Sandbox admission server | 2 | 0.1 CPU, 128 MiB |
| Portal | 2 | 0.1 CPU, 256 MiB |
| AI Gateway | 2 | None. Set `platform.values.cetirun-ai-gateway.resources` |
| AI Gateway PostgreSQL and Valkey | 1 each | None. PostgreSQL has a 100 GiB disk by default |
| Broker | 2 | 0.1 CPU, 128 MiB, with the Entra auth sidecar |
| Connections MCP | 2 | 0.05 CPU, 64 MiB |
| OAuth2 Proxy, on the `system` pool | 2 | 0.1 CPU, 128 MiB |
| Session store of OAuth2 Proxy, on the `system` pool | 1 | 0.05 CPU, 64 MiB, and a 1 GiB disk |

The README of the AI Gateway chart gives measured values and a starting size.
Refer to [Resources]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#resources).

## Defaults that drive cost

| Item | Default | Where to change it |
| --- | --- | --- |
| AKS pricing tier | `Premium` | `skuTier` in the cluster parameter file |
| Node pools | 20 nodes and 352 vCPUs at the minimum counts | VM sizes and counts in the cluster parameter file. Refer to [Infrastructure](../infrastructure/) |
| Node OS disks | 128 GB for each node | The OS disk parameters of the cluster template |
| HOME disks | 100 GiB for each Agent | For new OpenClaw and Hermes Agents, `persistence.size` in `platform.values.cetirun-operator.cetirun.helmValues`. For Custom Agents, `platform.values.cetirun-operator.customRuntime.storageSize` |
| AI Gateway database | 100 GiB | `postgresql.bundled.persistence.size`. You cannot change it after the first run |
| Log Analytics and managed Prometheus | On while `adxMode` has its default, `none`. A new Log Analytics workspace keeps the logs for 30 days | `enableContainerInsights`, `enablePrometheusMetrics`, and `logAnalyticsWorkspaceId` |
| Azure Data Explorer, with `adxMode: create` | The `Dev(No SLA)_Standard_E2a_v4` SKU | The `adx` parameters. Refer to [Azure Data Explorer](../infrastructure/#azure-data-explorer) |
| Agents of each user | 1 | `platform.values.cetirun-portal.features.maxInstancesPerUser` |
| Monthly model budget of each new Agent key | 10000 USD | `platform.values.cetirun-operator.aiGateway.defaultBudget`. Agent Policies set their own budgets |

Model use is a large variable cost. The Operator sets the default budget when
it creates the key of an Agent. A later change of the default does not change
the keys that exist. Refer to
[Choose a model](../../guides/models/) and
[Policy-bound Teams](../policy-teams/).

## A small test cluster

For a test cluster, start from the example parameter file in
[Cluster parameter file](../infrastructure/#cluster-parameter-file). Its
`agent` nodes have 8 vCPUs, so with the default Agent requests each node holds
one Agent.
