---
title: "Custom resources"
description: "This page describes the Agent, Team, Sandbox, and AgentPolicy resources in cetirun.microsoft.com/v1alpha1."
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

An Agent is a long-lived environment that a user owns. It is not an execution
or a template. A Team supplies shared configuration and skills. It does not
supply shared access to private data. Pod replacement does not create a new
Agent.

## Existing installations

Older installations can contain `OpenClawInstance` and `OpenClawTeam` objects
in the same API group and version. The operator and portal no longer register
those legacy kinds. The installation of these CRDs
does not convert, adopt, or delete legacy objects. The policy-bound API is a
replacement contract. It is not a data migration.

Do not delete legacy CRs or CRDs as an upgrade shortcut. Old finalizers can
uninstall workloads and delete namespaces. If you delete a CRD, the deletion
removes its stored objects. Before you transfer an installation that already
exists to the new API, examine data retention and resource ownership.

The cluster scope, finalizer, namespace and release names, and PVC behavior
do not change. Harness configuration uses `spec.agent`, which is a necessary
field. The old `spec.agentType` field is not supported. There is no automatic
resource conversion or field migration.

Helm manages the current CRDs as templates and upgrades their schemas with the
release. Their retention annotation prevents their deletion during an
uninstall. For earlier unmanaged CRDs, a separately reviewed ownership handover
is necessary. The chart does not force adoption, and it does not do a schema or
data migration. Retention does not protect against a schema downgrade.

## Agent

