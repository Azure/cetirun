---
title: "Security"
description: "Configure access controls, protect credentials, and manage logs and persistent data."
weight: 70
---

CetiRun agents run tools, read workspace files, and connect to external
services. Platform operators configure identity, workload isolation, network
access, and data retention for their deployment.

> **Warning:** Before you onboard users, restrict log access and set retention
> and redaction policies. Runtime logging is enabled by default and can include
> conversation content.

Refer to [PRIVACY.md]({{< repo "PRIVACY.md" >}}) for runtime data flows. Make
sure that the recipients, access controls, retention, and deletion settings are
correct for each service that your deployment uses.

## Access boundaries

- The supplied ingress configuration uses Entra ID and OAuth2 Proxy, and it
  must have an allowed-group configuration. The portal uses the forwarded
  identity headers. It does not repeat the group check. Keep backend access
  behind the trusted authentication path.
- The sign-in callback is `https://<domain>/oauth2/callback` at the apex host,
  for example `https://example.com/oauth2/callback`. The
  [OAuth2 VirtualService]({{< repo "infra/istio/templates/ingress/oauth2-proxy-virtualservice.yaml" >}})
  routes apex `/oauth2` requests. An `oauth2.<domain>` authentication exemption
  in the values does not make that subdomain the callback host.
- Ownership enforcement is path-based. For non-exempt paths, the Istio filter
  gets the instance from `/ui/<instance>` or from the first path segment. Then
  it calls the portal's internal ownership endpoint with the forwarded
  identity. This filter does not compare a host alias with the authenticated
  user's alias.
- Namespaces, NetworkPolicy, mesh policy, and RuntimeClass are separate
  controls. A namespace name or a Helm option does not establish that isolation
  is enforced. Restrict who can change custom resources, Helm values, service
  accounts, and the operator or portal.
- [Tailscale](../guides/tailscale/) adds another access path. The Tailscale
  configuration does not remove public ingress or the Gateway's own
  authentication.

The ownership filter defines its own fixed path exemptions. The Istio
templates use `authPolicy.aliasExemptPrefixes` only to identify which hosts
are explicit shared subdomain hosts and which are legacy instance subdomains.
They do not use the `authPolicy.adminAliases` value that the
[deployment helper]({{< repo "infra/istio/deploy.sh" >}}) supplies. If you set
this value, it does not configure an administrator ownership bypass.

The [chart defaults]({{< repo "app/cetirun-chart/values.yaml" >}}) select
`runtimeClassName: kata-vm-isolation`. The cluster must supply the runtime and
the node support that match this setting. The defaults also include a writable
root filesystem, `allowPrivilegeEscalation: true`, and enabled desktop and
ttyd services. The
[desktop launcher]({{< repo "app/cetirun-chart/configs/desktop-entrypoint.sh" >}})
starts Chrome with `--no-sandbox`. Before you run untrusted workloads, examine
the complete pod, runtime, and access boundary.

Sources: [OAuth2 Proxy configuration]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}}),
[portal authentication middleware]({{< repo "app/cetirun-portal/internal/auth/middleware.go" >}}),
[ingress authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}}),
and [ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}).

### Kubernetes exec restriction

`execRestriction.enabled` defaults to `true`. The
[admission template]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}})
renders an `admissionregistration.k8s.io/v1` `ValidatingAdmissionPolicy` and
`ValidatingAdmissionPolicyBinding`. The cluster must support these APIs (the
chart documents Kubernetes 1.30+). The installer must have permission to create
these cluster-scoped resources.

The policy denies `CONNECT` requests to `pods/exec` across the release
namespace. Its match conditions contain a fixed exception for an administrative
service account. This policy applies to the Kubernetes exec API, not to every
route to command execution. The enabled ttyd terminal and the agent tools are
separate access paths. After installation, make sure that the cluster accepts
and enforces the policy.

## Credentials and persistent state

Credentials are not only in Kubernetes Secrets. Authorized
[portal instance responses]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}})
can include the gateway token, the Hermes API server key, the CetiRun
auth-helper key, and a configured Tailscale auth key. Provider login state and
agent configuration can persist under `/home/vibe`.

The ingress authentication cookie is `_cetirun_oauth2`, configured with secure
transport, SameSite=Lax, and an eight-hour expiry. The portal stores its theme
preference as `cetirun-theme` in localStorage. The desktop uses a persistent
Chrome profile under `/home/vibe/.config/google-chrome/openclaw`, which can
keep website login state and history. Downloaded files can persist in
workspace storage. If Chrome sync is disabled, this does not establish that all
Chrome service traffic is disabled.

Keep browser access, terminal access, model credentials, and Kubernetes Secret
permissions consistent with instance ownership. Never include token files,
cookies, private keys, complete Helm values, browser profiles, or unreviewed
browser network captures in public diagnostics. A UI that masks a secret does
not encrypt the value of that secret.

