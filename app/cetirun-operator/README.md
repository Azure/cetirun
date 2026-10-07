# CetiRun operator

The operator reconciles `Agent` and `Team` resources in
`cetirun.microsoft.com/v1alpha1`.
The same API group also defines the inactive `Sandbox` and `AgentPolicy`
configuration foundations described below.

- [Custom-resource reference](../../site/content/en/docs/reference/crds.md)
- [Agent Harness contract](../../site/content/en/docs/reference/agent-harness.md)
- [Operator chart configuration](../../site/content/en/docs/reference/helm.md#operator-chart)
- [Architecture and resource names](../../site/content/en/docs/concepts/architecture.md)
- [Platform installation prerequisites](../../site/content/en/docs/getting-started/platform.md)
- [Lifecycle and deletion boundaries](../../site/content/en/docs/guides/lifecycle.md)

## API contract

| Kind | Resource | CRD name | Short name | Scope |
| --- | --- | --- | --- | --- |
| `Agent` | `agents` | `agents.cetirun.microsoft.com` | `cra` | Cluster |
| `Team` | `teams` | `teams.cetirun.microsoft.com` | `crt` | Cluster |
| `Sandbox` | `sandboxes` | `sandboxes.cetirun.microsoft.com` | `crs` | Cluster |
| `AgentPolicy` | `agentpolicies` | `agentpolicies.cetirun.microsoft.com` | `crp` | Cluster |

An Agent is a long-lived user-owned environment; a Team
supplies shared configuration, not access to private agent data.

The API replaces `OpenClawInstance` and `OpenClawTeam` without changing the
group or version. Installing these CRDs does not convert, adopt, or delete
legacy objects. Do not delete legacy CRs or CRDs as an upgrade shortcut:
finalizers may remove workloads and namespaces, and deleting a CRD removes
its stored objects. Existing installations need a reviewed data-retention and
ownership-handover plan. See the
[compatibility guidance](../../site/content/en/docs/reference/crds.md#existing-installations).

Source layout: `api/` defines the types, `config/crd/bases/` contains schemas,
`internal/` contains reconciliation and Helm/Azure integration, and `chart/`
deploys the operator. The chart packages CRDs under `chart/templates/crds/`;
Helm installs and upgrades them with the release. Their
`helm.sh/resource-policy: keep` annotation retains them on uninstall. Follow
[DEVELOPMENT.md](../../DEVELOPMENT.md#generated-files-and-dependencies) for
pinned generation commands.

`deploy/deploy.sh` is a connected, mutating helper, not a local smoke test.
For a Gateway in a separate namespace, configure the
[Gateway reference and workload namespace](../../site/content/en/docs/reference/helm.md#operator-chart)
independently.

## Sandbox foundations: execution is not enabled

`Sandbox` describes a reusable runtime, container resource declarations, private
home storage and scratch. `AgentPolicy` describes selection, security, network,
ingress and identity intent. Both configuration resources are cluster-scoped.
Teams bind a required normal policy, an optional admin policy, permitted
Sandboxes and a creation default; an Agent binds a Sandbox and a normal/admin
policy class. A human administrator's role does not select an Agent's class.
These bindings coexist with the required `spec.agent` Harness configuration.

The [sandbox package](internal/sandbox/) currently provides:

- Pure defaulting, validation and deterministic normalization without mutating
  source objects.
- Cross-resource validation of every configured Team Sandbox/policy pair.
- Class-specific resolution with explicit authorization of Agent, Team,
  Sandbox and AgentPolicy UIDs. Same-name replacement requires new authorization.
- A desired-configuration revision that excludes metadata, unselected policies,
  creation-default changes and unrelated model-access settings.
- Qualified-capability checks and a freshness/identity check for trusted
  observations. Zero-value registrations, missing observations, stale evidence
  and incomplete verification fail explicitly.

These helpers do not write workloads, authorize RBAC grants, compile a complete
network policy, or prove enforcement. There is no packaged guest-external
observer, writer-fencing adapter or default capability registration. Explicit
platform declarations can be read through the Kubernetes sources below; their
registration revision must represent qualified runtime/chart/topology/grant
inventory, not merely the existence of a RuntimeClass.
An observation validator checks an adapter's input; it does not generate evidence.

The new Team bindings and Agent `sandboxRef` remain optional in this additive
foundation schema so existing unbound resources retain their previous behavior.
The resolver requires a complete governed binding. This is not the final
required-field migration or authorization for deployment. The optional
configuration-admission process below adds a write boundary, not execution.
`policyClass` defaults to `normal`; that default alone does not adopt a legacy
Agent into the governed path.

The controller holds Agents that request Sandbox governance, belong to a Team
with governance settings, or already have Sandbox status or a hold finalizer.
It never passes them to built-in or Custom runtime preparation or application.
Before workload changes it persists a dedicated hold finalizer, which also
keeps newly held Agents available for cleanup. Clearing spec fields cannot
release that recorded hold, even if a later status write fails. If the initial
finalizer write fails, no workload changes are attempted; an unpersisted request
is not a durable hold.

For resources with verified ownership it removes the route and requests zero
replicas while retaining home storage. It reports `SandboxReady=False` and
preserves any last-applied binding. Explicit suspension remains `Suspended`;
shutdown/removal/completion failures are reported as failures and retried.
Built-in resources require an Agent UID owner reference and non-conflicting
ownership/release metadata. Older resources without that evidence are not
automatically adopted or stopped: the hold reports an ownership error requiring
reviewed recovery. Route and namespace deletes use UID/resourceVersion
preconditions. Custom shutdown uses the existing ownership checks,
preserves external home storage, and retries prior apply-completion records
without preparing a new workload.

Held built-in deletion removes only a verified Agent-owned namespace, waits for
its disappearance before removing finalizers, and never runs Helm uninstall
hooks. Unknown ownership leaves deletion pending for recovery. Deletion removes
namespace contents; home retention during a hold does not promise retention
after an explicit Agent deletion. Custom deletion retains its existing receipt
and completion checks.
For held built-in Agents, Agent Identity authorization and directory cleanup
must also succeed before namespace deletion or finalizer removal, even if the
runtime namespace is already absent or the identity feature was deselected.

Portal Team edits preserve governance fields that the Portal does not expose.
Portal creation registers the cleanup finalizer and binds only namespaces it
newly created to the persisted Agent UID. Existing namespaces are not adopted.
The Portal chart grants namespace `patch` for this ownership write, in addition
to its existing `get`/`create` permissions; deploy the matching RBAC with the
Portal binary.
If that ownership write fails, creation reports an error and the Agent remains
tracked for recovery; it must not be retried as an unrelated new Agent.

This hold is a development activation guard, not an enforcement adapter or a
fence for an unreachable node. Do not adopt existing live workloads until
admission, approved rendering, identity revocation, actual enforcement
observation/fencing and the ordered rollout path are implemented and qualified.
The guard is the temporary controller-owned stop path; normal runtime reconciliation
cannot concurrently recreate a held workload.

### Optional configuration admission

The operator image also supports `--mode=sandbox-admission`. This starts an
independent HTTPS admission process, not runtime controllers or leader election.
It uses uncached API reads and a separate ServiceAccount with only `get`/`list`
access to the four configuration kinds by default. Optional Pod admission adds
read access to workload prerequisites and namespace-scoped controller state,
not cluster-wide Secret or ConfigMap access. It does not mount operator cloud
credentials, registry credentials, or workload Secrets.
Agent validation fetches its Team and only that Team's named Sandbox/policy
references. With [Kubernetes-backed sources](#kubernetes-backed-sources-opt-in)
enabled, governed configuration checks also read the selected platform
declarations and verify RuntimeClass/StorageClass pins.
Shared-configuration checks scan consumers in pages of 200, reuse
named reference reads only within the current request, and fail on incomplete
or expired page reads. There is no cross-request reference cache.

The chart defaults `sandboxAdmission.enabled`, `sandboxAdmission.enforce` and
`sandboxAdmission.podsEnabled` to `false`. Default installations retain their
existing runtime behavior. Enabling the
server does not install webhook rules; `enforce` is a separate bootstrap step.
Once enforced, even legacy Agent writes depend on webhook availability, although
healthy admission leaves their Sandbox selection unchanged.

This increment implements:

- Explicit administrator usernames/groups for shared governance configuration,
  and trusted Team assignments for normal-class member Agents. Kubernetes RBAC
  must also authorize the operation.
  Portal administrators do not automatically acquire this independent authority.
  A human administrator's Agent still defaults to the `normal` policy class.
- Creation-only Agent selection defaulting, followed by validation against the
  current Team. Existing Agents are never silently re-defaulted. Governed member
  writes require an exact authenticated identity-to-Team assignment; Portal
  identity propagation is a separate integration and is not inferred here.
  Mutation and validation are installed together: Kubernetes runs mutation
  first, including for dry runs. Validation rejects a missing selection rather
  than assuming that a default was persisted.
  Agent creation using `metadata.generateName` is accepted before the API server
  assigns a name; once assigned, the object and admission request names must match.
- Durable admission-owned Team/Sandbox/selected-policy UID authorization on the
  Agent, independent of last-applied status. Same-name replacement never grants
  authority to the new UID. The mutator also handles explicit administrator
  reauthorization on UPDATE, without reapplying creation defaults. Mutation uses
  `reinvocationPolicy: Never`; both webhooks share the native cleanup exclusion.
- Shared normalization and compatibility validation, including every referencing
  Team on Sandbox/policy edits, and reference checks before shared deletion.
  Ordinary Portal Team context edits can preserve unchanged governance.
  Deleting an ungoverned Team retains the existing RBAC boundary, even with
  member Agents. Removing governance from, or deleting, a governed Team requires
  rebinding its members to another governed Team or deleting them first.
  Members inherit governance even without an explicit Agent `sandboxRef`;
  clearing the Team fields must not release those Agents into legacy execution.
- Protected lifecycle finalizers. Only the configured controller identity may
  change known lifecycle finalizers in a metadata-only update; it has no blanket
  exemption for spec changes. Removal also requires an already-deleting Agent.
  Kubernetes' exact authenticated controller-manager or generic
  garbage-collector username may remove only the built-in `foregroundDeletion`
  finalizer from an already terminating object in an otherwise metadata-only
  update.
  The legacy controller's chart-version and Harness image/port pinning writes
  remain behind the execution hold. A future governed path must authorize those
  specific writes or move them into authorized creation before releasing it.

There are **no production capability registrations**. Standalone Sandbox and
AgentPolicy definitions can be validated, but governed Team/Agent bindings are
rejected without a qualified registration. The tests inject synthetic
registrations; enabling this process does not make those production evidence
or release the controller hold.

Protected rendering, final-Pod checks and policy compilation are connected by
an opt-in **held controller preparation** path, described below. It does not serialize
concurrent shared-configuration changes, coordinate rollout/start barriers,
produce enforcement observations, or permit governed execution. Reference reads
are checks against current objects, not an atomic cross-resource transaction.
Those boundaries must be completed before production adoption.

#### Trusted Team assignments and explicit reauthorization

Configure assignments administratively, using the Kubernetes authenticated
identity rather than an Agent field or a Portal role:

```yaml
sandboxAdmission:
  teamAssignments:
    - team: research
      users: ["member@example.com"]
      groups: ["research-members"]
```

Every matching user/group must identify the same Team; conflicting memberships
reject instead of picking a precedence order. Assignments do not grant Kubernetes
RBAC permissions or access to the `admin` workload policy class. An unassigned
caller cannot create or update a governed member Agent. Do not map a shared
Portal ServiceAccount as if it identified the individual human caller.

`TeamAssignmentConflict` means the authenticated user's effective memberships
match more than one configured Team. Administrators must review both the
assignment configuration and identity-provider memberships; there is no
user-over-group or first-match precedence. Static configuration validation cannot
discover overlapping live memberships. The diagnostic does not disclose users,
groups or matched Team names.

For recovery after reference deletion or replacement, restore the required
configuration or explicitly select valid replacement references, then authorize
the exact current UIDs with a fresh administrator request. The Agent UID and
execution hold are retained, including when several references change together.
Missing references are not grounds for clearing authorization or falling back
to legacy execution. Consumed reauthorization requests remain replay state;
do not strip either protected annotation as ordinary metadata cleanup.

Server-side apply owns individual annotation keys. Desired manifests should omit
the admission-owned authorization and reauthorization keys, rather than copying
them from a live-object export. Repeated applies and removal of ordinary,
application-owned annotations preserve governance. If a field manager has
already claimed protected keys, an administrator can establish another manager's
ownership of their exact unchanged values before the original manager omits
them. This is an ownership handoff, not authorization removal; forcing apply
cannot bypass admission, and changing a binding still requires the explicit
reauthorization protocol below.

The controller persists hold and cleanup finalizers using optimistic,
metadata-only patches. These preserve sparse server-side-applied specifications
without a typed full-object rewrite. Finalization still requires the exact
controller and narrow lifecycle transition; it does not exempt specification,
status or protected-annotation changes.

Admission stamps `cetirun.microsoft.com/sandbox-authorization` during creation.
Callers cannot supply, copy, remove or directly rewrite that record. Existing
unrecorded governed Agents and changed reference UIDs require an administrator
to update `cetirun.microsoft.com/sandbox-reauthorize` with a fresh `requestID`
and the exact current `binding` tuple: Agent, Team, Sandbox and AgentPolicy UIDs,
plus policy class. The mutator validates that request and replaces the snapshot
atomically with the Agent update. Reauthorization changes metadata, not
necessarily generation; consumers must not rely only on generation changes.
`sandboxbinding.Read` reads the persisted authorization through an uncached
reader; the shared resolver must then compare it with fresh references.

#### Protected preparation, controls and status

The preparation chain is `sandboxbinding.Read` -> `sandbox.Resolve` ->
`sandboxpolicy.Compile` -> `sandboxworkload.ControlsFromPlan`/`Build` ->
`helm.PrepareSandboxChart`. The existing chart/renderer prepares OpenClaw and
Hermes; it does not introduce another runtime renderer. Generic user values are
validated before merging, and the protected bundle is applied last. The
`sandboxWorkload` chart values are compiler-owned outputs, not a user activation
switch. Governed Custom rendering is explicitly unsupported by this slice.

Prepared manifests protect resource accounting (including init/helper containers
and RuntimeClass overhead), runtime/security settings, owned HOME, read-only
skills, scratch mounts and identity projections. Existing HOME and skills claims
must match their trusted bindings. An incompatible storage or StatefulSet
transition fails explicitly; no live writer-exclusion or data-adoption guarantee
is inferred from rendering. Prepared Sandbox releases remain at zero replicas.
The generic Helm install/upgrade path still refuses them; the separate guarded
`StageSandbox` operation can apply only the sealed, held manifest through Helm.
It never starts Pods, reruns source templates, runs hooks or creates claims.
Native ConfigMap/Service ownership is included before sealing, alongside the
StatefulSet and compiler resources.

Retired resources are excluded from Helm's implicit pruning; grant removal
remains an identity-checked, journalled operation. Live updates must enforce
UID/resourceVersion conditions and repair custom-resource drift rather than
comparing only the old and new release manifests. Staging verifies the live
resources against the sealed manifest, allowing only supported API defaults,
before accepting a deployed release as prepared.

##### Held controller integration

[`AgentReconciler.SandboxPreparation`](internal/controller/sandbox_preparation.go)
connects the components without treating a successful render as enforcement.
This is an internal platform integration seam, **not a tenant activation flag**.
By default, the operator installs neither a preparation source nor qualified
production registrations. With no source, governed Agents retain their existing
execution hold. The opt-in Kubernetes sources below wire authoritative reads of
platform configuration and existing owned resources into the shipped binary.
They do not supply a qualified writer-exclusion implementation, so held staging
still cannot complete through stock startup. Tests inject that separate
dependency; their results are not platform qualification or a permit to start.
An operator namespace alone does not enable preparation or routine state reads
for untracked Agents. Existing preparation markers still require cleanup when
a provider is disabled. Deletion also probes state while the hold finalizer
remains, so interrupted finalization is not lost after the preparation marker
has already been released.

A trusted integration must provide:

- The same fresh `SandboxConfigurationSource` used by the Sandbox and
  AgentPolicy configuration reconcilers.
- `SandboxPreparationSource` approvals for the complete Agent generation,
  chart version, helpers, protected namespace UID, existing HOME/skills PVC
  UIDs and existing runtime Secret UID. Namespace/storage/model provisioning,
  controller write permissions and serving admission are platform prerequisites;
  the controller does not discover or adopt them by name alone.
- A qualified `CheckQuiesced` implementation that confirms previous writers
  have terminated or are durably fenced, including unreachable nodes. It must
  support cleanup using the persisted prior namespace/HOME tuple after
  suspension, reference deletion and restart. A Kubernetes scale request or an
  empty Pod list is not this evidence.
- A held Helm installer with authoritative, read-only release-store inspection,
  an explicit protected state namespace, and the
  dedicated admission ServiceAccount. There is no trusted-source fallback to
  Agent status or arbitrary tenant ConfigMaps.

The controller resolves every referenced Team slot and verifies current
runtime/storage/grant approvals. New or changed preparation first confirms
qualified quiescence and Pod absence, then compiles controls and renders the
native chart. A failed preflight creates no preparation receipt, grant journal
or Agent preparation finalizer. Existing preparation state still requires its
qualified cleanup fence. The controller persists ownership and write-ahead
grant inventory before any grants or workloads are applied. Old grants are
revoked and observed absent; fresh source checks, qualified quiescence and Pod
absence still guard held staging.
Source-read RBAC gives admission only `get` on the named workload Secrets and
ConfigMaps, not blanket namespace Secret reads.

After staging, the controller records binding-object UIDs and an
`ExecutionHeld=true` Pod expectation in the operator namespace. Source
UID/resourceVersion snapshots include the Team, referenced configurations,
RuntimeClass/StorageClass, namespace, claims, Pod sources and compiled controls.
The Agent instead pins UID, generation, and a digest of its spec, protected
authorization/reauthorization metadata and lifecycle holds. Status writes and
unrelated labels/annotations do not invalidate it; metadata-only reauthorization
still does. This is expectation format `v2`, separate from the unchanged render
contract. Old expectations fail closed until the controller rebuilds them;
upgrade controller and admission together, without editing stored JSON or
lifting holds to bypass version checks.

Successful attempts refresh every 15 seconds, so source changes, prerequisite
expiry and explicit reauthorization are not missed. An unchanged attempt uses
a controller-owned digest of approved inputs, rendering values, confirmed grant
inventory, the expectation, native Service identity and deployed Helm record.
It rechecks source identities, qualified fencing and Pod absence, but does not
render, stage, rewrite journals or republish expectations. Changing any of
these inputs returns to the guarded preparation path; missing/corrupt durable
inventory or unavailable authority remains an error. All mutation guards in
the controller and Helm action remain in place.

The ordinary/admin fake-client regression fixtures enforce a bounded read-only
reuse path with zero writes, one release-store inspection and one fence check.
These are regression budgets, not production throughput measurements; providers
and release storage may perform additional requests. Current grant-journal and
observed ControllerRevision identities are included in the source reads.
Inventories remain bounded
to 128 sources and 512 KiB per expectation, with a one-Pod existence query.
No production fleet size is qualified. Activation review must establish a
measured capacity limit and evaluate additional dependency watches alongside
the existing Agent/Team/Sandbox/AgentPolicy watches and expiry polling.

`AgentConfigReady=True` with reason `SandboxPrepared` means **held preparation
only**: `SandboxReady` and `Ready` remain false, the access URL is empty, and
applied-binding history is not advanced. Success is published only after the
held expectation persists and its source versions are checked again.

##### Current held start-input pins

The [expectation publisher](internal/controller/sandbox_preparation_sources.go)
adds only identities needed to validate a prospective start: the UID and
resourceVersion of the validated grant journal, and the owned ControllerRevision
once the StatefulSet controller has observed the current generation. The
revision's actual recorded template must match the sealed native template,
including the Kubernetes template-replacement patch form. A stale generation
does not acquire a current revision pin. Both operator and admission receive
read-only `get` access to ControllerRevisions.

These references use the existing `v2` expectation source format; they are not
another intent journal or an execution permit. Valid older held expectations
without the pins are rebuilt through guarded preparation. Older controllers
may omit them, so a future start action must explicitly require the complete
current identities rather than infer qualification from format version alone.
No preparation-receipt upgrade, attempt history, new finalizer or GC lifecycle
is introduced. The existing `v1` cleanup receipt retains its 64 KiB bound and
historical namespace/HOME tuple; unsupported receipt formats still fail closed.
The current expectation retains the existing 128-source/512-KiB bounds and
Agent owner reference, and is subject to the existing fenced cleanup and GC.

**Controlled start is not implemented.** Both execution holds and zero-replica
staging remain. Stock startup cannot reach successful preparation with its
unavailable writer-exclusion provider. Synthetic API tests do not demonstrate
production reachability, runtime enforcement or a qualified fence.
Actual start requires separately approved start/change exclusion, lifetime
pool isolation, qualified old-writer fencing, a distinct pre-start enforcement
acknowledgement and trusted current Pod/node/epoch observations. Neither a
held expectation nor pool deletion alone supplies those authorities.

Tailscale, Teams, workload identity and governed Custom configurations remain
unsupported by this native path. They retain an explanatory `UnsupportedControl`
hold and bounded requeue instead of a permanent preparation-error loop. Existing
grants are revoked if a previously prepared Agent transitions to one of them.
Unqualified platform controls also retain the bounded `UnsupportedControl`
hold; unavailable adapters, missing inventory and API read failures remain
preparation errors rather than being reported as unsupported.
An unsupported result does not suppress source, cleanup or status-write failures.

If a recorded binding disappears after preparation, the controller
authoritatively confirms its absence and durably retires its old journal entry
before recording fresh write-ahead intent and creating a replacement. The same
check covers admission source-read bindings, namespaced workload RoleBindings
and workload ClusterRoleBindings, including deletion during Helm preflight.
Staging guards recheck confirmed grant identities; unrelated same-name bindings
are not adopted, overwritten or revoked, and healthy bindings are not retired.
Read errors, journal conflicts and unconfirmed retirement prevent replacement
creation. New UIDs are confirmed for reuse and later revocation; interrupted
retirement or creation resumes through the normal write-ahead preparation path.

Partial writes, stale sources, suspension and provider disablement revoke
recorded grants while retaining the hold and recovery state. Deletion revokes
access first, waits for qualified writer fencing and all Pods (including
terminating Pods), and only then removes the owned namespace. The controller
releases preparation state and finalizers after namespace absence. A missing or
corrupt journal, foreign ownership, an unavailable fence, or a conflicting
state update blocks completion rather than implying successful cleanup.

For deleting Sandbox-held native Agents and legacy Custom Agents, a grant-journal
read or revocation failure does not skip independent workload stop requests,
Custom terminal shutdown, owned-route removal, or attempts to clear readiness
and the access URL. Status-write failures likewise do not skip revocation or
shutdown. Errors from these independent operations are joined and block registry
credential, namespace/storage cleanup and finalizer release until retry succeeds.

Both deletion paths also remove retained managed registry credentials after
revocation/shutdown requests and before namespace cleanup or finalizer release.
Registry cleanup failures retain deletion state and a bounded retry; they do not
skip shutdown or bypass the Sandbox writer-fencing requirement.

Cleanup can resume after interrupted journal-finalizer release only when the
remaining record is valid, owned and empty. This cleanup-only recovery neither
authorizes new grants nor accepts unprotected nonempty inventory. Foreign
finalizers are preserved, including when they retain a terminating empty journal.

Local tests exercise this chain with synthetic platform providers and an
isolated API server:

```bash
go test ./internal/controller -run TestSandboxPreparation -count=1
KUBEBUILDER_ASSETS=/path/to/preinstalled/envtest make test-sandbox-integration
```

The held-input cases exercise real API-assigned identities, expectation CAS
conflicts, grant-journal races, restart recovery, both native runtimes and
default/explicit qualified images, and HTTPS Pod admission denial.
The ControllerRevision producer and platform approvals are synthetic.
These tests do not run agent containers or establish runtime, Cilium, CSI,
cloud identity or writer-fence qualification. Storage/identity handoffs,
retired non-binding controls, shared-configuration reservations, authorized
starts, Pod/node/epoch observations and route publication still require their
qualified lifecycle integrations. Pending or incompatible Helm releases fail
closed for coordinated recovery rather than being adopted or force-unlocked.

##### Kubernetes-backed sources (opt-in)

`sandboxSources.enabled` in the operator chart selects capability declarations
for configuration admission as well as sources for held native preparation.
It serializes `CETIRUN_SANDBOX_SOURCES` for the controller and derives both
`platformNamespace` and `platformConfigMapName` in the admission process's
`CETIRUN_SANDBOX_ADMISSION` from the same chart `namespace` and
`sandboxSources.platformConfigMapName`. There are no separate admission-source
Helm values. When sources are disabled, both admission fields and the
controller's source setting are omitted.
This option requires configuration and Pod admission to be
enabled and enforced (`sandboxAdmission.enabled`, `enforce`, and `podsEnabled`).
The admission ServiceAccount is fixed to the separately deployed
`cetirun-sandbox-admission`; it is not the controller ServiceAccount.
Admission reuses its existing operator-namespace ConfigMap `get` Role and
Pod-admission RuntimeClass/StorageClass `get` permissions. Sources alone add no
RBAC grants. The optional skills publication catalog described below adds only
PersistentVolume `get` for its metadata dependencies, not write permissions,
Secret lists or cluster-wide ConfigMap access.
The default-off chart output is unchanged.

```yaml
sandboxSources:
  enabled: true
  bootstrapOwnedPrerequisites: false
  platformConfigMapName: sandbox-platform
  homeClaimName: home
  skillsClaimName: skills
  skillsCatalogConfigMapName: ""
  helpers:
    doctor: false
    logger: false
    desktop: false
```

These are operator settings, never Agent or Team Helm overrides. The constructor
uses the selected operator namespace and chart version. The
[configuration source](internal/controller/sandbox_kubernetes_configuration.go)
and configuration admission read the same `platform.json` from the named,
pre-existing ConfigMap in that namespace.
Its `v1` document contains `registrations`, `platforms`, `runtimeClasses` and
`storageClasses`. It reuses the existing inner contract types and their
case-sensitive JSON fields: registrations use `Capabilities`/`Available`,
RuntimeClass pins use `UID`/`Handler`, and StorageClass pins use
`UID`/`Provisioner`. Capability and platform objects retain their existing
lower-camel field names. RuntimeClass and StorageClass declarations pin their
UIDs and handler/provisioner; role approvals pin role identity, version and rules.
Live role checks are scoped to the grants requested by each policy during
assessment and workload preparation, including mutation guards. A missing,
stale, unavailable or ambiguously approved role rejects that request without
invalidating the shared snapshot or unrelated policies, including policies
without Kubernetes access. Different policies may hold different approval pins
for the same role; each request must match its own pins.
Replacement, concurrent change, malformed configuration and API failures are
errors, not automatic reapproval or a fallback to cached tenant data.
The document is bounded to 512 KiB, 16,384 JSON tokens, 12 nesting levels and
128 distinct API objects including the ConfigMap. Unknown/duplicate fields,
nulls and incomplete object pins are rejected.

Each relevant configuration-admission request uses current authoritative reads;
there is no cross-request inventory cache or fallback to earlier declarations.
Missing, inaccessible, malformed or drifted platform inventory blocks governed
configuration writes that require capability checks. This source dependency
does not apply to configuration `/status`, Agent deletion, narrowly authorized
controller cleanup, authorized suspension-only updates or unrelated legacy
writes. Suspension must change only `spec.suspended` from false (or omitted) to
true; all other spec fields, authorization records and caller-controlled
metadata must remain unchanged. Only server-managed generation and field
ownership changes are ignored. Existing caller-authority and lifecycle checks
still apply. Creation, resume, binding changes and reauthorization still require
valid inventory. This does not change webhook-outage behavior:
other matched writes, including legacy writes, still fail closed when admission
itself is unavailable.

The platform owner must protect this ConfigMap from tenant writes and maintain
its qualification records, complete topology and explicit approvals. The chart
does not create that inventory or set capabilities to true. An empty inventory
can be represented without claiming any platform support:

```json
{
  "version": "v1",
  "registrations": {},
  "platforms": {},
  "runtimeClasses": {},
  "storageClasses": {}
}
```

The [prerequisite source](internal/controller/sandbox_kubernetes_prerequisites.go)
selects the protected Agent namespace and configured existing HOME/skills
claims, verifies their ownership and current identities, and reads only
metadata for the Agent's `gatewayTokenSecretRef`. It never provisions or adopts
storage by name, generates model credentials, or reads Secret contents.
Supply these resources through the trusted platform, or opt in to the limited
owned bootstrap described below. The reader itself is not a storage migration
or credential-issuance mechanism.

The namespace needs its existing protected label, matching Agent-name/UID
annotations and exact controller owner reference. Retained HOME without an
owner reference is supported only with the current Agent UID annotation and
the selected Sandbox's exact filesystem storage profile. Skills can retain
their supported shared/RWX shape, with no owner or the current Agent/Team owner.
The runtime Secret must be Agent-owned; its contents, usability and immutability
are not inferred from metadata. Pending claims are allowed for delayed-binding
storage classes, without being mistaken for writer-exclusion evidence.

Both sources return revisions bound to their checked inputs and reject changes
during capture. Configuration observations are dated at the start of the read;
consumers check freshness after it completes and reject observations that expire
during capture. The same platform declarations are used by configuration
admission and the Agent, Sandbox and AgentPolicy reconcilers. They do not grant
Portal callers Kubernetes admission authority or replace trusted Team
assignments. A configured source does not establish runtime, network or CSI
enforcement. In particular, the stock prerequisite source has
no writer-exclusion adapter and returns an explicit error before preparation
creates its qualified-preparation cleanup state. Bootstrap-only ownership state
is separate and does not claim a writer fence. An Agent rejected by this
preflight remains deletable, including after sources are disabled; existing
preparation state never bypasses fencing.
Kubernetes Pod absence, PVC access modes and zero replicas cannot replace it.
Qualified fencing, successful held staging, controlled starts and running
enforcement observations remain separate lifecycle work. Do not remove holds
or finalizers to bypass these missing guarantees.

##### Owned bootstrap (separate opt-in)

With `sandboxSources.bootstrapOwnedPrerequisites: true`, new native governed
Agents can create the prerequisites they own while remaining held. This flag
requires enabled Kubernetes sources and the same enforced configuration/Pod
admission prerequisites; it defaults to false. Existing namespace inputs stay
on the read-only path, without adoption or mutation.

The Agent must explicitly specify the configured approved `spec.helm.version`,
native `spec.agent.image` and `spec.agent.port`. Bootstrap does not write these
fields or add a controller admission exemption. After current binding/platform
checks, it records ownership intent, then creates the protected Agent namespace,
the existing operator Role/RoleBinding, HOME matching the selected Sandbox, and
the native authentication Secret named by `gatewayTokenSecretRef`. It reuses the
ordinary OpenClaw/Hermes key initialization; existing credentials are neither
read nor enriched by bootstrap. No cluster-wide RBAC grants are added.

Bootstrap does **not** create an empty replacement for published skills,
model-provider credentials, Pods, routes or execution permits. By default the
platform must supply the configured, distinct skills claim in the newly created
namespace, with the supported Agent/Team or retained ownership. Missing skills
remain an explicit prerequisite error. Alternatively, the separately selected
publication catalog below can create static bindings to already-published
content. In either mode the stock binary still stops at unavailable qualified
writer exclusion. The native authentication Secret is not evidence that a
model provider has issued usable credentials.

A bounded controller-owned receipt records each created UID. Each object is
marked with that receipt's UID so an interrupted create can be recognized;
confirmed missing/replaced objects, storage resize, foreign objects and changed
ownership require recovery instead of automatic recreation. Changing the native
type, Secret name or approved chart likewise requires an explicit handoff.
Every bootstrap-owned resource has its own finalizer: Agent finalizers and
`blockOwnerDeletion` do not prevent foreground garbage collection from deleting
dependents, and a namespace finalizer alone does not retain its contents.
Dependent DELETEs can mark HOME, native authentication and operator RBAC as
terminating, but their bootstrap finalizers retain the objects until independent
shutdown, receipt/ownership validation and any existing preparation fencing
obligation succeed. Cleanup removes only its own finalizer with resourceVersion
checks, releases HOME/credentials before RBAC, and releases namespace protection
last. Interrupted cleanup retries without recreating resources or clearing
another controller's finalizers.
Bootstrap-only Agents can be deleted without a fence that has never been used
to authorize preparation. Disabling bootstrap does not discard its inventory or
allow recorded inputs to bypass ownership checks; deletion remains available.

Tests cover cold bootstrap, lost API responses, source/UID changes, immutable
HOME, credential-preserving retries and cleanup using synthetic fixtures and
an isolated API server. Foreground-GC regressions explicitly issue dependent
DELETEs because these test environments do not run a garbage collector.
They do not qualify runtime/storage/network behavior.

###### Published Blob NFS skills catalog (separate opt-in)

Set `sandboxSources.skillsCatalogConfigMapName` to a **separate, pre-existing
ConfigMap in the operator namespace**, and enable both sources and owned
bootstrap. The default is empty. This setting changes neither `platform.json`
nor the configuration-admission wire contract. The operator never creates or
updates the catalog, publishes source content, contacts Azure, imports caller
PVs/Secrets or resolves storage credentials. `Team.spec.skillStorage`, Agent
Helm values and namespace-local ConfigMaps are not publication authority.
Protect catalog writes using operator-namespace RBAC.

For example, this synthetic catalog binds a publication to one exact Team
name and UID; replace the example UID with the intended Team object's UID:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sandbox-published-skills
  namespace: cetirun-system
data:
  skills.json: |
    {
      "version": "v1",
      "teams": {
        "example-team": {
          "teamUID": "00000000-0000-4000-8000-000000000001",
          "revision": "published-v1",
          "source": {
            "resourceGroup": "skills-example",
            "storageAccount": "skillsexample123",
            "containerName": "skills-v1",
            "storageClassName": "azureblob-nfs-premium",
            "storageSize": "1Gi"
          }
        }
      }
    }
```

The ConfigMap must contain only `data["skills.json"]`, with no binary data.
The case-sensitive `v1` document requires `teams`, which may be empty, and all
shown fields for each entry. It is bounded to 64 KiB, 128 Teams, 8,192 JSON tokens
and five nesting levels. Unknown/duplicate fields, nulls, malformed descriptors,
Secret references, protocol overrides and mount options are rejected. Missing
approval for the current Team name **and UID** is a prerequisite error before
cold-create effects. A same-name replacement Team does not inherit approval.
The publisher must keep content stable for the selected revision and publish
changes under a new approval/source; the operator does not inspect or hash NFS
contents. The revision is an approval identifier, not storage qualification.

New catalog-backed bootstrap uses a `v2` write-ahead receipt containing the
catalog UID, selected Team/publication and both binding names. It creates the
Agent-owned skills PVC with an explicit `volumeName`, records its API UID, then
creates a deterministic per-Agent PV with that exact PVC UID in `claimRef`.
This is static Blob NFS binding, never an empty dynamically provisioned claim.
It follows the existing shared-skills RWX shape while enforcing CSI `readOnly`,
the `ro` mount option, read-only native workload mounts and PV reclaim policy
`Retain`. HOME remains a different claim with its own authorized profile.
No shared source object or backing data is modified.

Each returned object UID is recorded before the next effect. Interrupted creates
can resume only for the exact receipt-marked intent; foreign objects, replacement
UIDs and immutable spec drift require recovery. The selected catalog UID,
Team UID, revision and source cannot change in place. Unrelated catalog edits
are allowed, but every attempt guards the freshly read resourceVersion.
Removing/changing the selected approval or disabling its catalog holds further
preparation and requires recovery, not automatic migration or fallback.
Existing namespaces and older `v1` bootstrap receipts retain their externally
provided skills mode; enabling a catalog does not adopt or migrate them.

Held preparation includes the catalog, bootstrap receipt and PV identities in
its checked admission source inventory. With the catalog enabled, the admission
chart adds only cluster-scoped PersistentVolume `get`; no list, write or Secret
permissions are added. Catalog/receipt metadata use its existing
operator-namespace ConfigMap `get` Role, and namespace-local runtime source
grants remain name-scoped.

Deletion uses the durable receipt without requiring the live catalog. Existing
shutdown, ownership and any qualified-preparation fencing gates still apply.
Skills bindings carry the bootstrap finalizer through foreground deletion.
The controller releases the PVC with namespace cleanup, then deletes only the
recorded `Retain` PV after namespace and PVC absence, using UID/resourceVersion
preconditions. It removes only its own finalizer and retains Agent/receipt
protection until PV absence is observed. Other controller finalizers and the
published backing data are preserved. This is not a source-content deletion or
a cloud fencing implementation.

Launch image/chart/port inputs remain explicit; no controller spec-write
exemption is introduced. Portal namespace precreation and identity delegation
remain separate work. Catalog bindings, pending PVCs and successful held
rendering do not qualify CSI/NFS behavior, provider credentials, execution
starts or running enforcement.

**Manual recovery after forced finalizer removal.** Do not force-remove Agent
or state finalizers as routine cleanup. If an Agent disappears outside the
normal lifecycle, its journal, bootstrap and preparation receipts deliberately
retain their finalizers and may block state-namespace deletion. There is no automatic
orphan reaper. A separately authorized recovery must:

1. Preserve and strictly validate the original Agent UID, journal/receipt
   identities, complete grant inventory and recorded namespace/HOME tuple.
   Missing or corrupt evidence is not permission to substitute empty state.
2. Compare any current same-name Agent by UID. A replacement is unrelated and
   must not be adopted or have its resources removed. A matching terminating
   Agent should use normal controller cleanup.
3. Quiesce in-flight grant writers and obtain the platform's qualified fence
   for the original writers. Agent absence, operator shutdown, scale-to-zero
   and an empty Pod list are not sufficient evidence.
4. Revoke every recorded binding, including obsolete entries, after checking
   owner UID, role, subject and revision. Pin the actual object UID/version when
   an inventory entry predates UID confirmation. Use deletion preconditions
   and authoritative absence checks; terminating or replaced bindings remain
   unresolved. Discovery supplements but never replaces durable inventory.
5. Persist only confirmed removals with resourceVersion checks. After empty
   inventory, continued fencing and original namespace/lifecycle cleanup are
   confirmed, release only the journal's own finalizer and confirm deletion.
   Handle the receipt separately under its original identity; preserve foreign
   finalizers. Do not finalize the entire operator namespace to discard state.

This procedure is not a tested live recovery tool. Shared-environment activation
needs a reviewed and qualified orphan-recovery mechanism.

When the desktop helper is enabled, it uses a separate `tmp-desktop` emptyDir
for `/tmp`; the Agent retains `tmp`. Both volumes carry the Sandbox scratch
size limit, and the existing container/Pod resource accounting is unchanged.
This keeps their Supervisor configuration, socket and PID files independent.
The protected inventory rejects shared, missing, unbounded or modified scratch
mounts/volumes. Disabling desktop omits its scratch volume.

#### Trusted Sandbox policy transition logs

The operator's `sandbox-policy` logger emits structured
`sandbox.policy.accepted`, `sandbox.policy.rejected`,
`sandbox.policy.applied`, `sandbox.enforcement.failed`, and
`sandbox.enforcement.lost` events after successful optimistic status
persistence. These are operator logs, not Agent stdout or Kubernetes Event
mirrors. Configuration acceptance does not establish Agent enforcement;
rejection can include unavailable prerequisites, distinguished by
`conditionStatus=Unknown`.

Preparation errors use the shared typed failure classifier before status
persistence. Missing or drifted approved roles therefore emit a committed
failure rather than an ordinary pending hold. Unavailable trusted dependencies
set `SandboxReady=Unknown` with reason `ObservationUnavailable`; `Ready` and
`AgentConfigReady` remain false. Ordinary pending/prepared holds stay silent.
Error propagation, grant revocation and explicit reapproval requirements are
unchanged.

Events include a UTC timestamp, bounded reason, resource UID and generation,
condition status, stable `transitionID`, and separate
`attemptedBinding`/`attemptedRevision` and
`appliedBinding`/`appliedRevision` fields. Attempted Agent identities come from
admission-owned authorization metadata, not applied history. Unresolved fields
are explicitly `unresolved`. Configuration assessments and failed status paths
have no verified attempt revision at these hooks and report it as unresolved
rather than reuse the previous applied revision. Loss reports prior applied
history separately from the current attempt.

Canonical Kubernetes UUIDs and compiled SHA-256 revisions are retained.
Other opaque identity or revision values are represented by SHA-256
fingerprints. Event fields exclude raw specifications, Secret data,
free-form status messages, provider errors and conversation content.

Application events require committed, current-generation verified applied
status with matching authorization. Held preparation is not application.
Intentional suspension and deletion are not enforcement failures, including
when a previous failure condition remains. These logs do not add observation,
fencing or permission to start execution.

Unchanged semantic status does not repeatedly emit events. Correlation IDs
exclude timestamps, resource versions and free-form content; they are not
unique delivery receipts. Status persistence and log delivery are not atomic,
so exactly-once delivery across crashes is not guaranteed.

Protected Pods use `DNSPolicy=None` with exactly the compiler's one to three
distinct resolver IPs. No fallback resolvers, search suffixes or DNS options are
added. User overrides and final-Pod DNS changes reject rather than silently
changing the trusted network binding.

The candidate Azure CNI Overlay/Cilium compiler emits explicit network controls
and per-Agent identity resources from trusted topology and grant inventories.
Gateway endpoints/ports come from that inventory, not hardcoded defaults or
policy-supplied destinations. Conflicting required destinations reject. FQDN
blocking and mTLS remain unsupported and reject rather than becoming no-ops.
The effective compiled revision includes topology, identity and role-content
changes; use it rather than only the base resolver revision.

The trusted topology must explicitly supply `DNSResolverIPs`, the one to three
distinct Pod resolver addresses, separately from `DNS` peer membership.
Resolvers must belong to approved DNS peers and use qualified IP families.
Missing or unapproved resolvers reject; the compiler does not infer them from
backend addresses or truncate the inventory. DNS peers retain all Service and
backend IPs for network allowances and DNS deny exemptions. Adding backend
replicas therefore changes the enforcement revision, not the Pod's nameservers.

The current egress posture permits public IP destinations except explicit
exclusions, while restricting private, cluster, node, metadata and management
ranges and qualifying required DNS/Gateway destinations. It is not a
public-destination allow-list. Cilium default-deny establishes isolation for
unmatched traffic; it does not remove the compiler's explicit public-egress
rules. Whole-address-space exclusions fail compilation rather than producing
an empty allow rule. Cilium selectors use source-qualified keys such as `k8s:`;
their JSON shape is not a Kubernetes API-machinery label-selector contract.

Network compilation has fixed budgets: 1,024 input entries, 32,768 partition
work units, 2,048 rules, 16,384 expanded entries and 512 KiB for the complete
serialized Cilium policy, including ownership/revision metadata. The
[budget accounting](internal/sandboxpolicy/limits.go) bounds work before
expansion as well as final output size. Exceeding a limit rejects the plan;
entries are never silently truncated to obtain a successful result.
Independently of those budgets, emitted allow and deny groups contain at most
40 entries per `toPorts[].ports` array to match Cilium's validation limits.
Grouping happens after port sets are combined and complementary deny ranges
are computed, including overlapping internal CIDRs and merged managed-peer
destinations. It preserves the complete allowed and denied port/protocol sets,
including fragmented ranges; no entries are truncated.

Ordinary workloads keep the default ServiceAccount without token automount.
Approved Kubernetes API and Entra identities use separate audience-bound token
projections; Entra-only identity does not implicitly authorize Kubernetes access.
The compiler does not provision or revoke cloud identities. RBAC revocation
helpers require authoritative UID/ownership checks and confirmed absence, and
preserve shared roles/accounts. The held controller uses
[`GrantJournalStore`](internal/sandboxpolicy/journal.go) to persist both workload
and admission-source bindings before writes, record their UIDs, and integrate
retryable revocation into hold, suspension and deletion paths. Its protected
ConfigMap survives foreground owner deletion, uses resourceVersion CAS, and is
bounded to 1,024 references and 512 KiB. Discovering visible bindings is never
treated as complete cleanup inventory. This does not provide cloud-identity
revocation or authorize execution. Recording unchanged canonical inventory uses
one authoritative read and no write or confirming read; real changes retain the
write-and-confirm path and never discard an already recorded binding UID.

Each approved grant must carry the live ClusterRole UID, resource version and
normalized rules; the approval's `Revision` is a separate administrative
revision, not a substitute for an API observation. Configuration assessment
calls [`VerifyPolicyGrants`](internal/sandboxpolicy/grants.go) through the
authoritative reader for only the requesting policy's grants. Held preparation,
its mutation guards and future workload application boundaries must also verify
immediately before consuming requested grants,
invalidate changed observations and coordinate revocation on drift. The
compiler remains a pure transformation and cannot detect a later role edit.
Explicitly approved wildcard or Secret access is not silently rewritten.
The operator gains only `get` access to ClusterRoles for this check, not
role editing, binding or escalation authority.

The operator registers Sandbox and AgentPolicy configuration reconcilers.
Sandbox `Ready` describes prerequisites; AgentPolicy `Accepted` describes
configuration validity/support. Without a qualified platform snapshot source,
these conditions remain `Unknown`, not ready. Indexed configuration watches
requeue referencing Agents, including pending bindings. Per-Agent status helpers
require matching current binding/revision/Pod/node/epoch and fresh guest-external
observation, preserve applied history on failure, and do not release the hold.
`Accepted` does not establish compatibility with a particular Sandbox or a
ready-to-run workload. Reconciliation uses the cache for its primary
configuration object, but authoritative reads for prerequisites and grants.
Successful refreshes are jittered between 50% and 62.5% of the remaining
snapshot freshness window; failures retain controller backoff. No production
snapshot source is installed by these helpers.

#### Optional final-Pod admission

With `sandboxAdmission.podsEnabled=true`, the server registers
`/validate-sandbox-pods` and reads expectation ConfigMaps in the operator
namespace. Deploy this server setting before enabling enforcement. Once
`enforce=true`, the chart also installs final-Pod matching and native namespace
protection. Configuration admission must remain enforced for the UID records
used by Pod admission to be authoritative.

Only the exact configured controller may initialize a protected namespace with
`cetirun.microsoft.com/sandbox-protected: "true"`, the matching `agent-name` and
`agent-uid` annotations under `cetirun.microsoft.com/`, and one canonical Agent
owner reference. The marker, identity and ownership become immutable, including
for that controller and namespace status/finalize updates. The operator/state
namespace cannot receive the marker. Native validation does not depend on the
webhook being available.

Matching uses marker presence, not mutable Pod labels. Each request rechecks the
live namespace/Agent ownership, durable authorization, exact controller-owned
expectation, source UIDs/resource versions, StatefulSet revision/template, storage
and RuntimeClass. Missing/stale state, suspension or either execution hold denies
the request. All container, init-container, ephemeral-container and resize paths
are checked. Pod DELETE and `/status` remain outside the webhook; unmarked
platform/repair namespaces remain outside its matching.

The exact Kubernetes garbage-collector identities `system:kube-controller-manager`
and `system:serviceaccount:kube-system:generic-garbage-collector` may remove only
`foregroundDeletion` from an already terminating Pod. This narrow update is
excluded by webhook matching and handled without controller-state reads, so
missing/stale expectations, execution holds and webhook outages do not prevent
finalization. All other Pod fields and finalizers must remain unchanged, apart
from server-managed `managedFields`. This does not grant either identity a
general admission exemption.

CONNECT requests for `pods/exec`, `pods/attach` and `pods/portforward` are denied
in protected namespaces, independently of expectations and without an
administrator or controller exemption. The protected runtime chart does not
install the legacy per-release exec policy; the namespace-scoped admission
guard replaces it. Kubernetes RBAC must also restrict access to these
subresources. This guard does not change access to unmarked workloads.

Protected namespaces must be excluded from unrelated Pod mutators, including
sidecar/identity injectors and metadata-rewriting policies. Validation compares
the complete specification and protected metadata after supported API defaults;
unknown annotations and labels are not harmless by definition. For a mutating
webhook, combine its existing namespace requirements with this exclusion:

```yaml
namespaceSelector:
  matchExpressions:
    - key: cetirun.microsoft.com/sandbox-protected
      operator: DoesNotExist
```

Before enforcement, inspect the actual matching rules, namespace/object
selectors and match conditions of every relevant mutator, including native
mutation policies. Verify supported templates through the API-server request
path in the target environment. The presence of an unrelated webhook alone is
not a conflict, and the Istio opt-out is not a general mutation exemption.
API-server round-trip tests cover current native templates and explicit
DownwardAPI/field-selector defaults; this is not cross-version qualification.

Denials include stable reason codes (for example `SourceChanged`,
`ExpectationMissing`, `TemplateMismatch` or `RequestDeadlineExceeded`).
Structured logs and `cetirun_sandbox_pod_admission_denials_total{reason=...}`
use bounded reasons. Template diagnostics expose at most eight field paths,
not environment values, Secret contents, map keys or raw API errors. Correct
the trusted source/expectation or incompatible mutator rather than disabling
validation. Source GETs are uncached, cancellation-aware and limited to four
concurrent reads per request. The chart supplies a total validation budget of
80% of `timeoutSeconds`, retaining the final expectation revision check.

Controller state must be writable only by the trusted controller. The chart
grants admission ConfigMap reads only in that state namespace. Required workload
Secret/ConfigMap metadata reads need separately scoped, name-restricted RBAC in
the protected workload namespaces; the chart deliberately does not grant blanket
cluster-wide access. Missing source permissions fail closed. No deployment is
qualified by these packaging defaults.

The separate platform Copilot helper template/ownership contract is not defined
yet; there is no label-, namespace- or ServiceAccount-only helper exemption.
Protected preparation and expectation creation remain held until the separate
lifecycle/start-barrier and enforcement-observation integration is qualified.
There is no production expectation producer in this increment. `podsEnabled`
alone only registers the endpoint; `enforce` installs matching for marked
namespaces, not every Pod in the cluster. Do not mark namespaces or enable a
governed start path until the producer, scoped read permissions, source
refresh/revocation and workload lifecycle are qualified together. A chart
version, readiness probe or manually authored expectation is not that evidence.

#### Dedicated-node placement guard (separate opt-in)

`sandboxAdmission.placementConfigMapName` selects a pre-existing `placement.json`
ConfigMap in the controller state namespace. Empty is the default. It requires
configuration and final-Pod admission to be enabled; the placement webhook is
installed only when `sandboxAdmission.enforce` is true.

This read-only guard checks placement across **all namespaces**, including
explicit `Pod.spec.nodeName`, scheduler `pods/binding`, Pod updates, and reserved
Node identity changes/deletion. Labels and taints do not establish ownership.
The deprecated core `bindings` endpoint is matched and denied rather than
allowed to bypass the guard. Binding annotation overrides are rejected because
the API server can apply them to the Pod without a separate Pod UPDATE request.
An explicit, complete Node inventory pins Node names, UIDs and provider IDs;
unknown or same-name replacement nodes fail closed rather than become ordinary
capacity. Known unreserved nodes retain their ordinary placement path.

The registry uses the following shape; every identity below is synthetic:

```json
{
  "version": "v1",
  "nodes": [
    {
      "name": "ordinary-worker",
      "uid": "00000000-0000-4000-8000-000000000001",
      "providerID": "example://ordinary-worker"
    },
    {
      "name": "reserved-worker",
      "uid": "00000000-0000-4000-8000-000000000002",
      "providerID": "example://reserved-worker",
      "reservation": {
        "agent": {
          "name": "example-agent",
          "uid": "00000000-0000-4000-8000-000000000003"
        },
        "namespace": {
          "name": "example-workload",
          "uid": "00000000-0000-4000-8000-000000000004"
        },
        "statefulSet": {
          "name": "example-agent",
          "uid": "00000000-0000-4000-8000-000000000005"
        }
      }
    }
  ],
  "platformDaemonSets": []
}
```

Store this JSON as `data["placement.json"]` in the selected ConfigMap, with no
binary data. Registrations must come from persisted API objects, not guessed
names or labels. The inventory is bounded to 256 KiB, 256 Nodes and 64 platform
DaemonSets; each DaemonSet may enroll at most 256 persisted Pod identities.
Unknown fields, duplicate keys, nulls and inconsistent identities are rejected.
An empty platform inventory provides no platform-workload exemptions.

A reserved node is assigned to an exact Agent, namespace and StatefulSet
identity. Agent placement must also match the current protected workload
expectation. This does not relax final-Pod validation, release an execution hold,
or issue a start grant. Ordinary unrelated workloads cannot acquire reserved
placement by setting a node name, selecting a node, adding tolerations or
forging a namespace label.

With placement configured, both binding and protected-Pod validation accept
Kubernetes-added region/zone labels only at the freshly verified registered
Node's values. Explicit template labels are never overwritten; other additions
remain forbidden. Without this opt-in, the existing exact-template comparison
is unchanged.

Platform workloads are not exempt by namespace or ServiceAccount. Only
explicitly registered DaemonSets with pinned identities and templates can use
the platform path. Each `platformDaemonSets` entry contains `name`, `uid`,
`namespace: {name, uid}`, `templateDigest`, `trustedCreator`, and a
`bootstrapPods` array of `{name, uid}`. The digest is computed from the current
API-defaulted selector and Pod template using
[`CanonicalDaemonSetDigest`](internal/sandboxplacement/daemonset.go), not from
caller-provided Pod labels or the DaemonSet's status. Registered controller
identities are checked on creation;
reserved-node binding/update additionally requires the exact persisted Pod name
and UID in the protected registration. A replacement may be created by its
trusted controller but remains unable to bind to reserved capacity until its
new UID is enrolled. That is an intentional fail-closed pilot limitation, not
automatic platform recovery.

Explicit platform `priorityClassName` selections are resolved from the current
PriorityClass with read-only access. Its numeric priority and preemption policy
are checked rather than copied from the incoming Pod. Generated Pod names use
Kubernetes' bounded prefix/suffix rules for long DaemonSet names; the full
registered controller name, UID and generation prefix still identify the owner.
Pod-only resource request defaults are derived from each regular or init
container's registered limits, including restartable init containers. Explicit
requests remain unchanged. The incoming Pod and stored DaemonSet template are
not rewritten, and unrelated admission-added resource requests remain rejected.

The platform owner must protect registry writes and establish the registry on
an empty, isolated dedicated pool before granting workload access. The guard
does not discover or adopt nodes, mutate scheduling inputs, install DaemonSets,
write reservations, or prove historical pool isolation. Registry changes need
their own authorized, stopped lifecycle; removing a record is not permission
to reuse a pool or skip old-writer fencing.

Bootstrap the serving replicas and TLS first on unreserved nodes, publish the
registry, exercise direct/binding request-path tests, and only then enable
enforcement. This opt-in expands the failure domain: registry or serving failure
blocks matched requests cluster-wide. Pod status and deletion remain
independent, as does the existing narrow garbage-collector finalizer completion.
When serving is available, ordinary terminating Pods can remove only finalizers
without altering placement or other object fields; unrelated changes are not
cleanup exemptions.
ConfigMap updates and webhook-registration repair remain available. A new
serving Pod is still a matched CREATE: plan recovery using existing serving
capacity or an authorized, stopped maintenance procedure, not an assumed
admission bypass.

This is one placement boundary needed for a future dedicated-pool Agent pilot.
It is not a `CheckQuiesced` provider, an AKS deletion authorization, a complete
start/change barrier, or proof of pre-start and ongoing runtime enforcement.
Those gates and the actual governed-Agent end-to-end demonstration remain
separate requirements.

#### Schema and binary upgrades

Helm upgrades all four CRDs as ordinary templates. Compatible schema changes
need no external CRD apply step or upgrade Job. The CRD retention annotation
prevents deletion; it does not make schema changes or rollback reversible.

`sandboxAdmission.validationMode` defaults to `compatible`. CR object decoding
tolerates fields introduced by a newer schema, so an added status field does
not block ordinary writes through an older admission replica. Known field
types, authorization, binding checks, deletion protections and dependency
failures still apply. Server configuration, Pod admission and namespace
protection remain strict; `failurePolicy` remains `Fail`.

Metadata-only cleanup compares the raw objects, including unknown fields.
Non-administrators cannot use legacy Agent/Team authority to add, change or
remove unknown top-level spec fields. They can continue ordinary updates when
those unknown fields are unchanged. Administrator authority is not proof that
an older binary implements a new policy: additions within the same served API
must preserve the old authorization and execution semantics. New security
semantics require a qualified version/capability transition, not merely an
unknown field or a default that silently activates them.

The optional `strict` mode rejects unknown CR object fields. In that mode,
schema additions may reject requests until all serving binaries understand
them, including additions delivered through API-server defaults. Use it only
with an explicitly ordered schema/binary upgrade procedure.

Operator finalizer changes, spec backfills and status updates use merge patches
with resource-version checks rather than replacing the whole object or status.
Conflicting writes must be retried from a fresh object. Other clients must follow
the same rule if they write objects whose schema can outlive their compiled types.

The template-managed installation is the new baseline. It does not adopt
unmanaged CRDs from an earlier installation or CRDs owned by another release.
Existing installations need a separately reviewed ownership handover; never
delete and recreate CRDs to bypass ownership checks. Breaking schema changes
and downgrades require a reviewed migration. Do not assume Helm rollback or
automatic rollback can safely restore an older schema and binary.

API-server tests cover a newer status schema with strict and compatible
handlers, continuous writes during Helm upgrade, retention on uninstall, and
status patches that preserve unknown fields. They do not qualify arbitrary new
policy semantics or prove a production rolling-upgrade availability guarantee.

#### Availability and bootstrap

Use an explicitly authorized environment and platform certificate issuer.
Admission is currently gated to Kubernetes 1.37+, matching the tested minor
version; native match-condition support alone does not qualify older versions.
Only one operator/admission installation per cluster is supported by the fixed
cluster-scoped resource names.

1. Provision a `kubernetes.io/tls` Secret containing `tls.crt` and `tls.key` for
   `cetirun-sandbox-admission.<operator-namespace>.svc`. Supply its name through
   `sandboxAdmission.tlsSecretName`; no issuer is installed and no serving
   certificate is generated by this chart.
2. Configure exact `sandboxAdmission.adminUsers`/`adminGroups`, then deploy
   `enabled=true, enforce=false`. Verify TLS, readiness, API read permissions,
   two available replicas and their placement. The chart supplies a disruption
   budget and spreading constraints; rendering does not establish availability.
   Use a waiting rollout and request-path checks before the next step; an
   initial install with both switches enabled is not a supported bootstrap.
3. Supply the issuer's base64 PEM CA bundle as `sandboxAdmission.caBundle`.
   After request-path verification, enable `enforce=true`.
   The configurations use `failurePolicy: Fail` and
   `sandboxAdmission.timeoutSeconds` (default 10; integer range 1-30).
   Measure request latency at the intended configuration/Agent count; a larger
   deadline bounds waiting but does not establish a capacity guarantee.
   Neither flag enables runtime execution.
4. During CA rotation, first trust both old and new CAs, rotate the mounted pair,
   verify every replica serves the new certificate, then retire the old CA.
   Each new TLS handshake reads the current pair and reuses the parsed
   certificate only when its contents match. Validity is checked every time;
   invalid, missing or expired material fails the handshake and readiness.
   Neither a filesystem watcher nor unchanged modification times can authorize
   stale cached material. Existing TLS connections may outlive replacement.

Scheduling uses independent `sandboxAdmission.nodeSelector`, `affinity` and
`tolerations`, not the controller's placement. An optional `priorityClassName`
must name an existing, platform-approved PriorityClass; the chart does not
automatically assign system-critical priority. Spread constraints do not
guarantee distinct hosts when only one eligible topology domain exists, so
verify actual placement on at least two eligible hosts for availability.
The disruption budget permits voluntary disruptions only while at least
`replicaCount - 1` replicas remain available.
A native 10-second preStop sleep allows endpoint changes to propagate before
shutdown; the process has a 35-second manager shutdown budget within a
50-second Pod termination grace period, covering the maximum webhook deadline.
These settings reduce rollout races; live endpoint-drain and replica-loss
testing is still required.

The rules do not intercept their own Deployment, Service, RBAC or certificate
Secret. They exclude configuration `/status` and Agent DELETE.
Optional protected Pod rules are installed only with `podsEnabled=true` and
`enforce=true`; they always exclude Pod deletion and `/status`.
Status writes still require appropriate Kubernetes RBAC.

An API-server-side match condition also excludes **only** the exact controller's
removal of known lifecycle finalizers from an already-deleting Agent, with
unchanged spec, status and protected metadata. It preserves foreign finalizers
and cannot exempt additions, annotations, ownership changes or spec updates.
Thus authorized status, deletion and narrow cleanup remain possible while the
webhook is unavailable; shared-configuration deletion and other matched writes
fail closed. Repair the service or certificate rather than weakening the rules
or manually stripping finalizers. Any exceptional rollback needs a separately
reviewed recovery procedure while starts remain held.

#### Monitoring and platform boundaries

The controller-runtime server exports `controller_runtime_webhook_latency_seconds`,
`controller_runtime_webhook_requests_total` and
`controller_runtime_webhook_requests_in_flight`. Since admission denials are
usually carried in HTTP 200 responses, use
`cetirun_sandbox_admission_decisions_total` for bounded `allowed`, `denied`,
`invalid_request` and `dependency_error` outcomes by handler.
`cetirun_sandbox_admission_certificate_not_after_seconds` reports the current
serving certificate's Unix expiry time (zero for unreadable/unparseable
material); on admission pods, alert on zero, expiry approaching, dependency
errors and sustained request latency approaching the configured deadline.
No object names or arbitrary error strings become metric labels.

`cetirun_sandbox_admission_request_duration_seconds` is a histogram of time
inside each completed admission handler, including its dependency reads. Its
`_count` supplies completed-call counts split by operation; the existing
`cetirun_sandbox_admission_decisions_total{handler,outcome}` schema is unchanged.

| Label | Values |
| --- | --- |
| `handler` | `validate`, `default`, `pod`, `placement`, or defensive `unknown` |
| `operation` | `CREATE`, `UPDATE`, `DELETE`, `CONNECT`, or `unknown` |
| `outcome` | `allowed`, `denied`, `invalid_request`, `dependency_error` |

Other operation strings, including empty ones, collapse to `unknown` in metrics
without changing the request passed to admission. Request identities, resource
names, user information, patches, warnings and error messages are not labels or
exemplars. Finite duration buckets are 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
1, 2.5, 5, 10 and 30 seconds, followed by the implicit `+Inf` bucket.
Only observed label combinations are exported.

This histogram does not include TLS, request decoding before dispatch, response
serialization, or API-server transport time. A handler that panics or never
returns has no completed-call sample; panic handling and cancellation behavior
are unchanged. Use the controller-runtime metrics and API-server observations
alongside it for transport failures, in-flight calls and full request latency.
A denial sent with HTTP 200 is still `outcome="denied"`, not an allowed call.
The metric adds no API reads, scrape dependency, request deadline, listener or
readiness gate. The existing metrics-disable setting remains effective.

For example, after separately configuring an authorized admission-process
scraper, inspect p95 handler time and request rate per target, handler and
operation. Replace these synthetic scrape labels with your own; retain any
cluster/fleet labels when selecting a target:

```promql
histogram_quantile(0.95,
  sum without (outcome) (
    rate(cetirun_sandbox_admission_request_duration_seconds_bucket{
      job="sandbox-admission-example", instance="admission.example.invalid:8080"
    }[5m])
  )
)

sum without (outcome) (
  rate(cetirun_sandbox_admission_request_duration_seconds_count{
    job="sandbox-admission-example", instance="admission.example.invalid:8080"
  }[5m])
)
```

These queries combine outcomes, not replicas or fleets. Inspect the rate and
scrape health with the quantile: no completed calls or missing series do not
establish zero latency or a healthy service. No latency target is qualified by
these examples.

The serving certificate metric does **not** verify the CA bundle configured in
the API server, its expiry, or hostname/trust alignment. Before enabling
enforcement and during every rotation, the platform must check those separately
and verify an actual API-server request through every serving replica. CA
injection is not installed or assumed; the issuer and trusted CA value remain
platform-managed.

The TLS listener authenticates the server, not the API-server client. A direct
request cannot apply an object, but it can exercise validation and consume API
reads. Metrics are also served on a plaintext, unauthenticated Pod port.
Before production enforcement, qualify cluster-specific network restrictions
for API-server access, node health probes and authorized metrics scrapers, or
an explicitly configured API-server client-certificate arrangement. No generic
NetworkPolicy is installed here: guessing control-plane source addresses can
block the fail-closed webhook itself. Keep these production gates separate from
local loopback and chart tests.

### Sandbox reported-status metrics

The operator exports the following additional gauges from its existing
`/metrics` endpoint, bound to `:8080` by default. These are operator metrics,
not admission-process metrics. No new listener, Service, scrape configuration,
RBAC grant or policy-enforcement behavior is installed.
Setting the existing `--metrics-bind-address=0` flag also skips this collector
and its metrics-only informer initialization.

| Metric | Meaning |
| --- | --- |
| `cetirun_sandbox_agents{runtime,class,status,reason}` | Counts explicit or Team-governed Sandbox intent and retained-held/history Agents by their reported status. Ordinary ungoverned Agents are excluded. |
| `cetirun_sandbox_metrics_collection_success` | `1` when the complete cached snapshot was collected; `0` when collection is unavailable or fails. Failed collection emits no partial fleet counts. |

Startup initializes the shared Agent, Team and Sandbox informers on every
operator replica, regardless of leadership. Scrapes do not initialize informers.
Incomplete startup reports collection health `0` with no fleet counts;
operational initialization failures are logged and retried without stopping
the operator. The manager cache retains its normal background list/watch and
reconnect traffic.

Each scrape recomputes counts from that cache with a shared five-second read
context, without retaining per-Agent metric series or querying a live
enforcement provider. The cache is eventually consistent, and lists of
different resource kinds are not an atomic snapshot. Cache updates and
deletions are reflected on subsequent scrapes. These reads do not add API calls
to reconciliation or status writes.
An empty or legacy-only fleet has successful collection and no fleet series;
it is different from failed collection or an unavailable scrape target.

Labels are bounded: `runtime` is `kata`, `gvisor` or `unknown`; `class` is
`normal`, `admin` or `unknown`; `status` is `True`, `False` or `Unknown`;
`reason` uses an allowlist and bounded fallback categories. Names, UIDs,
namespaces, revisions, messages, provider errors and arbitrary values never
become metric labels. Runtime classification requires the current,
non-terminating cached Sandbox to match the admission-owned UID binding;
a same-name replacement is not adopted for attribution.

Missing, stale or malformed conditions report `Unknown` instead of repeating
an old success. Otherwise the valid current-generation condition is reported;
it is not independently reverified by the collector.
Intentional suspension and deletion take precedence over old conditions and
remain distinguishable from failures. Applied-binding history alone is not
current readiness. Cached Team governance fields count as intent only: they
never assign a class, select a Sandbox or establish an authorization binding.

**These are cached status reports, not fresh enforcement evidence.** A declared
runtime label does not qualify its adapter, and `SandboxPrepared` still means
zero-replica held preparation. Scrapes neither check running Pod/node/epoch
observations nor authorize execution. Condition transition timestamps are not
observation-freshness measurements. Collection failure is not evidence that
there are zero affected Agents.

#### Inspection queries and response guidance

Configure an authorized scraper separately; the existing metrics port is
plaintext and unauthenticated. Keep the deployment's network/access controls.
Select one operator target for one cluster/fleet before aggregation. Multiple
operator replicas report the same cluster-wide population: do not sum their
snapshots. The following target labels are synthetic examples to replace with
the scraper's actual labels.

```promql
cetirun_sandbox_metrics_collection_success{
  job="example-cetirun-operator", instance="operator.example.com:8080"
}
```

Only interpret an empty count as zero after confirming both collection success
and the selected target's `up` signal:

```promql
sum(cetirun_sandbox_agents{
  job="example-cetirun-operator", instance="operator.example.com:8080"
}) or vector(0)
```

Inspect non-ready reports, excluding explicit suspension/deletion:

```promql
sum by (runtime, class, reason) (cetirun_sandbox_agents{
  job="example-cetirun-operator", instance="operator.example.com:8080",
  status!="True", reason!~"Suspended|Deleting|InstanceDeleting"
})
```

This includes expected preparation holds; it is not an automatic failure
alert. Choose alert thresholds and durations only after qualifying the
deployment's expected lifecycle and collection behavior.

| Observation | Operator response |
| --- | --- |
| Collection failure, missing gauge, or target down | Check target selection/version, operator health, cache synchronization and API connectivity/authorization. Treat fleet counts as unavailable, not healthy or empty. |
| Missing references or unauthorized bindings | Inspect current status and trusted operator logs. Restore valid references or obtain explicit administrator reapproval of the current identities; never adopt same-name replacements or edit journal UIDs to bypass checks. |
| Unknown evidence or unavailable runtime | Check the trusted platform inventory and qualified providers. Restore the required evidence; do not reinterpret Pod absence, scale-zero or a PVC access mode as writer fencing. |
| Apply/conflict failures or reported enforcement loss | Investigate the affected control through trusted status/logs and the qualified containment procedure. Metrics and alerts do not stop execution, revoke grants or establish containment. |

Keep holds and recovery finalizers intact while investigating. This increment
does not add observation-age, rollout or fencing-duration metrics, durable
failure counters, alert rules, dashboards or production qualification.

### Enforcement feasibility baseline

Source inspection identifies Azure CNI Overlay powered by Cilium and Kata node
pools in the [cluster template](../../infra/cluster/modules/aks-cluster.bicep).
The [current chart network template](../cetirun-chart/templates/networkpolicy.yaml)
uses standard NetworkPolicy, namespace-wide DNS and an IPv4 egress rule. It is
not a qualified implementation of the new contract.

Public documentation reviewed on 2026-09-25 narrows the next trial:

- [Cilium deny policies](https://docs.cilium.io/en/stable/security/policy/deny/)
  provide precedence over allow policies, but do not support `toFQDNs` denial.
  Native deny rules alone therefore cannot implement full hostname blocking.
- [Cilium endpoint lifecycle](https://docs.cilium.io/en/stable/security/policy/lifecycle/)
  exposes endpoint regeneration state. This is a candidate observation input,
  not proof of all required controls or protection during initialization/failure.
- [Managed Azure CNI capabilities](https://learn.microsoft.com/en-us/azure/aks/azure-cni-powered-by-cilium)
  have version/feature restrictions and node/pod `ipBlock` limitations. Upstream
  Cilium support must not be assumed to exist in a managed deployment.
- [gVisor compatibility](https://gvisor.dev/docs/user_guide/compatibility/)
  distinguishes host-enforced sandbox resource bounds from unenforced
  in-sandbox competing-process limits. Container declarations do not request
  independent hard quotas by themselves.

The initial candidate is the configured managed Cilium path, with a separately
qualified upstream Cilium installation as a comparison only where authorized.
FQDN blocking, gateway-to-workload mTLS and any unqualified protocols remain
explicitly unsupported. Required network isolation, port blocking, runtime
accounting and observation/fencing still need actual target-version tests.
No cluster was inspected or modified to establish this baseline.

### Offline checks

With the documented pinned tools and dependencies already installed:

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
  make -C app/cetirun-operator generate manifests
make -C app/cetirun-operator check-crds
python3 -B scripts/ci/validate.py go-test --component operator
python3 -B scripts/ci/validate.py go-lint --component operator
bash scripts/cetirun-platform-helm-test.sh
```

Run these from the repository root. Unit/schema/render checks are not API-server
admission tests or live CNI/CSI/runtime qualification.
`make -C app/cetirun-operator test-envtest` starts isolated Kubernetes API-server
and etcd processes, with RBAC-enabled admission fixtures and the rendered webhook
rules. It covers schema/default persistence, compatible and strict decoding, authority,
reference deletion, outage/recovery and the native cleanup predicate. A separate
loopback serving test exercises the actual admission process and overlapping
certificate rotation. These checks require pinned envtest assets and Helm 3 or 4;
see [development setup](../../DEVELOPMENT.md#generated-files-and-dependencies).
They never use an existing cluster, execute an Agent, or qualify a production
issuer, replica-loss recovery, runtime, CNI or CSI.
