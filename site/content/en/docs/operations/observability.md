---
title: "Monitoring and logs"
description: "The metrics, logs, and alerts of CetiRun, and their collection into Azure Data Explorer with ADX-Mon."
weight: 29
---

The CetiRun components write their logs to standard output, and the Operator,
the Sandbox admission server, and the AI Gateway serve Prometheus metrics. The
`cetirun-observability` chart sends these logs and metrics to Azure Data
Explorer (ADX). It also sends the output of the Agents, the Kubernetes events,
the node journal of the kubelet and containerd, and the metrics of the
cluster. It runs [ADX-Mon](https://github.com/Azure/adx-mon) from its images on
MCR, pinned by digest. It is CetiRun's collection of logs and metrics:
`make deploy` installs it with the platform. In the platform chart, it is off
by default.

ADX-Mon is in active development upstream, and its releases are 0.x versions.
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
| `cetirun-adx-mon-collector` | DaemonSet, every node | Reads the container log files and the kubelet and containerd journal of the node, and scrapes the kubelet and the annotated platform Pods of the node |
| `cetirun-adx-mon-ingestor` | StatefulSet with a volume | Receives the collectors' data over TLS and uploads it to ADX with its workload identity |
| `cetirun-adx-mon-alerter` | Deployment, only with an alert receiver | Runs the query of each `AlertRule` and sends the result rows to the receiver |
| `cetirun-telemetry` | Deployment | Writes the Agent inventory and the Kubernetes events as JSON lines |
| `cetirun-kube-state-metrics` | Deployment | Reports the state of Pods, workloads, nodes and claims |

The collector reads the files that the container runtime writes on the node.
Agents in Kata VM-isolated Pods write their output to the same files, so the
collector needs no access to the Agent Pods. The collector runs as root,
because the log files belong to root. It has no Linux capabilities and a
read-only root file system. It mounts `/var/log`, `/run/log`, and
`/etc/machine-id` read-only; the last two are for the journal. It also mounts
the host directory `collector.hostPath` with write access, for its write-ahead
log and its read positions.

The collector accepts no data from other Pods: it has no OTLP or Prometheus
remote write receiver, and its listener binds the Pod's loopback address.

The chart also installs the ADX-Mon custom resource definitions `Function`,
`ManagementCommand`, `SummaryRule`, and `AlertRule`. The ingestor runs the Kusto
commands of the first three with its own identity, and this identity can read
the Agent logs. The alerter runs the query of every `AlertRule`, in any
namespace, with its own identity. Allow only cluster administrators to create
these objects.

## Where the data goes

The chart uses three databases. Grant each one separately.

| Database | Tables | Content |
|---|---|---|
| `CetiRunLogs` | `Platform`, `Gateway`, `Ingress`, `Telemetry`, `Kubelet`, `Containerd` | Logs of the platform namespaces, the AI Gateway, the ingress gateway, and the telemetry program; the node journal of the kubelet and containerd |
| `CetiRunMetrics` | One table for each metric | Kubelet, kube-state-metrics, AI Gateway (LiteLLM), CetiRun operator and admission, ADX-Mon |
| `CetiRunAgents` | `AgentLogs`, `CopilotProxy` | Output of every container of the Agent Pods, Custom terminals and GitHub Copilot proxies |

Agent output contains conversation content. The session logger of the agent
chart prints one JSON record for each user input, assistant output, tool call,
and tool result of an Agent. The collector parses each record into `Body`.
`Body.kind` is `user`, `assistant`, `tool_call`, or `tool_result`.
`Body.content` holds the text, and `Body.sessionId`, `Body.ts`, `Body.model`,
and `Body.tool` describe it.

By default, OpenClaw runs most of its tools through its Tool Search tool
`tool_call`. For these tools, `Body.tool` is the name of the tool that ran, and
`Body.via` is `tool_call`. Two kinds of records keep the name `tool_call`. One is
a call that only OpenClaw's tool catalog can match to a tool. Its result record
names the tool, and `Body.toolCallId` links the two records. The other is the
result of a call that matched no tool.

An Agent command can also print any text. Give `CetiRunAgents` a smaller group
of readers and a shorter retention than the other databases.

In a new Agent Pod, the logger prints the records that HOME keeps again. The
`id` of a record identifies the repeats. For example, the conversations of the
last day, each record once:

```kusto
AgentLogs
| where Timestamp > ago(1d) and Container == "logger" and isnotempty(Body.id)
| summarize arg_min(Timestamp, Body) by Cluster, Namespace, Id = tostring(Body.id)
| project Time = todatetime(Body.ts), Namespace, Session = tostring(Body.sessionId),
    Kind = tostring(Body.kind), Content = tostring(Body.content)
| order by Namespace asc, Time asc
```

When its value `content.enabled` is `true`, the AI Gateway writes the prompt and
the response of each call to a separate database, by default `CetiRunContent`.
It uses its own identity, and ADX-Mon has no access to this database. With
`make deploy`, the `content` block of `observability` sets it. Refer to
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

The `Kubelet` and `Containerd` tables have a row for each journal entry of the
kubelet and of containerd, on every node. They show why a Pod does not start,
for example an image that cannot be pulled or a Kata VM that fails to start.
`Body.message` holds the line, and `Resource._HOSTNAME` names the node. A
kubelet line keeps its klog header, whose first letter is the severity: `I`,
`W`, `E` or `F`. For example, the errors of the last hour:

```kusto
Kubelet
| where Timestamp > ago(1h) and tostring(Body.message) startswith "E"
| project Timestamp, Node = tostring(Resource._HOSTNAME), Line = tostring(Body.message)
```

A containerd line has `key=value` fields, such as `level` and `msg`. The
`parse-kv` operator of Kusto splits them. Set `logs.node.enabled` to `false`
to leave both journals out.

## Attribution

Every row has the columns `Cluster`, `Namespace`, `Pod` and `Container`. A
metric row also has `Host`, the node whose collector scraped it. Each
Agent has its own namespace, `cetirun-<agent>`. The owner and the Team of an
Agent are not on its Pods. The telemetry program writes them from the Agent
resources, with read-only access, whenever an Agent changes, and in a snapshot
when it starts and every hour.

`AgentLogsWithOwner()` in `CetiRunAgents` gives each line of Agent output the
Agent, owner and Team that had its namespace when the line was written. A
deleted Agent's lines keep their owner when a later Agent of the same name
gets the namespace. Readers of `AgentLogsWithOwner()` also need the `Viewer`
role on `CetiRunLogs`.

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
| `CetiRunLogs` | `GatewayRequests()` | One AI Gateway call with its Agent, owner, Team, model, tokens, spend, routing and outcome. No request or response content. `CallOrigin` is `health_check` for LiteLLM's own probes, the origin of the classifier for the classifier call of a router, and empty for a client's call. `CopilotRoute` is `true` when a GitHub Copilot route of the owner answered the call |
| `CetiRunLogs` | `GatewayDailyUsageV2`, a materialized view | The AI Gateway calls, failed calls, tokens and spend of one day per cluster, Agent, AI Gateway user, owner, Team, model, provider and provider model, GitHub Copilot route and call origin, from the creation of the view. It keeps 400 days by default, longer than the request log |
| `CetiRunLogs` | `KubeEvents()` | One Kubernetes event version |
| `CetiRunLogs` | `AgentInventoryHistory()`, `AgentInventory()` | Each record of an Agent's namespace, owner, Team and Agent Policy; the current Agents |
| `CetiRunLogs` | `InventorySnapshots()` | One complete inventory snapshot of a kind, per cluster |
| `CetiRunAgents` | `AgentLogsWithOwner()` | Agent output with the Agent, owner and Team that had the namespace at the time |
| `CetiRunMetrics` | `CollectionHealth(window)` | One check of the collection per cluster, with `Status` `OK`, `Warning` or `Failed` |
| `CetiRunAudit` | `AuditEvents()` | One audit event of the AKS API server: user, source address, verb, object, and answer. Only with the [audit events](../infrastructure/#kubernetes-audit-events) of the cluster template |

The template also creates the tables that these functions read, so that the
functions work before the first rows arrive. For example, the spend of each
Agent today:

```kusto
GatewayRequests()
| where Timestamp > startofday(now()) and CallOrigin != "health_check"
| summarize Calls = count(), Spend = sum(Spend) by Agent, Owner, Team, Model
```

And the spend of each Team by month, also for months before the retention of
the request log:

```kusto
GatewayDailyUsageV2
| where CallOrigin != "health_check"
| summarize Spend = sum(Spend) by Month = startofmonth(Day), Team
```

The name of an Agent and the alias of an owner can repeat. A new Agent can get
the name of a deleted Agent, and users from two domains can have the same
alias. To count each Agent or user once, group by `AgentUid` or `UserId`.
`UserId` is the AI Gateway user of the key:

- Since CetiRun v0.2.0, the owner's Entra tenant and object IDs, as
  `<tenant>/<object>`. An Agent without them has `agent:<Agent UID>`.
- Before v0.2.0, the owner's alias. The rows of these calls keep it.

`CopilotRoute` separates the calls that the owner's GitHub Copilot
subscription paid for. Their `Spend` is the list price that the AI Gateway
charges to the budget. Refer to
[What the budget counts](../budgets/#what-the-budget-counts). For example, the
spend of the shared models for each user by month:

```kusto
GatewayDailyUsageV2
| where CallOrigin != "health_check" and not(CopilotRoute)
| summarize Spend = sum(Spend), Owners = make_set(Owner) by Month = startofmonth(Day), UserId
```

Earlier cluster templates created the view `GatewayDailyUsage`, without these
columns. Refer to [Azure Data Explorer](../infrastructure/#azure-data-explorer).

`CollectionHealth()` checks the last 15 minutes by default:

- Platform or AI Gateway logs still arrive, and the metrics of the collector
  still arrive.
- The collector dropped no log lines, and no backlog was refused: no data was
  refused because a write-ahead log was full. ADX-Mon also measures a budget
  for each table, but the chart does not enforce these budgets, so they refuse
  no data.
- The oldest unsent data is not older than 10 minutes. Older data gives
  `Warning`, and data older than 30 minutes gives `Failed`.
- The uploads of the ingestor to ADX succeed. A failed upload gives `Warning`:
  the ingestor tries it again.
- The ingestor reports healthy. When it does not, the check shows the reason,
  for example `MaxDiskUsageExceeded` for a full write-ahead log. Before
  ADX-Mon v0.4, the ingestor reports no reason.
- No container in `cetirun-system`, `cetirun-credentials`, or `cetirun-istio`
  restarted more than three times in an hour.

A counter that has not counted yet counts as zero, and a metric table that
does not exist yet reads as empty. It does not read the Agent logs database.
To see only the problems:

```kusto
CollectionHealth() | where Status != "OK"
```

A cluster that stops sending stays `Failed` until its newest row is a day
old. After that, the cluster is not in the result. The chart's alerter can
send an alert for each failed check. Refer to [Alerts](#alerts).

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

The chart runs the ADX-Mon alerter when you name a service that receives the
alerts. With `make deploy`, add the `alerter` block to `observability`:

```yaml
observability:
  # clusterName, adx and ingestorClientId, as in "Set it up with make deploy"
  alerter:
    endpoint: https://alerts.example.com
    clientId: <alerter-client-id>
    destination: example-team
```

The chart then creates the `AlertRule` `cetirun-collection-health`. Every 5
minutes, it queries `CollectionHealth()` for this cluster. Each check with the
status `Failed` becomes one alert. The alerter sends it again on each run while
the check fails, with the same `CorrelationID`. It sends nothing when the check
recovers: a receiver can resolve an alert that stops arriving.

The alerter posts each alert as JSON to `<endpoint>/alerts`. It counts only
`201 Created` as delivered. When you remove the `alerter` block, `make deploy`
removes the alerter but keeps the `AlertRule`, because Helm does not delete the
resources of its hooks. Delete the rule with
`kubectl delete alertrule cetirun-collection-health --namespace <platform namespace>`.

| Field | Content |
| --- | --- |
| `Destination` | `destination` of the `alerter` block |
| `Title` | The cluster and the failed check |
| `Severity` | `3` |
| `Summary` | The detail of the check, and links that run the query in Azure Data Explorer |
| `Description` | Where to see every check |
| `Source` | The namespace and the name of the `AlertRule` |
| `CorrelationID` | `<platform namespace>/cetirun-collection-health://cetirun-collection-health/<cluster>/<check>` |
| `CustomFields` | `Cluster` and `Check` |

When a query fails, the alerter sends an alert with the title
`Alert <namespace>/<rule> has query errors on <Kusto endpoint>`. The alerter
uses its own managed identity, with the `Viewer` role on the logs and metrics
databases only.

To alert on more, create `AlertRule` objects with a
`database`, an `interval` of at least `30s`, and a `query`. The alerter reads
the rules of all namespaces every minute. It skips a rule with a shorter
interval and logs `Skipping invalid AlertRule`; the status of that rule keeps
its last result. The query must project `Title` and `Severity`, and can
project `Summary`, `Description`, and `CorrelationId`. The alerter puts
`<namespace>/<rule>://` in front of each correlation ID, and sends the other
columns as custom fields.

The directory
[`app/cetirun-operator/observability`]({{< repo "app/cetirun-operator/observability/README.md" >}})
has example Prometheus rules for the Sandbox status metrics of the Operator,
with `promtool` tests. Adapt the rules as their README shows, then load them
into your own Prometheus that scrapes the Operator. The rules alert when a
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
  `infra/cluster/modules/adx/`, each in its database:
  - `cetirun-logs.kql`, then `cetirun-gateway-usage.kql`, in the logs database
  - `cetirun-metrics.kql`, `cetirun-health.kql`, and `adxmon-functions.kql` in
    the metrics database
  - `cetirun-agents.kql` in the Agent logs database
  - `cetirun-content.kql` in `CetiRunContent`, if the AI Gateway keeps its
    content in Azure Data Explorer

  Before you run them, replace `__LOGS_DATABASE__` in `cetirun-health.kql`
  and `cetirun-agents.kql` with the name of the logs database. Replace
  `__RETENTION_DAYS__` in `cetirun-gateway-usage.kql` with the days that the
  view keeps. Also set the
  ingestion batching policy of each database. The template sets 30 seconds.
- For the alerter, a user-assigned managed identity with a federated
  credential for
  `system:serviceaccount:<platform namespace>:cetirun-adx-mon-alerter`, and
  the `Viewer` role on the logs and metrics databases. The cluster template
  creates it with `adxAlerter: true`.
- For the Portal's usage report, a user-assigned managed identity with a
  federated credential for
  `system:serviceaccount:<platform namespace>:cetirun-portal`, and the
  `Viewer` role on the logs database. No other CetiRun cluster may write to
  that database while the identity has the role. Refer to
  [The Portal's usage identity](#the-portals-usage-identity).
- Outbound HTTPS from the platform namespace to the Kusto cluster, its
  `ingest-` endpoint, the Azure Storage endpoints of ADX ingestion, and Entra
  ID. With the alerter, also to the alert receiver.
- Pods can resolve the names of the nodes. The collector scrapes the kubelet of
  its node at the node's name, port 10250. The Azure-provided DNS of the virtual
  network resolves these names. A custom DNS server of the virtual network must
  resolve them too.

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
are its `adx.endpoint` and `ingestorClientId`. The optional `alerter` block
turns the alerter on; refer to [Alerts](#alerts). The optional `portal` block
lets the Portal read the AI Gateway usage; refer to
[The Portal's usage identity](#the-portals-usage-identity).

The optional `content` block sends the prompts and responses of the AI Gateway
to a database of their own. The spend log in PostgreSQL then keeps none:

```yaml
observability:
  # clusterName, adx and ingestorClientId, as above
  content:
    clientId: <content-client-id>
    database: CetiRunContent    # The default
```

It sets the AI Gateway's `content` values with the `clusterName` and the Kusto
cluster above. With `adxContent`, the cluster template creates the database
and the identity; its outputs `adxContentDatabase` and `adxContentClientId` are
`database` and `clientId`. Without the block, the spend log keeps prompts and
responses for seven days.

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

## The Portal's usage identity

With the optional `portal` block, the Portal reads the AI Gateway usage of this
cluster from the logs database. It reads with a managed identity of its own:

```yaml
observability:
  # clusterName, adx and ingestorClientId, as above
  portal:
    clientId: <portal-usage-client-id>
```

The block sets the Portal's `usage` values with the `clusterName`, the Kusto
cluster, and the logs database above. The identity needs a federated
credential for `system:serviceaccount:<platform namespace>:cetirun-portal`
and the `Viewer` role on the logs database. With `adxMode: create` and
`adxPortalUsage: true`, the cluster template creates it. Its output
`adxPortalUsageClientId` is `clientId`.

Kusto grants a role on a whole database, not on the rows of one cluster. With
the `Viewer` role, the identity can read every table of the logs database.
These tables include the platform logs, the ingress access log, and the
inventory. So the role has a prerequisite: no other CetiRun cluster writes to
the logs database while the identity has the role. `adxMode: create` makes the
databases for this cluster, but another installation can still use them, for
example with `adxMode: existing`.

Before another CetiRun cluster writes to the logs database, do these steps:

1. Remove the `portal` block from the environment file, and run
   `make deploy`. The Portal stops reading the usage.
2. If the cluster template created the identity, set `adxPortalUsage` to
   `false` in its parameters. Otherwise, its next deployment gives the role
   again.
3. Remove the role. Step 2 does not remove it. As an admin of the logs
   database, run:

   ```kusto
   .drop database CetiRunLogs viewers ('aadapp=<portal-usage-client-id>;<tenant-id>')
   ```

You can also delete the identity `<clusterName>-portal-usage`.

When the Portal starts, it looks for AI Gateway rows of another cluster in the
logs database. If it finds one, it turns the usage report off. This check finds
a configuration error. It does not limit the identity.

If the Portal cannot read the database, for example before the role exists, it
checks again after 30 seconds. It then doubles the wait, up to 10 minutes. The
Portal log shows the result:

| Log message | Meaning |
| --- | --- |
| `Usage report reads the AI Gateway usage from Azure Data Explorer` | The check passed. |
| `Usage report is off` | The database also holds rows of another cluster, or the configuration is incomplete. For example, the workload identity webhook did not set the variables of the Pod. The log names the reason. For rows of another cluster, remove the role with the steps above. |
| `Usage report: cannot check the logs database yet` | The Portal could not read the database. The error names the cause, for example a missing role or federated credential. |

To use the Portal chart without `make deploy`, set `usage.enabled`,
`usage.clusterName`, `usage.adx.endpoint`, `usage.adx.database`, and
`usage.adx.clientId` under `cetirun-portal` in the platform values. The cluster
needs workload identity.

## Data loss and limits

- The collector keeps a write-ahead log of at most `collector.maxDiskUsage`
  bytes on the node. If the ingestor or ADX is unavailable for longer than
  that log holds, the collector drops new data. It never stops a workload.
- The ingestor keeps at most 1 GiB of the data that it receives in memory.
  Above that, it asks the collectors to send again later, and they keep the
  data in their write-ahead logs. ADX-Mon also sets the memory limit of Go in
  the ingestor to 90% of the memory limit of its container. The chart gives
  the ingestor a memory limit of 2Gi (`ingestor.resources`), which leaves room
  for both. A limit near 1 GiB can stop the ingestor for lack of memory.
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
- In the same way, it reads the kubelet and containerd journal from the oldest
  entry that the node keeps. The first upload of each node can be large.
- ADX-Mon does not collect Kubernetes events itself. The telemetry program
  writes them; by default, warnings and events about CetiRun resources and
  namespaces.

## Size Azure Data Explorer

On a test cluster with four nodes and two Agents, the collection wrote these
amounts each day:

| Database | Rows | Data before compression |
|---|---|---|
| Metrics | About 13 million | About 5.6 GB. The container metrics of the kubelet and the Pod state of kube-state-metrics are the largest tables |
| Logs | About 200,000 | About 190 MB. The AI Gateway, with LiteLLM's health check probes, the platform, and the node journal write most of it |
| Agent logs | Some hundreds | Less than 1 MB. It grows with what the Agents print |

Kusto compresses this data about ten times. The metrics grow with the number
of containers, and the logs with the use of the AI Gateway and the Agents. When
the collection starts on a node, it also sends the kubelet and containerd
journal that the node keeps. On the test cluster, this was about 350,000 rows
and 125 MB for four nodes.

The default SKU of the cluster template, `Dev(No SLA)_Standard_E2a_v4`, has one
engine node, no SLA, and one ingestion slot. Use it for evaluation, and give each
CetiRun cluster in testing a Dev cluster of its own. With one CetiRun cluster, a
log line reached ADX in about 80 seconds. With two CetiRun clusters on one Dev
cluster, it took 12 to 20 minutes while one of them sent its nodes' old log
files.

For production data, use the Standard tier with at least two instances. For
example, set these parameters of the cluster template with `adxMode: create`:

```bicep
param adxSkuName = 'Standard_E2ads_v5'
param adxSkuTier = 'Standard'
param adxSkuCapacity = 2
```

A Standard cluster costs more than a Dev cluster: Azure Data Explorer also
charges for each of its vCores. Select the size for your ingestion and queries,
as
[Choose an optimal compute SKU](https://learn.microsoft.com/azure/data-explorer/manage-cluster-choose-sku)
describes.

## When data is late or missing

`CollectionHealth()` names the check that fails. These steps find the cause:

1. **Logs or metrics stop arriving.** Check that the ingestor Pod is ready,
   and read its log. A missing database or a missing role stops the ingestor
   when it starts.
2. **The ingestor backlog grows, or its uploads fail.** ADX takes the data more
   slowly than it arrives, or refuses it. Run
   `.show ingestion failures | where FailedOn > ago(1h)` in each database. In
   the Azure portal, read the metric *Ingestion utilization* of the Kusto
   cluster. A cluster that is too small needs a larger SKU or more instances.
3. **Only the collector backlog grows.** The collectors lose their connection
   to the ingestor. Check its Service, its NetworkPolicy, and the
   `cetirun-adx-mon-ingestor-tls` Secret. The `Host` column of
   `AdxmonCollectorWalSegmentsMaxAgeSeconds` names the nodes whose collectors
   fall behind.
4. **The collector drops log lines, or a backlog is refused.** One source writes
   more than the collection can send. Find the table that grows fastest:

   ```kusto
   union withsource = Table Platform, Gateway, Ingress, Telemetry, Kubelet, Containerd
   | where ingestion_time() > ago(1h)
   | summarize Rows = count() by Table, Namespace, Pod
   | top 10 by Rows
   ```

   Run the same query in the Agent logs database on `AgentLogs`. Stop the
   source, for example by suspending its Agent. Or leave it out:
   `logs.agents.enabled`, `logs.node.enabled`, and `metrics.drop` do that for a
   whole class of data. For more room on the node, raise
   `collector.maxDiskUsage`.

## Azure Monitor

CetiRun does not use Container Insights or Azure Monitor managed Prometheus.
The cluster template turns both off, also on a cluster that an earlier
template created with them. [Infrastructure](../infrastructure/#identity-images-and-collection)
lists the workspaces and data collection rules of an earlier template, which a
deployment does not delete. It also shows how to upgrade a platform whose
installed release still holds the AI Gateway's PodMonitor of managed
Prometheus.

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
