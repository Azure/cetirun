---
title: "Security"
description: "Configure access controls, protect credentials, and manage logs and persistent data."
weight: 70
---

CetiRun agents execute tools, read workspace files, and connect to external
services. Platform operators configure identity, workload isolation, network
access, and data retention for their deployment.

> **Warning:** Runtime logging is enabled by default and can include
> conversation content. Restrict log access and set retention and redaction
> policies before onboarding users.

See [PRIVACY.md]({{< repo "PRIVACY.md" >}}) for runtime data flows. Confirm the
recipients, access controls, retention, and deletion settings for each service
used by your deployment.

## Access boundaries

- The provided ingress configuration uses Entra ID and OAuth2 Proxy with
  required allowed-group configuration. The portal consumes forwarded identity
  headers; it does not repeat the group check. Keep backend access behind the
  trusted authentication path.
- The sign-in callback is `https://<domain>/oauth2/callback` at the apex host,
  for example `https://example.com/oauth2/callback`. The
  [OAuth2 VirtualService]({{< repo "infra/istio/templates/ingress/oauth2-proxy-virtualservice.yaml" >}})
  routes apex `/oauth2` requests. An `oauth2.<domain>` authentication exemption
  in the values does not make that subdomain the callback host.
- Ownership enforcement is path-based. For non-exempt paths, the Istio filter
  extracts the instance from `/ui/<instance>` or the first path segment and
  calls the portal's internal ownership endpoint with the forwarded identity.
  This filter does not compare a host alias with the authenticated user's alias.
- Namespaces, NetworkPolicy, mesh policy, and RuntimeClass are separate
  controls. A namespace name or a Helm option does not establish that isolation
  is enforced. Restrict who can change custom resources, Helm values, service
  accounts, and the operator or portal.
- [Tailscale](../guides/tailscale/) adds another access path; configuring it
  does not remove public ingress or the Gateway's own authentication.

The ownership filter defines its own fixed path exemptions. The Istio
templates consume `authPolicy.aliasExemptPrefixes` only to distinguish explicit
shared subdomain hosts from legacy instance subdomains. They do not consume
`authPolicy.adminAliases` supplied by the
[deployment helper]({{< repo "infra/istio/deploy.sh" >}}); setting it does not
configure an administrator ownership bypass.

The [chart defaults]({{< repo "app/cetirun-chart/values.yaml" >}}) select
`runtimeClassName: kata-vm-isolation`; the cluster must supply the matching
runtime and node support. They also include a writable root filesystem,
`allowPrivilegeEscalation: true`, and enabled desktop and ttyd services.
The [desktop launcher]({{< repo "app/cetirun-chart/configs/desktop-entrypoint.sh" >}})
starts Chrome with `--no-sandbox`. Review the complete pod, runtime, and access
boundary before running untrusted workloads.

Sources: [OAuth2 Proxy configuration]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}}),
[portal authentication middleware]({{< repo "app/cetirun-portal/internal/auth/middleware.go" >}}),
[ingress authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}}),
and [ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}).

### Kubernetes exec restriction

`execRestriction.enabled` defaults to `true`. The
[admission template]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}})
renders an `admissionregistration.k8s.io/v1` `ValidatingAdmissionPolicy` and
`ValidatingAdmissionPolicyBinding`. The cluster must support those APIs
(the chart documents Kubernetes 1.30+) and the installer must have permission
to create these cluster-scoped resources.

The policy denies `CONNECT` requests to `pods/exec` across the release namespace,
with a fixed administrative service-account exception in its match conditions.
This targets the Kubernetes exec API, not every route to command execution:
the enabled ttyd terminal and agent tools are separate access paths. Verify
that the cluster accepts and enforces the policy after installation.

## Credentials and persistent state

Credentials are not confined to Kubernetes Secrets. Authorized
[portal instance responses]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}})
can include the gateway token, Hermes API server key, CetiRun auth-helper key,
and a configured Tailscale auth key. Provider login state and agent
configuration can persist under `/home/vibe`.

The ingress authentication cookie is `_cetirun_oauth2`, configured with secure
transport, SameSite=Lax, and an eight-hour expiry. The portal stores its theme
preference as `cetirun-theme` in localStorage. The desktop uses a persistent
Chrome profile under `/home/vibe/.config/google-chrome/openclaw`, which can
retain website login state and history. Downloaded files can persist in
workspace storage. Disabling Chrome sync does not establish that all Chrome
service traffic is disabled.

Keep browser access, terminal access, model credentials, and Kubernetes Secret
permissions aligned with instance ownership. Never include token files,
cookies, private keys, complete Helm values, browser profiles, or unreviewed
browser network captures in public diagnostics. Secret masking in a UI does
not encrypt the underlying value.

