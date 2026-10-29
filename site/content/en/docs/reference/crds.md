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
| `user.team` | Deprecated legacy display context only; not a Team or policy assignment |
| `gatewayTokenSecretRef` | Required native Secret name for Hermes/OpenClaw; forbidden for Custom |
| `agent` | Required Harness configuration object |
| `agent.type` | `hermes` (default), `openclaw`, or `custom`; immutable |
| `agent.image` | Required for Custom; once set, cannot be cleared. Built-in defaults are pinned from the selected chart |
| `agent.registrySecretRef.name` | Optional Portal-managed source Secret reference; no namespace, key, or inline credentials |
| `agent.username` | Defaults to `vibe`; immutable, non-root image username |
| `agent.port` | HTTP port, 1–65535; required for Custom Web UI, otherwise optional. Omitted for Custom means headless |
| `agent.webUI` | Boolean, default false; Custom-only. When true, `agent.port` serves a Web UI rather than the Chat API; see [Agent Harness](../agent-harness/#custom-web-ui) |
| `team` | Optional for legacy Agents; required for policy binding. Portal Gateway creation still requires an existing Team |
| `sandboxRef` | Opt into a Team-permitted Sandbox; omitted for legacy execution |
| `model` | Optional model name, at most 256 characters; policy-bound Agents must select a policy-allowed shared model or, when the policy allows Copilot, a Copilot model by its Gateway name or as `copilot/<model ID>` |
| `modelAccess.sharedModels` | Explicit request for policy-bound Agents; defaults on for legacy Agents |
| `modelAccess.githubCopilot` | Explicit request for policy-bound Agents; defaults on for legacy Agents when the deployment offers Copilot |
| `modelAccess.monthlyBudgetUSD` | Optional LiteLLM USD monthly budget override, bounded by the policy maximum |
| `skillOverrides.add` / `remove` | Set-like skill lists, at most 20 per list; effective IDs are `(team ∪ add) − remove` |
| `helm.version` | Agent chart pin; backfilled from the operator default on first reconciliation |
| `helm.values` | Map-shaped top-level entries; see admission notes below |
| `suspended` | Request zero replicas and removal of the instance VirtualService; false when omitted |
| `restartRequestID` | Optional retained-restart ID: 16-128 letters, digits, `_` or `-`. Changing it requests a new Pod through normal reconciliation; reusing it does not. Cannot be removed once set or changed while suspended |
| `plannedMigration` | Opt-in administrator request with `requestID` and destination `target.name`/`target.uid`; common-executor OpenClaw/Hermes and one CSI/RWOP HOME only. An accepted request is immutable until completed; the same ID cannot change its destination |
| `tailscale.authKeySecretRef` | Required non-empty Secret name when `tailscale` is present; instance-namespace Secret must contain non-empty `TAILSCALE_AUTH_KEY` |
| `msteams.enabled` | Opt in to Teams provisioning; default false |
| `msteams.appId` | Optional lowercase UUID client ID; the controller uses it as a fallback after tracked/name-based resolution and app creation |
| `msteams.tenantId` | Optional lowercase UUID tenant ID; otherwise use operator configuration |
| `msteams.webhookPort` | `int32`, schema default 3978; the CRD does not constrain its range, so use a valid non-conflicting listener port |

The namespace and release derive from `metadata.name`. For example, `alice`
uses `cetirun-alice` and `openclaw-alice`. An owner can have a differently named
instance; use the reported status instead of deriving names from the alias.
Use short DNS-label instance names. Portal creation reserves `oauth2`, `api`,
`internal`, `healthz`, `readyz`, `assets`, `portal`, `instances`, `admin`,
`connections`, and `ui`. Direct CRD admission does not impose that same list; the controller
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
```

The StorageClass is a deployment prerequisite, not created by this fragment.
Select a Gateway model with `spec.model`; provider overrides are rejected. In contrast, top-level `replicaCount: 1`,
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
| `observedRestartRequestID` | Restart request observed on the current ready Pod; an accepted request or applied template alone does not set it |
| `plannedMigration` | Controller-owned source/storage identity and acknowledgement checkpoint; phases are `Protecting`, `Stopping`, `SourceTerminated`, `Detached`, `Starting`, `Completed`. Not a physical-fencing receipt or permission to edit status |
| `observedOperatorHash` | Applied configuration fingerprint used by the reconciliation cache, including the gateway Secret content hash and referenced team's resource version |
| `lastReconcileTime` | Timestamp written by full Ready/suspension handling; can also be written while suspension ingress removal is failing |
| `msteams` | Teams state, message, resolved app and bot resource identifiers |
| `aiGateway` | AI Gateway state: phase and reason, key alias and ID (never the key), access groups, models, native USD budget/spend/reset observations, block state, and applied policy/pricing revisions; for GitHub Copilot, each registered model's Gateway name (`copilot.routes`) and the models left out for lack of a price (`copilot.unpriced`) or because a shared model has their name (`copilot.skipped`); `modelRouters` lists the Team's smart-routing models and, for an instance with an Agent Policy, the routers configured in the Gateway that the Policy lists, each with `granted`, the `model` name it sends when granted (a Team router's internal name, or a Gateway router's own name), a Team router's configured limits, or the `reason` it is not granted |
| `signInHelper` | Compatibility field; the Operator records `false` because model sign-in uses the Portal and AI Gateway |
| `registryCredentials` | Receipt identifying the synchronized source Secret UID/resourceVersion and local pull Secret UID; not credential data or proof of successful image pulling |
| `sandbox` | Last verified binding: `teamUID`, `sandboxUID`, `agentPolicyUID`, `appliedRevision`; omitted before successful enforcement |
| `policy` | Resolved effective policy plus desired Sandbox, Gateway, and credential revisions |

`AgentConfigReady=False` with reason `AgentRuntimeNotImplemented` reports a
setting that cannot be provisioned, such as a non-default built-in username.
`AgentConfigInvalid` reports rejected profile, image or port settings, and a
built-in Agent chart that could not be downloaded from the registry for a new
Agent or after a spec change; the operator log records the registry's error
for the latter. Every full reconcile downloads the chart again, including the
periodic passes of a running Agent. When the chart of an Agent that is already
installed for its current generation cannot be downloaded, reason
`AgentChartUnavailable` reports the temporary failure: the deployed release
stays unchanged, and the operator retries every 30 seconds. Workload health is
still observed: lost ready replicas or an unreadable StatefulSet make a running
Agent `Provisioning` with `PodReady=False` and `Ready=False`; readiness recovers
when the workload does, even while the registry remains unavailable, provided
`BuiltInPrerequisitesReady=True` records a completed prerequisite pass. A later
prerequisite failure clears that condition independently of `PodReady`; a
healthy Pod alone cannot clear that failure. An older status without this
condition needs a successful full reconcile before readiness can recover.
Suspension and failures or waits for other prerequisites are preserved. The
deployed access URL is retained until the corresponding route is reconciled.
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
| `agentPolicyRef` | The Team's single policy; required when this Team opts into policy |
| `sandboxRefs` | Nonempty permitted Sandbox set; required for a policy-enabled Team |
| `defaultSandboxRef` | Listed creation default; required for a policy-enabled Team, not a later implicit reselection |
| `memberContext.role` | Role rendered into runtime-specific team context |
| `memberContext.description` | Context lines, at most 20 |
| `defaults.provider` | Removed; non-empty values are rejected. Configure upstream providers in the AI Gateway |
| `skills` | Default identifiers, at most 20 |
| `skillStorage.resourceGroup` | Required when `skillStorage` is present; existing storage resource group |
| `skillStorage.storageAccount` | Required when `skillStorage` is present; existing Blob account |
| `skillStorage.containerName` | Required when `skillStorage` is present; existing skills container |
| `skillStorage.storageClassName` | Optional override; chart default `azureblob-nfs-premium` |
| `skillStorage.storageSize` | Optional shared PVC request; chart default `100Gi` |
| `skillStorage.mountPath` | Optional read-only alias for native Agents; common view at `$HOME/.agents/skills` |
| `modelRouters` | Smart-routing models, at most 10, keyed by `name`; see [smart routing](../../guides/teams/#smart-routing) |
| `modelRouters[].name` | Model name instances request: 1 to 64 characters without whitespace or `/`, not starting with `cetirun-` in any letter case |
| `modelRouters[].classifier.type` | `heuristic` (default), `llm` or `jev`; LiteLLM's licence-limited classifier types are not accepted |
| `modelRouters[].classifier.model` | Shared model that classifies requests, not a router of the Gateway: a chat model for `llm`, a decision model (`model_info.mode: evaluation`) for `jev`; required for `llm` and `jev` and not allowed for `heuristic` |
| `modelRouters[].tiers.simple`, `.medium`, `.complex` | Required model of each tier: the name of a shared model that answers conversations and is not a router of the Gateway, or `copilot/<model>` for a GitHub Copilot model; `medium` also takes requests that cannot be classified |
| `modelRouters[].tiers.reasoning` | Optional; empty uses the `complex` model |

| Field under `status` | Contract |
| --- | --- |
| `modelRouters[].name` | Router name from `spec.modelRouters` |
| `modelRouters[].gatewayModel` | Internal Gateway model name instances send requests to; stable while the router's configuration changes |
| `modelRouters[].ready` / `message` | Whether the router is registered with its current configuration, and why not |
| `conditions[type=ModelRoutersReady]` | `True` when every router is registered; `False` names each router that is not; `Unknown` while the Gateway cannot be reached |

Each Team has a single `agentPolicyRef`; Agents do not select a policy class.
The pre-release `normalAgentPolicyRef`, `adminAgentPolicyRef` and Agent
`policyClass` fields are removed from the API and its CRD schemas. This is a new
system contract, not a backward-compatible upgrade of the dual-policy design.
No compatibility fields or migration path are provided for that design.

Instances select this object through `spec.team`. Selection applies the Team's
runtime defaults and, when configured, its single policy; it does not grant access to another Agent's
data or runtime. There is no separate Team membership or authorization record.
Model and credential entitlements come from that policy and are requested per
Agent.

A Team used for administrative work is an independent Team with its own context
and skills; it does not select a more privileged policy for another Team.
Permission to manage Teams or AgentPolicies is a human control-plane permission
enforced separately from workload authorization. Workload authorization comes
from the selected Team, its `agentPolicyRef`, and the Agent's explicit requests.

Teams with no policy or Sandbox fields retain legacy behavior. Setting any one
of those fields opts into the full policy binding contract; partial bindings
cannot fall back to legacy. The Team controller accepts a legacy Team without
those references, or a policy-enabled Team after its policy and Sandbox
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

### Team and Sandbox administration

Portal administrators can create and edit Teams with `agentPolicyRef`,
`sandboxRefs`, and `defaultSandboxRef`. The Portal requires complete bindings,
validates referenced resources, and checks that the default belongs to the
allowed Sandbox set and Team Skills are permitted by the Policy. The Team DTO
returns these fields along with `uid` and `resourceVersion`; updates and deletes
must send those two version fields. A stale version returns `409 Conflict`,
and missing version fields return `428 Precondition Required`.

The **Admin → Sandboxes** pages manage the complete Sandbox configuration:
runtime type, Agent and helper resource requests/limits, HOME storage, and
scratch size. They use these admin-only endpoints:

- `GET` and `POST /api/v1/admin/sandboxes`
- `GET`, `PUT`, and `DELETE /api/v1/admin/sandboxes/{name}`

The Sandbox request contains `name`, optional `type`, `resources`, and `storage`
with the same structure as the CRD spec. Omitting `type` or setting it to `null`
defaults to `kata` on both create and full-spec update, matching Kubernetes
defaulting. An explicit invalid value, including an empty string, is rejected
rather than replaced by the default. Resource and storage fields remain required.
Updates and deletes also require the `uid`
and `resourceVersion` returned by a read. The response reports `ready` only
from a condition for the current generation. Saving a Sandbox does not
install a runtime or establish runtime readiness.

Team and Sandbox changes use the existing configuration coordination client
when configured, including conditional writes, retained uncertain outcomes,
and background deletion. Admission remains responsible for platform
compatibility and current consumer checks. Kubernetes validation/admission
rejections are shown in the editor, while transport failures stay in logs.

## Sandbox and AgentPolicy

`Sandbox` describes the prepared `kata`, `gvisor` or `container` runtime, Agent/helper
resource budgets, private home storage and scratch. `AgentPolicy` describes
selection constraints, read-only root filesystem, network and ingress controls,
and optional Kubernetes or Entra identity intent. Configuration references and
schema acceptance do not grant access or prove enforcement.

`container` is explicit ordinary-container execution (such as runc), not VM
isolation: the Agent shares the node kernel. It requires a trusted registration
with an explicitly pinned RuntimeClass UID/handler and the enabled common
executor. Kata remains the omitted/null default; gVisor's execution scope does
not change. Container uses the same AgentPolicy, Gateway, Cilium and retained
HOME lifecycle, with per-container resource limits and no implicit VM overhead.
Saving a configuration does not discover or install a handler. Container
supports the same separately approved [managed ingress mTLS](../../guides/agent-ingress-mtls/)
as Kata; a capability flag alone does not authorize the endpoint.
FQDN blocking, Entra ID and Kubernetes grants remain unsupported on this native
backend. Select a new Sandbox/Agent instead of attempting online runtime conversion.

Policy assignment is opt-in, not a prerequisite for legacy deployment.
An Agent with no Sandbox selection or recorded governed lifetime can use no
Team or a Team without policy settings. Such Agents retain the existing runtime flow and do not claim policy
enforcement. For opted-in Agents, the controller resolves owner and Team
authorization, the effective AgentPolicy, and the selected Sandbox before a
workload may run. Invalid or unavailable authorization is fail-closed.

The native Sandbox path compiles and stages protected resources at zero
replicas. Execution remains held unless the separately configured, default-off
native execution profile is enabled and its supported prerequisites pass.
Custom has a typed Gateway binding but no typed Sandbox execution adapter;
policy-bound Custom Agents remain held, while ungoverned Custom Agents retain
their existing runtime provider path.

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
Compatibility does not automatically release an old Agent that was already
held by a mandatory-policy version. Review its ownership and retained data
before recovery; removing spec fields is not permission to clear its recorded
policy lifetime.

The Agent creator selects a Team directly. Admission defaults the Team's
Sandbox, then persists UID-bound references so same-name resource replacement
cannot be adopted implicitly. Later Team, Sandbox, policy, or budget changes
require an administrator and a suspended Agent.
Existing policy-enabled resources enter a controlled hold for cleanup. Changing
or clearing references does not return them to legacy execution; changes require
explicit reauthorization and an ordered stop-and-cleanup sequence. A lossless
rolling migration between policies is not supported.
Operator status reports desired per-domain revisions and only marks Policy
Applied after matching enforcement evidence is current.

AgentPolicy additionally allowlists Harnesses, skills, exact Gateway
model names, Copilot, and Agent Identity. A model entitlement requires a native
LiteLLM default and maximum monthly USD budget. The allowed Harnesses are
OpenClaw, Hermes and Custom. Custom additionally requires the common executor
and an approved Gateway-qualified image; policy permission alone does not
qualify an image or backend. Agent Identity cannot complete aggregate `PolicyApplied` until a typed
credential-service executor supplies matching read-back evidence.

### AgentPolicy rate limits

`modelAccess.rateLimits` sets AI Gateway limits on the virtual key of every
policy-bound Agent whose Team uses the policy. Each Agent's key keeps its own
counters, so Agents do not use up each other's limits. Every field is optional
and an omitted field is unlimited. There is no zero value: the Gateway rejects
every request of a key whose limit is zero, so the schema rejects values below
the minimums.

| Field | Minimum | Gateway key setting |
| --- | --- | --- |
| `maxParallelRequests` | 1 | `max_parallel_requests`, requests in flight at once |
| `rpm` | 1 | `rpm_limit`, requests per minute across all models |
| `tpm` | 4096 | `tpm_limit`, tokens per minute across all models |
| `perModel.<model>.rpm` | 1 | `model_rpm_limit` in the key metadata |
| `perModel.<model>.tpm` | 4096 | `model_tpm_limit` in the key metadata |

`perModel` limits at most 64 models. Its keys are the exact Gateway model names
the Agent requests (1 to 256 characters without whitespace), and each entry sets
`rpm`, `tpm`, or both. `maxParallelRequests` is at most 2,147,483,647, the largest
value the Gateway stores; the other limits have no upper bound. The values are
not checked against a model's context window.

The operator sets the limits when it creates a key, compares the key's stored
limits with the policy on every reconcile, and updates the same key in place
when they differ. Removing a field clears it on the key. Other key settings and
metadata are kept. The limits are not part of the Gateway policy revision, so
changing them does not start the reprojection fence that a model or budget
change starts, and the operator keeps each Agent's key. Sandbox execution,
including coordinated configuration reservations, also excludes
`modelAccess.rateLimits` from its execution inputs. Adding, editing, or removing
only these limits, including `perModel`, does not stop or restart running
native Agents. For common-executor Sandboxes this includes workloads still
initializing or completing a retained restart: a limits-only update does not
reapply the workload, temporarily block its key, or release its unchanged
native reservation. A policy status update during bootstrap is compared against
the captured execution inputs and immutable UID, not its resource version alone.
While policy status catches up to the new generation, the
operator reassesses the current policy against the same trusted platform
inventory and grants used by the policy controller instead of stopping an Agent
solely for that observation delay. Unavailable or invalid policy evidence still
holds execution. Budgets, model authorization, and other execution controls
retain their normal fencing and coordination checks; the exact coordinated
write still includes the limits. Keys of legacy Agents without a policy are
not changed; Gateway administrators manage their limits.

Persisted execution evidence is not rewritten by this change. Older single-start
lifetimes or coordination reservations whose saved policy digest includes
`modelAccess.rateLimits`, even an empty object, remain fail-closed on an input
mismatch and require the existing stop/recreate procedure. Upgrade the operator and admission readers
before editing limits; do not manually alter their saved digests.

The total limits count every request on the key, including the Agent's GitHub
Copilot models. A per-model limit applies to the model name a request uses after
model aliases; for an `auto` router that is the router's name, not the model it
routes to. A request over a limit receives HTTP 429 with a `retry-after` header.
Gateway replicas share the counters through Redis.

The Gateway counts `tpm` from the tokens of each finished request, cached input
tokens excluded, and refuses a key's requests once the minute's count has
reached the limit, until the next minute; a single request can take the count
past the limit. That is the Gateway chart's default, `tpmReservation: false`.
With `tpmReservation: true`, LiteLLM instead reserves a request's input plus
its maximum output tokens before calling the model and refuses the request when
that does not fit. OpenClaw asks for the model's maximum output, 128,000 tokens
for current GPT models, with every request, so in that mode a `tpm` of 200,000
refused every request of an OpenClaw conversation past about 55,000 tokens of
input, the reservation alone being larger than the limit, and OpenClaw gave up
after nine one-minute retries (measured on a test cluster). The minimum of
4,096 tokens per minute keeps a limit from refusing all but the smallest
requests.

### AgentPolicy administration

The admin Portal displays the complete cluster-scoped `AgentPolicy` configuration.
Administrators can edit allowed Harnesses and skills, Copilot and shared model
access, monthly USD budgets, per-Agent rate limits, multiple-Sandbox
selection, read-only root filesystem, network CIDR and port rules, and ingress
mode.

Kubernetes API access appears under Read-only policy capabilities in the Portal.
The summary shows any requested ServiceAccount and namespace or cluster-wide permissions already recorded in the
Policy. Platform administrators manage this configuration outside the Portal;
authorization requires separately approved platform grants. Saving other Policy
fields preserves the existing Kubernetes access configuration.

The Internal network access editor groups each destination CIDR and its complete
port list into one rule card, matching one `allowedInternalCIDRs` entry. Edit rule
opens the entire rule; Update rule replaces its destination and ports together.
Cancel discards the draft. Delete rule removes the entire entry. In the editor,
ports are comma-separated, such as `443, 5432, 53/UDP`, and use TCP unless `/UDP`
or `/SCTP` is specified. Rule changes are submitted when the Policy is saved.

Agent Identity, Entra ID, blocked FQDNs, and ingress mTLS are read-only in this
editor. Existing values remain visible and are preserved when other fields are
saved. FQDN blocking lacks supported enforcement; ingress mTLS requires the
separate [administrator-registered managed endpoint](../../guides/agent-ingress-mtls/),
not a Portal toggle. Broker-backed Agent Identity requires current credential-policy
readback for aggregate `PolicyApplied`; common-executor workloads also require the
registered native Connections integration. Other controls depend on the selected
execution profile and platform capabilities.
New common-executor Kata lifetimes support internal CIDR grants and SCTP port
rules only with the corresponding explicit platform registration. SCTP internal
grants are limited to direct endpoints, excluding registered Service and
Kubernetes API destinations. Retained version-1 native single-start lifetimes
still reject internal grants and SCTP requests. See the
[native SCTP usage and qualification boundary]({{< repo "examples/native-sandbox/README.md" >}}#governed-sctp-egress);
an accepted API value does not qualify the actual network path.

The Operator retains the Policy's `Accepted` condition for controller decisions
and diagnostics; the Portal list and editor do not display this status.
`Accepted=True` describes configuration acceptance only: Sandbox compatibility,
Agent authorization, and runtime enforcement must still succeed. Saving a Policy
does not prove that its consumers have applied it. Editing an already-bound
Policy can affect those consumers.

#### HTTP API

These admin-only endpoints manage `AgentPolicy` resources:

- `GET` and `POST /api/v1/admin/policies`
- `GET`, `PUT`, and `DELETE /api/v1/admin/policies/{name}`

Update and delete requests include the UID and resource version returned by a
previous read. Stale requests fail with `409 Conflict`. Responses include all
Policy configuration groups, `generation`, `observedGeneration`, `conditions`,
and `deleting`. These status fields are read-only.

For newly exposed fields, omission preserves the stored value, including omitted
nested network fields. Explicit empty arrays clear editable lists.
Kubernetes access, Identity, Entra ID, blocked FQDNs, and mTLS cannot be changed
through this API. Attempts to change or clear Kubernetes access are rejected;
omitting it preserves the existing configuration. Existing clients that
send only Harnesses, skills, and model access continue to preserve all other
configuration. Updates retain metadata, status, and the existing coordinated
mutation and optimistic concurrency protections.

When strict Sandbox admission uses a UID-pinned configuration authority, set
the Portal chart's `configurationCoordination.namespace`, `name`, and `uid` to
the same authority configured by the Operator. The platform chart rejects
mismatched values. The Portal then reserves the exact whole-resource mutation,
records `Changing` before submitting one conditional Update or background
Delete, observes the result, and releases only a definitively completed
operation. Its namespaced Role permits access only to the named authority
ConfigMap. UID/resource-version checks remain request concurrency protection;
they do not replace this admission contract.

A competing reservation returns `409 Conflict`. An unavailable authority or
uncertain API write returns `503 Service Unavailable` and deliberately retains
the reservation; do not blindly repeat the mutation. Recover the recorded
operation with the Operator's configuration-change procedure. Update recovery
requires the exact original inputs. New deletion reservations durably record
the content digest needed for recovery: an administrator can use the operation
ID from the pinned authority journal with
`--action observe-delete --operation-id ID`, even after the policy and the
Portal request are gone.
Observation retains the reservation; explicit `--action delete --operation-id ID`
releases it only after verifying that the original UID is absent or terminating,
without resubmitting a Delete. Older deletion reservations still require the
original `--before` input. Upgrade all coordination readers before issuing
reservations with the new recovery metadata. Creation is not covered because
configuration coordination protects updates and deletions, not new resources.

The API validates model access and budgets as one unit: Copilot or shared model
access requires a positive default and maximum monthly USD budget, and the
default cannot exceed the maximum. It applies the schema's rate-limit minimums
before writing. `modelAccess.rateLimits` in an update replaces the stored limits;
an update without it removes them, so a client saves the limits it read. The
Portal editor treats an empty input as no limit and never sends zero.

#### Agent creation options

The creation form selects a Team before offering Harness and model choices.
`GET /api/v1/instance/create-options?team=<name>` requires a signed-in user and
returns the selected Team, Policy name when bound, allowed Agent types, effective
Gateway catalog, Agent Identity availability/requirement, the Policy's per-Agent
rate limits, and any configuration blocker. Responses use `Cache-Control: no-store`.
This endpoint does not expose other administrative Policy fields or grant runtime
authorization. The form and the Agent details page show the rate limits
read-only; the details page reads them from the Agent's Team Policy.

Harness choices intersect deployment settings with `harnesses.allowed`. Shared
models intersect the Gateway catalog with `modelAccess.sharedModels.allowed`;
Copilot requires both deployment availability and Policy permission. Required
Agent Identity is selected automatically and cannot be turned off. It also
requires the deployment feature, verified owner, Policy credential-service
entitlement, and Entra capability. Missing prerequisites block creation.

Both Agent creation endpoints re-read the same constraints before writing an
Agent or credentials, including a private-registry creation retry. Explicitly
forbidden choices are rejected; omitted model-source switches use the currently
available sources and are persisted as explicit booleans. The default model must
belong to an enabled, authorized source. Team default Skills must satisfy the
Policy; the runtime continues to apply its default Gateway budget. No Skills or
budget editor is added to the Agent form.

Incomplete Team bindings, missing or deleting Policies, unreadable dependencies,
and no available Harness/model source cannot fall back to unrestricted creation.
A Team without Policy/Sandbox settings retains optional policy assignment.
Policy-controlled native Agents require the AI Gateway. Configuration admission
and runtime enforcement remain responsible for final authorization and execution;
available form choices do not establish Sandbox readiness.

#### Activation boundary

The Team editor manages `agentPolicyRef`, `sandboxRefs`, and `defaultSandboxRef`.
Multiple permitted Sandboxes require the selected Policy's
`sandboxSelection.allowMultiple`. When switching to a restrictive Policy,
existing selections remain visible and must be corrected before saving; the
Portal does not silently remove bindings. Backend validation re-reads the Policy
before accepting the Team request, and admission remains authoritative.

Available configuration controls do not establish runtime readiness. See the
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