| Field under `spec` | Contract |
| --- | --- |
| `user.alias` | Necessary owner alias that matches `^[a-z][a-z0-9-]{0,62}$` |
| `user.displayName` | Necessary display name |
| `user.email` | Optional email |
| `user.team` | Deprecated. Only for legacy display context. It is not a Team or policy assignment |
| `gatewayTokenSecretRef` | Necessary native Secret name for Hermes and OpenClaw. Not allowed for Custom |
| `agent` | Necessary Harness configuration object |
| `agent.type` | `hermes` (default), `openclaw`, or `custom`. It cannot be changed |
| `agent.image` | Necessary for Custom. After it is set, it cannot be cleared. Built-in defaults are pinned from the selected chart |
| `agent.registrySecretRef.name` | Optional reference to a Portal-managed source Secret. The reference has no namespace, key, or inline credentials |
| `agent.username` | The default is `vibe`. Non-root image username that cannot be changed |
| `agent.port` | HTTP port, 1–65535. Necessary for a Custom Web UI, and optional in other cases. For Custom, an omitted port means that the Agent is headless |
| `agent.webUI` | Boolean, default false. For Custom only. When true, `agent.port` serves a Web UI instead of the Chat API. See [Agent Harness](../agent-harness/#custom-web-ui) |
| `team` | Optional for legacy Agents. Necessary for policy binding. The Portal also creates Agents without a Team |
| `sandboxRef` | Opts in to a Sandbox that the Team allows. Omitted for legacy execution |
| `model` | Optional model name, at most 256 characters. A policy-bound Agent must select a shared model that the policy allows, or a Copilot model if the policy allows Copilot. A Copilot model is selected by its Gateway name or as `copilot/<model ID>` |
| `modelAccess.sharedModels` | Explicit request for policy-bound Agents. On by default for legacy Agents |
| `modelAccess.githubCopilot` | Explicit request for policy-bound Agents. On by default for legacy Agents when the deployment offers Copilot |
| `modelAccess.monthlyBudgetUSD` | Optional override of the monthly LiteLLM budget in USD, up to the policy maximum |
| `skillOverrides.add` / `remove` | Set-like skill lists, at most 20 for each list. The effective IDs are `(team ∪ add) − remove`. With the Skill Marketplace, the operator refuses an entry without the `npm:` prefix with `AgentConfigInvalid` |
| `helm.version` | Agent chart pin. It is backfilled from the operator default on the first reconciliation |
| `helm.values` | Map-shaped top-level entries. See the admission notes below |
| `suspended` | Requests zero replicas and the removal of the instance VirtualService. If omitted, the value is false |
| `restartRequestID` | Optional retained-restart ID: 16-128 letters, digits, `_` or `-`. A change to the ID requests a new Pod through normal reconciliation. A reused ID does not request a new Pod. After it is set, it cannot be removed. It cannot be changed while the Agent is suspended |
| `plannedMigration` | Opt-in administrator request with `requestID` and the destination `target.name`/`target.uid`. Only for common-executor OpenClaw or Hermes with one CSI/RWOP HOME. An accepted request cannot be changed until it is completed. A request with the same ID cannot change its destination |
| `tailscale.authKeySecretRef` | Necessary non-empty Secret name when `tailscale` is present. The Secret in the instance namespace must contain a non-empty `TAILSCALE_AUTH_KEY` |

The namespace and the release get their names from `metadata.name`. For
example, `alice` uses `cetirun-alice` and `openclaw-alice`. An owner can have an
instance with a different name. Use the reported status instead of names that
you derive from the alias.

Use short DNS-label instance names. Portal creation reserves `oauth2`, `api`,
`internal`, `healthz`, `readyz`, `assets`, `portal`, `instances`, `admin`,
`connections`, and `ui`. Direct CRD admission does not enforce that same list.
The controller explicitly rejects `ui`. Avoid all reserved names. Do not rely
on schema acceptance to make routing safe.

For minimal YAML, see [first instance](../../getting-started/first-instance/).

The operator resolves Hermes and OpenClaw images and ports through the Harness
interface. The `vibe` username is necessary for their profiles. Custom uses a
separate runtime path that supports non-default usernames. Without a binding
provider, it reports `CustomBindingsPending`. Administrators can opt in to the
`customRuntime` Kubernetes provider for approved image digests. Portal Custom
self-service is a separate opt-in.

Private images use `agent.registrySecretRef.name` to refer to a Portal-managed
source Secret. Before the operator copies the Secret to the runtime namespace
for image pulls, it validates the Secret's Agent UID ownership, registry host,
and workload permissions. Credentials are never stored in the Agent spec. See
the [Agent Harness contract](../agent-harness/) for the runtime rules and the
[private registry workflow](../agent-harness/#private-registries).

### Helm-value admission

The controller deep-merges maps and replaces arrays or scalar values. But the
CRD sets `helm.values.additionalProperties.type: object`. As a result, each
top-level entry must be map-shaped. An arbitrary standalone Helm values file is
not a valid `spec.helm.values` object. For example, this is
a valid **spec fragment**:

```yaml
helm:
  values:
    persistence:
      storageClass: approved-storage-class
      accessMode: ReadWriteOnce
      size: 100Gi
```

The StorageClass is a deployment prerequisite. This fragment does not create
it. Select a Gateway model with `spec.model`. Provider overrides are rejected.
In contrast, top-level `replicaCount: 1` or `extraEnv: [...]` under
`helm.values` do not match this schema. Deployment-wide `cetirun.helmValues`
and direct Helm input are separate configuration surfaces.

Even accepted values can break what the controller expects. Do not use Helm
overrides to change resource names, the agent type, route ports, or the managed
Secret reference. Use the applicable CRD fields where they exist. A schema
check does not prove that an override will reconcile successfully.

### Observed state

| Status field | Meaning |
| --- | --- |
| `phase` | `Pending`, `Provisioning`, `Ready`, `Failed`, `Deleting`, or `Suspended` |
| `conditions` | Individual readiness observations with reasons and messages |
| `accessUrl` | Configured native UI URL, `https://<domain>/ui/<instance>/`. It is not a live reachability test |
| `namespace`, `helmReleaseName` | Actual locations of the managed resources |
| `observedGeneration` | Generation recorded after Helm succeeds, before the pod and ingress are ready |
| `observedRestartRequestID` | Restart request observed on the current ready Pod. An accepted request alone or an applied template alone does not set it |
| `plannedMigration` | Controller-owned source and storage identity and acknowledgment checkpoint. The phases are `Protecting`, `Stopping`, `SourceTerminated`, `Detached`, `Starting`, and `Completed`. It is not a physical-fencing receipt, and it does not give permission to edit status |
| `observedOperatorHash` | Fingerprint of the applied configuration, for use by the reconciliation cache. It includes the content hash of the gateway Secret and the resource version of the referenced team |
| `lastReconcileTime` | Timestamp that a full Ready or suspension pass writes. It can also be written when the ingress removal for a suspension fails |
| `aiGateway` | AI Gateway state. It shows the phase and reason, the key alias and ID (never the key), access groups, and models. It also shows native USD budget, spend, and reset observations, the block state, and the applied policy and pricing revisions. For GitHub Copilot, it shows the Gateway name of each registered model (`copilot.routes`), and `vision` when the model accepts images. It also shows the models that are excluded because they have no price (`copilot.unpriced`) or because a shared model has their name (`copilot.skipped`). With an Agent Policy, it also shows the models that are the same models as shared models that the Policy does not allow (`copilot.notAllowed`). `modelRouters` lists the smart-routing models of the Team. For an instance with an Agent Policy, it also lists the routers that are configured in the Gateway and that the Policy lists. Each entry has `granted`. If the router is granted, the entry has its `model` name in the Gateway. For a Team router, this is its internal name. For a Gateway router, this is its own name. The entry of a granted Team router also has `alias`, the router's name, when the key reaches the router under that name. A Hermes instance sends that name. Other instances send the internal name. Each granted entry also has its configured limits, and `vision` when the instance sends images to the router. If the router is not granted, the `reason` tells why |
| `registryCredentials` | Receipt that identifies the UID and resourceVersion of the synchronized source Secret and the UID of the local pull Secret. It is not credential data or proof of a successful image pull |
| `sandbox` | Last binding that was found correct: `teamUID`, `sandboxUID`, `agentPolicyUID`, and `appliedRevision`. Omitted before enforcement succeeds |
| `policy` | Resolved effective policy, and the desired Sandbox, Gateway, and credential revisions |

`AgentConfigReady=False` with reason `AgentRuntimeNotImplemented` reports a
setting that cannot be provisioned, such as a non-default built-in username.
`AgentConfigInvalid` reports rejected profile, image, or port settings. It also
reports a failed download of a built-in Agent chart from the registry, for a
new Agent or after a spec change. For a failed download, the operator log
records the error from the registry.

Every full reconcile downloads the chart again. This includes the periodic
passes of a running Agent. If the chart of an Agent that is already installed
for its current generation cannot be downloaded, reason `AgentChartUnavailable`
reports the temporary failure. The deployed release stays unchanged, and the
operator retries every 30 seconds.

Workload health is still observed. If ready replicas are lost or the
StatefulSet cannot be read, a running Agent changes to `Provisioning` with
`PodReady=False` and `Ready=False`. If `BuiltInPrerequisitesReady=True` records
a completed prerequisite pass, readiness recovers when the workload recovers,
even if the registry is still unavailable. A later prerequisite failure clears
that condition independently of `PodReady`. A healthy Pod alone cannot clear
that failure. If an older status does not have this condition, a successful
full reconcile is necessary before readiness can recover.

Suspension, and failures or waits for other prerequisites, are kept. The
deployed access URL is kept until the related route is reconciled.
`AgentConfigResolved` reports a prepared built-in configuration, not runtime
readiness. The API does not expose a separate `status.agent` structure.

`RegistryCredentialsReady` reports credential synchronization. A true condition
does not prove that the registry accepted the credentials or that the image was
pulled. Examine Pod readiness and pull events separately. Custom readiness also
depends on its validated platform bindings, not only on schema acceptance.

Other condition types include `PolicyResolved`, `PolicyApplied`, `Ready`,
`HelmDeployed`, `IngressReady`, `PodReady`, and `TailscaleReady`. Examine them
independently. Overall readiness can be true even if ingress creation failed.
`TailscaleReady` reports that the key was copied. It does not report that a
node joined the tailnet.

`Suspended` skips pod-readiness checks. Examine the actual replicas and pod
termination. The chart allows zero or one replica and limits any enabled HPA to
`minReplicas=maxReplicas=1`.

## Team

| Field under `spec` | Contract |
| --- | --- |
| `agentPolicyRef` | The Team's single policy. Necessary when this Team opts in to policy |
| `sandboxRefs` | Nonempty set of allowed Sandboxes. Necessary for a policy-enabled Team |
| `defaultSandboxRef` | Listed creation default. Necessary for a policy-enabled Team. It is not a later implicit reselection |
| `memberContext.role` | Role rendered into runtime-specific team context |
| `memberContext.description` | Context lines, at most 20 |
| `skills` | Default identifiers, at most 100. With the Skill Marketplace, each entry without the `npm:` prefix is the name of a marketplace skill. See [Skill Marketplace](../../operations/skill-marketplace/) |
| `skillStorage.resourceGroup` | Necessary when `skillStorage` is present. A storage resource group that exists |
| `skillStorage.storageAccount` | Necessary when `skillStorage` is present. A Blob account that exists |
| `skillStorage.containerName` | Necessary when `skillStorage` is present. A skills container that exists |
| `skillStorage.storageClassName` | Optional override. The chart default is `azureblob-nfs-premium` |
| `skillStorage.storageSize` | Optional shared PVC request. The chart default is `100Gi` |
| `skillStorage.mountPath` | Optional read-only alias for native Agents. The common view is at `$HOME/.agents/skills` |
| `modelRouters` | Smart-routing models, at most 10, keyed by `name`. See [smart routing](../../guides/teams/#smart-routing) |
| `modelRouters[].name` | Model name that instances request: 1 to 64 characters without whitespace or `/`. It cannot start with `cetirun-` in any letter case |
| `modelRouters[].classifier.type` | `heuristic` (default), `llm`, or `jev`. LiteLLM's license-limited classifier types are not accepted |
| `modelRouters[].classifier.model` | Shared model that classifies requests. It is not a router of the Gateway. It is a chat model for `llm`, and a decision model (`model_info.mode: evaluation`) for `jev`. Necessary for `llm` and `jev`, and not allowed for `heuristic` |
| `modelRouters[].tiers.simple`, `.medium`, `.complex` | Necessary model of each tier. The value is the name of a shared model that answers conversations and is not a router of the Gateway. For a GitHub Copilot model, the value is `copilot/<model>`. `medium` also takes requests that cannot be classified |
| `modelRouters[].tiers.reasoning` | Optional. If it is empty, the tier uses the `complex` model |

| Field under `status` | Contract |
| --- | --- |
| `modelRouters[].name` | Router name from `spec.modelRouters` |
| `modelRouters[].gatewayModel` | Internal Gateway model name that instances send requests to. It stays the same when the configuration of the router changes |
| `modelRouters[].ready` / `message` | Shows if the router is registered with its current configuration. If it is not, it shows why |
| `modelRouters[].sharedModels` | The shared models that the router's tiers send requests to, as registered. A GitHub Copilot tier that joins a shared model is included, and the classifier is not. An Agent with an AgentPolicy gets the router only when the policy allows each of these models |
| `modelRouters[].copilotModels` | The `copilot/<model>` tiers that are registered under the ID of the Copilot model. An Agent must have GitHub Copilot and that model |
| `conditions[type=ModelRoutersReady]` | `True` when every router is registered. `False` names each router that is not registered. `Unknown` when the Gateway cannot be reached |

Each Team has a single `agentPolicyRef`. Agents do not select a policy class.
The pre-release `normalAgentPolicyRef`, `adminAgentPolicyRef`, and Agent
`policyClass` fields are removed from the API and its CRD schemas. This is a new
system contract. It is not a backward-compatible upgrade of the dual-policy
design. There are no compatibility fields or migration path for that design.

Instances select this object through `spec.team`. The selection applies the
Team's runtime defaults and, when configured, its single policy. It does not
grant access to the data or runtime of another Agent. There is no separate Team
membership or authorization record. Model and credential entitlements come from
that policy and are requested for each Agent.

A Team that is used for administrative work is an independent Team with its own
context and skills. It does not select a more privileged policy for another
Team. Permission to manage Teams or AgentPolicies is a human control-plane
permission. It is enforced separately from workload authorization. Workload
authorization comes from the selected Team, its `agentPolicyRef`, and the
Agent's explicit requests.

Teams with no policy or Sandbox fields keep the legacy behavior. If you set any
one of those fields, the Team opts in to the full policy binding contract.
Partial bindings cannot fall back to legacy. The Team controller accepts a
legacy Team without those references. It also accepts a policy-enabled Team
after its policy and Sandbox references form a valid current configuration.

Runtime context persistence is also different. Hermes refreshes `TEAM.md` on
initialization, but OpenClaw keeps the `USER.md` of an initialized workspace.
See [configure a team](../../guides/teams/).

Skill entries in `skills` and `skillOverrides` follow this schema pattern:

```text
^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$
```

The `npm:` prefix selects the npm installer. Without the Skill Marketplace,
other accepted identifiers are only passed to the reserved `skills.clawHub`
list. They are not installed. Shared storage uses chart-level protocol and
mount options. Those options are not fields on `skillStorage`. See
[configuration and skills](../../concepts/configuration/).

When the operator's `skillMarketplace.enabled` is on, every entry without the
`npm:` prefix is the name of a Skill Marketplace skill. Such a name has
lowercase letters and digits in words that single hyphens join, and at most
64 characters. These rules apply:

- The operator does not pass the name to `skills.clawHub`.
- For a Team with an Agent Policy, the Skill Marketplace publishes the
  selected skills. Its common-executor Sandbox Agents copy them when they
  start. OpenClaw and Hermes read them in `$HOME/.aks/skills`, and Custom in
  `$HOME/.agents/skills`.
- An Agent cannot add or remove such a name in `skillOverrides`. The operator
  sets the Agent to `Failed`, with `AgentConfigInvalid` and this message:
  `skillOverrides cannot add or remove Skill Marketplace skills; select them on the Team`.
  This also applies to Agents without a policy.
- A change of the Team's selection restarts the Team's running Sandbox Agents
  one time, as each Team change does.

See [Skill Marketplace](../../operations/skill-marketplace/).

### Team and Sandbox administration

Portal administrators can create and edit Teams with `agentPolicyRef`,
`sandboxRefs`, and `defaultSandboxRef`. The Portal accepts only complete
bindings and validates the referenced resources. It also makes sure that the
default is in the allowed Sandbox set and that the Policy allows the Team
Skills. The Team DTO returns these fields together with `uid` and
`resourceVersion`. Updates and deletes must send those two version fields. A
stale version returns `409 Conflict`, and a request without those version fields
returns `428 Precondition Required`.

The Teams list shows compatibility for the saved Policy and all selected
Sandboxes. Expand a Team to see its overall compatibility, labeled Policy and
Sandbox references, reasons, and check time. Multiple Sandboxes appear in a
table with a separate compatibility result for each Policy and Sandbox
combination.

The Policy and Sandbox lists also show the Teams that refer to them. The Sandbox
view shows the overall Team result separately from the result of that Sandbox. A
Policy's configuration acceptance and a Sandbox's prerequisites are separate
from Team compatibility. None of these observations proves that an Agent applied
its Policy or is ready to run.

The Operator records this report in the optional `Team.status.compatibility`
field. It examines saved configurations every 15 seconds and after Policy or
Sandbox changes. The report identifies the Team, Policy, and each Sandbox by
name, UID, and generation, with `checkedAt` and `validUntil` timestamps. The
report expires after 30 seconds. The Portal shows `Pending` when no report
exists and `Unknown` when an observation expires or a referenced version
changes. Teams without Policy configuration show `Not configured`.

The report is for display. It does not change the Team `Accepted` condition or
authorize Agent execution. The admin Team API includes the report, the Team
generation, and deletion state. The Sandbox API also returns generation,
observed generation, and conditions.

Edit pages show field validation and errors from the current save. They do not
examine unsaved drafts or show saved status as a draft result. Kubernetes
admission examines the submitted configuration before it is stored.

For a known compatibility rejection, the Portal returns
`422 configuration_incompatible` with an optional `error.details` list of field
and combination diagnostics. The editor keeps the unsaved input. Version,
permission, and coordination errors stay separate. A successful save does not
mean that the Operator already examined the new version.

For an upgrade, update the CRD schema, admission, and all other strict Team
readers. Then run the Operator that writes compatibility status. A migration of
the Teams that already exist is not necessary. They get a report when the
Operator reconciles them. Older objects without the optional field stay
readable. Do not downgrade a strict reader until it can tolerate the new status
field.

The **Admin → Sandboxes** pages manage the complete Sandbox configuration:
runtime type, Agent and helper resource requests and limits, HOME storage, and
scratch size. They use these admin-only endpoints:

- `GET` and `POST /api/v1/admin/sandboxes`
- `GET`, `PUT`, and `DELETE /api/v1/admin/sandboxes/{name}`

The Sandbox request contains `name`, optional `type`, `resources`, and
`storage`, with the same structure as the CRD spec. If `type` is omitted or set
to `null`, the value is `kata` on both create and full-spec update. This agrees
with how Kubernetes applies defaults. An explicit invalid value, for example an
empty string, is rejected. It is not replaced by the default. Resource and
storage fields are still necessary.

For updates and deletes, the `uid` and `resourceVersion` that a read returns are
also necessary. The response reports `ready` only from a condition for the
current generation. If you save a Sandbox, the save does not install a runtime
or establish runtime readiness.

When it is configured, Team and Sandbox changes use the configuration
coordination client. This includes conditional writes, uncertain outcomes that
are kept, and background deletion. Admission is still responsible for platform
compatibility and current consumer checks. The editor shows Kubernetes
validation and admission rejections, but transport failures stay in logs.

### Skill Marketplace administration

The Portal chart's `skillMarketplace.enabled` turns on these Portal API
behaviors. Keep it equal to the operator's `skillMarketplace.enabled`.

- `GET /api/features` returns `skillMarketplaceEnabled`.
- `GET /api/v1/admin/skill-marketplace` is for administrators only. It returns
  `enabled`, `known`, and `catalog`, with `Cache-Control: no-store`. `enabled`
  is `false` when the Portal setting is off. `known` is `false` until the
  Skill Marketplace writes its catalog, and `catalog` is then `null`.
- The catalog is the `catalog.json` document of the ConfigMap
  `cetirun-skill-marketplace` in the operator namespace. The Portal accepts
  only the document version `v1`, without duplicate fields, and at most 1 MiB.
  If it cannot read or decode the document, the response is
  `503 Service Unavailable` with the code `skill_marketplace_unavailable`.
- Team creation and update examine `skills`. Each entry without the `npm:`
  prefix must be a valid marketplace skill name. Otherwise, the response is
  `422` with the code `invalid_team`. Each entry must be in the Policy's
  `skills.allowed` (`400`, `invalid_team_binding`).
- Each marketplace skill that the stored Team does not select yet must be in
  the catalog (`422`, `invalid_team`). When the Skill Marketplace rejected the
  repository's copy, the message gives the reason. Before the catalog exists,
  the response is `422` with `invalid_team`. If the Portal cannot read the
  catalog, the response is `503` with `skill_marketplace_unavailable`. A skill
  that the Team already selects can stay after it leaves the repository.
- A Policy update cannot remove a marketplace skill from `skills.allowed`
  while a Team of the Policy selects it. The response is `422` with the code
  `invalid_policy`, and the message names the Team.
- `GET /api/v1/instances/{name}` returns `teamSkills` for an Agent whose Team
  has an Agent Policy and selects marketplace skills. Each entry has `name`,
  `description`, `version`, and `state`. The version is the one in the release
  of the Team. The state is `published`, `updating`, `pending`, `retained`, or
  `missing`. `updating` means that the repository has another version, which
  the entry gives as `repositoryVersion`. Without a catalog, each skill is
  `pending`. The field is absent when the Portal cannot read the Team or the
  catalog.
- The same response has `teamSkillsPublicationFailed: true` when the catalog
  has a publication error for the Team. The response does not include the
  error; administrators see it on **Admin → Skills**.
- The same response has `teamSkillsRestartNeeded: true` when the newest Pod
  of the Agent copied an earlier Team release. Both releases are of the current
  Team selection. The Portal reads the copied release ID from the termination
  message of the `init-home-data` container. Without that ID, the field is
  absent.

See [Skill Marketplace](../../operations/skill-marketplace/) for the catalog
fields and the states.

## Sandbox and AgentPolicy

`Sandbox` describes the prepared `kata`, `gvisor`, or `container` runtime, Agent
and helper resource budgets, private home storage, and scratch. `AgentPolicy`
describes selection constraints, read-only root filesystem, network and ingress
controls, and optional Kubernetes or Entra identity intent. Configuration
references and schema acceptance do not grant access or prove enforcement.

`container` is explicit ordinary-container execution (for example, runc), not
VM isolation. The Agent shares the node kernel. A trusted registration with an
explicitly pinned RuntimeClass UID/handler and the enabled common executor are
necessary for it. Kata is still the default when the value is omitted or null.
gVisor's execution scope does not change.

Container uses the same AgentPolicy, Gateway, Cilium, and retained HOME
lifecycle, with per-container resource limits and no implicit VM overhead. If
you save a configuration, the save does not discover or install a handler.
Container supports the same separately approved
[managed ingress mTLS](../../guides/agent-ingress-mtls/) as Kata. A capability
flag alone does not authorize the endpoint.

Observed-SNI blocking and Kubernetes grants must have the same explicit
common-adapter registrations and source approvals as Kata. The container
RuntimeClass alone does not enable either of them. Select a new Sandbox or
Agent instead of an online runtime conversion.

Policy assignment is opt-in. It is not a prerequisite for legacy deployment. An
Agent with no Sandbox selection or recorded governed lifetime can use no Team,
or a Team without policy settings. Such Agents keep the runtime flow that exists
and do not claim policy enforcement. For opted-in Agents, the controller
resolves owner and Team authorization, the effective AgentPolicy, and the
selected Sandbox before a workload can run. Invalid or unavailable
authorization is fail-closed.

The native Sandbox path compiles and stages protected resources at zero
replicas. Execution stays held unless the separately configured, default-off
native execution profile is enabled and its supported prerequisites pass.
Approved Gateway-backed Custom images use this same common executor. The
[Custom image and interface contract](../agent-harness/#policy-governed-custom-execution)
also applies to them. If an approval is absent or a combination is not
supported, the Agent stays held. Ungoverned Custom Agents keep their runtime
provider path.

The controller records a hold finalizer before it changes workloads. As a
result, a failed status update cannot release the recorded hold. Explicit
suspension stays `Suspended` unless shutdown fails. Built-in resources without
verifiable Agent-UID ownership are left untouched, and recovery is necessary
for them. A match of resource names alone is not sufficient.

When a held built-in Agent is deleted, only the namespace that is confirmed as
its own is removed, without Helm uninstall hooks. Finalizers are kept until
cleanup completes. Home storage is kept during a hold, but not after an
explicit namespace deletion. Agent Identity authorization and directory cleanup
is still a step that blocks the deletion of held built-in Agents. This is also
true when the runtime namespace is absent.

Compatibility does not automatically release an old Agent that was already held
by a mandatory-policy version. Before recovery, examine its ownership and the
data that is kept. The removal of spec fields is not permission to clear its
recorded policy lifetime.

The Agent creator selects a Team directly. Admission applies the Team's default
Sandbox. Then it persists UID-bound references, so that a replacement resource
with the same name cannot be adopted implicitly. An administrator and a
suspended Agent are necessary for later Team, Sandbox, policy, or budget
changes.

Policy-enabled resources that exist enter a controlled hold for cleanup. If
references are changed or cleared, this does not return them to legacy
execution. Explicit reauthorization and an ordered stop-and-cleanup sequence are
necessary for changes. A lossless rolling migration between policies is not
supported. Operator status reports desired per-domain revisions. It marks Policy
Applied only after enforcement evidence that matches is current.

AgentPolicy also allowlists Harnesses, skills, exact Gateway model names,
Copilot, and Agent Identity. A native LiteLLM default and maximum monthly USD
budget are necessary for a model entitlement. The allowed Harnesses are
OpenClaw, Hermes, and Custom. For Custom, the common executor and an approved
Gateway-qualified image are also necessary. Policy permission alone does not
qualify an image or backend. Agent Identity cannot complete aggregate
`PolicyApplied` until a typed credential-service executor supplies read-back
evidence that matches.

`skills.allowed` lists the exact entries that the Team's `skills` and an
Agent's `skillOverrides.add` can contain, `npm:` packages included. If a Team
or an Agent requests a skill that the list does not contain, the operator
denies the Agent. Its `PolicyResolved` condition is `False`, with reason
`PolicyDenied` and the message `skill "<name>" is not allowed by policy`. The
operator then revokes the Agent's Gateway key and stops the Agent.

The skill allowlist is not an execution input of Sandbox Agents. A change
that only adds or removes allowed skills does not restart running native
Agents. A Policy with an empty or absent allowlist keeps the execution digest
that earlier operators recorded. For a Policy that allows skills, the digest
that earlier operators recorded includes the allowlist. After the operator
upgrade, the first reconciliation sees that Policy as changed, as after an edit
of its execution settings.

### AgentPolicy model access

| Field | Contract |
| --- | --- |
| `modelAccess.sharedModels.allowed` | Exact Gateway model names that the Agents of the policy can use, routers included. For a router, also list the models of its tiers, its default model, and its context compaction model, if it has one. Each name has at most 256 characters, without whitespace |
| `modelAccess.copilot.allowed` | Boolean. Lets the Agents use their owners' GitHub Copilot. The Portal shows it as **Allow GitHub Copilot** |
| `modelAccess.gatewayBudget.defaultMonthlyUSD`, `maxMonthlyUSD` | Monthly budgets in US dollars, as decimal strings above zero with at most six decimals. Necessary when the policy allows a shared model or Copilot. Not allowed otherwise. The default budget must not exceed the maximum |
| `modelAccess.rateLimits` | Request limits on the key of each Agent. See [AgentPolicy rate limits](#agentpolicy-rate-limits) |

The key of each Agent gets the default budget. An administrator can set the
`modelAccess.monthlyBudgetUSD` of a suspended Agent, up to the maximum. A
budget resets on the first day of each month, UTC. A background job of the
Gateway applies the reset, so it can occur up to about ten minutes late.

When the spend of a key reaches its budget, the Gateway refuses the requests of
the key with HTTP 402 until the reset. The requests to the Agent's GitHub
Copilot models count against the same budget. See
[When the budget runs out](../../guides/models/#when-the-budget-runs-out).

### AgentPolicy rate limits

`modelAccess.rateLimits` sets AI Gateway limits on the virtual key of every
policy-bound Agent whose Team uses the policy. The key of each Agent keeps its
own counters. As a result, Agents do not use up the limits of other Agents.
Every field is optional, and an omitted field is unlimited. There is no zero
value. The Gateway rejects every request of a key whose limit is zero, so the
schema rejects values below the minimums.

| Field | Minimum | Gateway key setting |
| --- | --- | --- |
| `maxParallelRequests` | 1 | `max_parallel_requests`, requests in flight at the same time |
| `rpm` | 1 | `rpm_limit`, requests per minute across all models |
| `tpm` | 4096 | `tpm_limit`, tokens per minute across all models |
| `perModel.<model>.rpm` | 1 | `model_rpm_limit` in the key metadata |
| `perModel.<model>.tpm` | 4096 | `model_tpm_limit` in the key metadata |

`perModel` limits at most 64 models. Its keys are the exact Gateway model names
that the Agent requests (1 to 256 characters without whitespace). Each entry
sets `rpm`, `tpm`, or both. `maxParallelRequests` is at most 2,147,483,647,
which is the largest value that the Gateway stores. The other limits have no
upper bound. The values are not compared with the context window of a model.

For a router of the Agent's Team, a `perModel` key can also be the name of the
router. The operator then also sets the limit under the internal name of the
router, which the Gateway counts for the router. The limit under the name of
the router stays. It applies to another model that keeps the name, for example
a GitHub Copilot model of the Agent. If the policy sets a limit for both names,
the internal name gets the smaller value.

The operator sets the limits when it creates a key. On every reconcile, it
compares the stored limits of the key with the policy. When they are different,
it updates the same key in place. A removed field is cleared on the key. Other
key settings and metadata are kept.

The limits are not part of the Gateway policy revision. As a result, a change to
them does not start the reprojection fence that a model or budget change starts.
For the same reason, the operator keeps the key of each Agent.

Sandbox execution also excludes `modelAccess.rateLimits` from its execution
inputs. This also applies to coordinated configuration reservations. A change
that only adds, edits, or removes these limits does not stop or restart running
native Agents. This is also true for `perModel`. For common-executor Sandboxes,
this also applies to workloads during initialization or during the completion
of a retained restart. A limits-only update does not reapply the workload,
temporarily block its key, or release its unchanged native reservation.

During bootstrap, a policy status update is compared with the captured
execution inputs and the immutable UID, not only with its resource version.
Until policy status reaches the new generation, the operator assesses the
current policy again. It uses the same trusted platform inventory and grants as
the policy controller. It does not stop an Agent only because of that
observation delay. Unavailable or invalid policy evidence still holds execution.

Budgets, model authorization, and other execution controls keep their normal
fencing and coordination checks. The exact coordinated write still includes the
limits. Keys of legacy Agents without a policy are not changed. Gateway
administrators manage their limits.

This change does not rewrite persisted execution evidence. The saved policy
digest of an older single-start lifetime or coordination reservation can include
`modelAccess.rateLimits`, even as an empty object. In that case, the lifetime or
reservation stays fail-closed on an input mismatch, and the stop/recreate
procedure that exists is necessary. Before you edit limits, upgrade the operator
and admission readers. Do not manually change their saved digests.

The total limits count every request on the key. This includes the Agent's
GitHub Copilot models. A per-model limit applies to the model name that a
request uses after model aliases. For an `auto` router, that is the name of the
router, not the model that it routes to. For a router of a Team, that is the
internal name of the router, also when the Agent sends the router's name.

A request over a limit gets HTTP 429 with a `retry-after` header. Gateway
replicas share the counters through Redis.

The Gateway counts `tpm` from the tokens of each finished request. Cached input
tokens are excluded. When the count for the minute reaches the limit, the
Gateway refuses the requests of the key until the next minute. A single request
can take the count past the limit. That is the Gateway chart's default,
`tpmReservation: false`.

With `tpmReservation: true`, LiteLLM instead reserves the input of a request and
its maximum output tokens before it calls the model. If they do not fit, LiteLLM
refuses the request. With every request, OpenClaw asks for the maximum output of
the model, 128,000 tokens for current GPT models. As a result, in that mode a
`tpm` of 200,000 refused every request of an OpenClaw conversation past about
55,000 input tokens. The reservation alone was larger than the limit.
OpenClaw stopped after nine one-minute retries (measured on a test cluster).

The minimum of 4,096 tokens per minute makes sure that a limit does not refuse
all requests except the smallest.

### AgentPolicy administration

The admin Portal shows the complete cluster-scoped `AgentPolicy` configuration.
Administrators can edit allowed Harnesses and skills, Copilot and shared model
access, monthly USD budgets, and per-Agent rate limits. They can also edit
multiple-Sandbox selection, read-only root filesystem, network CIDR and port
rules, ingress mode, Agent Identity, and Entra workload identity. With the Skill
Marketplace, the editor also lists the skills of its catalog, so that
administrators can select the allowed skills.

The **Identity** section has two independent controls. **Agent Identity**
selects `Disabled`, `Optional`, or `Required` for an independent Microsoft Entra
directory identity used through Broker/Connections. Optional lets the user
choose during Agent creation; Required selects it automatically. Both enabled
modes set the current credential-service entitlement, `azure-resource-manager`.
Disabled clears that entitlement. The platform must have the
[Agent Identity setup](../../operations/agent-identity/) in place.

**Entra workload identity** controls `entraID.enabled`. It projects an exchange
token and Azure SDK environment into the main application container using an
explicitly approved identity. It requires platform registration, a pinned
ServiceAccount, federation, and external resource permissions. It does not
create the Broker-backed directory identity or grant Kubernetes access. Neither
control automatically enables the other. New policies default both to disabled.

The Portal shows Kubernetes API access under Read-only policy capabilities. The
summary shows any requested ServiceAccount and namespace or cluster-wide
permissions that are already recorded in the Policy. Platform administrators
manage this configuration outside the Portal. For authorization, separately
approved platform grants are necessary. If you save other Policy fields, the
save keeps the Kubernetes access configuration that exists.

New common-executor Kata OpenClaw/Hermes Agents can use explicitly registered
Kubernetes access with exact policy-role and per-Agent ServiceAccount approvals.
The operator projects a bounded, audience-specific token only into the main
container. It pins role UID/version/rules and journals the owned binding UIDs.
Suspension, grant removal, and deletion revoke those bindings. Shared
ClusterRoles are not adopted or deleted. Nil access grants no Kubernetes API
token or permissions.

Kubernetes access does not enable Entra ID or Broker/Connections. See the
[administrator setup and validation boundaries](https://github.com/Azure/cetirun/tree/main/examples/native-sandbox#kubernetes-api-access).

The Internal network access editor puts each destination CIDR and its complete
port list into one rule card. Each card matches one `allowedInternalCIDRs`
entry. Edit rule opens the entire rule. Update rule replaces its destination
and ports together. Cancel discards the draft. Delete rule removes the entire
entry.

In the editor, ports are comma-separated, for example `443, 5432, 53/UDP`. A
port uses TCP unless it has `/UDP` or `/SCTP`. Rule changes are submitted when
the Policy is saved.

Blocked FQDNs and ingress mTLS are read-only in this editor. Values that exist
stay visible, and they are kept when other fields are
saved. For observed-SNI FQDN filtering, separately registered controlled Envoy
and trusted capture-init images are necessary. This feature is not full
hostname enforcement. After the explicit one-second inspection window, it
continues uninspected.

For ingress mTLS, the separate
[administrator-registered managed endpoint](../../guides/agent-ingress-mtls/)
is necessary, not a Portal toggle. For aggregate `PolicyApplied`, Broker-backed
Agent Identity must have current credential-policy readback. Common-executor
workloads must also have the registered native Connections integration. Other
controls depend on the selected execution profile and platform capabilities.

With the applicable explicit platform registration and approvals, new
common-executor Kata lifetimes support internal CIDR grants, approved
Kubernetes access, and SCTP port rules. SCTP internal grants are limited to
direct endpoints. Registered Service and Kubernetes API destinations are
excluded. Version-1 native single-start lifetimes that are kept still reject
internal grants, Kubernetes access, Entra projection, and SCTP requests.

Common-executor Entra projection must have explicit platform approval and a
pinned bootstrap-owned ServiceAccount. It does not grant Kubernetes access. See
the
[native SCTP usage and qualification boundary]({{< repo "examples/native-sandbox/README.md" >}}#governed-sctp-egress).
An accepted API value does not qualify the actual network path.

The Operator keeps the Policy's `Accepted` condition for controller decisions
and diagnostics. The Portal list shows the condition for the current generation,
and the editor shows only field validation and save errors. `Accepted=True`
describes only configuration acceptance. Sandbox compatibility, Agent
authorization, and runtime enforcement must still succeed. If you save a Policy,
the save does not prove that its consumers applied it. If you edit a Policy that
is already bound, the edit can affect those consumers.

#### HTTP API

These admin-only endpoints manage `AgentPolicy` resources:

- `GET` and `POST /api/v1/admin/policies`
- `GET`, `PUT`, and `DELETE /api/v1/admin/policies/{name}`

Update and delete requests include the UID and resource version that a
previous read returned. Stale requests fail with `409 Conflict`. Responses
include all Policy configuration groups, `generation`, `observedGeneration`,
`conditions`, and `deleting`. These status fields are read-only.

Each create or update replaces `agentIdentity` and `entraID` as complete
objects. Omitting a group or sending `null` disables that group; an empty
object also uses disabled defaults. The Portal always sends both groups.
`agentIdentity.required` requires `allowed: true`. Allowed identities must
include `azure-resource-manager` in `credentialServices`; disabled identities
must have no credential services. Unknown and duplicate services are rejected.
There is no dependency between `agentIdentity` and `entraID.enabled`.

For Sandbox selection, root filesystem security, and editable network fields,
omission keeps the stored value. Explicit empty arrays clear editable lists.
Kubernetes access, blocked FQDNs, and mTLS cannot be changed through this API.

A request that tries to change or clear Kubernetes access is rejected. If a
request omits it, the configuration that exists is kept. Updates keep metadata,
status, and the coordinated
mutation and optimistic concurrency protections that exist.

When strict Sandbox admission uses a UID-pinned configuration authority, set
the Portal chart's `configurationCoordination.namespace`, `name`, and `uid`.
Set them to the same authority that the Operator uses. The platform chart
rejects mismatched values.

The Portal then reserves the exact whole-resource mutation. It records
`Changing` before it submits one conditional Update or background Delete, and
then it observes the result. It releases only an operation whose outcome is
confirmed. Its namespaced Role allows access only to the named authority
ConfigMap. UID/resource-version checks are still request concurrency
protection. They do not replace this admission contract.

A reservation conflict returns `409 Conflict`. An unavailable authority or an
uncertain API write returns `503 Service Unavailable`. In that case, the
reservation is kept deliberately. Do not blindly repeat the mutation. Recover
the recorded operation with the Operator's configuration-change procedure.

Update reservations record the complete writable-content digests before and
after the change. Before a save, the Portal looks for a recorded update, also
after a restart. A read of the same live UID at a different resourceVersion
allows recovery when the content matches the proposed configuration (applied).
It also allows recovery when the content matches the original configuration
(superseded). A status-only refresh can cause a superseded result, and then the
requested edit is not applied.

Other configuration changes or an unknown outcome keep the reservation. Recovery
never submits the old update again, and it never relaxes the version checks of
the caller. A stale edit must still get a new read before a save.

CLI update recovery uses the exact original `--before` and `--after` files.
`--action observe` is read-only. It reports
`update-superseded-reservation-retained` when a newer version still has only the
original writable content. Explicit `--action update` then releases the old
reservation and reports `update-superseded`. It does not write the
configuration.

New deletion reservations durably record the content digest that is necessary
for recovery. An administrator can use the operation ID from the pinned
authority journal with `--action observe-delete --operation-id ID`. This is
possible even after the policy and the Portal request are gone. Observation
keeps the reservation. Explicit `--action delete --operation-id ID` releases it
only after it makes sure that the original UID is absent or in termination. It
does not submit a Delete again.

For older deletion reservations, the original `--before` input is still
necessary. Before you issue reservations with the new recovery metadata,
upgrade all coordination readers. Creation is not covered because configuration
coordination protects updates and deletions, not new resources.

The API validates model access and budgets as one unit. For Copilot or shared
model access, a positive default and maximum monthly USD budget are necessary.
The default cannot be more than the maximum. The API applies the schema's
rate-limit minimums before it writes.

`modelAccess.rateLimits` in an update replaces the stored limits. An update
without it removes them. As a result, a client saves the limits that it read.
The Portal editor treats an empty input as no limit and never sends zero.

#### Agent creation options

The creation form selects a Team before it offers Harness and model choices.
For `GET /api/v1/instance/create-options?team=<name>`, a signed-in user is
necessary. This endpoint returns the selected Team, the Policy name when bound,
allowed Agent types, and the effective Gateway catalog. It also shows if Agent
Identity is available and if it is necessary. It also returns the Policy's
per-Agent rate limits and any configuration blocker. Responses use
`Cache-Control: no-store`.

This endpoint does not expose other administrative Policy fields or grant
runtime authorization. The form and the Agent details page show the rate
limits read-only. The details page reads them from the Agent's Team Policy.

Harness choices intersect deployment settings with `harnesses.allowed`. Shared
models intersect the Gateway catalog with `modelAccess.sharedModels.allowed`.
For Copilot, both deployment availability and Policy permission are necessary.
If Agent Identity is necessary, it is selected automatically, and it cannot be
turned off. Agent Identity must also have the deployment feature, a confirmed
owner and a Policy credential-service entitlement. The separate
`entraID.enabled` workload projection is not required for Agent Identity.
Prerequisites that are not met block creation.

Both Agent creation endpoints re-read the same constraints before they write an
Agent or credentials. This also applies to a private-registry creation retry.
Explicitly forbidden choices are rejected. Omitted model-source switches use
the sources that are available now, and they are persisted as explicit
booleans. The default model must belong to an enabled, authorized source.

Team default Skills must satisfy the Policy. The runtime continues to apply its
default Gateway budget. No Skills or budget editor is added to the Agent form.

Incomplete Team bindings cannot fall back to unrestricted creation. The same is
true for Policies that are absent or in deletion, and for unreadable
dependencies. It is also true when no Harness/model source is available. A Team
without Policy/Sandbox settings keeps optional policy assignment.

Policy-controlled native Agents must have the AI Gateway. Configuration
admission and runtime enforcement are still responsible for final
authorization and execution. Available form choices do not establish Sandbox
readiness.

#### Activation boundary

The Team editor manages `agentPolicyRef`, `sandboxRefs`, and
`defaultSandboxRef`. For multiple allowed Sandboxes, the selected Policy's
`sandboxSelection.allowMultiple` is necessary. When you switch to a restrictive
Policy, the selections that exist stay visible. You must correct them before
you save. The Portal does not silently remove bindings. Backend validation
re-reads the Policy before it accepts the Team request, and admission stays
authoritative.

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
- [Portal name validation]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}})

Use `agents.cetirun.microsoft.com` and `teams.cetirun.microsoft.com` in scripts
to avoid ambiguity with resources from other API groups.
