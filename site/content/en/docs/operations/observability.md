---
title: "Observability with ADX-Mon"
description: "Collect the logs, Kubernetes events and metrics of the platform and the Agents into Azure Data Explorer."
weight: 29
---

The optional `cetirun-observability` chart sends the logs of the platform and
the Agents, the Kubernetes events, and the metrics of the cluster to Azure Data
Explorer (ADX). It runs [ADX-Mon](https://github.com/Azure/adx-mon), which
CetiRun builds from pinned upstream source. It is off by default.

ADX-Mon is in active development upstream and has no stable release line.
Treat this collection as a preview.

## What the chart runs

| Workload | Kind | Purpose |
|---|---|---|
| `cetirun-adx-mon-collector` | DaemonSet, every node | Reads the container log files of the node, and scrapes the kubelet and the annotated platform Pods of the node |
| `cetirun-adx-mon-ingestor` | StatefulSet with a volume | Receives the collectors' data over TLS and uploads it to ADX with its workload identity |
| `cetirun-telemetry` | Deployment | Writes the Agent inventory and the Kubernetes events as JSON lines |
| `cetirun-kube-state-metrics` | Deployment | Reports the state of Pods, workloads, nodes and claims |

The collector reads the files that the container runtime writes on the node.
Agents in Kata VM-isolated Pods write their output to the same files, so the
collector needs no access to the Agent Pods. The collector runs as root,
because the log files belong to root. It has no Linux capabilities and a
read-only root file system. It mounts `/var/log` read-only.

The collector accepts no data from other Pods: it has no OTLP or Prometheus
remote write receiver, and its listener binds the Pod's loopback address.

## Where the data goes

The chart uses three databases. Grant each one separately.

| Database | Tables | Content |
|---|---|---|
| `CetiRunLogs` | `Platform`, `Gateway`, `Ingress`, `Telemetry` | Logs of the platform namespaces, the AI Gateway, the ingress gateway, and the telemetry program |
| `CetiRunMetrics` | One table for each metric | Kubelet, kube-state-metrics, AI Gateway (LiteLLM), CetiRun operator and admission, ADX-Mon |
| `CetiRunAgents` | `AgentLogs`, `CopilotProxy` | Output of every container of the Agent Pods, Custom terminals and GitHub Copilot proxies |

Agent output can contain conversation content: for example, what a session
logger or an Agent command prints. Give `CetiRunAgents` a smaller group of
readers and a shorter retention than the other databases.

The collector routes a Pod by its labels and namespace, not by annotations on
the Pod. The routes of Agent Pods come first, so an Agent's output never
reaches the platform tables. Pods in `kube-system` and in namespaces that the
chart does not list are not collected.

An Agent's values cannot set Pod annotations that start with `adx-mon/`. The
operator refuses them for built-in, Custom and Sandbox Agents.

## Attribution

Every row has the columns `Cluster`, `Namespace`, `Pod` and `Container`. Each
Agent has its own namespace, `cetirun-<agent>`. The owner and the Team of an
Agent are not on its Pods. The telemetry program writes them from the Agent
resources, with read-only access. Join on the namespace:

```kusto
AgentLogs
| lookup kind=leftouter (
    database("CetiRunLogs").AgentInventory()
    | project Cluster, Namespace = AgentNamespace, Agent = AgentName, Owner, Team
  ) on Cluster, Namespace
```

The function `AgentLogsWithOwner()` in `CetiRunAgents` does the same join.
Treat the content of Agent output as untrusted: an Agent can print any text.
The columns above come from the collector and the API server.

## Query functions

The cluster template creates these functions:

| Database | Function | Rows |
|---|---|---|
| `CetiRunLogs` | `GatewayRequests()` | One AI Gateway call with its Agent, owner, Team, model, tokens, spend and outcome. No request or response content |
| `CetiRunLogs` | `KubeEvents()` | One Kubernetes event version |
| `CetiRunLogs` | `AgentInventoryHistory()`, `AgentInventory()` | Each record of an Agent's namespace, owner, Team and policy; the current Agents |
| `CetiRunAgents` | `AgentLogsWithOwner()` | Agent output with the Agent, owner and Team |

For example, the spend of each Agent today:

```kusto
GatewayRequests()
| where Timestamp > startofday(now())
| summarize Calls = count(), Spend = sum(Spend) by Agent, Owner, Team, Model
```

## Prerequisites

- A Kusto cluster. The [cluster template](../infrastructure/#azure-data-explorer)
  can create one, with the three databases, their query functions, and the
  ingestor's managed identity.
- A user-assigned managed identity for the ingestor, with a federated
  credential for the subject
  `system:serviceaccount:<platform namespace>:cetirun-adx-mon-ingestor` and
  the cluster's OIDC issuer.
- The roles `User` and `Ingestor` for that identity on each of the three
  databases. The ingestor creates the tables and its own functions when data
  first arrives. It needs no `Admin` role.
- Outbound HTTPS from the platform namespace to the Kusto cluster, its
  `ingest-` endpoint, the Azure Storage endpoints of ADX ingestion, and Entra
  ID.

## Turn it on with make deploy

Add the `observability` block to the environment file:

```yaml
observability:
  clusterName: example
  adx:
    endpoint: https://<kusto-cluster>.<region>.kusto.windows.net
    databases:
      logs: CetiRunLogs
      metrics: CetiRunMetrics
      agentLogs: CetiRunAgents
  ingestorClientId: <ingestor-client-id>
```

`clusterName` names this CetiRun cluster in every row. Several CetiRun clusters
can share one Kusto cluster. `make deploy` does not call Azure: it does not
examine the Kusto cluster or the roles of the identity. The ingestor examines
them when it starts, and stops if a database is missing.

To use the chart without `make deploy`, set the same values under
`cetirun-observability` in the platform values, or install
`app/cetirun-observability/chart` in the platform namespace.

## Data loss and limits

- The collector keeps a write-ahead log of at most `collector.maxDiskUsage`
  bytes on the node. If the ingestor or ADX is unavailable for longer than
  that log holds, the collector drops new data. It never stops a workload.
- Delivery is at least once: a row can appear twice.
- ADX replaces a dynamic value larger than 1 MB with null. A very long log
  line can lose its body.
- Data reaches ADX about 30 to 60 seconds after the log line or the scrape,
  with the template's ingestion batching of 30 seconds.
- When the collector first starts on a node, it also reads the container log
  files that the node already has, back to the oldest file that the node
  keeps. It records its position in `collector.hostPath`, so a new collector
  Pod continues from there.
- ADX-Mon does not collect Kubernetes events itself. The telemetry program
  writes them; by default, warnings and events about CetiRun resources and
  namespaces.

## Container Insights

When the cluster template creates or uses Azure Data Explorer (`adxMode`
`create` or `existing`), it turns Container Insights and managed Prometheus off
by default, and creates no Log Analytics workspace or Azure Monitor workspace.
Set `enableContainerInsights` or `enablePrometheusMetrics` to `true` to keep
them next to ADX-Mon, for example to compare the two. Both collect the same
container output, so each one needs its own access review. On a cluster that
already runs them, the template turns them off at its next deployment.

On Kata node pools with the node image `AKSAzureLinux-V3katagen2-202610.30.1`
or later, Container Insights and managed Prometheus collect nothing from those
nodes. Their `addon-token-adapter` container needs the legacy iptables `nat`
table, which the kernel of these images does not provide, so it restarts in a
loop. The CetiRun platform and its Agents run on those nodes, so their logs do
not reach Log Analytics. The ADX-Mon collector does not depend on that
container: use ADX-Mon to collect from Kata node pools. To find the affected
pods:

```bash
kubectl get pods -n kube-system -o wide | grep -E 'ama-(logs|metrics-node)-' | grep -v Running
```
