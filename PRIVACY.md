# Runtime Data and Privacy

CetiRun runs Agents on infrastructure that the organization that runs the
deployment selects. This document describes how data is handled, as the
repository's application code, charts, and infrastructure templates show. It is
not a statement that a particular deployment was inspected. It is also not a
promise about third-party services or a substitute for the privacy notice of
the organization that runs the deployment.

**The default configuration can keep conversation content in container logs,
in the AI Gateway's database, and in the memory notes of each Agent.** The AI
Gateway keeps the request and the response of each model call in its database
for seven days. With `content.enabled`, it keeps them in an Azure Data Explorer
database instead. The agent chart enables the session logger, and `make deploy`
installs the ADX-Mon collection, which keeps container output in Azure Data
Explorer. Memory, which is on by default, keeps the notes of each Agent in its
HOME. Before you allow sensitive workloads, check these settings. No single
CetiRun setting disables logging, telemetry, and network activity across every
component.

## Data processed by CetiRun

| Surface | Data and purpose | Where it can be processed or stored |
| --- | --- | --- |
| Portal and sign-in | User alias, preferred username, display name, email claims, group authorization, and Portal administrator status. These items are used to identify users and authorize access. | Browser, OAuth2 Proxy/identity provider, Portal request context, and application logs. Agent ownership and profile fields are also stored in Kubernetes resources. |
| Agent management | Agent and Team names, owner alias, model selection, profile information, configuration, status, events, and resource identifiers | Portal, operator, Kubernetes API objects, Helm release state, and operational logs |
| Agent activity | Prompts, responses, files, tool arguments/results, session history, and user/team context | Agent process and HOME storage. Configured model, tool, and channel services. The AI Gateway's database, or its Azure Data Explorer content database when `content.enabled` is on, for seven days by default. Possibly container logs. |
| Authentication material | The OpenClaw gateway token, the Hermes API key, the AI Gateway virtual key, provider credentials, OAuth device-flow state, and optional Tailscale credentials | Kubernetes Secrets, mounted configuration, agent home files, process memory, and the services that authenticate them |
| Browser and terminal | Commands/output, visited pages, downloads, screenshots, browsing history, and browser login state | Agent processes and HOME storage. Websites or services that the browser/tools contact. Terminal/desktop streams that go to the user. |
| CetiAdmin | The operations Agent, enabled by default. It can read the Secrets and, through `kubectl exec`, the HOME files of each standard Agent, which does not run in a Sandbox. It can also read the Pod logs and diagnostic resources of the Agents that it manages, and of each namespace in `cetiadmin.platformReadNamespaces`. When this list contains the platform namespace, CetiAdmin can read the logs of the AI Gateway, the Portal, and the operator. Refer to [CetiAdmin](site/content/en/docs/operations/cetiadmin.md). | The CetiAdmin Agent and its HOME. Its model context, the configured model provider, and the AI Gateway's database, for seven days by default. The container log of its session logger, which the ADX-Mon collection can copy. The Slack channel of the monitor, when `cetiadmin.monitor.deliver` is `slack`. |
| Agent memory | Facts and lessons that the Agent keeps from its sessions. All sessions that the agent admits can read and write them, also channel and group chats. A reply in a group can quote them. | The `$HOME/.cetirun/memory` directory on the HOME volume. The AI Gateway receives the model calls that use the notes. |
| Connections | The external accounts that an owner connects to an Agent, for example GitHub, Google, or Azure, and the encrypted tokens of these accounts | The Agent Identity Broker, in Kubernetes Secrets of the Broker namespace and of the Agent namespace. Connections MCP and the connected services, for each call that the Agent makes. |
| Portal Chat history | The conversations of an owner with a Custom Agent in Portal Chat | The IndexedDB database `cetirun-portal-chat` in the owner's browser. The Custom Agent. |

The Portal does not show only public metadata. For an OpenClaw or Hermes
Agent, the authorized Portal API response can include credentials. These are
the OpenClaw gateway token, the Hermes API key, and a configured Tailscale auth
key. Treat browser access to an Agent and its API responses as access to
sensitive information. Do not publish browser network captures, session files,
or support bundles before you examine them.

