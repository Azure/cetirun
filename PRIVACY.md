# Runtime Data and Privacy

CetiRun runs agent workspaces on infrastructure that the deployment operator
selects. This document describes how data is handled, as the repository's
application code, charts, and infrastructure templates show. It is not a
statement that a particular deployment was inspected. It is also not a promise
about third-party services or a substitute for the operator's own privacy
notice.

**The default configuration can collect conversation content in container logs
and in the AI Gateway's database.** The agent chart enables the session logger.
The infrastructure templates enable Container Insights and managed Prometheus
configuration. The AI Gateway keeps the request and the response of each model
call in its database for seven days. Before you allow sensitive workloads,
examine these settings. There is no single CetiRun setting that disables
logging, telemetry, and network activity across every component.

## Data processed by CetiRun

| Surface | Data and purpose | Where it can be processed or stored |
| --- | --- | --- |
| Portal and sign-in | User alias, preferred username, display name, email claims, group authorization, and administrator status. These items are used to identify users and authorize access. | Browser, OAuth2 Proxy/identity provider, portal request context, and application logs. Instance ownership/profile fields are also stored in Kubernetes resources. |
| Workspace management | Instance and team names, owner alias, model selection, profile information, configuration, status, events, and resource identifiers | Portal, operator, Kubernetes API objects, Helm release state, and operational logs |
| Agent activity | Prompts, responses, files, tool arguments/results, session history, and user/team context | Agent process and workspace storage. Configured model, tool, and channel services. The AI Gateway's database, for seven days by default. Possibly container logs. |
| Authentication material | Gateway/API keys, provider credentials, OAuth device-flow state, and optional Tailscale credentials | Kubernetes Secrets, mounted configuration, agent home files, process memory, and the services that authenticate them |
| Browser and terminal | Commands/output, visited pages, downloads, screenshots, browsing history, and browser login state | Workspace processes and storage. Websites or services that the browser/tools contact. Terminal/desktop streams that go to the user. |

The portal does not show only public metadata. Its authorized instance response
can include gateway tokens, a Hermes API server key, the CetiRun auth helper
key, and a configured Tailscale auth key. Treat browser access to a workspace
and its API responses as access to sensitive information. Do not publish
browser network captures, session files, or support bundles before you examine
them.

Source: [portal claims](app/cetirun-portal/internal/auth/claims.go),
[authentication middleware](app/cetirun-portal/internal/auth/middleware.go),
[instance handlers](app/cetirun-portal/internal/handler/instance.go),
[instance API types](app/cetirun-operator/api/v1alpha1/agent_types.go),
[agent configuration templates](app/cetirun-chart/templates/agents/openclaw/_runtime.tpl),
and [Hermes configuration templates](app/cetirun-chart/templates/agents/hermes/_runtime.tpl).

## Logging and metrics

### Session and application logs

`logger.enabled` is `true` by default. For OpenClaw, the logger sidecar follows
active session JSONL files under `/home/vibe/.openclaw/agents` and writes new
lines to stdout. Those records can contain user input, assistant output, tool
calls, and tool results. OpenClaw, as the agent image packages it, keeps its
sessions in SQLite instead and writes no such files. As a result, the logger
now exports nothing for OpenClaw.

For Hermes, the logger follows `/home/vibe/.hermes/logs/gateway.log`. The
content of this file depends on the installed Hermes version and configuration.
This content is not necessarily a complete transcript.

The logger does not redact content. The logger starts to follow each file at
the current end of that file and does not replay the whole history. The
OpenClaw file selection looks for recent files and limits the number of files
that it follows. These limits are not a data-retention or privacy guarantee.
Anyone with access to the container logs that contain this output, or to their
downstream log store, can possibly read the content.

Portal and operator logs also include operational information, for example
aliases, display names, team/instance names, resource IDs, access URLs,
reconciliation status, and errors. Some update logs record field names instead
of values. But this does not prove that all logs are free of personal data or
secrets. Agent, terminal, auth-helper, and optional Tailscale processes also
write stdout/stderr.

