# Runtime Data and Privacy

CetiRun runs agent workspaces on infrastructure selected by the deployment
operator. This document describes data handling visible in the repository's
application code, charts, and infrastructure templates. It is not a statement
that a particular deployment has been inspected, a promise about third-party
services, or a substitute for the operator's own privacy notice.

**The default configuration can collect conversation content in container logs.**
The agent chart enables the session logger, and the infrastructure templates
enable Container Insights and managed Prometheus configuration. Review these
settings before allowing sensitive workloads. There is no single CetiRun setting
that disables logging, telemetry, and network activity across every component.

## Data processed by CetiRun

| Surface | Data and purpose | Where it can be processed or stored |
| --- | --- | --- |
| Portal and sign-in | User alias, preferred username, display name, email claims, group authorization, and administrator status, used to identify users and authorize access | Browser, OAuth2 Proxy/identity provider, portal request context, and application logs; instance ownership/profile fields are also stored in Kubernetes resources |
| Workspace management | Instance and team names, owner alias, model selection, profile information, configuration, status, events, and resource identifiers | Portal, operator, Kubernetes API objects, Helm release state, and operational logs |
| Agent activity | Prompts, responses, files, tool arguments/results, session history, and user/team context | Agent process and workspace storage; configured model, tool, and channel services; potentially container logs |
| Authentication material | Gateway/API keys, provider credentials, OAuth device-flow state, and optional Teams/Tailscale credentials | Kubernetes Secrets, mounted configuration, agent home files, process memory, and the services that authenticate them |
| Browser and terminal | Commands/output, visited pages, downloads, screenshots, browsing history, and browser login state | Workspace processes and storage; websites or services contacted by the browser/tools; terminal/desktop streams delivered to the user |
| Teams integration | Message text, sender name/ID, conversation and message IDs, service routing URLs, and responses | Agent runtime, Microsoft Teams/Bot Framework, plugin state, and operational logs |

The portal does not merely display public metadata. Its authorized instance
response can include gateway tokens, a Hermes API server key, the CetiRun auth
helper key, and a configured Tailscale auth key. Treat browser access to a
workspace and its API responses as access to sensitive information. Do not
publish browser network captures, session files, or support bundles without
reviewing them.

Source: [portal claims](app/cetirun-portal/internal/auth/claims.go),
[authentication middleware](app/cetirun-portal/internal/auth/middleware.go),
[instance handlers](app/cetirun-portal/internal/handler/instance.go),
[instance API types](app/cetirun-operator/api/v1alpha1/agent_types.go),
[agent configuration templates](app/cetirun-chart/templates/agents/openclaw/_runtime.tpl),
and [Hermes configuration templates](app/cetirun-chart/templates/agents/hermes/_runtime.tpl).

## Logging and metrics

### Session and application logs

`logger.enabled` defaults to `true`. For OpenClaw, the logger sidecar follows
active session JSONL files beneath `/home/vibe/.openclaw/agents` and writes new
lines to stdout. Those records can contain user input, assistant output, tool
calls, and tool results. For Hermes, it follows
`/home/vibe/.hermes/logs/gateway.log`; its content depends on the installed
Hermes version and configuration, and is not necessarily a complete transcript.

The logger does not redact content. It starts following at the current end of
each file rather than replaying the whole history. The OpenClaw file selection
looks for recent files and limits how many it follows; those limits are not a
data-retention or privacy guarantee. Anyone with access to the resulting
container logs or their downstream log store may be able to read the content.

Portal and operator logs also include operational information such as aliases,
display names, team/instance names, resource IDs, access URLs, reconciliation
status, and errors. Some update logging records field names rather than values,
but this does not establish that all logs are free of personal data or secrets.
The Teams adapter can log rejected sender names/IDs and remote error text.
Agent, terminal, auth-helper, and optional Tailscale processes also write
stdout/stderr.

Source: [chart defaults](app/cetirun-chart/values.yaml),
[session logger](app/cetirun-chart/configs/session-logger.sh),
[OpenClaw entrypoint](app/cetirun-chart/agents/openclaw/bootstrap/entrypoint.sh),
[Hermes entrypoint](app/cetirun-chart/agents/hermes/bootstrap/hermes-entrypoint.sh),
[portal handlers](app/cetirun-portal/internal/handler/instance.go),
[operator reconciliation](app/cetirun-operator/internal/controller/agent_controller.go),
and [Teams adapter](app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/adapter.py).

