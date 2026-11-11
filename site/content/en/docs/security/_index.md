---
title: "Security"
description: "Trust boundaries, sign-in and ownership, administrator roles, workload isolation, credentials, network access, and data handling in CetiRun."
weight: 70
---

CetiRun Agents run tools, read the files in their HOME, and connect to external
services. Platform administrators configure identity, workload isolation,
network access, and data retention for their deployment. Each control on this
page has prerequisites. Check them for your deployment before you give users
access.

> **Warning:** By default, CetiRun keeps conversation content in the AI Gateway
> database, in container logs, and in the memory notes of each Agent. CetiRun
> does not redact these stores. Before you onboard users, restrict who can read
> them. [Data collection](#data-collection) lists the stores.

[PRIVACY.md]({{< repo "PRIVACY.md" >}}) is the detailed source for how the
runtime collects, stores, and sends data. This page gives a summary. Make sure
that the recipients, access controls, retention, and deletion settings are
correct for each service that your deployment uses.

## Security model

When a deployment uses the supplied configuration, CetiRun enforces these
rules:

- Users sign in with Microsoft Entra ID before they reach the Portal or an
  Agent.
- Only the owner of an Agent can open its Agent UI, chat, terminal, and desktop
  through the ingress gateway.
- Each Agent runs in its own namespace and Pod, with its own HOME volume,
  Secrets, and AI Gateway virtual key.
- The operator configures each Agent to call models through the AI Gateway.
  The AI Gateway keeps the provider credentials, and the Agent Identity Broker
  keeps the credentials of connections.
- Sandbox Agents get only what their Agent Policy allows.

CetiRun does not protect against these risks:

- A person or a service that can write to the platform namespaces, to the
  CetiRun resources, or to the Secrets in Agent namespaces. Treat these
  permissions as cluster administrator permissions.
- An Agent that misuses what it can reach: its HOME, its credentials, its
  connections, and the network destinations that its policies allow. Prompt
  injection can cause this.
- Model calls that do not use the AI Gateway. The NetworkPolicy of a standard
  Agent allows public IPv4 traffic. Thus, tools in the Agent can call other
  model services with credentials that a user supplies.
- What model providers, connected services, and websites do with the data that
  they receive.

## Trust boundaries

| Boundary | What enforces it | What it depends on |
| --- | --- | --- |
| Internet to the ingress gateway | OAuth2 Proxy signs users in with Microsoft Entra ID and admits only the groups in `allowedGroups` | The Istio authentication chart |
| Ingress gateway to an Agent | The ownership filter asks the Portal if the signed-in user owns the Agent | The internal Portal listener on port 8081 |
| Ingress gateway to the Portal | The Portal trusts the identity headers of the ingress gateway, and the Portal chart admits only the ingress gateway | Istio sidecar injection in the Portal namespace, and `istio.internalAuthMTLS: true` |
| Agent to models | The virtual key of the Agent | The AI Gateway |
| Agent to external accounts | Connections MCP, with short-lived tokens from the Broker | The connections that the owner authorized |
| Agent to the network | The NetworkPolicy of a standard Agent, or the CiliumNetworkPolicy of a Sandbox Agent | A network plugin that enforces these policies |

### Identity headers

Before OAuth2 Proxy runs, the ingress gateway removes these headers from each
client request: `x-auth-request-user`, `x-auth-request-email`,
`x-auth-request-preferred-username`, `x-auth-request-groups`,
`x-auth-request-access-token`, `x-forwarded-user`, `x-forwarded-email`,
`x-forwarded-groups`, `x-internal-instance`, and `x-internal-alias`. OAuth2
Proxy then sets the identity headers that the Portal trusts.

The Portal chart protects ports 8080 and 8081 with a STRICT mTLS
PeerAuthentication, an AuthorizationPolicy, and a NetworkPolicy that admits
only the ingress gateway namespace. These controls need Istio sidecar injection
in the Portal namespace. The chart does not add the injection label. Its value
`istio.sidecarInjection: true`, the default, only confirms that the namespace
has injection. With `istio.internalAuthMTLS: false`, the default of the
standalone Portal chart, port 8081 uses plain HTTP, and only the NetworkPolicy
protects it. `make deploy` sets `istio.internalAuthMTLS: true` and labels the
platform namespaces for sidecar injection.

After the ownership check, the ingress gateway removes the sign-in cookies:
`_cetirun_oauth2`, its parts, and its CSRF cookie. It also removes the
`authorization` header, which holds the ID token. Then it forwards the request
to the Agent. The Agent still receives the `x-auth-request-user`,
`x-auth-request-email`, and `x-auth-request-preferred-username` headers.

### Entry points without sign-in

The ingress gateway sends these requests to their destination without the
OAuth2 Proxy sign-in:

- On the apex domain: `/oauth2/*`, `/healthz`, `/readyz`, and the GET and POST
  requests to the connection callback `/api/v1/connections/callback`. The
  Broker checks a one-time state on each callback.
- POST requests to the paths in `authPolicy.apexPostBypassPaths`. The default
  list is empty.
- Requests to a `<prefix>.<domain>` host for each prefix in
  `authPolicy.authExemptPrefixes`. The defaults are `oauth2` and `llmapi`. No
  route of CetiRun uses these hosts. If you add a route for such a host, the
  route is public. For each deployment, remove the prefixes that you do not
  use.

The ownership filter defines its own fixed path exemptions. The Istio
templates use `authPolicy.aliasExemptPrefixes` only to identify which hosts
are explicit shared subdomain hosts and which are legacy instance subdomains.
They do not use the `authPolicy.adminAliases` value that the
[deployment helper]({{< repo "infra/istio/deploy.sh" >}}) supplies. If you set
this value, it does not configure an administrator ownership bypass. The
shared subdomain hosts skip the ownership check, as
[Exemptions](../operations/istio/#exemptions) describes.

### Other access paths

In `entra-id` mode, OAuth2 Proxy also accepts a valid Microsoft Entra ID bearer
token instead of a session cookie (`skipJwtBearerTokens: true` by default). The
ownership check still applies to these requests.

- [Tailscale](../guides/tailscale/) adds a private access path to an Agent.
  Requests on the tailnet do not go through the Istio ingress gateway, OAuth2
  Proxy, or the ownership check. The public routes stay in place. Configure
  the tailnet ACLs so that only the devices of the Agent owner can reach the
  Agent.

For the configuration of sign-in and ownership, read
[Authentication and ownership](../operations/istio/#authentication-and-ownership).

Sources: [OAuth2 Proxy configuration]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}}),
[portal authentication middleware]({{< repo "app/cetirun-portal/internal/auth/middleware.go" >}}),
[ingress authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}}),
[security headers]({{< repo "infra/istio/templates/ingress/envoy-filter-security-headers.yaml" >}}),
and [ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}).

