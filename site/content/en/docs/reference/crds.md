---
title: "Custom resources"
description: "Agent, Team, Sandbox and AgentPolicy in cetirun.microsoft.com/v1alpha1."
weight: 10
---

All four resources are **cluster-scoped**. Their API group is
`cetirun.microsoft.com/v1alpha1`. Do not add `metadata.namespace`.

| Kind | Resource | CRD name | Short name |
| --- | --- | --- | --- |
| `Agent` | `agents` | `agents.cetirun.microsoft.com` | `cra` |
| `Team` | `teams` | `teams.cetirun.microsoft.com` | `crt` |
| `Sandbox` | `sandboxes` | `sandboxes.cetirun.microsoft.com` | `crs` |
| `AgentPolicy` | `agentpolicies` | `agentpolicies.cetirun.microsoft.com` | `crp` |

An Agent is a long-lived, user-owned environment, not an execution or template.
A Team supplies shared configuration and skills, not shared access to private
data. Pod replacement does not create a new Agent.

## Existing installations

Older installations may contain `OpenClawInstance` and `OpenClawTeam` objects
in the same API group and version. The operator and portal no longer register
those legacy kinds. Installing these CRDs does not convert, adopt, or delete
legacy objects. The policy-bound API is a replacement contract, not a data
migration.

Do not delete legacy CRs or CRDs as an upgrade shortcut. Old finalizers can
uninstall workloads and delete namespaces; deleting a CRD removes its stored
objects. Review data retention and resource ownership before handing an
existing installation over to the new API.

The cluster scope, finalizer, namespace/release names, and PVC behavior are
retained. Harness configuration uses required `spec.agent`; the old
`spec.agentType` field is not supported. There is no automatic resource
conversion or field migration. Helm manages the current CRDs as templates and
upgrades their schemas with the release. Their retention annotation prevents
uninstall from deleting them. Earlier unmanaged CRDs need a separately reviewed
ownership handover; the chart does not force adoption or perform schema/data
migration. Retention does not protect against a schema downgrade.

## Agent

