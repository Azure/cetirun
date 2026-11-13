---
title: "Architecture"
description: "How the Portal, the operator, the AI Gateway, and the ingress gateway run Agents on AKS, and how requests and models reach each Agent."
weight: 10
---

A platform administrator installs CetiRun on an AKS cluster. The installation
has shared services for all users, and one set of Kubernetes objects for each
Agent. Users create Agents in the Portal. The operator makes a running
workload from each `Agent` resource. Each Agent calls models through the AI
Gateway and uses external accounts through Connections MCP. For the terms on
this page, read the [Glossary](../glossary/).

## Components

`make deploy` installs three Helm releases: the Istio authentication chart,
the Agent Identity Broker, and the platform chart. In the platform chart, it
enables the operator, the Portal, the AI Gateway, and Connections MCP. The
operator then installs one agent chart release for each Agent.

### Shared services

| Component | Source | Responsibility |
| --- | --- | --- |
| Operator | [app/cetirun-operator]({{< repo "app/cetirun-operator" "tree" >}}) | Reconciles `Agent`, `Team`, `Sandbox`, and `AgentPolicy`. For each Agent, it installs the agent chart release, the route, and the Secrets. It also gives each Agent its AI Gateway virtual key, its GitHub Copilot proxy, and, when Agent Identity is on, its Agent Identity |
| Sandbox admission | Operator chart | Webhooks that check each write of an `Agent`, `Team`, `Sandbox`, or `AgentPolicy`, and limit who can write policy-bound configuration. Off by default in the operator chart. `make deploy` enables and enforces it |
| Skill Marketplace worker | Operator chart | Publishes approved skills from one Git repository for policy-bound Teams. Off by default |
| CetiAdmin | [Operator CetiAdmin module]({{< repo "app/cetirun-operator/internal/cetiadmin" "tree" >}}) | Hermes administration Agent, operations skills, and helpers deployed by the operator. Enabled by default. Refer to [CetiAdmin](../../operations/cetiadmin/) for its access |
| Portal | [app/cetirun-portal]({{< repo "app/cetirun-portal" "tree" >}}) | Self-service UI and API for owners, and the admin pages. It also answers the ownership check of the ingress gateway |
| AI Gateway | [app/cetirun-ai-gateway]({{< repo "app/cetirun-ai-gateway" "tree" >}}) | LiteLLM with PostgreSQL and Valkey. The model endpoint that the operator configures for each Agent. It applies the models, budget, and rate limits of each virtual key, and keeps the provider credentials |
| Connections MCP | [app/cetirun-connections-mcp]({{< repo "app/cetirun-connections-mcp" "tree" >}}) | MCP server that Agents call to use their connections. It calls the external services with short-lived tokens from the Broker |
| Agent Identity Broker | [app/cetirun-agent-broker]({{< repo "app/cetirun-agent-broker" "tree" >}}) | Keeps the encrypted credentials of connections, and gives Connections MCP short-lived tokens |
| Istio authentication chart | [infra/istio]({{< repo "infra/istio" "tree" >}}) | Configuration of the ingress gateway, OAuth2 Proxy with its session store, the ownership filter, and egress audit logs |

### Objects for each Agent

| Component | Source | Responsibility |
| --- | --- | --- |
| Agent chart release | [app/cetirun-chart]({{< repo "app/cetirun-chart" "tree" >}}) | StatefulSet, Services, HOME volume, network policy, desktop, terminal, and session logger |
| Memory | [app/cetirun-memory-context]({{< repo "app/cetirun-memory-context" "tree" >}}) | An init container that installs the `cetirun-memory` skill into each OpenClaw, Hermes, and Custom Agent |
| GitHub Copilot proxy | Agent chart, or the operator for a Custom Agent | Holds the owner's GitHub Copilot sign-in. The AI Gateway sends the Copilot model calls of the Agent to it |
| Custom terminal | The operator, or the agent chart for a Sandbox Agent | A separate terminal workload for a Custom Agent |

### Installation and infrastructure