## Authentication and authorization

### Sign-in and alias

OAuth2 Proxy signs users in with Microsoft Entra ID. The ingress gateway admits
only the members of the groups in `allowedGroups`. The Portal does not repeat
the group check. The sign-in callback is `https://<domain>/oauth2/callback` on
the apex domain, for example `https://example.com/oauth2/callback`. The
[OAuth2 VirtualService]({{< repo "infra/istio/templates/ingress/oauth2-proxy-virtualservice.yaml" >}})
routes the apex `/oauth2` requests.

The Portal identifies each user by an alias. The alias is the lowercase part
before `@` of the `preferred_username` claim, or of the email address when that
claim is empty. An alias must start with a letter, and it can contain only lowercase
letters, digits, and hyphens, up to 63 characters. A sign-in name with a
period, for example `first.last@example.com`, does not give a valid alias. For
such a user, the Portal rejects each API request with HTTP 403 and the code
`invalid_identity`. Before you install, check the names of your users as
[User names](../getting-started/platform/#user-names) describes.

> **Warning:** The Portal identifies users and Portal administrators by alias.
> Make sure that the part before `@` is unique for all users who can sign in,
> and that users cannot change it. Do not add guest accounts to the groups in
> `allowedGroups`. Otherwise, the Portal cannot tell these users apart.

### Agent ownership

The Portal records the owner of each new Agent in `spec.user`. When the Portal
verifies Microsoft Entra ID tokens, it also records the tenant ID and the
object ID of the owner. It writes them in the annotations
`agentidentity.cetirun.microsoft.com/owner-tenant-id` and
`agentidentity.cetirun.microsoft.com/owner-object-id`. For such an Agent, the
Portal API and the ownership check admit only that identity. The Portal
verifies tokens when `istio.internalAuthMTLS`, Agent Identity, or the Agent
tools feature is on. `make deploy` enables them.

For an Agent without these annotations, the Portal API compares the alias of
the user with `spec.user.alias`. The ownership check also accepts a match with
`spec.user.email`.

By default, each user can create one Agent (`features.maxInstancesPerUser: 1`
of the Portal chart). The Portal names it `<alias>`, or `<alias>-<suffix>`.

### Roles

| Role | How a person or a workload gets it | What it can do |
| --- | --- | --- |
| Agent owner | Creates the Agent in the Portal | Opens and uses the Agent. Changes, suspends, resumes, and deletes it. Cannot open the Agents of other users |
| Portal administrator | The alias is in `admins.aliases` of the Portal chart. `make deploy` adds `ownerAlias`. The Portal reads the list when it starts | Sees all Agents with their status and events. Changes their image, display name, and model, and deletes them. Creates, changes, and deletes Teams, Agent Policies, and Sandboxes. Cannot open the Agent UI, chat, terminal, or desktop of another user's Agent, and does not see Secrets in the Portal |
| Admission administrator | `sandboxAdmission.adminUsers` and `sandboxAdmission.adminGroups` of the operator chart. The Portal service account is also one | Writes Sandboxes, Agent Policies, and policy-bound Teams through the Kubernetes API |
| Platform administrator | The Azure and cluster permissions of the deployment | Installs, upgrades, and operates CetiRun. With Kubernetes RBAC, can also read Secrets and use exec in Agent namespaces |
| CetiAdmin | The operator chart. On by default | Diagnoses and repairs standard Agents with its own Kubernetes permissions. Only its owner can use it. Refer to [CetiAdmin](../operations/cetiadmin/) |
| Agent workload | Runs in the Agent Pod | Uses its HOME, its Secrets, its connections, and the network destinations that its policies allow |

Any signed-in user can see and select any Team. CetiRun does not check Team
membership. An Agent Policy limits the Agents of a Team. It does not limit
which users select the Team.

Sources: [portal claims]({{< repo "app/cetirun-portal/internal/auth/claims.go" >}}),
[administrator list]({{< repo "app/cetirun-portal/internal/auth/admin.go" >}}),
[ownership checks]({{< repo "app/cetirun-portal/internal/handler/instance_management.go" >}}),
and [Team selection]({{< repo "app/cetirun-portal/internal/handler/create_options.go" >}}).

## Workload isolation

| Control | Standard Agent | Sandbox Agent |
| --- | --- | --- |
| Runtime | The `kata-vm-isolation` RuntimeClass by default | The isolation type of the Sandbox: `kata` (the default) or `container`. The platform registration names the RuntimeClass. With `container`, the Agent shares the kernel of the node. Sandbox execution does not run `gvisor` |
| Root filesystem and privileges | A writable root filesystem and `allowPrivilegeEscalation: true` in the Agent container | A read-only root filesystem by default (`security.readOnlyRootFilesystem`). The containers run as non-root, drop all capabilities, and cannot escalate privileges |
| Inbound traffic | From the ingress gateway namespace to the Agent ports | The same by default. `network.ingress.mode: disabled` stops inbound traffic, and `network.ingress.requireMTLS` adds [ingress mTLS](../guides/agent-ingress-mtls/) |
| Outbound traffic | The NetworkPolicy of the agent chart. Refer to [Network access](#network-access) | A CiliumNetworkPolicy from the network rules of the Agent Policy, which allows public internet addresses by default, and a proxy in the Pod for blocked host names. Refer to [Policy-bound Teams](../operations/policy-teams/) |
| Write checks | Sandbox admission, when it is on | Sandbox admission. Only admission administrators can write policy-bound configuration |
| HOME | The agent chart defaults | The HOME storage of the Sandbox |

The [chart defaults]({{< repo "app/cetirun-chart/values.yaml" >}}) also
enable the desktop and the ttyd terminal. The
[desktop launcher]({{< repo "app/cetirun-chart/configs/desktop-entrypoint.sh" >}})
starts Chrome with `--no-sandbox`. Before you run untrusted workloads, check
the complete Pod, runtime, and access boundary.

### The trust boundary of an Agent Pod

All containers of an Agent Pod share one trust boundary:

- For OpenClaw and Hermes, the ttyd terminal runs in the Agent container. A
  terminal user can read HOME and the credentials in the environment of the
  Agent, for example the virtual key and the OpenClaw gateway token. A Custom
  Agent uses a separate terminal workload.
- The Agent controls the desktop browser through the Chrome DevTools port
  18800. As a result, the Agent can use each website session that a user opens
  in the desktop.

Do not sign in to a website in the desktop if the Agent must not use that
session.

### Kubernetes exec restriction

`execRestriction.enabled` defaults to `true`. The
[admission template]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}})
renders an `admissionregistration.k8s.io/v1` `ValidatingAdmissionPolicy` and
`ValidatingAdmissionPolicyBinding`. The cluster must support these APIs (the
chart documents Kubernetes 1.30+). The installer must have permission to create
these cluster-scoped resources.