Persistence is enabled by default, with `persistence.retainPolicy: Retain`
for StatefulSet volume claims. This does not guarantee survival of namespace
deletion or storage-provider cleanup. Deleting an instance also does not erase
all copies in logs, backups, retained volumes, browser devices, or external
providers. Define retention and deletion for each store; there is no global
CetiRun erasure schedule. See the storage and deletion details in
[PRIVACY.md]({{< repo "PRIVACY.md" >}}).

## Logging is enabled by default

`logger.enabled: true` starts a
[sidecar]({{< repo "app/cetirun-chart/configs/session-logger.sh" >}})
that follows files from their current end and writes new records to stdout
without redaction. For OpenClaw it follows active session JSONL under
`/home/vibe/.openclaw/agents`; these records can include prompts, responses, tool
calls, and tool results. OpenClaw as packaged in the agent image keeps its
sessions in SQLite and writes no such files, so the logger currently exports
nothing for OpenClaw. For Hermes it follows
`/home/vibe/.hermes/logs/gateway.log`, whose content depends on the installed
version and configuration and is not necessarily a complete transcript.

The [cluster template]({{< repo "infra/cluster/main.bicep" >}}) defaults
`enableContainerInsights` and `enablePrometheusMetrics` to `true`. It enables
managed Prometheus and the Container Insights monitoring add-on. The add-on
collects container stdout and stderr, including the session logger's output and
what Agents print themselves, which can include conversation text. Its
collection rules target configured Azure Log Analytics and Azure Monitor
workspaces. Actual collection depends on deployed collectors, permissions, and
settings. By default the add-on also copies containers' environment variables,
Secret values included; the
[agent settings]({{< repo "infra/cluster/container-azm-ms-agentconfig.yaml" >}})
that the cluster helper creates, or checks on a cluster that has its own,
turn that off.
The [Log Analytics template]({{< repo "infra/cluster/modules/aks-cluster.bicep" >}})
sets 30-day retention when creating a workspace; existing workspaces,
per-table settings, exports, and other stores may differ.

The Istio chart defaults `egress.accessLog.enabled` to `true`.
[Access logs]({{< repo "infra/istio/templates/egress/accesslog.yaml" >}})
include traffic metadata such as hosts, addresses, and HTTP paths when
available, not decrypted application bodies. Paths can still contain sensitive
data. Portal, operator, agent, and tool logs are additional data sources.
`desktop.logging: false` suppresses x11vnc/websockify output, but Chrome stderr
still reaches container stderr.

Restrict access to logs and monitoring workspaces, and document retention,
redaction, and deletion procedures. Disabling one logger does not disable every
agent, provider, browser, tool, or infrastructure collection path.

## External services

Configured model providers and tools can receive prompts, conversation context,
files, and tool output. GitHub Copilot login and model discovery contact
authentication and catalog endpoints separately from inference requests.
Entra ID and Tailscale introduce their own service boundaries. Skills,
plugins, CLI tools, package registries, and browsed sites create further routes.
Review each service's terms, retention, training-use settings, and region.
There is no single CetiRun setting that opts out of all third-party telemetry
or network activity.

The [NetworkPolicy template]({{< repo "app/cetirun-chart/templates/networkpolicy.yaml" >}})
includes an external IPv4 rule without port restrictions, excluding configured
blocked ranges. Defaults exclude private ranges and IMDS, with explicit internal
exceptions. It is not HTTPS-only. Review the deployment-specific internal
exception list before use. The
[Istio ServiceEntries and access-log settings]({{< repo "infra/istio/values.yaml" >}})
are audit configuration, not a destination allowlist. Source configuration and access logs
do not prove that unlisted destinations are blocked.

## Documentation site privacy

These pages are a static documentation site, separate from the portal and agent
runtime. The [site configuration]({{< repo "site/hugo.toml" >}}) enables
client-side search, and the
[page head]({{< repo "site/layouts/_partials/head.html" >}}) loads bundled jQuery
and Lunr assets and reads the `td-color-theme` localStorage preference. No
analytics service is configured in this site's source.

That statement does not cover a hosting provider's request logs, external sites
opened through links, or runtime telemetry. Hosting behavior needs its own
review. See the [site guide](../contributing/site/) for build and asset details;
use [PRIVACY.md]({{< repo "PRIVACY.md" >}}) for runtime data handling.

## Report a vulnerability

Report suspected vulnerabilities privately to the Microsoft Security Response
Center (MSRC), following the Microsoft reporting instructions linked
from [SECURITY.md]({{< repo "SECURITY.md" >}}). Do not report vulnerabilities,
exploits, or sensitive reproductions through public GitHub issues or pull
requests, even after removing credentials or deployment identifiers.

For non-security questions, use the channels in
[SUPPORT.md]({{< repo "SUPPORT.md" >}}). For licensing and third-party material,
see [the contribution guide](../contributing/#licenses-and-third-party-material),
[LICENSE]({{< repo "LICENSE" >}}), and [NOTICE]({{< repo "NOTICE" >}}).