| Component | Source | Responsibility |
| --- | --- | --- |
| Platform chart | [app/cetirun-platform]({{< repo "app/cetirun-platform" "tree" >}}) | Installs the operator, the Portal, the AI Gateway, Connections MCP, and the observability chart as one release |
| Observability chart | [app/cetirun-observability]({{< repo "app/cetirun-observability" "tree" >}}) | ADX-Mon collection of logs, events, and metrics into Azure Data Explorer. `make deploy` installs it with the platform |
| Infrastructure | [infra]({{< repo "infra" "tree" >}}) | Bicep for AKS, Azure Blob NFS, and Azure Data Explorer, and cert-manager configuration for public TLS |

## Control path

The control path creates and changes Agents. Browser traffic to an Agent does
not use this path.

```text
Owner or Portal administrator
  │  Portal UI and API
  ▼
Portal ──▶ Kubernetes API ──▶ Sandbox admission
                │
                ▼
            operator ──▶ agent chart release in the namespace cetirun-<name>
                     ├─▶ VirtualService for the Agent route
                     ├─▶ AI Gateway virtual key and GitHub Copilot proxy
                     └─▶ Agent Identity, through Microsoft Graph
```

The Portal writes `Agent` resources with its own service account. When Sandbox
admission is on, it checks each write before the Kubernetes API stores it. The
operator watches the four resources and reconciles the Kubernetes objects of
each Agent. When a Team changes, the operator updates the Agents of the Team.

## Request path

The request path carries the traffic of users and Agents.

```text
Browser ─▶ ingress gateway ─▶ OAuth2 Proxy (Microsoft Entra ID)
             ├─ Portal paths ─────────────────────────────▶ Portal
             └─ /ui/<name>/, /<name>/term/, /<name>/desktop/
                  ─▶ ownership check (asks the Portal) ──────▶ Agent Pod

Agent Pod ─▶ AI Gateway ─▶ model providers
                      └──▶ GitHub Copilot proxy of the Agent ─▶ GitHub Copilot
Agent Pod ─▶ Connections MCP ─▶ GitHub, Google, Azure
                  └─ short-lived token from the Agent Identity Broker

Tailnet device ─▶ Agent Pod   (optional Tailscale path, without the ingress gateway)
```

The apex domain is the `domain` value of the Istio chart, for example
`example.com`. The Portal and the Agent routes share this host. OAuth2 Proxy
uses `https://<domain>/oauth2/callback`. A platform administrator prepares the
Istio authentication chart, the Microsoft Entra ID application, DNS, and TLS
before users create Agents.

After OAuth2 Proxy signs a user in, a filter on the ingress gateway checks
ownership for each Agent route. On the apex domain, the filter reads the Agent
name from `/ui/<name>/` or from the first path segment. On a host of the form
`<name>.<domain>`, it reads the name from the host. The operator binds the
route of each Agent only to the apex domain. Portal paths, for example `/api`,
`/admin`, `/instances`, and `/assets`, skip this check.

The filter asks the Portal's internal `/internal/auth` listener on port 8081 if
the signed-in user owns the Agent. It rejects the request with HTTP 403 unless
the Portal confirms. The Portal API also checks ownership for each Agent
request. The route goes from the ingress gateway directly to the Agent's
Service, not through the Portal or the operator.

Keep the internal listener and backend Services private.

The filter does not use `authPolicy.adminAliases`. For each prefix in
`authPolicy.aliasExemptPrefixes`, it skips the ownership check on the host
`<prefix>.<domain>`. Refer to [Exemptions](../../operations/istio/#exemptions).

Sources: [OAuth2 deployment]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}}),
[ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}),
and [portal handlers]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

## Resource model

All CetiRun resources are cluster-scoped, in the API group
`cetirun.microsoft.com/v1alpha1`. For their fields, read
[Custom resources](../../reference/crds/).

| Resource | What it decides |
| --- | --- |
| `Agent` | One Agent: the owner, the agent type and image, the Team, the Sandbox, the model, and the lifecycle settings |
| `Team` | Context, default skills, shared skill storage, and smart-routing models for its Agents. A policy-bound Team also names one `AgentPolicy`, the allowed Sandboxes, and a default Sandbox |
| `AgentPolicy` | For Sandbox Agents: the agent types, skills, models, budget, rate limits, identity, Kubernetes access, network access, inbound access, and root filesystem |
| `Sandbox` | For Sandbox Agents: the isolation type, the CPU and memory, and the HOME storage |