The policy denies `CONNECT` requests to `pods/exec` across the release
namespace. The chart template has no fixed exception. When CetiAdmin is
enabled, the operator adds an exception for the CetiAdmin service account to
the policy of each standard Agent. Refer to
[Exec exception](../operations/cetiadmin/#exec-exception). For a Custom Agent
with the platform terminal, the policy applies the prepared exec rule of the
terminal.

The policy covers only `pods/exec`. Kubernetes RBAC alone controls other Pod
operations, for example `pods/attach`, `pods/portforward`, and ephemeral
containers. These operations use the Kubernetes API directly, not the ingress
gateway. Give these permissions in Agent namespaces only to trusted
administrators.

This policy applies to the Kubernetes exec API, not to every route to command
execution. The enabled ttyd terminal and the agent tools are separate access
paths. After installation, make sure that the cluster accepts and enforces the
policy.

## Platform component privileges

The platform components have more privileges than any Agent:

- The Portal service account can get, create, and update the Secrets in all
  namespaces. It can also create, change, and delete the CetiRun resources,
  and it can create namespaces.
- The operator service account can create, change, and delete Secrets,
  namespaces, Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, and
  ValidatingAdmissionPolicies. It can also read the AI Gateway master key.
- The AI Gateway master key can create and change virtual keys. It also gives
  access to the request records in the AI Gateway database.
- When connections are on, the Broker can get, list, create, update, and
  delete all Secrets in the namespace of each Agent.
- CetiAdmin has its own permissions on standard Agents. Refer to
  [CetiAdmin](../operations/cetiadmin/#access).

A person who controls the Portal or the operator can read the credentials of
all Agents.

Sources: [Portal ClusterRole]({{< repo "app/cetirun-portal/chart/templates/clusterrole.yaml" >}})
and [operator ClusterRole]({{< repo "app/cetirun-operator/chart/templates/clusterrole.yaml" >}}).

## Credentials

| Credential | Where it is | Who reads it |
| --- | --- | --- |
| OpenClaw gateway token, Hermes API key, and Hermes dashboard secret | Secret `gateway-token` in the Agent namespace, and the environment of the Agent container | The Agent. The Portal shows the token and the API key only to the owner |
| Virtual key of the Agent | Secret `ai-gateway-virtual-key`, and the environment of the Agent container | Each process in the Agent container |
| GitHub token of the owner, for GitHub Copilot | Secret `github-copilot-token` in the Agent namespace. The operator creates it, and the Portal writes the token after the owner signs in | The GitHub Copilot proxy of the Agent |
| Key of the GitHub Copilot proxy | Secret `ai-gateway-copilot-proxy` in the Agent namespace | The AI Gateway and the proxy |
| Tailscale auth key | Secret `tailscale-auth` in the Agent namespace | The Agent. The Portal shows it only to the owner |
| AI Gateway master key, salt key, and console password | The Secrets that the AI Gateway values `auth.*.existingSecret` name, as `make deploy` requires. Without them, the chart creates the Secret `cetirun-ai-gateway-auth` | The AI Gateway. The operator reads the master key from the Secret that its value `aiGateway.masterKeySecret` names |
| Model provider credentials | Secrets of the AI Gateway | The AI Gateway |
| Sign-in sessions | The cookie secret in `oauth2-proxy-credentials`, and the sessions, with refresh tokens, in the Valkey session store on the PVC `oauth2-proxy-sessions` | OAuth2 Proxy |
| Connection credentials | Encrypted records of the Broker. A key in Azure Key Vault protects their encryption keys. Shared connections are in the Broker namespace. Agent connections and bindings are in the Agent namespace | The Broker |
| OAuth client secrets of the GitHub and Google providers | The environment of the Broker | The Broker |
| Registry credentials | Source Secrets in the `cetirun-registry-credentials` namespace, and a pull Secret `registry-pull-<uid>` in the Agent namespace | The kubelet, to pull the image |

Provider sign-in state and agent configuration can persist under `/home/vibe`. The uninstallation of the Istio authentication
chart keeps the session PVC and its password Secret.

Keep browser access, terminal access, model credentials, and Kubernetes Secret
permissions consistent with instance ownership. Some platform identities have
wider Secret access, for example [CetiAdmin](../operations/cetiadmin/#access)
and the [Broker](../operations/agent-identity/#3-prepare-namespaces-and-tls).
Never include token files, cookies, private keys, complete Helm values, browser
profiles, or unreviewed browser network captures in public diagnostics. A UI
that masks a secret does not encrypt the value of that secret.

## Network access

A standard Agent uses the
[NetworkPolicy template]({{< repo "app/cetirun-chart/templates/networkpolicy.yaml" >}})
of the agent chart. By default, it allows this traffic:

| Direction | Allowed traffic |
| --- | --- |
| Inbound | From the ingress gateway namespace (`aks-istio-ingress` with the operator) to the Agent ports, the desktop on 6080, and the terminal on 7681 |
| Outbound | DNS. istiod on 15012. The AI Gateway on 4000. Connections MCP on 8443, when connections are on. The ranges in `networkPolicy.allowedInternalCIDRs`. All other IPv4 addresses except `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, and `169.254.169.254/32` |

The rules cover IPv4 only, so the policy denies IPv6 traffic. The outbound
rule has no port restrictions. The isolation between Agents depends on Pod and
Service address ranges inside the blocked private ranges, and the
infrastructure template uses such ranges. Review `allowedInternalCIDRs` for
each deployment.

The Sandbox Agents of policy-bound Teams use compiled Cilium policies instead.
These policies also allow public internet addresses by default. Refer to
[What changes for Agents](../operations/policy-teams/#what-changes-for-agents).

The [Istio ServiceEntries and access-log settings]({{< repo "infra/istio/values.yaml" >}})
are audit configuration, not a destination allowlist. Source configuration and
access logs do not prove that unlisted destinations are blocked.

## Data collection

By default, these stores can contain conversation content:

| Store | Default content | Control |
| --- | --- | --- |
| AI Gateway database (`LiteLLM_SpendLogs`) | The request and the response of each model call, for seven days by default | `spendLogs.storePrompts` and `spendLogs.retention` of the AI Gateway chart |
| AI Gateway content database (optional) | With `content.enabled: true`, the complete request and response of each call, in the Azure Data Explorer database `CetiRunContent`, for seven days in the cluster template. The spend log then keeps no content | `content.enabled` of the AI Gateway chart, and the readers with the `Viewer` role on that database |
| Container logs | The output of the session logger and of the Agents. Container Insights or the optional ADX-Mon collection can copy them to Azure | `logger.enabled` of the agent chart, and the collection settings of the cluster |
| Memory | The notes of each Agent, in its HOME | `memoryContext.enabled` of the operator chart, `true` by default |
| HOME | Sessions, files, configuration, and the browser profile of the desktop | The Agent lifecycle |

The [cluster template]({{< repo "infra/cluster/main.bicep" >}}) turns
Container Insights and managed Prometheus off. `make deploy` installs the
[ADX-Mon collection](../operations/observability/), which collects the
container stdout and stderr of the platform and the Agents into Azure Data
Explorer. The Agent output, which can contain conversation text, goes to a
database of its own, `CetiRunAgents`, with readers of its own.

If you turn Container Insights on yourself, it also copies the environment
variables of containers by default, and these copies include Secret values. The
[agent settings]({{< repo "infra/cluster/container-azm-ms-agentconfig.yaml" >}})
disable that copy. The cluster helper creates these settings, or examines them
on a cluster that has its own. Some CetiRun containers, for example the Agent
runtime container, keep credentials in their environment and do not disable
the copy for themselves. For the list, refer to
[PRIVACY.md]({{< repo "PRIVACY.md" >}}).

The
[Azure Data Explorer template]({{< repo "infra/cluster/modules/adx.bicep" >}})
sets the retention of each database: 90 days for logs, 30 days for metrics, and
14 days for Agent logs. Databases that already exist, exports, and other stores
can differ.

`egress.accessLog.enabled` of the Istio chart is `true` by default. The access
log applies to each proxy in the mesh, including the ingress gateway. It
records the client address and the request path of user requests. When the AI
Gateway metrics are on, as in the platform chart, their port has no
authentication. Their labels include key aliases, user IDs, and Team names.

For the details of each path, read [PRIVACY.md]({{< repo "PRIVACY.md" >}}).
Document the retention, redaction, and deletion procedures of each store. If
you disable one logger, the other collection paths continue.

## Persistent data and deletion

When you delete an Agent, the operator deletes its namespace and the HOME
volume claim in it. With the AKS `default` StorageClass, whose reclaim policy
is usually `Delete`, this also deletes the disk and its data.
`persistence.retainPolicy: Retain` keeps the volume claim only when the
StatefulSet is deleted or scaled to zero, for example during suspension.
Before you delete an Agent, make a backup of HOME. For the details, read
[Storage](../operations/storage/#retention-is-not-deletion-protection).

The deletion of an Agent does not erase the other copies of its data. Copies
can stay in logs, the AI Gateway database, backups, kept volumes, browser
devices, and external providers. CetiRun has no global erasure schedule.
Define the retention and the deletion for each store.

## External services

Configured model providers and tools can receive prompts, conversation context,
files, and tool output. Connected services receive the requests that an Agent
makes through its connections. GitHub Copilot sign-in and model discovery
connect to authentication and catalog endpoints, separately from inference
requests.

Microsoft Entra ID and Tailscale add their own service boundaries. Skills,
plugins, CLI tools, package registries, and browsed sites create more routes.
Check the terms, retention, training-use settings, and region of each
service. No single CetiRun setting disables all third-party telemetry or
network activity.

## Deployment checklist

- Make sure that the part before `@` is unique for all users who can sign in,
  and keep guest accounts out of `allowedGroups`.
- Keep `admins.aliases` of the Portal chart to the people who manage the
  Portal.
- Give write access to the platform namespaces, the CetiRun resources, and the
  Secrets in Agent namespaces only to cluster administrators.
- Keep Istio sidecar injection in the Portal namespace, and keep
  `istio.internalAuthMTLS: true`.
- Remove the unused prefixes from `authPolicy.authExemptPrefixes`, and review
  `networkPolicy.allowedInternalCIDRs`.
- Before you onboard users, decide the values of `spendLogs.storePrompts`,
  `spendLogs.retention`, and the log retention.
- Review the permissions of [CetiAdmin](../operations/cetiadmin/), or disable
  it.

## Report a vulnerability

Report suspected vulnerabilities privately to the Microsoft Security Response
Center (MSRC). Follow the instructions at
[https://aka.ms/SECURITY.md](https://aka.ms/SECURITY.md), which
[SECURITY.md]({{< repo "SECURITY.md" >}}) also links. Do not report
vulnerabilities, exploits, or sensitive reproductions through public GitHub
issues or pull requests. This rule applies even after you remove credentials or
deployment identifiers.

For non-security questions, use the channels in
[SUPPORT.md]({{< repo "SUPPORT.md" >}}). For the privacy of this documentation
site, read
[Theme and third-party assets](../contributing/site/#theme-and-third-party-assets).
