---
title: "Monitoring and logs"
description: "The metrics, logs, and alerts of CetiRun, and their collection into Azure Data Explorer with ADX-Mon."
weight: 29
---

The CetiRun components write their logs to standard output, and the Operator,
the Sandbox admission server, and the AI Gateway serve Prometheus metrics. The
`cetirun-observability` chart sends these logs and metrics to Azure Data
Explorer (ADX), together with the output of the Agents, the Kubernetes events,
and the metrics of the cluster. It runs [ADX-Mon](https://github.com/Azure/adx-mon),
which CetiRun builds from pinned upstream source. It is CetiRun's collection of
logs and metrics: `make deploy` installs it with the platform. In the platform
chart, it is off by default.

ADX-Mon is in active development upstream and has no stable release line.
Treat this collection as a preview.

## Signals of CetiRun

| Component | Signal | ADX-Mon writes it to |
| --- | --- | --- |
| Operator | Metrics on port 8080: `cetirun_sandbox_agents{runtime,status,reason}` and `cetirun_sandbox_metrics_collection_success` | `CetiRunMetrics` |
| Sandbox admission server | Metrics on port 8080: `cetirun_sandbox_admission_decisions_total`, `cetirun_sandbox_admission_request_duration_seconds`, `cetirun_sandbox_pod_admission_denials_total`, and `cetirun_sandbox_admission_certificate_not_after_seconds` | `CetiRunMetrics` |
| AI Gateway | LiteLLM metrics on the port named `metrics` | `CetiRunMetrics` |
| AI Gateway | One JSON line for each model call, with the `log_type` `cetirun.ai_gateway.request`. The line has the model, the tokens, the spend, the timing, the Agent, the Team, and the outcome. It has no prompt or response content | The `Gateway` table of `CetiRunLogs`. `GatewayRequests()` reads it |
| Other platform components | Logs on standard output | The `Platform` table of `CetiRunLogs` |

The metrics ports of the Operator, the admission server, and the AI Gateway use
plain HTTP without authentication. Allow only your metrics scrapers to connect
to these ports, for example with a NetworkPolicy. The
[Operator README]({{< repo "app/cetirun-operator/README.md" >}}#sandbox-reported-status-metrics)
describes the labels and the values of the Sandbox metrics.

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
read-only root file system. It mounts `/var/log` read-only. It also mounts the
host directory `collector.hostPath` with write access, for its write-ahead log
and its read positions.

The collector accepts no data from other Pods: it has no OTLP or Prometheus
remote write receiver, and its listener binds the Pod's loopback address.

The chart also installs the ADX-Mon custom resource definitions `Function`,
`ManagementCommand`, and `SummaryRule`. The ingestor runs the Kusto commands of
these objects with its own identity, and this identity can read the Agent
logs. Allow only cluster administrators to create these objects.

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

When its value `content.enabled` is `true`, the AI Gateway writes the prompt and
the response of each call to a separate database, by default `CetiRunContent`.
It uses its own identity, and ADX-Mon has no access to this database. Refer to
[Content in Azure Data Explorer]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#content-in-azure-data-explorer).

The routes of the chart select a Pod by its labels and namespace. The routes of
Agent Pods come first, so an Agent's output never reaches the platform tables.
The chart does not collect the logs of Pods in `kube-system` and in the
namespaces that it does not list. The kubelet metrics, kube-state-metrics, and
Warning events cover all namespaces.

The platform namespaces of the chart are its release namespace and, by
default, `cetirun-credentials`, `cetirun-istio`, and `aks-istio-system`. The
access log comes from `aks-istio-ingress`. `make deploy` does not pass the
namespaces of the environment file to the chart. If you use other namespaces,
set `logs.platformNamespaces` and `logs.ingressNamespace` under
`platform.values.cetirun-observability`.

ADX-Mon also reads `adx-mon/` annotations on a Pod. An Agent's values cannot
set Pod annotations that start with `adx-mon/`. The operator refuses them for
built-in, Custom and Sandbox Agents.

## Attribution

Every row has the columns `Cluster`, `Namespace`, `Pod` and `Container`. Each
Agent has its own namespace, `cetirun-<agent>`. The owner and the Team of an
Agent are not on its Pods. The telemetry program writes them from the Agent
resources, with read-only access, whenever an Agent changes, and in a snapshot
when it starts and every hour.

`AgentLogsWithOwner()` in `CetiRunAgents` gives each line of Agent output the
Agent, owner and Team that had its namespace when the line was written. A
deleted Agent's lines keep their owner when a later Agent of the same name
gets the namespace. Its readers also need the `Viewer` role on `CetiRunLogs`.

`AgentInventory()` lists the current Agents: those in the latest complete
snapshot of each cluster, and those created or changed after that snapshot
started. A snapshot is complete once the telemetry program has written every
object of a kind. It counts once all of its entries have reached ADX: the
collector uploads batches in parallel, so the record that marks a snapshot
complete can arrive before some of its entries. Until then, the previous
snapshot counts. An Agent that was deleted while the program was not running
drops out with the snapshot it takes when it starts again. An Agent with a
delete record is never current.

Treat the content of Agent output as untrusted: an Agent can print any text.
The columns above come from the collector and the API server.

## Query functions

The cluster template creates these functions:

| Database | Function | Rows |
|---|---|---|
| `CetiRunLogs` | `GatewayRequests()` | One AI Gateway call with its Agent, owner, Team, model, tokens, spend, routing and outcome. No request or response content. `CallOrigin` is `health_check` for LiteLLM's own probes, the origin of the classifier for the classifier call of a router, and empty for a client's call |
| `CetiRunLogs` | `KubeEvents()` | One Kubernetes event version |
| `CetiRunLogs` | `AgentInventoryHistory()`, `AgentInventory()` | Each record of an Agent's namespace, owner, Team and policy; the current Agents |
| `CetiRunLogs` | `InventorySnapshots()` | One complete inventory snapshot of a kind, per cluster |
| `CetiRunAgents` | `AgentLogsWithOwner()` | Agent output with the Agent, owner and Team that had the namespace at the time |
| `CetiRunMetrics` | `CollectionHealth(window)` | One check of the collection per cluster, with `Status` `OK`, `Warning` or `Failed` |

For example, the spend of each Agent today:

```kusto
GatewayRequests()
| where Timestamp > startofday(now()) and CallOrigin != "health_check"
| summarize Calls = count(), Spend = sum(Spend) by Agent, Owner, Team, Model
```

`CollectionHealth()` checks the last 15 minutes by default:

- Platform or Gateway logs still arrive, and the metrics of the collector
  still arrive.
- The collector dropped no log lines, and no backlog was refused.
- The oldest unsent data is not older than 10 minutes. Older data gives
  `Warning`, and data older than 30 minutes gives `Failed`.
- The ingestor reports healthy.
- No container in `cetirun-system`, `cetirun-credentials`, or `cetirun-istio`
  restarted more than three times in an hour.

It does not read the Agent logs database. To see only the problems:

```kusto
CollectionHealth() | where Status != "OK"
```

A cluster that stops sending stays `Failed` until its newest row is a day
old. After that, the cluster is not in the result. The cluster template
creates no alert.

## Dashboard

`app/cetirun-observability/dashboards/cetirun.json` is an Azure Data Explorer
dashboard with two pages:

- **AI Gateway**: calls, failures, spend and tokens by Agent, owner and Team;
  calls over time; tokens by model; failed calls by error; latency by model.
  LiteLLM's health check probes are left out.
- **Platform health**: `CollectionHealth()`, container restarts, Warning
  events with CetiRun's namespaces first, and the current Agents.

To use it:

1. In the file, replace both `clusterUri` values with your Kusto cluster's
   query URI. The two data sources are the logs and metrics databases; change
   their `database` values if you renamed them.
2. Open [Azure Data Explorer dashboards](https://dataexplorer.azure.com/dashboards)
   in the tenant of the Kusto cluster. Choose **New dashboard**, then
   **File** > **Replace dashboard with file**, and save it.
3. Choose your clusters in the **Cluster** parameter, and save that as the
   default.

Readers of the dashboard need the `Viewer` role on the logs and metrics
databases. It does not read the Agent logs database.

## Alerts

CetiRun installs no alert rules. The directory
[`app/cetirun-operator/observability`]({{< repo "app/cetirun-operator/observability/README.md" >}})
has example Prometheus rules for the Sandbox status metrics of the Operator,
with `promtool` tests. Load them into a Prometheus of your own that scrapes the
Operator, after you adapt them as their README shows. The rules alert when a
scrape target is missing or down, when the collection of the metrics fails, and
when an Agent reports `EnforcementLost`.

Also watch these signals of the platform:

- The ready replicas of the Sandbox admission server. Its webhooks refuse
  writes when it is not available.
- The `Ready` and `IngressReady` conditions of the Agents.
- The errors and the spend of the AI Gateway.
- The expiry of the certificates. Refer to
  [Certificates and credentials](../certificates/).
- The use of the HOME volumes and of the AI Gateway database.
- The node counts of the node pools, compared with their maximum counts.

## Prerequisites

- A Kusto cluster. By default, the
  [cluster template](../infrastructure/#azure-data-explorer) creates one, with
  the three databases, their query functions, and the ingestor's managed
  identity.
- A user-assigned managed identity for the ingestor, with a federated
  credential for the subject
  `system:serviceaccount:<platform namespace>:cetirun-adx-mon-ingestor` and
  the cluster's OIDC issuer. The `observabilityNamespace` parameter of the
  cluster template sets this namespace. Keep it equal to `platform.namespace`.
- The roles `User` and `Ingestor` for that identity on each of the three
  databases. The ingestor creates the tables and its own functions when data
  first arrives. It needs no `Admin` role.
- The `admins` role of that identity on the functions `prom_increase`,
  `prom_rate` and `prom_delta` of the metrics database. The ingestor alters
  them each time it starts, and Kusto lets only a function's creator or an
  admin alter it. The cluster template creates the functions and grants the
  role. If you prepare the databases yourself, run for each function:
  `.add function prom_increase admins ('aadapp=<ingestor-client-id>;<tenant-id>')`.
  Without the role, only the first CetiRun cluster's ingestor starts.
- If you prepare the databases yourself, also run the scripts in
  `infra/cluster/modules/adx/`. In these scripts, replace `__LOGS_DATABASE__`
  with the name of the logs database. Also set the ingestion batching policy of
  each database. The template sets 30 seconds.
- Outbound HTTPS from the platform namespace to the Kusto cluster, its
  `ingest-` endpoint, the Azure Storage endpoints of ADX ingestion, and Entra
  ID.

## Set it up with make deploy

`make deploy` requires the `observability` block of the environment file:

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

The outputs `adxClusterUri` and `adxIngestorClientId` of the cluster template
are its `adx.endpoint` and `ingestorClientId`.

`clusterName` names this CetiRun cluster in every row. Several CetiRun clusters
can share one Kusto cluster. `make deploy` does not call Azure: it does not
examine the Kusto cluster or the roles of the identity. The ingestor examines
them when it starts, and stops if a database is missing.

To use the chart without `make deploy`, set `cetirun-observability.enabled:
true` in the platform values. Also set `clusterName`, `adx.endpoint`,
`adx.databases`, and `ingestor.clientId` under `cetirun-observability`. You can
also install `app/cetirun-observability/chart` in the platform namespace.

The chart generates the TLS certificate of the ingestor with a Helm `lookup`,
and it keeps that certificate. A tool that renders without access to the
cluster, for example `helm template`, generates a new certificate each time.
The collectors then refuse the ingestor until they restart. With such a tool,
set `ingestor.tls.secretName` to a Secret that you supply.

## Data loss and limits

- The collector keeps a write-ahead log of at most `collector.maxDiskUsage`
  bytes on the node. If the ingestor or ADX is unavailable for longer than
  that log holds, the collector drops new data. It never stops a workload.
- Delivery is at least once: a row can appear twice.
- ADX replaces a dynamic value larger than 1 MB with null. A very long log
  line can lose its body.
- Data reaches ADX some time after the log line or the scrape. The collector,
  the ingestor, and ADX each collect data for up to 30 seconds before they
  send it.
- When the collector first starts on a node, it also reads the container log
  files that the node already has. It reads back to the oldest file that the
  node keeps. It records its position in `collector.hostPath`, so a new collector
  Pod continues from there.
- ADX-Mon does not collect Kubernetes events itself. The telemetry program
  writes them; by default, warnings and events about CetiRun resources and
  namespaces.

## Azure Monitor

CetiRun does not use Container Insights or Azure Monitor managed Prometheus.
The cluster template turns both off, also on a cluster that an earlier
template created with them. [Infrastructure](../infrastructure/#identity-images-and-collection)
lists the workspaces and data collection rules of an earlier template, which a
deployment does not delete.

On Kata node pools with the node image `AKSAzureLinux-V3katagen2-202610.30.1`
or later, Container Insights and managed Prometheus collect nothing from those
nodes. Their `addon-token-adapter` container needs the legacy iptables `nat`
table, which the kernel of these images does not provide, so it restarts in a
loop. The CetiRun platform and its Agents run on those nodes. The ADX-Mon
collector does not depend on that container. On a cluster that still runs the
add-on, to find the affected pods:

```bash
kubectl get pods -n kube-system -o wide | grep -E 'ama-(logs|metrics-node)-' | grep -v Running
```