| Field under `spec` | Contract |
| --- | --- |
| `user.alias` | Required owner alias matching `^[a-z][a-z0-9-]{0,62}$` |
| `user.displayName` | Required display name |
| `user.email` | Optional email |
| `gatewayTokenSecretRef` | Required native Secret name for Hermes/OpenClaw; forbidden for Custom |
| `agent` | Required Harness configuration object |
| `agent.type` | `hermes` (default), `openclaw`, or `custom`; immutable |
| `agent.image` | Required for Custom; once set, cannot be cleared. Built-in defaults are pinned from the selected chart |
| `agent.registrySecretRef.name` | Optional Portal-managed source Secret reference; no namespace, key, or inline credentials |
| `agent.username` | Defaults to `vibe`; immutable, non-root image username |
| `agent.port` | HTTP port, 1–65535; required for Custom Web UI, otherwise optional. Omitted for Custom means headless |
| `agent.webUI` | Boolean, default false; Custom-only. When true, `agent.port` serves a Web UI rather than the Chat API; see [Agent Harness](../agent-harness/#custom-web-ui) |
| `team` | Required selected `Team`; an Agent creator may choose any available Team |
| `sandboxRef` | Team-permitted Sandbox selected for Policy enforcement |
| `policyClass` | `normal` (default) or administrator-assigned `admin` |
| `model` | Optional AI Gateway model name, at most 256 characters; it must be granted by the effective policy |
| `modelAccess.sharedModels` | Explicit request for policy-allowed shared models; omission does not grant them |
| `modelAccess.githubCopilot` | Explicit request for the policy-allowed per-user Copilot proxy |
| `modelAccess.monthlyBudgetUSD` | Optional LiteLLM USD monthly budget override, bounded by the policy maximum |
| `skillOverrides.add` / `remove` | Set-like skill lists, at most 20 per list; effective IDs are `(team ∪ add) − remove` |
| `helm.version` | Agent chart pin; backfilled from the operator default on first reconciliation |
| `helm.values` | Map-shaped top-level entries; see admission notes below |
| `suspended` | Request zero replicas and removal of the instance VirtualService; false when omitted |
| `tailscale.authKeySecretRef` | Required non-empty Secret name when `tailscale` is present; instance-namespace Secret must contain non-empty `TAILSCALE_AUTH_KEY` |
| `msteams.enabled` | Opt in to Teams provisioning; default false |
| `msteams.appId` | Optional lowercase UUID client ID; the controller uses it as a fallback after tracked/name-based resolution and app creation |
| `msteams.tenantId` | Optional lowercase UUID tenant ID; otherwise use operator configuration |
| `msteams.webhookPort` | `int32`, schema default 3978; the CRD does not constrain its range, so use a valid non-conflicting listener port |

The namespace and release derive from `metadata.name`. For example, `alice`
uses `cetirun-alice` and `openclaw-alice`. An owner can have a differently named
instance; use the reported status instead of deriving names from the alias.
Use short DNS-label instance names. Portal creation reserves `oauth2`, `api`,
`internal`, `healthz`, `readyz`, `assets`, `portal`, `instances`, `admin`, and
`ui`. Direct CRD admission does not impose that same list; the controller
explicitly rejects `ui`. Avoid all reserved names rather than relying on
schema acceptance to establish safe routing.

See [first instance](../../getting-started/first-instance/) for minimal YAML and
[Teams](../../guides/msteams/) for the optional cloud integration.

The operator resolves Hermes and OpenClaw images and ports through the Harness
interface; their profiles require the `vibe` username. Custom uses a separate
runtime path that supports non-default usernames. Without a binding provider,
it reports `CustomBindingsPending`. Administrators can opt in to the
`customRuntime` Kubernetes provider for approved image digests; Portal Custom
self-service is a separate opt-in.

Private images use `agent.registrySecretRef.name` to reference a Portal-managed
source Secret. Operator validates its Agent UID ownership, registry host and
workload permissions before copying it to the runtime namespace for image
pulling. Credentials are never stored in the Agent spec. See the
[Agent Harness contract](../agent-harness/) for runtime requirements and the
[private registry workflow](../agent-harness/#private-registries).

### Helm-value admission

The controller deep-merges maps and replaces arrays or scalar values. However,
the CRD sets `helm.values.additionalProperties.type: object`.
Consequently, each top-level entry must be map-shaped; an arbitrary standalone
Helm values file is not a valid `spec.helm.values` object. For example, this is
a valid **spec fragment**:

```yaml
helm:
  values:
    persistence:
      storageClass: approved-storage-class
      accessMode: ReadWriteOnce
      size: 100Gi
    secrets:
      provider: openai
      model: your-authorized-model-id
```

The StorageClass and provider credential are deployment prerequisites, not
created by this fragment. In contrast, top-level `replicaCount: 1`,
`msteamsAllowedUsers: "..."`, or `extraEnv: [...]` under `helm.values` do not
match this schema. Deployment-wide `cetirun.helmValues` and direct Helm input
are separate configuration surfaces.

Even accepted values can violate controller assumptions: do not change
resource names, agent type, route ports, or the managed Secret reference using
Helm overrides. Use the corresponding CRD fields where provided. A schema
check does not prove that an override will reconcile successfully.

### Observed state

| Status field | Meaning |
| --- | --- |
| `phase` | `Pending`, `Provisioning`, `Ready`, `Failed`, `Deleting`, or `Suspended` |
| `conditions` | Individual readiness observations with reasons and messages |
| `accessUrl` | Configured native UI URL, `https://<domain>/ui/<instance>/`; not a live reachability test |
| `namespace`, `helmReleaseName` | Actual managed resource locations |
| `observedGeneration` | Generation recorded after Helm succeeds, before pod/ingress readiness is established |
| `observedOperatorHash` | Applied configuration fingerprint used by the reconciliation cache, including the gateway Secret content hash and referenced team's resource version |
| `lastReconcileTime` | Timestamp written by full Ready/suspension handling; can also be written while suspension ingress removal is failing |
| `msteams` | Teams state, message, resolved app and bot resource identifiers |
| `aiGateway` | AI Gateway state: phase and reason, key alias and ID (never the key), access groups, models, native USD budget/spend/reset observations, block state, and applied policy/pricing revisions |
| `signInHelper` | Whether the workload serves the in-agent model sign-in helper that `/<instance>/cetirun-auth/` routes to; omitted until the operator records it |
| `registryCredentials` | Receipt identifying the synchronized source Secret UID/resourceVersion and local pull Secret UID; not credential data or proof of successful image pulling |
| `sandbox` | Last verified binding: `teamUID`, `sandboxUID`, `policyClass`, `agentPolicyUID`, `appliedRevision`; omitted before successful enforcement |
| `policy` | Resolved effective policy plus desired Sandbox, Gateway, and credential revisions |

`AgentConfigReady=False` with reason `AgentRuntimeNotImplemented` reports a
setting that cannot be provisioned, such as a non-default built-in username.
`AgentConfigInvalid` reports rejected profile, image or port settings;
`AgentConfigResolved` reports a prepared
built-in configuration, not runtime readiness. The API does not expose a separate
`status.agent` structure.

`RegistryCredentialsReady` reports credential synchronization. A true condition
does not prove that the registry accepted the credentials or the image was
pulled; inspect Pod readiness and pull events separately. Custom readiness also
depends on its validated platform bindings, not just schema acceptance.

Other condition types include `PolicyResolved`, `PolicyApplied`, `Ready`,
`HelmDeployed`, `IngressReady`, `PodReady`,
`TailscaleReady`, and `TeamsReady`. Check them independently; overall readiness
can be true while ingress creation or Teams provisioning failed.
`TailscaleReady` reports that the key was copied, not that a node joined the
tailnet. `Suspended` skips pod-readiness checks; inspect actual replicas and
pod termination. The chart permits zero or one replica and restricts any
enabled HPA to `minReplicas=maxReplicas=1`.

The Teams state schema accepts `Provisioning`, `PendingAppRegistration`,
`Ready`, `Failed`, and `Deleting`. The controller's missing-OIDC-issuer branch
attempts to write `Pending`, which is not in that enum; its status update can
be rejected. Do not wait specifically for `Pending` to diagnose missing OIDC
configuration. Inspect conditions, events, and a narrowly scoped operator log
without disclosing credentials.

Teams status may contain `appId`, `appObjectId`, `botResourceId`,
`servicePrincipalId`, and `appNotFoundSince`. These are deployment identifiers,
not material to paste into a public bug report.

## Team

| Field under `spec` | Contract |
| --- | --- |
| `normalAgentPolicyRef` | Required normal-class policy |
| `adminAgentPolicyRef` | Optional admin-class policy; never falls back to the normal policy |
| `sandboxRefs` | Required nonempty set of permitted Sandbox names |
| `defaultSandboxRef` | Listed Sandbox selected at Agent creation, not a later implicit reselection |
| `memberContext.role` | Role rendered into runtime-specific team context |
| `memberContext.description` | Context lines, at most 20 |
| `defaults.provider` | String without a CRD enum; chart supports `github_copilot`, `openai`, `anthropic`; instance Helm settings take precedence |
| `skills` | Default identifiers, at most 20 |
| `skillStorage.resourceGroup` | Required when `skillStorage` is present; existing storage resource group |
| `skillStorage.storageAccount` | Required when `skillStorage` is present; existing Blob account |
| `skillStorage.containerName` | Required when `skillStorage` is present; existing skills container |
| `skillStorage.storageClassName` | Optional override; chart default `azureblob-nfs-premium` |
| `skillStorage.storageSize` | Optional shared PVC request; chart default `100Gi` |
| `skillStorage.mountPath` | Optional in-container path; chart default `/home/vibe/.aks/skills` |

Instances select this object through `spec.team`. Selection applies the Team's
runtime defaults and normal policy; it does not grant access to another Agent's
data or runtime. There is no separate Team membership or authorization record.
The optional admin policy remains an administrator-controlled selection. Model
and credential entitlements come from the selected policy slot and are
requested per Agent.

The Team controller reports `Accepted` only after its policy slots and Sandbox
references form a valid current configuration. Runtime context
persistence also differs:
Hermes refreshes `TEAM.md` on initialization, while OpenClaw preserves an
initialized workspace's `USER.md`. See [configure a team](../../guides/teams/).

Skill entries in `skills` and `skillOverrides` follow this schema pattern:

```text
^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$
```

The `npm:` prefix selects the npm installer. Other accepted identifiers are
only passed to the reserved `skills.clawHub` list, not installed. Shared storage
uses chart-level protocol and mount options; those are not fields on
`skillStorage`. See [configuration and skills](../../concepts/configuration/).

## Sandbox and AgentPolicy

`Sandbox` describes the prepared `kata` or `gvisor` runtime, Agent/helper
resource budgets, private home storage and scratch. `AgentPolicy` describes
selection constraints, read-only root filesystem, network and ingress controls,
and optional Kubernetes or Entra identity intent. Configuration references and
schema acceptance do not grant access or prove enforcement.

Policy assignment is mandatory. The controller resolves owner and Team
authorization, the effective AgentPolicy, and the selected Sandbox before any
workload may run. Invalid or unavailable authorization is fail-closed.

The native Sandbox path compiles and stages protected resources at zero
replicas. Production start, observation, and routing adapters are not enabled
yet, so `SandboxReady` remains false and execution remains held. Custom has a
typed Gateway binding but no typed Sandbox execution adapter and is also held.

The controller records a hold finalizer before changing workloads, so a failed
status update cannot release the recorded hold. Explicit suspension remains
`Suspended` unless shutdown fails. Built-in resources without verifiable
Agent-UID ownership are left untouched and require recovery; matching resource
names alone are insufficient. Deleting a held built-in Agent removes only its
verified namespace, without Helm uninstall hooks, and retains finalizers until
cleanup completes. Home storage is retained during a hold, not after explicit
namespace deletion.
Agent Identity authorization and directory cleanup remains a blocking deletion
step for held built-in Agents, including when the runtime namespace is absent.

The Agent creator selects a Team directly. Admission defaults the Team's Sandbox
and normal policy class, then persists UID-bound references so same-name
resource replacement cannot be adopted implicitly. Later Team, Sandbox, admin
policy, or budget changes require an administrator and a suspended Agent.
Operator status reports desired per-domain revisions and only marks Policy
Applied after matching enforcement evidence is current.

AgentPolicy additionally allowlists built-in Harnesses, skills, exact Gateway
model names, Copilot, and Agent Identity. A model entitlement requires a native
LiteLLM default and maximum monthly USD budget. Custom is not an allowed policy
Harness. Agent Identity cannot complete aggregate `PolicyApplied` until a typed
credential-service executor supplies matching read-back evidence.

Production Sandbox start and observation still require implementation and
qualification. See the
[operator foundation notes]({{< repo "app/cetirun-operator/README.md" >}})
for the execution-hold, authority, and validation boundaries.

## Schema and implementation

API validation uses these schemas:

- [Agent CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_agents.yaml" >}})
- [Team CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_teams.yaml" >}})
- [Sandbox CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_sandboxes.yaml" >}})
- [AgentPolicy CRD]({{< repo "app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_agentpolicies.yaml" >}})
- [Go API types]({{< repo "app/cetirun-operator/api/v1alpha1" "tree" >}})
- [Reconciler and status writes]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
- [Helm value and route builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
- [Teams state handling]({{< repo "app/cetirun-operator/internal/controller/teams.go" >}})
- [Portal name validation]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}})

Use `agents.cetirun.microsoft.com` and `teams.cetirun.microsoft.com` in scripts
to avoid ambiguity with resources from other API groups.