Source: [chart defaults](app/cetirun-chart/values.yaml),
[session logger](app/cetirun-chart/configs/session-logger.sh),
[OpenClaw entrypoint](app/cetirun-chart/agents/openclaw/bootstrap/entrypoint.sh),
[Hermes entrypoint](app/cetirun-chart/agents/hermes/bootstrap/hermes-entrypoint.sh),
[portal handlers](app/cetirun-portal/internal/handler/instance.go),
and [operator reconciliation](app/cetirun-operator/internal/controller/agent_controller.go).

### AI Gateway request log and metrics

The AI Gateway's `requestLog.enabled` is `true` by default. Each model call then
writes one JSON line to the Gateway's stdout with the call's metadata. This
metadata contains the virtual key's alias and hash, the user ID, the Team, and
the Agent's name and UID recorded on the key. It also contains the model, token
counts, spend, timing, the client's address and user agent, and error text for
failed calls. Error text can quote the provider's response. The line contains
no request or response content.

By default, `metrics.enabled` is `false` in the Gateway chart and `true` in the
platform chart. When this setting is enabled, each Gateway pod serves
Prometheus metrics without authentication on a separate port. Their labels
include virtual key aliases and hashes, user IDs, and Team names, together with
spend. Anything that can reach the port can read them. The platform chart also
creates a PodMonitor. As a result, Azure Monitor managed Prometheus collects
them where it runs.

The Gateway's PostgreSQL database keeps daily spend totals for each key, user,
and Team. Unless `spendLogs.enabled` is `false`, the database also keeps
LiteLLM's own record of each call in `LiteLLM_SpendLogs`. The recorded calls
also include health-check probes. Each record contains the call's metadata, for
example the key, user, Team, model, token counts, spend, timing, and the error
of a failed call.

By default (`spendLogs.storePrompts: true`), each record also contains the
request and the response of the call. These include the messages, images,
tool calls, and tool results that an Agent sends, and the model's answer.
LiteLLM shortens each value that is longer than 2,048 characters, and keeps its
start and its end. As a result, a long message or an image is stored only in
part. The Gateway console's request log pages show these records. Anyone who
can sign in to the console, use the Gateway's master key, or read its database
or its backups can read them.

With `spendLogs.storePrompts: false`, each record contains only the metadata.
This setting does not remove the content of records that are already stored.
A `general_settings` block that is saved in the console can override it.

With `content.enabled: true`, the Gateway writes the request and the response
of each call to an Azure Data Explorer database instead (`CetiRunContent` when
the cluster template creates it), and the spend log keeps no content, whatever
`spendLogs.storePrompts` says. The content is complete, not shortened, except
that inline images and files and encrypted reasoning are replaced by notes with
their type and size. Each record names the Agent, its owner and Team, and the
LiteLLM user and Team of the key. The Gateway writes and reads it with its own
workload identity; the ADX-Mon collector and ingestor have no access to that
database. The console's request pages read a call's content from there and show
it to administrators, and to other console users only for their own calls and
Teams. People with the `Viewer` role on that database can read every call.
The database keeps the content for its retention, seven days in the cluster
template. Content written while ADX cannot be reached is lost after three
attempts, and while the Gateway's queue is full.

When these rows and the stored health-check results are older than
`spendLogs.retention`, LiteLLM deletes them. The default value is seven days,
and the check occurs every hour. An empty retention keeps them.

