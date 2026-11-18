---
title: "Glossary"
description: "One name and one meaning for each CetiRun term: Agents, Teams, Agent Policies, Sandboxes, the AI Gateway, the ingress gateway, and administrator roles."
weight: 30
---

Each term on this page has one meaning in the CetiRun documentation. Where the
Portal or the Kubernetes API shows a name, the documentation uses the same
name. For how the components work together, read
[Architecture](../architecture/). Older pages and the code can use other names.
[Other names](#other-names) maps them to the names on this page.

## Agents

### Agent

One user's long-lived AI agent in CetiRun. A cluster-scoped `Agent` resource
declares it. [Resource model](../architecture/#resource-model) lists what the
resource decides. "AI agent" in lowercase means the general idea.

### Owner

The user who creates an Agent. Only the owner can open the Agent through the
ingress gateway: its Agent UI, chat, terminal, and desktop. This rule needs a
unique [alias](#alias) for each user. For the rules, read
[Authentication and authorization](../../security/#authentication-and-authorization).

### Agent type

The program that an Agent runs: Hermes (the default), OpenClaw, or Custom. The
field is `spec.agent.type`, and the Portal label is **Agent Type**.

### Custom Agent

An Agent of the Custom agent type. It runs a container image that a platform
administrator approved. The image must follow the Custom image contract in
[Agent Harness](../../reference/agent-harness/). A Custom Agent can serve a chat
API, a browser application (its Web UI), or no HTTP interface.

### Agent harness

The contract between CetiRun and the program in an Agent: the `spec.agent`
fields, the image, HOME, the ports, and the HTTP interfaces.
[Agent Harness](../../reference/agent-harness/) defines it. The Agent Policy
field `harnesses.allowed` lists the agent types that a policy allows.

### Instance

The Kubernetes objects that the operator creates for one Agent. They include
the namespace `cetirun-<name>`, one Helm release, a StatefulSet with zero or
one Pod, and the HOME volume.
[Architecture](../architecture/#kubernetes-objects-of-an-agent) lists them. This
documentation uses "instance" only for these objects and in names that contain
the word, for example the Portal path `/instances/<name>`.

### HOME

The persistent volume of an Agent. OpenClaw and Hermes mount it at
`/home/vibe`, and a Custom Agent mounts it at `/home/<username>`. HOME keeps
files, configuration, credentials, and memory notes when the Pod restarts and
while the Agent is suspended. When you delete the Agent, the operator deletes
the HOME volume claim with the namespace of the Agent.

### Workspace

The Portal page that opens one Agent. Its buttons open the **Overview**,
**Chat**, **Agent UI**, **Terminal**, and **Desktop** of the Agent. **Chat**
shows only for a Custom Agent that serves chat.

### Agent UI

The native browser interface of an Agent at `/ui/<name>/`: the OpenClaw Control
UI, the Hermes Webapp, or the Web UI of a Custom Agent.

### Portal Chat

The chat page in the Portal for a Custom Agent that serves the CetiRun chat API
and has no Web UI. For the task, read
[Chat with a Custom Agent](../../guides/custom-chat/).

### Suspension

The state of an Agent whose StatefulSet has zero replicas. HOME stays. CetiRun
does not suspend idle Agents automatically.

## Teams and policies

### Team

Shared configuration that an Agent selects with `spec.team`: context for the
agent, default skills, shared skill storage, and smart-routing models. A Team
that also binds one Agent Policy and its Sandboxes is a
[policy-bound Team](#policy-bound-team).

A Team has no members. Any signed-in user can select any Team, and a Team does
not give access to the Agents of other users. A Team is optional for an Agent.

### Agent Policy

A cluster-scoped `AgentPolicy` resource that limits what the Agents of a
policy-bound Team can use, for example their models, budget, skills, and
network access. A Portal administrator or an admission administrator creates
it. [Resource model](../architecture/#resource-model) lists its controls. The
Portal lists Agent Policies under **Agent policies**.

### Sandbox

A cluster-scoped `Sandbox` resource that sets the isolation type, the CPU and
memory, and the HOME storage of Sandbox Agents. A Portal administrator or an
admission administrator creates it. A Sandbox is not the Kata RuntimeClass
`kata-vm-isolation` that standard Agents use by default, and it is not the
Chrome sandbox.

### Isolation type

The `spec.type` of a Sandbox: `kata` (the default), `gvisor`, or `container`.
With `container`, the Agent shares the kernel of the node. Sandbox execution
runs only `kata` and `container`, so a `gvisor` Sandbox does not become ready.
The Portal labels this field **Runtime**.

### Policy-bound Team

A Team that names one Agent Policy (`agentPolicyRef`) and the Sandboxes
(`sandboxRefs` and `defaultSandboxRef`) that its Agents can use. A Team must
set all three fields or none of them.

### Sandbox Agent

An Agent that an Agent Policy controls. It runs in one of the Sandboxes of its
Team and gets only what the Agent Policy allows. An Agent becomes a Sandbox
Agent when it selects a policy-bound Team or a Sandbox. For the cluster
requirements, read [Policy-bound Teams](../../operations/policy-teams/).

### Standard Agent

An Agent that does not select a policy-bound Team or a Sandbox. It can have no
Team, or a Team without an Agent Policy. It uses the agent chart defaults and
the deployment-wide settings. No Agent Policy applies to it.

### Sandbox execution

The operator values `sandboxSources.enabled` and
`sandboxSources.nativeExecution`, which let the operator run Sandbox Agents.
Both are off by default, and `make deploy` does not enable them. While they are
off, a Sandbox Agent shows the phase `Failed` with the reason
`UnsupportedControl`. To enable Sandbox execution, read
[Enable Sandbox execution](../../operations/policy-teams/#enable-sandbox-execution).

### Held

A Sandbox Agent that the operator does not run. The operator keeps the
StatefulSet of the Agent at zero replicas and removes its route. The Reference
pages use this word.

### Common executor

The operator flow that runs Sandbox Agents, including Custom Agents in a
Sandbox, when Sandbox execution is on. A standard Custom Agent does not use it.
The Reference pages use this name for what other pages call Sandbox Agents.

### Single-start lifetime

The lifetime that the operator recorded for a Sandbox Agent that started under
the earlier version-1 native execution profile. Such an Agent keeps that
profile and its limits. The Reference pages use this name.

## Models

### AI Gateway

The shared model endpoint of the platform. It runs LiteLLM with its own
PostgreSQL database and Valkey cache. The operator configures each Agent to
call models through the AI Gateway.

### Virtual key

The AI Gateway key of one Agent. The operator creates it and keeps it in the
Secret `ai-gateway-virtual-key` in the Agent's namespace. The key carries the
models and the monthly budget of the Agent. For a Sandbox Agent, it also
carries the rate limits of the Agent Policy.

### Budget

A monthly spend limit in US dollars for one virtual key. An Agent Policy sets
the default and the maximum budget of its Agents. A standard Agent gets the
budget in the operator value `aiGateway.defaultBudget` when the operator
creates its key. CetiRun has no token quota.

### Smart routing

The AI Gateway sends each request for a router model to a cheaper or a stronger
model. A Team can offer its own model routers. For the configuration, read
[Smart routing](../../operations/smart-routing/).

### GitHub Copilot proxy

A proxy in the namespace of one Agent that holds the owner's GitHub Copilot
sign-in. The AI Gateway sends the Agent's Copilot model calls to this proxy.

## Skills, memory, and connections

### Skill

A directory with a `SKILL.md` file that gives an agent instructions and helper
files. A skill does not give access to a tool or a service.

### Skill Marketplace

An optional service, off by default, that publishes approved skills from one
Git repository as immutable releases. Only the Sandbox Agents of policy-bound
Teams receive marketplace skills. For the configuration, read
[Skill Marketplace](../../operations/skill-marketplace/).

### Memory

The built-in `cetirun-memory` skill, on by default. It keeps the notes of an
Agent in HOME, under `$HOME/.cetirun/memory`. For what it keeps and which
sessions use it, read
[Private Memory Context](../../reference/agent-harness/#private-memory-context).

### Connection

An authorization that an owner gives to an Agent, so that the Agent can use an
external account, for example a GitHub account. The owner adds it in the
**Identities** section of the Agent with **Add identity**. The **Connections**
page of the Portal lists and removes the connections of the owner.

The Agent Identity Broker keeps the long-lived credentials. Connections MCP
gets a short-lived token from the Broker and calls the service for the Agent.
The Agent does not receive the tokens. The Portal shows connections only when
the deployment enables them, as `make deploy` does. For the task, read
[Agent Connections and Identity](../../guides/agent-identity/).

### Agent Identity

A Microsoft Entra Agent Identity for one Agent. The operator creates it through
Microsoft Graph. The Agent uses it to call Azure as itself, not as its owner.

### Agent Identity Broker

The service that keeps the encrypted credentials of connections and gives
Connections MCP short-lived tokens. After the first mention on a page, the
documentation calls it "the Broker".

### Connections MCP

The MCP server that Agents call to use their connections.

## Platform

### Operator

The CetiRun Kubernetes operator. It reconciles the `Agent`, `Team`, `Sandbox`,
and `AgentPolicy` resources and creates the Kubernetes objects of each Agent.
In this documentation, "operator" always means this component.

### Apex domain

The `domain` value of the Istio chart, for example `example.com`. The Portal,
the sign-in callback, and the Agent routes use this host.

### Ingress gateway

The managed Istio ingress gateway that receives all browser and API traffic.
The Agent routes refer to the Istio `Gateway` `aks-istio-ingress/cetirun-gateway`
by default.

### Ownership check

The check that the ingress gateway does for each Agent route. The ingress
gateway asks the Portal if the signed-in user owns the Agent. It rejects the
request with HTTP 403 unless the Portal confirms.

### Alias

The lowercase part of a user's sign-in name before `@`. The Portal uses the
alias to name Agents and to identify Portal administrators. All users who can
sign in must have different aliases. For the rules, read
[Authentication and authorization](../../security/#authentication-and-authorization).

### OpenClaw gateway token

The token that protects the OpenClaw Control UI. The Portal field is **Gateway
Token**. It is in the Secret `gateway-token`, which also holds the Hermes API
key. It is not a model credential.

### Hermes gateway process

The Hermes process that serves its messaging channels and writes `gateway.log`.

Several names contain the word "gateway": the AI Gateway, the ingress gateway,
the OpenClaw gateway token, and the Hermes gateway process. In code format,
`Gateway` is a Kubernetes resource of the Istio or Gateway API, and
`openclaw-gateway` and `hermes-gateway` are image names.

## Roles

[Roles](../../security/#roles) lists how a person gets each role and what the
role allows.

- **Agent owner:** the user who creates an Agent.
- **Portal administrator:** a user whose verified object ID is in the Portal
  value `admins.objectIDs`, in the configured Entra tenant. Manages Agents,
  Teams, Agent Policies, and Sandboxes in the Portal.
- **Admission administrator:** a user or group that can write policy-bound
  configuration directly through the Kubernetes API.
- **Platform administrator:** a person who installs, upgrades, and operates
  CetiRun, with the Azure and cluster permissions of the deployment.
- **CetiAdmin owner:** the user in `cetiadmin.owner` of the operator chart.
  Opens [CetiAdmin](../../operations/cetiadmin/), the operations Agent, and can
  tell it to use its access to standard Agents.

## Other names

| Other name | Name in this documentation |
| --- | --- |
| "instance", "workspace" (for the Agent) | Agent |
| "workspace" (for the volume) | HOME |
| "the Gateway" | AI Gateway, ingress gateway, or OpenClaw gateway token |
| "legacy Agent", "ordinary Agent" | standard Agent |
| "governed Agent", "native Sandbox", "common-executor Sandbox Agent", "policy-bound Agent", "policy-bound instance" | Sandbox Agent |
| "policy-enabled Team", "policy-configured Team" | policy-bound Team |
| "Agent policy", "Policy" | Agent Policy, or `AgentPolicy` for the kind |
| "BYO", "Custom runtime", "Custom provider", "Custom Harness" | Custom Agent |
| "runtime", "harness" (for the agent type) | agent type |
| "runtime chart" | agent chart |
| "console" (of an Agent), "native interface" | Agent UI |
| "Reauthorize" (for a connection) | **Sign in again**, the label in the Portal |
| "administrator" (without a role name) | the name of the role |