For the memory controls of an owner, read the
[Private Memory Context](site/content/en/docs/reference/agent-harness.md#private-memory-context)
reference.

Source: [Portal claims](app/cetirun-portal/internal/auth/claims.go),
[authentication middleware](app/cetirun-portal/internal/auth/middleware.go),
[instance handlers](app/cetirun-portal/internal/handler/instance.go),
[Portal Chat storage](app/cetirun-portal/web/src/chat/store.ts),
[memory owner access](app/cetirun-memory-context/OWNER_ACCESS.md),
[Broker](app/cetirun-agent-broker/README.md),
[Agent API types](app/cetirun-operator/api/v1alpha1/agent_types.go),
[agent configuration templates](app/cetirun-chart/templates/agents/openclaw/_runtime.tpl),
and [Hermes configuration templates](app/cetirun-chart/templates/agents/hermes/_runtime.tpl).

## Logging and metrics

### Session and application logs

`logger.enabled` is `true` by default. It turns on two parts:

- **The session log plugin of the runtime.** OpenClaw's plugin reads the new
  messages of each session from the transcript that OpenClaw stores. Hermes's
  plugin receives each turn and each tool call from the hooks of Hermes. The
  plugin writes one JSON record for each user input, assistant output, tool
  call, and tool result of the sessions, also of the sessions of messaging
  channels. It writes them to
  `/home/vibe/.openclaw/cetirun-session-log/` or
  `/home/vibe/.hermes/cetirun-session-log/` in HOME.
- **The logger sidecar.** It copies these records to stdout, where the cluster's
  log collection captures them.

The records do not include system prompts, the history that the runtime sends
with each model request, reasoning, or the data that a provider needs to
continue a response. Before the plugin writes a record, the redaction of the
runtime masks the secrets that it recognizes, for example API keys and tokens.
This redaction does not find every secret or every item of personal data. Tool
calls and tool results can contain file contents, command output, and other
data that the Agent read.

The plugin shortens the content of a record that is longer than the line limit
of 16,000 bytes, and marks the record with `"truncated": true`. It keeps the
newest records in HOME: a file of up to 8 MiB and one rotated file. In a new
Pod, the logger copies these files again from their start, so the same record
can occur more than once in the log store. The `id` of each record identifies
these repeats. The processes of the Agent write the records, so the Agent can
change them before the logger copies them. These limits are not a
data-retention or privacy guarantee. Anyone with access to the container logs
that contain this output, or to their downstream log store, can possibly read
the content.

The plugins export only the conversations from their start in an Agent on.
After an upgrade to a chart version with them, the earlier messages of a
continued session are not exported. When `logger.enabled` becomes `false`, the
next start of the Agent removes the plugin from its configuration in HOME and
deletes the plugin's files. While the logger is off, nothing is recorded, and
after it is on again, the plugin exports only the conversations from then on.

The logger does not copy the log files of the runtimes, such as
`/home/vibe/.hermes/logs/gateway.log`.

Portal and operator logs also include operational information, for example
aliases, display names, Team and Agent names, resource IDs, access URLs,
reconciliation status, and errors. Some update logs record field names instead
of values. But this does not prove that all logs are free of personal data or
secrets. Agent, terminal, auth-helper, and optional Tailscale processes also
write stdout/stderr.

Source: [chart defaults](app/cetirun-chart/values.yaml),
[session logger](app/cetirun-chart/configs/session-logger.mjs),
[OpenClaw session log plugin](app/cetirun-chart/agents/openclaw/plugins/cetirun-session-log/exporter.mjs),
[Hermes session log plugin](app/cetirun-chart/agents/hermes/plugins/cetirun-session-log/__init__.py),
[OpenClaw entrypoint](app/cetirun-chart/agents/openclaw/bootstrap/entrypoint.sh),
[Hermes entrypoint](app/cetirun-chart/agents/hermes/bootstrap/hermes-entrypoint.sh),
[Portal handlers](app/cetirun-portal/internal/handler/instance.go),
and [operator reconciliation](app/cetirun-operator/internal/controller/agent_controller.go).

### AI Gateway request log and metrics

The AI Gateway's `requestLog.enabled` is `true` by default. Each model call
then writes one JSON line with the call's metadata to the stdout of the AI
Gateway. This metadata contains the virtual key's alias and hash, the user ID,
the Team, and the Agent's name and UID recorded on the key. It also contains
the model, token counts, spend, timing, the client's address and user agent,
and error text for failed calls. Error text can quote the provider's response.
The line contains no request or response content.

By default, `metrics.enabled` is `false` in the AI Gateway chart and `true` in
the platform chart. When this setting is enabled, each AI Gateway Pod serves
Prometheus metrics without authentication on a separate port. Their labels
include virtual key aliases and hashes, user IDs, and Team names, together with
spend. Anything that can reach the port can read them. The ADX-Mon collection
scrapes them into Azure Data Explorer where it runs.

The PostgreSQL database of the AI Gateway keeps daily spend totals for each
key, user, and Team. Unless `spendLogs.enabled` is `false`, the database also keeps
LiteLLM's own record of each call in `LiteLLM_SpendLogs`. The recorded calls
also include health-check probes. Each record contains the call's metadata, for
example the key, user, Team, model, token counts, spend, timing, and the error
of a failed call.

By default (`spendLogs.storePrompts: true`), each record also contains the
request and the response of the call. These include the messages, images,
tool calls, and tool results that an Agent sends, and the model's answer.
LiteLLM shortens each value that is longer than 2,048 characters, and keeps its
start and its end. As a result, a long message or an image is stored only in
part. The request log pages of the AI Gateway console show these records.
Anyone who can sign in to the console, use the AI Gateway master key, or read
its database or its backups can read them.

With `spendLogs.storePrompts: false`, each record contains only the metadata.
This setting does not remove the content of records that are already stored.
A `general_settings` block that is saved in the console can override it.

With `content.enabled: true`, the AI Gateway writes the request and the
response of each call to an Azure Data Explorer database instead:
`CetiRunContent` when the cluster template creates it. The spend log then keeps
no content, whatever `spendLogs.storePrompts` says. Calls that LiteLLM makes itself, health check
probes and auto router classifier calls, are not written. The content is
complete, not shortened, except
that inline images and files and encrypted reasoning are replaced by notes with
their type and size. Each record names the Agent, its owner and Team, and the
LiteLLM user and Team of the key. The AI Gateway writes and reads it with its
own workload identity; the ADX-Mon collector and ingestor have no access to that
database. The console's request pages read a call's content from there. They
show it to administrators, and to other console users only for their own calls
and Teams. People with the `Viewer` role on that database can read every call.
The database keeps the content for its retention, seven days in the cluster
template. Content written while ADX cannot be reached is lost after three
attempts, and while the queue of the AI Gateway is full.

When these rows and the stored health-check results are older than
`spendLogs.retention`, LiteLLM deletes them. The default value is seven days,
and the check occurs every hour. An empty retention keeps them.

Source: [AI Gateway chart defaults](app/cetirun-ai-gateway/chart/values.yaml),
[platform chart defaults](app/cetirun-platform/values.yaml),
[request log callback](app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py),
the [entrypoint that registers it](app/cetirun-ai-gateway/entrypoint.py),
[LiteLLM configuration](app/cetirun-ai-gateway/chart/templates/configmap.yaml),
and the AI Gateway chart's
[spend log section](app/cetirun-ai-gateway/chart/README.md#litellms-spend-log).

### Infrastructure collection and traffic metadata

The cluster template turns Container Insights and managed Prometheus off.
`make deploy` installs the `cetirun-observability` chart, which sends logs,
Kubernetes events and metrics to Azure Data Explorer with ADX-Mon. Its
collector reads the container output of the platform namespaces into the
`CetiRunLogs` database. This database also receives the access log of the
ingress gateway and the request log of the AI Gateway. The collector reads the
output of every container of the Agent Pods, Custom terminals, and GitHub
Copilot proxies into the `CetiRunAgents` database. That output includes the
session logger's output that is described above and what Agents print
themselves. This output can include conversation text. The collector reads
container output from the node logs and does not copy container environment
variables. It also reads the node journal of the kubelet and containerd into
the `Kubelet` and `Containerd` tables of `CetiRunLogs`. These entries name
Pods, containers, and images, and the errors of their starts.

The chart's telemetry program writes each Agent's namespace, owner alias,
display name, email, Entra tenant and object IDs, Team and Agent Policy into
`CetiRunLogs`. By default, it also writes the warning events and the events
about CetiRun resources and namespaces. Metrics go to the `CetiRunMetrics`
database. The AI Gateway's metric labels include key aliases, user IDs, and
Team names. With an alert receiver, the alerter sends that receiver the
cluster name and the failed checks of the collection. Retention is a property
of each database: the cluster template sets 90 days for logs, 30 days for
metrics, and 14 days for Agent logs. The materialized view
`GatewayDailyUsageV2` in `CetiRunLogs` has its own retention, 400 days by
default. It keeps the calls, tokens, and spend of each day per Agent, owner
alias, AI Gateway user ID, Team, and model. Since CetiRun v0.2.0, this user ID
is the owner's Entra tenant and object IDs. The view `GatewayDailyUsage` of
earlier cluster templates keeps its rows until an administrator drops it.
Successful collection still depends on the deployed collectors, permissions,
and settings. Source configuration alone does not prove that a live cluster
collects data.

With the `portal` block of the `observability` input, the Portal reads the AI
Gateway usage of its cluster from `CetiRunLogs`. It reads with a managed
identity that has the `Viewer` role on that database. The role covers every
table of the database, including the owner aliases, emails, and Entra IDs of
the inventory. Give it only on a logs database that holds the rows of one
cluster. At start, the Portal checks whether the database holds AI Gateway rows
of another cluster. It reads only the name of such a cluster, and turns the
usage report off.

With the cluster template parameter `adxAuditLogs`, the AKS API server sends
its audit events through an Event Hub, which keeps each message for one day,
into the database `CetiRunAudit`. These are all requests except `get` and
`list`. An event names the user, the source address, the user agent, and the
object of a request. For many kinds, such as Deployments, role bindings, and
webhook configurations, it also holds the request and the response. For
Secrets and ConfigMaps, it holds neither.

A cluster that runs Container Insights anyway collects container stdout and
stderr into a Log Analytics workspace. By default, Container Insights also
copies the environment variables of every container into its
`ContainerInventory` table. This copy includes values that come from
Kubernetes Secrets. The cluster helper creates the settings in
`infra/cluster/container-azm-ms-agentconfig.yaml`, which disable that
collection for the cluster. On a cluster that already has agent settings, the
helper examines them to find if they disable that collection.

Some CetiRun containers that keep credentials in their environment also
disable the collection for themselves with `AZMON_COLLECT_ENV=False`. They are
the AI Gateway with its bundled PostgreSQL and Valkey, the GitHub Copilot proxy
of each OpenClaw and Hermes Agent, and OAuth2 Proxy with its Valkey session
store. Three other containers keep credentials in their environment but do not
set the variable. The credentials of the Agent runtime container include the
AI Gateway key and the native authentication keys of the Agent. The GitHub
Copilot proxy of a Custom Agent keeps its key from the Secret
`ai-gateway-copilot-proxy`. The Broker keeps the OAuth client secrets of its
GitHub and Google providers when you configure them. A cluster that runs the
add-on without the cluster-wide settings collects these values.

The Istio chart sets `egress.accessLog.enabled` to `true` by default. Its Envoy
filters write traffic metadata to stdout. The filters apply to each proxy in the
mesh, including the ingress gateway, so they also record the requests of
users. This metadata includes timestamps,
authority/host, HTTP method/path when available, status, and byte counts. It
also includes duration, upstream addresses, downstream remote addresses, and
TLS server names. URL paths can contain identifying or sensitive information.
These access logs are not a transcript of encrypted application bodies, but
the separate session logger can expose agent content in plaintext.

By default, the operator configures a controller-runtime metrics listener at
`:8080`. To expose an endpoint is not the same as to scrape or export it. The
collection of those metrics in a deployment depends on the monitoring
configuration of that deployment.

Source: [cluster defaults](infra/cluster/main.bicep),
[Container Insights and managed Prometheus off](infra/cluster/modules/aks-cluster.bicep),
[Container Insights agent settings](infra/cluster/container-azm-ms-agentconfig.yaml),
[Istio defaults](infra/istio/values.yaml),
[Envoy access-log fields](infra/istio/templates/egress/accesslog.yaml),
[operator metrics configuration](app/cetirun-operator/cmd/main.go),
[ADX-Mon collection](app/cetirun-observability/chart/templates/_helpers.tpl),
[Azure Data Explorer databases and retention](infra/cluster/modules/adx.bicep),
and [Kubernetes audit events](infra/cluster/modules/aks-audit.bicep).

## Network and third-party boundaries

- **Models and agent tools.** Requests from OpenClaw, Hermes, and Custom Agents
  to models that the platform supplies use the AI Gateway. The AI
  Gateway forwards prompts, conversation context, file contents, and tool
  output to configured providers. Agent tools can send data and authentication
  information to other services. Model choice alone does not describe every
  recipient: skills, plugins, command-line tools, browsers, and user-supplied
  endpoints create more routes. Examine the terms, retention, training-use
  settings, and region of each selected service with that service.
- **GitHub Copilot authentication and model discovery.** The Portal starts
  GitHub device authorization and stores the completed credential in a
  Kubernetes Secret in the Agent's namespace. The operator provisions each
  Agent's Copilot proxy and AI Gateway access. The proxy exchanges the credential
  and gets the Copilot model catalog. New sign-ins do not write provider
  credentials into Agent HOME. These operations are separate from inference
  requests to the AI Gateway.
- **Identity.** The ingress sign-in flow uses Microsoft Entra ID. When Agent
  Identity is configured, the operator calls Microsoft Graph to manage Agent
  Identity service principals.
- **Connections.** When an owner connects a GitHub, Google, or Azure account to
  an Agent, the Agent Identity Broker keeps the encrypted tokens of that
  account. Connections MCP gets a short-lived token from the Broker and calls
  the service for the Agent. Each service receives the requests and the data
  that the Agent sends to it.
- **Browser and remote access.** The desktop sidecar runs Google Chrome and
  exposes a desktop through noVNC. ttyd exposes a terminal. Browsed sites
  receive browser requests and submitted data. Configured Tailscale access
  starts Tailscale processes and connects them to that service. These
  components handle data in their own way and have their own diagnostic
  behavior.
- **Packages, images, and shared skills.** Builds download third-party
  software. The operator pulls its configured Helm chart from an OCI registry.
  Runtime initialization can get configured skill packages and mount shared
  skill storage. These registries, package services, storage services, and the
  tools that result from them are separate trust boundaries.

CetiRun's first-party settings do not give a single opt-out for all third-party
telemetry. For example, if you disable OpenClaw's startup update check, this
change applies to that specific check. It does not prove that OpenClaw, Hermes,
Chrome, Azure CLI, coding assistants, dependencies, or added skills make no
other network requests. The runtime Dockerfiles install more tools, for example
Codex CLI, Claude Code, OpenCode, GitHub CLI, and Azure CLI. Their provider
requests, diagnostics, update behavior, and service terms must be examined
separately for the versions and configuration that are deployed.

The Istio external-service entries are audit configuration. They are not a
complete allowlist of possible destinations. Do not conclude that an unlisted
provider, website, or telemetry service is unreachable. Validate the deployed
network controls independently.

Source: [Portal Copilot sign-in](app/cetirun-portal/internal/handler/copilot_login.go),
[AI Gateway provisioning](app/cetirun-operator/internal/controller/ai_gateway.go),
[Graph client](app/cetirun-operator/internal/azure/graphclient.go),
[base image recipe](app/cetirun-chart/Dockerfile.base),
[desktop recipe](app/cetirun-chart/Dockerfile.desktop),
[skill initialization](app/cetirun-chart/configs/init-skills.sh),
[Helm client](app/cetirun-operator/internal/helm/client.go),
and [egress settings](infra/istio/values.yaml).

## Browser storage, persistence, and deletion

The supplied ingress configuration uses an authentication cookie named
`_cetirun_oauth2`, with secure transport, SameSite=Lax, and an eight-hour
expiry. The Portal stores the theme preference under `cetirun-theme` in
localStorage. Portal Chat keeps the conversations of an owner with a Custom
Agent in the IndexedDB database `cetirun-portal-chat` of the browser.
Third-party Agent UIs and visited sites can use their own cookies and
browser storage. The Portal's theme setting is not an inventory of all browser
state.

The desktop starts Chrome with
`--user-data-dir=/home/vibe/.config/google-chrome/openclaw` on the HOME
volume. It disables Chrome sync in that invocation, but it does not promise to
disable all Chrome service traffic. Browser profiles can keep website login
state, history, and other personal data. `desktop.logging=false`, the default,
suppresses x11vnc/websockify process output. Chrome stderr still goes to the
container's stderr. If you disable this diagnostic output, you do not erase the
profile or stop website requests.

Agent persistence is enabled by default. When you delete an Agent, the
operator revokes its AI Gateway key and its Broker records. Then it uninstalls
the release and deletes the namespace with the HOME volume claim. With the AKS
`default` StorageClass, whose reclaim policy is usually `Delete`, this also
deletes the disk. The disk holds the files and the memory notes of the Agent.
`persistence.retainPolicy: Retain`, the chart default, keeps the claim only
when the StatefulSet is deleted or scaled to zero, for example during
suspension. It does not protect HOME when the namespace is deleted.

The deletion of an Agent does not erase the other copies of its data. Copies
can stay in cloud logs, the AI Gateway database, backups, kept volumes, browser
devices, and external providers. Effective retention depends on the PVC/PV lifecycle,
reclaim policy, storage backend, backup policies, and downstream services.
The personal connections of the owner stay in the Broker namespace, with their
encrypted GitHub and Google tokens. The owner can remove them on the
**Connections** page.

The cluster template sets the retention of each Azure Data Explorer database
of the ADX-Mon collection: 90 days for logs, 30 days for metrics, and 14 days
for Agent logs. The daily AI Gateway usage view in the logs database keeps
400 days. Databases that already exist, exports, and other stores can have
different retention. CetiRun does not implement one global deletion schedule
across these systems.

Source: [OAuth2 Proxy configuration](infra/istio/templates/ingress/oauth2-proxy-deployment.yaml),
[theme storage](app/cetirun-portal/web/src/context/ThemeContext.tsx),
[desktop process configuration](app/cetirun-chart/configs/desktop-entrypoint.sh),
[volume mounts and retention](app/cetirun-chart/templates/statefulset.yaml),
[Agent deletion](app/cetirun-operator/internal/controller/agent_controller.go),
and [Azure Data Explorer retention](infra/cluster/modules/adx.bicep).

## Checklist for platform administrators

- Tell users which models, tools, channels, and monitoring systems receive their
  data. Also tell them if conversation content is collected in logs.
- Check `logger.enabled`, agent logging, `desktop.logging`,
  `egress.accessLog.enabled`, the AI Gateway's `requestLog`, `metrics`,
  `spendLogs.storePrompts`, `spendLogs.retention`, and `content.enabled`, the
  `cetirun-observability` chart, and metrics scraping separately. If you
  disable only the session logger, this does not disable the other paths.
- Check `memoryContext.enabled`, the connections that the Portal offers, and
  `cetiadmin.enabled`. Tell users that Agents keep memory notes, and that a
  reply in a group chat can quote them.
- Restrict access to the Portal, Kubernetes Secrets, HOME volumes, desktop
  and terminal endpoints, operational logs, and the Azure Data Explorer
  databases of the logs and metrics. Evaluate administrator roles and
  automation identities as data-access roles.
- Set retention, backup, export, and deletion procedures for each storage
  location. Handle provider-side data separately from the deletion of an Agent.
- Before use, examine added skills, tools, images, and provider settings. Do not
  put secrets in prompts or public issue reports. Logs and tool output can
  contain sensitive values, even when a particular handler does not log them.

Ask the platform administrators of your deployment about the recipients,
access controls, retention, and deletion process of the actual deployment. This
repository-level disclosure cannot determine those choices for a hosted
service.