### Infrastructure collection and traffic metadata

The cluster template defaults `enableContainerInsights` and
`enablePrometheusMetrics` to `true`. With them it enables the Container
Insights monitoring add-on and managed Prometheus, and defines data collection
rules and associations for Container Insights, including ContainerLogV2, and
for managed Prometheus. Collected data is directed to the configured Azure Log
Analytics and Azure Monitor workspaces. Container Insights then collects
container stdout and stderr outside `kube-system`, together with Kubernetes
inventory, events and performance data. That output includes the session
logger's output described above and what Agents print themselves, which can
include conversation text. Successful collection still depends on the deployed
collectors, permissions, and settings; source configuration alone does not
prove that a live cluster is collecting data.

By default Container Insights also copies every container's environment
variables, values taken from Kubernetes Secrets included, into its
`ContainerInventory` table. The cluster helper applies
`infra/cluster/container-azm-ms-agentconfig.yaml`, which turns that collection
off for the cluster, and CetiRun containers that keep credentials in their
environment turn it off for themselves with `AZMON_COLLECT_ENV=False`. A
cluster that runs the add-on without these settings collects those values.

The Istio chart defaults `egress.accessLog.enabled` to `true`. Its Envoy filters
write traffic metadata to stdout, including timestamps, authority/host, HTTP
method/path when available, status, byte counts, duration, upstream addresses,
downstream remote addresses, and TLS server names. URL paths may contain
identifying or sensitive information. These access logs are not a transcript of
encrypted application bodies, but the separate session logger can expose agent
content in plaintext.

The operator configures a controller-runtime metrics listener at `:8080` by
default. Exposing an endpoint is not the same as scraping or exporting it.
Whether a deployment collects those metrics depends on its monitoring setup.

Source: [cluster defaults](infra/cluster/main.bicep),
[collection destinations and retention](infra/cluster/modules/aks-cluster.bicep),
[Container Insights agent settings](infra/cluster/container-azm-ms-agentconfig.yaml),
[Istio defaults](infra/istio/values.yaml),
[Envoy access-log fields](infra/istio/templates/egress/accesslog.yaml),
and [operator metrics configuration](app/cetirun-operator/cmd/main.go).

## Network and third-party boundaries

- **Models and agent tools.** OpenClaw, Hermes, and the tools they invoke can
  send prompts, conversation context, file contents, tool output, and
  authentication information to configured providers or other services.
  Model choice alone does not describe every recipient: skills, plugins,
  command-line tools, browsers, and user-supplied endpoints create additional
  routes. Check the terms, retention, training-use settings, and region of each
  selected service with that service.
- **GitHub Copilot authentication and model discovery.** The in-pod auth helper
  contacts GitHub device authorization/token endpoints, exchanges credentials
  for Copilot access, and retrieves a model catalog from the resolved Copilot
  API endpoint. Successful login persists provider credentials in the agent's
  home configuration. These operations are separate from inference requests
  made by the agent runtime.
- **Identity and Teams.** The ingress sign-in flow uses Microsoft Entra ID.
  When Teams integration is configured, the operator calls Microsoft Graph
  and Azure Bot Service to manage application, identity, and bot resources.
  The Hermes Teams adapter retrieves Bot Framework signing metadata/keys and
  exchanges messages with Teams/Bot Framework services. It persists a bounded
  conversation-to-service-URL map in the Hermes home directory for later replies.
- **Browser and remote access.** The desktop sidecar runs Google Chrome and
  exposes a desktop through noVNC; ttyd exposes a terminal. Browsed sites
  receive browser requests and submitted data. Configured Tailscale access
  starts Tailscale processes and connects them to that service. These
  components have their own data handling and diagnostic behavior.
- **Packages, images, and shared skills.** Builds download third-party software.
  The operator pulls its configured Helm chart from an OCI registry. Runtime
  initialization can obtain configured skill packages and mount shared skill
  storage. These registries, package services, storage services, and the
  resulting tools are separate trust boundaries.