Source: [Gateway chart defaults](app/cetirun-ai-gateway/chart/values.yaml),
[platform chart defaults](app/cetirun-platform/values.yaml),
[request log callback](app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py),
the [entrypoint that registers it](app/cetirun-ai-gateway/entrypoint.py),
[LiteLLM configuration](app/cetirun-ai-gateway/chart/templates/configmap.yaml),
and the Gateway chart's
[spend log section](app/cetirun-ai-gateway/chart/README.md#litellms-spend-log).

### Infrastructure collection and traffic metadata

The cluster template sets `enableContainerInsights` and
`enablePrometheusMetrics` to `true` by default, and to `false` when `adxMode`
selects Azure Data Explorer for the `cetirun-observability` collection. With these settings, the
template enables the Container Insights monitoring add-on and managed
Prometheus. It also defines data collection rules and associations for
Container Insights and for managed Prometheus. The rules for Container Insights
include ContainerLogV2. The collected data is sent to the configured Azure Log
Analytics and Azure Monitor workspaces.

Container Insights then collects container stdout and stderr outside
`kube-system`, together with Kubernetes inventory, events and performance data.
That output includes the session logger's output that is described above and
what Agents print themselves. This output can include conversation text.
Successful collection still depends on the deployed collectors, permissions,
and settings. Source configuration alone does not prove that a live cluster
collects data. For example, on Kata node pools with the node image
`AKSAzureLinux-V3katagen2-202610.30.1` or later, the add-on collects no
container output from those nodes.

By default, Container Insights also copies the environment variables of every
container into its `ContainerInventory` table. This copy includes values that
come from Kubernetes Secrets. The cluster helper creates the settings in
`infra/cluster/container-azm-ms-agentconfig.yaml`, which disable that
collection for the cluster. On a cluster that already has agent settings, the
helper examines them to find if they disable that collection. CetiRun
containers that keep credentials in their environment disable it for
themselves with `AZMON_COLLECT_ENV=False`. A cluster that runs the add-on
without these settings collects those values.

The Istio chart sets `egress.accessLog.enabled` to `true` by default. Its Envoy
filters write traffic metadata to stdout. This metadata includes timestamps,
authority/host, HTTP method/path when available, status, and byte counts. It
also includes duration, upstream addresses, downstream remote addresses, and
TLS server names. URL paths can contain identifying or sensitive information.
These access logs are not a transcript of encrypted application bodies, but
the separate session logger can expose agent content in plaintext.

By default, the operator configures a controller-runtime metrics listener at
`:8080`. To expose an endpoint is not the same as to scrape or export it. The
collection of those metrics in a deployment depends on the monitoring
configuration of that deployment.

The optional `cetirun-observability` chart, off by default, sends the same
kinds of data to Azure Data Explorer with ADX-Mon. Its collector reads the
container output of the platform namespaces into the `CetiRunLogs` database,
and the output of every container of the Agent Pods, Custom terminals and
GitHub Copilot proxies into the `CetiRunAgents` database. Agent output can
include conversation text. Its telemetry program writes each Agent's
namespace, owner alias, display name, email, Entra tenant and object IDs, Team
and policy, and Kubernetes events, into `CetiRunLogs`. Metrics go to the
`CetiRunMetrics` database; the AI Gateway's metric labels include key aliases,
user IDs and Team names. The chart does not copy container environment
variables. Retention is a property of each database: the cluster template sets
90 days for logs, 30 days for metrics and 14 days for Agent logs.

Source: [cluster defaults](infra/cluster/main.bicep),
[collection destinations and retention](infra/cluster/modules/aks-cluster.bicep),
[Container Insights agent settings](infra/cluster/container-azm-ms-agentconfig.yaml),
[Istio defaults](infra/istio/values.yaml),
[Envoy access-log fields](infra/istio/templates/egress/accesslog.yaml),
[operator metrics configuration](app/cetirun-operator/cmd/main.go),
[ADX-Mon collection](app/cetirun-observability/chart/templates/_helpers.tpl),
and [Azure Data Explorer databases](infra/cluster/modules/adx.bicep).

## Network and third-party boundaries

- **Models and agent tools.** Requests from OpenClaw, Hermes, and Custom Agents
  to models that the platform supplies go through the AI Gateway. The AI
  Gateway forwards prompts, conversation context, file contents, and tool
  output to configured providers. Agent tools can send data and authentication
  information to other services. Model choice alone does not describe every
  recipient: skills, plugins, command-line tools, browsers, and user-supplied
  endpoints create more routes. Examine the terms, retention, training-use
  settings, and region of each selected service with that service.
- **GitHub Copilot authentication and model discovery.** The Portal starts
  GitHub device authorization and stores the completed credential in a
  Kubernetes Secret in the Agent's namespace. The Operator provisions each
  Agent's Copilot proxy and Gateway access. The proxy exchanges the credential
  and gets the Copilot model catalog. New sign-ins do not write provider
  credentials into Agent HOME. These operations are separate from inference
  requests that go through the AI Gateway.
- **Identity.** The ingress sign-in flow uses Microsoft Entra ID. When Agent
  Identity is configured, the operator calls Microsoft Graph to manage Agent
  Identity service principals.
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
[Gateway provisioning](app/cetirun-operator/internal/controller/ai_gateway.go),
[Graph client](app/cetirun-operator/internal/azure/graphclient.go),
[base image recipe](app/cetirun-chart/Dockerfile.base),
[desktop recipe](app/cetirun-chart/Dockerfile.desktop),
[skill initialization](app/cetirun-chart/configs/init-skills.sh),
[Helm client](app/cetirun-operator/internal/helm/client.go),
and [egress settings](infra/istio/values.yaml).

## Browser storage, persistence, and deletion

The supplied ingress configuration uses an authentication cookie named
`_cetirun_oauth2`, with secure transport, SameSite=Lax, and an eight-hour
expiry. The portal stores the theme preference under `cetirun-theme` in
localStorage. Third-party agent interfaces and visited sites can use their own
cookies and browser storage. The portal's theme setting is not an inventory of
all browser state.

The desktop starts Chrome with
`--user-data-dir=/home/vibe/.config/google-chrome/openclaw` on workspace
storage. It disables Chrome sync in that invocation, but it does not promise to
disable all Chrome service traffic. Browser profiles can keep website login
state, history, and other personal data. `desktop.logging=false`, the default,
suppresses x11vnc/websockify process output. Chrome stderr still goes to the
container's stderr. If you disable this diagnostic output, you do not erase the
profile or stop website requests.

Agent persistence is enabled by default. For StatefulSet volume claims, the
chart sets `persistence.retainPolicy` to `Retain` by default. This is not a
guarantee that data survives namespace deletion or storage-provider cleanup.
Also, the deletion of a CetiRun instance is not a complete erasure of data from
cloud logs, backups, retained volumes, browser devices, or external providers.
Effective retention depends on the PVC/PV lifecycle, reclaim policy, storage
backend, backup policies, and downstream services.

When the infrastructure template creates a Log Analytics workspace, it sets
`retentionInDays` to 30. A workspace that already exists, per-table settings,
exports, and other stores can have different retention. CetiRun does not
implement one global deletion schedule across these systems.

Source: [OAuth2 Proxy configuration](infra/istio/templates/ingress/oauth2-proxy-deployment.yaml),
[theme storage](app/cetirun-portal/web/src/context/ThemeContext.tsx),
[desktop process configuration](app/cetirun-chart/configs/desktop-entrypoint.sh),
[volume mounts and retention](app/cetirun-chart/templates/statefulset.yaml),
[instance deletion](app/cetirun-operator/internal/controller/agent_controller.go),
and [Log Analytics configuration](infra/cluster/modules/aks-cluster.bicep).

## Deployment operator checklist

- Tell users which models, tools, channels, and monitoring systems receive their
  data. Also tell them if conversation content is collected in logs.
- Examine `logger.enabled`, agent logging, `desktop.logging`,
  `egress.accessLog.enabled`, the AI Gateway's `requestLog` and `metrics`,
  collection rules, the `cetirun-observability` chart, and metrics scraping
  separately. If you disable only the session logger, this does not disable the
  other paths.
- Restrict access to the portal, Kubernetes Secrets, workspace volumes, desktop
  and terminal endpoints, operational logs, and monitoring workspaces. Evaluate
  administrators and automation identities as data-access roles.
- Set retention, backup, export, and deletion procedures for each storage
  location. Handle provider-side data separately from local instance deletion.
- Before use, examine added skills, tools, images, and provider settings. Do not
  put secrets in prompts or public issue reports. Logs and tool output can
  contain sensitive values, even when a particular handler does not log them.

Ask your deployment operator about the recipients, access controls, retention,
and deletion process of the actual deployment. This repository-level disclosure
cannot determine those choices for a hosted service.
