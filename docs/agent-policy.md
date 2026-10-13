# AgentPolicy architecture

See the public [custom-resource reference](../site/content/en/docs/reference/crds.md)
for the complete field reference.

This document describes the first CetiRun AgentPolicy implementation. It is a
policy framework, not an approval workflow. There is no `GovernanceAction`,
`teamAuthorization`, policy class, or user-selected policy.

## Resource model

Built-in Agents (`openclaw` and `hermes`) use one unambiguous binding:

```text
Agent.spec.team
  -> Team.spec.agentPolicyRef
  -> AgentPolicy

Agent.spec.sandboxRef
  -> one of Team.spec.sandboxRefs
```

The Team also supplies `defaultSandboxRef` for creation, shared skills, and
team context. The Agent explicitly requests model sources and may request a
budget no greater than the policy maximum.

Custom Agents use the separate Custom runtime contract. They may use Team
configuration, and may explicitly use the platform Gateway integration, but
they do not resolve or execute an AgentPolicy and must not set `sandboxRef`.
This is an explicit runtime boundary, not a missing-policy fallback. The Portal
shows Policy as `Not applicable` for Custom Agents rather than implying that a
policy is pending.

The four cluster-scoped CRDs are:

| Kind | Purpose | Important spec fields |
| --- | --- | --- |
| `Agent` | One user-owned runtime | `agent.type`, `team`, `sandboxRef`, `modelAccess`, `model`, `skillOverrides`, `agentIdentity` |
| `Team` | Administrative grouping and the single policy assignment | `agentPolicyRef`, `sandboxRefs`, `defaultSandboxRef`, `skills`, `memberContext` |
| `AgentPolicy` | Allowed requests and component-specific control intent | `harnesses`, `skills`, `modelAccess`, `agentIdentity`, `security`, `network`, `kubernetesAccess` |
| `Sandbox` | Runtime isolation, resources, and storage | `type`, `resources`, `storage` |

`AgentPolicy.modelAccess.gatewayBudget` uses the AI Gateway's native monthly
USD budget. CetiRun does not implement a separate token quota.

## Resolution is not execution

The Agent controller resolves the Team assignment and validates the Agent's
requests. It reports only whether a current effective policy can be resolved:

| `Agent.status.policy.phase` | `PolicyResolved` | Meaning |
| --- | --- | --- |
| `Resolved` | `True` | Current Team/Policy UIDs and the requested harness, skills, models, budget, and identity are valid |
| `Denied` | `False` | The current request is not allowed or its protected binding no longer matches |
| absent | `Unknown` | A current Team or AgentPolicy observation cannot be read or is not ready |

Policy resolution does not claim that a Gateway grant, Sandbox control, or
identity has been applied. Each component executes the relevant part and owns
its own status:

- AI Gateway owns the per-Agent virtual key, exact model groups, monthly USD
  budget, blocking, and `status.aiGateway`.
- Sandbox preparation and admission own runtime/storage/network enforcement,
  workload start, and the Sandbox-related Agent conditions.
- Agent Identity owns Entra identity provisioning and
  `status.agentIdentity`.

There is intentionally no aggregate `PolicyApplied` condition. Operational
readiness comes from the normal component conditions and the Agent `Ready`
condition.

## Component responsibilities

| Component | Responsibilities | Must not do |
| --- | --- | --- |
| Portal | CRUD for AgentPolicy and Team; present policy and component status; validate creation input before submitting it | Let an Agent owner choose an arbitrary policy; report policy resolution as enforcement |
| Kubernetes admission | Validate references and protected updates; bind exact Agent, Team, Sandbox, and AgentPolicy UIDs; require explicit reauthorization when the binding changes | Create approval resources or infer authorization from object names |
| AgentPolicy/Team reconcilers | Normalize and validate configuration; publish `Accepted` for the observed generation | Start workloads or grant model access |
| Agent controller | Resolve the single Team policy; report `Resolved`, `Denied`, or `Unknown`; stop compute and model access when current evidence is not valid | Apply another component's controls or let a built-in runtime bypass policy |
| AI Gateway reconciler | Convert exact policy model IDs and USD budget into a per-Agent LiteLLM key; block before repairing grant drift; verify the result before unblocking | Use a broad shared-model group or expose provider credentials to the Agent |
| Sandbox resolver/compiler/admission | Resolve Sandbox inputs, compile supported controls, render a held workload, enforce protected mutations, and start only a qualified native profile | Treat an accepted policy as proof that every requested control is implemented |
| Agent Identity reconciler | Provision and verify the Agent's Entra identity when separately supported | Route LLM calls or define AgentPolicy resolution |
| Runtime chart | Render the selected OpenClaw or Hermes runtime and its probes/configuration | Decide policy assignment |

## Native Sandbox shared-model profile

The first executable policy path supports either Hermes or OpenClaw with:

- a Kata Sandbox;
- `ReadWriteOncePod` HOME storage;
- an explicitly requested shared AI Gateway model allowed by the AgentPolicy;
- a monthly USD budget within the policy maximum;
- baseline qualified networking and enforced Sandbox/Pod admission; and
- no selected skills, or skills supplied by the configured validated catalog.

The controller first renders and stages the workload at zero replicas. It
starts the workload only after the protected binding, Sandbox prerequisites,
admission expectations, and current Gateway projection all match. Same-name
Team, Sandbox, or AgentPolicy replacement does not preserve authorization,
because authorization records contain resource UIDs rather than names alone.

The first profile deliberately rejects Copilot sidecars, Agent Identity,
Tailscale, Teams, desktop, arbitrary Helm overrides, Kubernetes grants, and
custom network controls. Rejection is fail-closed and reported by the owning
component; unsupported controls are not silently ignored.

## Gateway credentials and model access

All built-in LLM traffic goes through AI Gateway. Agent Identity is unrelated
to LLM routing.

The Gateway projects only its per-Agent virtual key into the workload
namespace. Shared provider credentials remain in Gateway-owned secrets. Exact
shared models are mapped to collision-safe Gateway access groups from the live
catalog. A policy-bound key never receives a broad legacy shared-model group.

Custom Gateway access is a separate explicit runtime choice. When selected, it
uses the currently configured shared-model groups and does not pretend that an
AgentPolicy granted them.

## Failure and change behavior

- A denied policy revokes or blocks Gateway access and stops known owned
  compute and ingress.
- An unreadable dependency is `Unknown`, not `Pending` or an invented
  approval state; existing access is blocked while evidence is unavailable.
- Gateway grant drift is blocked before repair and unblocked only after
  read-back verifies the desired grant.
- A configuration name reused with a different UID requires a new protected
  authorization record.
- The native profile is single-start. Unsupported live changes remain held;
  delete and recreate the Agent after changing its protected runtime profile.
- Cleanup respects UID/resource-version and ownership checks and does not
  delete foreign workloads or externally owned HOME storage.

## Management API

The Portal exposes administrator endpoints for the resource model:

```text
GET    /api/v1/admin/policies
GET    /api/v1/admin/policies/{name}
POST   /api/v1/admin/policies
PUT    /api/v1/admin/policies/{name}
DELETE /api/v1/admin/policies/{name}

GET    /api/v1/admin/teams
GET    /api/v1/admin/teams/{name}
POST   /api/v1/admin/teams
PUT    /api/v1/admin/teams/{name}
DELETE /api/v1/admin/teams/{name}
```

These APIs manage Kubernetes `AgentPolicy` and `Team` resources. They do not
create an approval queue. Kubernetes RBAC and Portal administrator
authorization protect the management operations.