Persistence is enabled by default, with `persistence.retainPolicy: Retain` for
StatefulSet volume claims. This does not guarantee that the data survives
namespace deletion or storage-provider cleanup. The deletion of an instance
also does not erase all copies in logs, backups, kept volumes, browser devices,
or external providers. Define retention and deletion for each store. There is
no global CetiRun erasure schedule. For the storage and deletion details, refer
to [PRIVACY.md]({{< repo "PRIVACY.md" >}}).

## Logging is enabled by default

`logger.enabled: true` starts a
[sidecar]({{< repo "app/cetirun-chart/configs/session-logger.sh" >}})
that follows files from their current end and writes new records to stdout
without redaction. For OpenClaw, it follows active session JSONL under
`/home/vibe/.openclaw/agents`. These records can include prompts, responses,
tool calls, and tool results. In the agent image, OpenClaw keeps its sessions
in SQLite and writes no such files, so the logger now exports nothing for
OpenClaw. For Hermes, it follows `/home/vibe/.hermes/logs/gateway.log`. The
content of this file depends on the installed version and configuration, and it
is not necessarily a complete transcript.

The [cluster template]({{< repo "infra/cluster/main.bicep" >}}) sets
`enableContainerInsights` and `enablePrometheusMetrics` to `true` by default.
It enables managed Prometheus and the Container Insights monitoring add-on. The
add-on collects container stdout and stderr. This data includes the session
logger's output and what Agents print themselves, and both can include
conversation text. The add-on's collection rules target the configured Azure
Log Analytics and Azure Monitor workspaces. Actual collection depends on
deployed collectors, permissions, and settings.

By default, the add-on also copies the environment variables of containers,
and these copies include Secret values. The
[agent settings]({{< repo "infra/cluster/container-azm-ms-agentconfig.yaml" >}})
disable that copy. The cluster helper creates these settings, or examines them
on a cluster that has its own.

When the
[Log Analytics template]({{< repo "infra/cluster/modules/aks-cluster.bicep" >}})
creates a workspace, it sets 30-day retention. Workspaces that already exist,
per-table settings, exports, and other stores can differ.

The Istio chart sets `egress.accessLog.enabled` to `true` by default.
[Access logs]({{< repo "infra/istio/templates/egress/accesslog.yaml" >}})
include traffic metadata such as hosts, addresses, and HTTP paths, when they
are available. They do not include decrypted application bodies. Paths can
still contain sensitive data. Portal, operator, agent, and tool logs are other
data sources. `desktop.logging: false` disables x11vnc/websockify output, but
Chrome stderr still reaches container stderr.

Restrict access to logs and monitoring workspaces. Document the retention,
redaction, and deletion procedures. If you disable one logger, you do not
disable every agent, provider, browser, tool, or infrastructure collection
path.

## External services

Configured model providers and tools can receive prompts, conversation context,
files, and tool output. GitHub Copilot login and model discovery connect to
authentication and catalog endpoints, separately from inference requests.
Entra ID and Tailscale add their own service boundaries. Skills, plugins, CLI
tools, package registries, and browsed sites create more routes. Examine the
terms, retention, training-use settings, and region of each service. There is
no single CetiRun setting that opts out of all third-party telemetry or network
activity.

The [NetworkPolicy template]({{< repo "app/cetirun-chart/templates/networkpolicy.yaml" >}})
includes an external IPv4 rule that has no port restrictions and that excludes
the configured blocked ranges. The defaults exclude private ranges and IMDS,
with explicit internal exceptions. The rule is not HTTPS-only. Before use,
examine the deployment-specific internal exception list. The
[Istio ServiceEntries and access-log settings]({{< repo "infra/istio/values.yaml" >}})
are audit configuration, not a destination allowlist. Source configuration and
access logs do not prove that unlisted destinations are blocked.

## Documentation site privacy

These pages are a static documentation site, separate from the portal and agent
runtime. The [site configuration]({{< repo "site/hugo.toml" >}}) enables
client-side search. The
[page head]({{< repo "site/layouts/_partials/head.html" >}}) loads bundled
jQuery and Lunr assets and reads the `td-color-theme` localStorage preference.
No analytics service is configured in this site's source.

That statement does not cover a hosting provider's request logs, external sites
opened through links, or runtime telemetry. Hosting behavior must have its own
review. For build and asset details, refer to the
[site guide](../contributing/site/). For how the runtime handles data, refer to
[PRIVACY.md]({{< repo "PRIVACY.md" >}}).

## Report a vulnerability

Report suspected vulnerabilities privately to the Microsoft Security Response
Center (MSRC). Follow the Microsoft instructions for these reports. The link to
these instructions is in [SECURITY.md]({{< repo "SECURITY.md" >}}). Do not
report vulnerabilities, exploits, or sensitive reproductions through public
GitHub issues or pull requests. This rule applies even after you remove
credentials or deployment identifiers.

For non-security questions, use the channels in
[SUPPORT.md]({{< repo "SUPPORT.md" >}}). For information about licenses and
third-party material, refer to
[the contribution guide](../contributing/#licenses-and-third-party-material),
[LICENSE]({{< repo "LICENSE" >}}), and [NOTICE]({{< repo "NOTICE" >}}).