CetiRun's first-party settings do not provide a blanket third-party telemetry
opt-out. For example, disabling OpenClaw's startup update check addresses that
specific check; it does not establish that OpenClaw, Hermes, Chrome, Azure CLI,
coding assistants, dependencies, or added skills make no other network requests.
The runtime Dockerfiles install additional tools, including Codex CLI, Claude
Code, OpenCode, GitHub CLI, and Azure CLI. Their provider requests, diagnostics,
update behavior, and service terms must be reviewed separately for the versions
and configuration actually deployed.

The Istio external-service entries and configured blacklist are not a complete
allowlist of possible destinations. Do not infer that an unlisted provider,
website, or telemetry service is unreachable. Validate the deployed network
controls independently.

Source: [auth helper](app/cetirun-chart/configs/cetirun-auth-service.py),
[Graph client](app/cetirun-operator/internal/azure/graphclient.go),
[Bot Service client](app/cetirun-operator/internal/azure/botservice.go),
[Teams authentication](app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/auth.py),
[Teams adapter](app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/adapter.py),
[base image recipe](app/cetirun-chart/Dockerfile.base),
[desktop recipe](app/cetirun-chart/Dockerfile.desktop),
[skill initialization](app/cetirun-chart/configs/init-skills.sh),
[Helm client](app/cetirun-operator/internal/helm/client.go),
and [egress settings](infra/istio/values.yaml).

## Browser storage, persistence, and deletion

The provided ingress configuration uses an authentication cookie named
`_cetirun_oauth2`, with secure transport, SameSite=Lax, and an eight-hour expiry.
The portal stores the theme preference under `cetirun-theme` in localStorage.
Third-party agent interfaces and visited sites can use their own cookies and
browser storage; the portal's theme setting is not an inventory of all browser
state.

The desktop starts Chrome with
`--user-data-dir=/home/vibe/.config/google-chrome/openclaw` on workspace storage.
It disables Chrome sync in that invocation, but does not promise to disable all
Chrome service traffic. Browser profiles may retain website login state,
history, and other personal data. `desktop.logging=false`, the default,
suppresses x11vnc/websockify process output; Chrome stderr still goes to the
container's stderr. Disabling this diagnostic output does not erase the profile
or stop website requests.

Agent persistence is enabled by default. The chart defaults
`persistence.retainPolicy` to `Retain` for StatefulSet volume claims. That is
not a guarantee that data survives namespace deletion or storage-provider
cleanup. Conversely, deleting a CetiRun instance is not a complete erasure of
data from cloud logs, backups, retained volumes, browser devices, or external
providers. Effective retention depends on the PVC/PV lifecycle, reclaim policy,
storage backend, backup policies, and downstream services.

When the infrastructure template creates a Log Analytics workspace it sets
`retentionInDays` to 30. An existing workspace, per-table settings, exports, and
other stores can have different retention. CetiRun does not implement one
global deletion schedule across these systems.

Source: [OAuth2 Proxy configuration](infra/istio/templates/ingress/oauth2-proxy-deployment.yaml),
[theme storage](app/cetirun-portal/web/src/context/ThemeContext.tsx),
[desktop process configuration](app/cetirun-chart/configs/desktop-entrypoint.sh),
[volume mounts and retention](app/cetirun-chart/templates/statefulset.yaml),
[instance deletion](app/cetirun-operator/internal/controller/agent_controller.go),
and [Log Analytics configuration](infra/cluster/modules/aks-cluster.bicep).

## Deployment operator checklist

- Tell users which models, tools, channels, and monitoring systems receive their
  data, including whether conversation content is collected in logs.
- Review `logger.enabled`, agent logging, `desktop.logging`,
  `egress.accessLog.enabled`, collection rules, and metrics scraping separately.
  Turning off the session logger alone does not disable the other paths.
- Restrict access to the portal, Kubernetes Secrets, workspace volumes, desktop
  and terminal endpoints, operational logs, and monitoring workspaces. Evaluate
  administrators and automation identities as data-access roles.
- Set retention, backup, export, and deletion procedures for each storage
  location. Address provider-side data separately from local instance deletion.
- Review added skills, tools, images, and provider settings before use. Do not
  put secrets in prompts or public issue reports; logs and tool output can
  contain sensitive values even when a particular handler avoids logging them.

Ask your deployment operator about the actual deployment's recipients, access
controls, retention, and deletion process. This repository-level disclosure
cannot determine those choices for a hosted service.