```text
Agent ──spec.team──▶ Team ──agentPolicyRef──▶ AgentPolicy
  │                   └──sandboxRefs, defaultSandboxRef──▶ Sandbox
  └──spec.sandboxRef──▶ one of the Team's Sandboxes
```

### Two kinds of Agents

| | Standard Agent | Sandbox Agent |
| --- | --- | --- |
| How to tell | No Team, or a Team without an Agent Policy, and no Sandbox | A policy-bound Team or a selected Sandbox |
| Limits | The agent chart defaults and the deployment values, for example `features.allowedAgentTypes` and `features.maxInstancesPerUser` of the Portal chart, and `aiGateway.defaultBudget` of the operator chart | The Agent Policy and the Sandboxes of the Team |
| Rate limits | None | From the Agent Policy |
| Start | The operator starts the Agent | Only when Sandbox execution is on. Otherwise the Agent shows `Failed` with the reason `UnsupportedControl` |

For the isolation, network, and storage controls of each kind, read
[Workload isolation](../../security/#workload-isolation). To prepare a cluster
for Sandbox Agents, read [Policy-bound Teams](../../operations/policy-teams/).

### Kubernetes objects of an Agent

For an Agent named `alice`, the operator creates these objects. The names come
from `metadata.name`, not from the owner's alias.

| Object | Name |
| --- | --- |
| Namespace | `cetirun-alice` |
| Helm release of the agent chart | `openclaw-alice`, for every agent type |
| StatefulSet and Service | `openclaw-alice-cetirun`, with zero or one replica |
| HOME volume claim | `data-openclaw-alice-cetirun-0`. A Sandbox Agent that runs uses the claim `home` |
| VirtualService | `cetirun-alice` |
| Secrets | `gateway-token` for OpenClaw and Hermes, and `ai-gateway-virtual-key`. When GitHub Copilot is on, also `ai-gateway-copilot-proxy` and `github-copilot-token`. For what each one holds, read [Credentials](../../security/#credentials) |
| GitHub Copilot proxy | `openclaw-alice-copilot-proxy` |
| Custom terminal | `openclaw-alice-cetirun-terminal`, for a Custom Agent |
| Exec policy | The cluster-scoped ValidatingAdmissionPolicy and binding `openclaw-alice-cetirun-cetirun-alice-deny-exec` |

Do not override the resource names of the chart. The operator expects the
Service and StatefulSet names that it calculates.

## Names and routes

An Agent name is a DNS label. The Portal accepts names of at most 35
characters. Kubernetes adds a revision hash to the StatefulSet name in a label,
and a label can have at most 63 characters. Do not use a reserved name. For the
list, read [Custom resources](../../reference/crds/).

For OpenClaw and Hermes, and for a Custom Agent with a Web UI,
`status.accessUrl` is `https://<domain>/ui/<name>/`. It is the configured URL.
It does not show that ingress works.

| External path | Destination at the default ports |
| --- | --- |
| `/ui/<name>/` | OpenClaw Control UI on 18789, Hermes Webapp on 9120, or the Web UI of a Custom Agent |
| `/ui/<name>/v1...` and `/ui/<name>/health...` for Hermes | Hermes API on 8642, with the UI prefix removed |
| `/<name>/term/` | ttyd on 7681, or the Custom terminal |
| `/<name>/desktop/` | noVNC on 6080 |

OpenClaw serves its Control UI under the full `/ui/<name>/` prefix, so the
route keeps the path. A WebSocket upgrade on the exact `/ui/<name>` path goes
to OpenClaw without an HTTP redirect, because a browser cannot follow a
redirect during an upgrade.

Hermes serves its Webapp at `/`. The route removes the prefix and sends the
header `x-forwarded-prefix: /ui/<name>`. Hermes has its own login after the
platform sign-in. **Agent UI** in the Portal completes it with the username
`cetirun` and the Hermes API key of the Agent. Refer to
[Open a Hermes Agent](../../getting-started/first-instance/#open-a-hermes-agent).
These routes do not carry model calls.

The VirtualService of an Agent is in the Agent's namespace. It refers to the
operator's `istioGateway`, which is `aks-istio-ingress/cetirun-gateway` by
default. Configure the namespace of the ingress gateway workload separately in
`cetirun.helmValues.networkPolicy.istioIngressNamespace`. The operator sets the
route ports itself and does not read Helm port overrides. Keep the default
ports.

Sources: [route builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [route tests]({{< repo "app/cetirun-operator/internal/controller/istio_test.go" >}}).

## Isolation

By default, the agent chart selects the `kata-vm-isolation` RuntimeClass, so
a standard Agent runs in a Kata VM. The cluster must have the RuntimeClass and
compatible nodes. A rendered field does not show that the VM isolation works.
A Sandbox Agent uses the isolation type of its Sandbox.

`execRestriction.enabled` is `true` by default and renders
`admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy resources. The
cluster must support and enforce that API. This policy targets `pods/exec`, not
the separate browser terminal.

The policy does not cover `pods/attach`, `pods/portforward`, or ephemeral
containers. For a standard Agent, Kubernetes RBAC alone controls these
operations. For a Sandbox Agent, Sandbox admission refuses them and
`pods/exec`, except the exec of the Custom terminal. Refer to
[Kubernetes exec restriction](../../security/#kubernetes-exec-restriction).

Namespaces, NetworkPolicy, Istio policy, and the RuntimeClass are separate
controls, and each one covers a different path. All containers of an Agent Pod
share one trust boundary. Before you select workload privileges or storage
access, read [Security](../../security/).

Sources: [chart values]({{< repo "app/cetirun-chart/values.yaml" >}}),
[values schema]({{< repo "app/cetirun-chart/values.schema.json" >}}),
[chart validation]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
and [exec policy]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}}).

## Scaling and lifecycle

Each Agent runs as one Pod of a StatefulSet with zero or one replica. If you
enable the HPA of the agent chart, set both `minReplicas` and `maxReplicas` to
one. Suspension scales the StatefulSet to zero and keeps HOME. CetiRun does not
suspend idle Agents automatically. To suspend or resume an Agent, read
[Manage your Agent](../../guides/lifecycle/).

## Model access

The operator configures each Agent to call models through the AI Gateway, and
it gives each Agent its own virtual key. The key grants the models and the
monthly budget of the Agent. For a Sandbox Agent, the key also carries the rate
limits of the Agent Policy. The AI Gateway keeps the provider credentials, so
they are not in the Agent. Helm values cannot change the model settings of an
Agent. [Rules for all Agents](../configuration/#rules-for-all-agents) lists the
protected values.

The NetworkPolicy of a standard Agent allows public IPv4 traffic. Thus, tools
in the Agent can call other model services with credentials that a user
supplies.

For GitHub Copilot models, the AI Gateway sends the calls to the Copilot proxy
of the Agent, which holds the owner's sign-in. With smart routing, the AI
Gateway sends each request for a router model to a cheaper or a stronger model.
For the user task, read [Choose a model](../../guides/models/). For the
routers, read [Smart routing](../../operations/smart-routing/).

## Storage

With persistence enabled, OpenClaw and Hermes mount HOME at `/home/vibe`, for
files, agent configuration, and credentials. A Custom Agent mounts it at
`/home/<username>`. The operator, the standalone chart, and Custom Agents use
these defaults: `100Gi`, `ReadWriteOnce`, and the StorageClass named `default`
(Azure Disk on AKS). A Sandbox Agent gets its HOME storage from its Sandbox.
Shared skills can be mounted separately and read-only. HOME stays when the Pod
restarts and while the Agent is suspended.

HOME is not a backup. When you delete an Agent, the operator deletes HOME with
the namespace of the Agent. For the consequences, read
[Persistent data and deletion](../../security/#persistent-data-and-deletion).

Sources: [operator defaults]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).
