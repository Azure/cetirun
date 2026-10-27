# CetiRun operator

The operator reconciles `Agent`, `Team`, `Sandbox`, and `AgentPolicy` resources
in `cetirun.microsoft.com/v1alpha1`. Policy assignment and AI Gateway
projection are active control-plane contracts; native Sandbox execution remains
held at zero replicas until a separately delivered execution adapter is enabled.

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

Agents can retain the legacy deployment path without creating a Sandbox or
AgentPolicy. Omit `sandboxRef` and use either no Team or a Team with no policy
or Sandbox fields.
Existing chart, runtime, Gateway, ownership, and admission requirements still
apply; compatibility is not a bypass for requested policy.

A policy-bound Agent names one Team and one Team-permitted Sandbox. The Team's
single `agentPolicyRef` selects its policy; it does not derive from the human
caller's Portal role. The effective policy is the intersection of that
administrator-selected `AgentPolicy` and the Agent's
explicit requests for models, budget, skills, and identity.

For a policy-bound Agent, the Operator resolves the selected Team, Sandbox, and policy before it can
retain compute or model access. It projects model names and a native monthly USD
budget to a per-Agent AI Gateway virtual key, compiles the Sandbox/security/network
revision, and records the expected Gateway, Sandbox, and credential revisions in
Agent status. Missing, rejected, stale, or unreadable evidence fails closed.
There is no approval resource or separate governance workflow.

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

## Policy control plane and native Sandbox execution

`Sandbox` describes a reusable runtime, container resource declarations, private
home storage and scratch. `AgentPolicy` describes selection, security, network,
ingress and identity intent. Both configuration resources are cluster-scoped.
Policy-enabled Teams bind one policy, permitted Sandboxes and a creation
default; an Agent binds a Sandbox. A human administrator's role does not select
the workload policy.
These bindings coexist with the required `spec.agent` Harness configuration.

The [sandbox package](internal/sandbox/) currently provides:

- Pure defaulting, validation and deterministic normalization without mutating
  source objects.
- Cross-resource validation of every configured Team Sandbox/policy pair.
- Single-policy resolution with explicit authorization of Agent, Team,
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

### Common Sandbox reconciliation (explicit opt-in)

With `sandboxSources.nativeExecution: true`, new OpenClaw and Hermes Agents use the
ordinary controller's feature assembly, Harness adapters, Helm apply, readiness,
model integration and deletion flow. The selected Sandbox adds runtime, resource,
HOME, network and credential constraints to that flow; there is no fallback
deployment mode. The opt-in defaults to `false`.

Use the same release version for the Operator, Sandbox admission, Portal and
configuration-change tools. Mixed-version rollout sequencing and pre-upgrade
bootstrap-only recovery are outside this contract. There is no additional
reader-first switch: remove the retired `sandboxSources.reconciledExecution`
setting, which is rejected rather than silently ignored.

Persisted lifetime mode and owned namespace/HOME identities still govern recovery
and cleanup. Configuration changes do not migrate or adopt existing resources,
and missing ownership evidence still holds execution.

Kata HOME supports `ReadWriteOncePod` (RWOP) and `ReadWriteOnce` (RWO) when the
selected mode is explicitly listed in the platform's `homeAccessModes`.
Both use the same common-executor restart, retained suspension/resume and
cleanup paths. Coordination still waits for observed workload shutdown and
every namespace Pod to terminate before releasing a source reservation.
RWO is not a shared HOME grant or a single-Pod storage lock; private ownership,
one main StatefulSet replica and current Pod admission remain required.
Changing a Sandbox does not convert an existing HOME claim's access mode.

Shared-only, Copilot-only and mixed model configurations use the existing
Gateway and per-Agent Copilot proxy. The proxy is a separate Deployment, not a
sidecar: it cannot mount HOME or consume the Agent's runtime/model credentials.
Its optional controller-owned `aiGateway.copilotProxy.replicaCount` permits
authentication preparation independently of the Agent replica count. Suspension
and policy revocation stop both components. Pre-login creation does not grant
Copilot model access; the Portal continues to report sign-in required.
Suspension blocks an existing Gateway key, stops owned main/auxiliary workloads
and withdraws their route before chart or bootstrap prerequisites are checked.
A Gateway, component-stop or status-write failure does not skip the other
shutdown attempts. Unacknowledged shutdown still retains its source reservation;
normal Kubernetes stop requests are not physical fencing.
Loss of bootstrap/runtime prerequisites also holds an already-running,
unsuspended reconciled Agent: block its existing key, stop owned components and
withdraw ingress while preserving HOME and spend. A status error does not skip
these independent cleanup attempts.

The same protected expectation ConfigMap now has a version-2 representation for
these lifetimes. It records the current chart-owned workloads, complete source
identities and compiled controls. Pod admission verifies StatefulSet or
Deployment/ReplicaSet ownership and the current authorized template, rather than
permanently consuming one Pod UID or fixing helper names. Common helpers and
chart components remain subject to runtime, privilege, storage and credential
constraints. Config/key/model updates use normal Helm reconciliation and preserve
HOME and the existing Gateway spending identity. Missing or foreign ownership is
not repaired by adopting resources or clearing protection markers.
Ready and suspended Agents still recheck current sources and deployed controls.
Unchanged passes retain their phase and reconcile timestamp between periodic
refreshes instead of generating status-watch loops.

Private-image Agents complete owned namespace bootstrap before registry
credentials are synchronized. Missing runtime pins are filled first; existing
namespaces without the protected bootstrap receipt are not adopted. Credential
rotation still synchronizes the owned pull Secret before chart preparation.
The protected expectation pins that Secret's identity as a kubelet image-pull
binding, not as a Secret that Agent containers or the Copilot proxy may consume.

When Memory Context is enabled, reconciled OpenClaw and Hermes use their ordinary
native plugin registration and composed skill paths alongside the installed
assets. OpenClaw retains its conversation hook, and Hermes explicitly includes
`cetirun-memory-context` in `plugins.enabled`, including after HOME initialization.
Retained single-start templates remain disabled.

Every disk-backed `emptyDir`, including extension and auxiliary volumes, must
declare a positive `sizeLimit` no greater than the selected Sandbox scratch limit.
The common chart bounds its context, empty-skills and Tailscale temporary storage
too. Helpers with fixed temporary-volume sizes require a sufficiently large
selected limit; validation does not silently enlarge it. Memory-backed volumes
retain their separate memory accounting. These are per-volume checks, not a
replacement for container/Pod storage accounting.

Portal runtime profiles are optional only when its deployment explicitly enables
`sandboxRuntimeDefaultsFromController` after this common executor is enabled.
Without that explicit deployment setting, missing profiles reject creation.
With it enabled, the controller pins chart/image/port from its selected trusted
chart, with narrowly authorized admission updates. Disable the Portal opt-in
before disabling native execution. Explicit requests and configured
profiles are still validated.
The protected path disables the ordinary immutable-StatefulSet replacement hook;
unsupported storage/type transitions require a deliberate handoff, not orphaning
the old HOME writer.

Existing bootstrap receipts, including checkpoints created before the preparation
finalizer or Gateway projection, select their recorded lifetime mode. They retain
their HOME identity, chart pins and cleanup even after the new switch is enabled.
Version-1 preparation/start records also retain their recorded lifecycle.
They are not a second feature-development path
or an automatic conversion of a running lifetime. The historical
`native-single-start` capability-profile identifier remains accepted for
configuration compatibility; new version-2 lifetimes have the common lifecycle.
FQDN/mTLS and identity grants still require supported policy capabilities.
Kubernetes shutdown, RWOP and successful rolling updates do **not** establish
physical writer fencing, failed-node recovery, durable restart requests or
cross-node migration guarantees.

When the optional configuration-coordination authority is configured, these
lifetimes reserve their selected Team/Sandbox/policy too. A normal desired-template
generation can refresh that reservation only while the selected source identities
and effective inputs are unchanged. Shared configuration changes remain excluded
until suspension or deletion has acknowledged controller shutdown and ordinary
Kubernetes quiescence. This is a logical configuration reservation, not a physical
workload identity or writer-fencing receipt; unknown shutdown outcomes remain held.
A resume that encounters a recorded stop restores the hold and finishes that
guarded shutdown before acquiring new authority. A missing hold marker alone
does not discard the pending stop or establish quiescence.
Removed or never-created auxiliary Deployments still require their ReplicaSets
and namespace Pods to be absent. Their absence does not replace the main HOME
workload's acknowledged shutdown.
A fresh, durably recorded pre-submit checkpoint can be cancelled without a main
StatefulSet shutdown acknowledgement, but only before any apply is submitted and
after ordinary namespace/Pod cleanup checks. Its expectation is first persisted
as initializing, before the grant journal, and becomes pending only after the
empty journal is confirmed. Publication retries, including lost acknowledgements,
therefore retain the pre-submit evidence without inferring it from a missing
workload. No grants or workload apply are issued during initialization.
Cancellation races with submission
through a conditional state update. Submitted, uncertain or older unmarked
checkpoints still require acknowledged shutdown; a missing StatefulSet alone is
not evidence that no workload was submitted.
The Operator lists auxiliary ReplicaSets in the affected namespace; the chart
retains this read/list permission when new native starts are disabled so existing
lifetimes can finish cleanup.
Normal reconciled deletion revokes its recorded preparation grants and
releases/deletes the Operator-namespace grant journal before removing the Agent
finalizer. This cleanup remains available when new starts are disabled and
retries terminating journals during foreground deletion without adopting foreign
ownership or discarding grant history.

### Planned healthy-source migration (separate opt-in)

`sandboxSources.plannedMigration: true` enables a platform-administrator
Kubernetes API for cold moves of common-executor OpenClaw/Hermes Agents.
It requires `nativeExecution`, which selects common reconciliation. The default is off;
the false setting is omitted from controller configuration for reader-first
rollout. Upgrade the CRDs, all Operator and admission replicas, and Portal
readers before enabling new requests. Do not roll readers or schemas back while
migration records remain. Disabling new requests retains accepted operations
and their cleanup; it does not remove an existing destination pin.

This scope requires one bound filesystem CSI `ReadWriteOncePod` or `ReadWriteOnce` HOME,
with no other PVC mounts, a CSI driver that requires attachment, and a healthy
source on a different Node. It preserves Agent, namespace, StatefulSet, HOME
PVC/PV, chart pin, Gateway key and spending identities. It does not support
Custom or retained single-start lifetimes, shared-volume migration,
cross-cluster moves, forced deletion/detach, or unavailable-source recovery.
Use an explicitly authorized environment and qualify the actual runtime/CSI
artifact set before enabling this feature.

Set `spec.plannedMigration` to a fresh `requestID` and a `target` containing the
destination Node's current `name` and `uid`. IDs use the retained-restart
16-128 character format. Submit with Agent UID/resourceVersion preconditions.
The same ID cannot select a different destination; an accepted request cannot
be replaced until completed. A rejected, unaccepted request can be corrected.
An uncaptured request rejected by the activation gate still rechecks source
authorization; leaving that request present cannot retain revoked access.
After a recorded migration, keep the destination rather than removing the field.
Portal has no migration action in this increment and rejects new owner restarts
while a migration is pending.

The controller persists `status.plannedMigration` before side effects:

1. `Protecting` captures exact source/storage/Node identities and protects the
   source Pod and VolumeAttachment with separate finalizers. Once both are
   protected, failure to write the stop checkpoint does not skip independent
   authorization checks or shutdown of an unauthorized source. A transient dependency failure retains the source
   observations and continues shutdown from `Stopping`,
   rather than requiring readiness that its own hold cleared. Destination start
   still requires current authorization through common reconciliation; no
   desired generation is reported as applied merely to pass a migration check.
   An accepted checkpoint is handled before bootstrap-receipt lookups can enter
   an independent hold. During `Protecting`, failed source reads or protection
   writes delay scale-down even when suspension is requested; retries must
   retain both observations rather than destroy an unprotected source. Those
   retries still recheck current authorization. Suspension, withdrawn access,
   changed projections or unavailable authorization independently block the
   existing Gateway key, withdraw ingress and apply an all-traffic deny to the
   exact owned Cilium policy while incomplete protection prevents scale-down.
   Its Agent/namespace selector and the captured source/storage identities are
   unchanged; ordinary reconciliation restores the policy before destination use.
   Network write/read-back failures remain explicit and retryable, not evidence
   of successful dataplane isolation. The temporary block retains key/spend
   identity without applying a new grant or claiming an administrator's block.
   Access failures are returned with the protection failure for retry; preserving
   source observations does not permit continued use of an obsolete grant.
2. `Stopping` blocks the existing key, withdraws routing and stops main/auxiliary
   workloads. `SourceTerminated` requires terminal Pod status and a completed
   status for every named container, while the original source Node/boot and CSI
   identity remain healthy. A missing Pod is not a termination acknowledgement.
3. `Detached` requires the original, retained VolumeAttachment to be deleting
   and to have released the CSI attacher finalizer observed during capture.
   The attacher removes that finalizer after successful detach, before updating
   `attached` and clearing `detachError`; those later status writes may be lost
   without retry. The retained finalizer transition is therefore the completion
   evidence, even when status is stale. The attachment UID, volume, driver and
   source Node must still match. Only after recording `Detached` is the migration's
   attachment finalizer removed. A missing attachment or `attached: false` with
   the CSI finalizer still present does not establish detach. Ordinary deletion
   uses the same evidence to release the captured protection. See the upstream
   [external-attacher completion ordering](https://github.com/kubernetes-csi/external-attacher/blob/e19b215493af4f130a8a82ef5911b5f052edd81c/pkg/controller/util.go#L82).
4. `Starting` follows removal of source Pods/attachments and release of any
   configuration reservation. The destination must satisfy the bound PV
   topology, workload/RuntimeClass scheduling and supported Equal/Exists
   tolerations. Required node-name affinity intersects existing constraints;
   it never replaces them or sets `spec.nodeName` directly.
5. `Completed` requires the current Ready replacement Pod on the selected Node
   and an attached destination VolumeAttachment for the original HOME.

Both Pod binding APIs re-read the destination Node UID and provider identity.
Same-name replacement Nodes are rejected. Node health checks require a Ready
condition and its UID-owned lease renewed within the lease duration, capped at
60 seconds; lease timestamps more than five seconds ahead are rejected. Source
boot/CSI identity changes or uncertain observations block progress rather than
selecting another destination. These API observations are **not physical writer
fencing** and do not qualify failed-node recovery.

Suspension keeps the checkpoint and prevents destination start; resume continues
the same request. If suspension arrives before the first checkpoint, a still-ready
source is captured and protected before stopping. A rejected migration preflight
does not block independently requested suspension; an unaccepted request remains
available for administrator correction or withdrawal rather than being silently
cleared. If an authorization outage holds the source before capture, ordinary
reconciliation can restore its currently authorized running projection after
the dependency recovers. The unaccepted destination is not rendered or applied;
any previous completed destination pin remains in force. Both binding APIs allow
that source recovery, and capture is retried only after readiness returns.
Current authorization, suspension and the migration opt-in still apply, and a
newly persisted checkpoint invalidates an older recovery apply.
Pending migration/shutdown work reports `Pending`, not completed
suspension: setting zero replicas alone does not acknowledge source termination.
Deletion never creates a replacement and releases only the
captured protections after their acknowledgements. Do not remove finalizers or
edit status to bypass a wait. Source protection writes, acknowledgements and
replacement apply remain retryable after conflicts or lost responses.

The Operator retains `get` on Nodes/CSINodes/CSIDrivers and `get/list/patch` on
VolumeAttachments for observation and finalizer cleanup even when new requests
are disabled. Admission retains read-only Node access and validates only the
checkpoint-authorized Pod finalizer delta; it does not mutate lifecycle state.
Offline tests exercise synthetic kubelet/CSI observations and isolated API
servers, not a live AKS driver, model, storage durability or physical fencing.
See the [lifecycle guide](../../site/content/en/docs/guides/lifecycle.md#planned-node-migration).

### Retained single-start profile and capability registration

The following single-start lifecycle notes describe retained version-1 records;
new native lifetimes use the version-2 common flow above. The explicit native option
requires enabled,
enforced configuration/Pod admission, `sandboxSources.enabled`,
`sandboxSources.bootstrapOwnedPrerequisites`, and an enabled AI Gateway.
The controller receives the same option through `CETIRUN_SANDBOX_SOURCES`.
The option adds Node `get` permission for readiness. The manager always retains
Pod `patch` permission for recorded lifetime finalization, including after new
native starts are disabled; admission still limits protected Pod updates.
The native profile uses the registered RuntimeClass's scheduling constraints;
the separate durable placement registry is unsupported and rejected.

Those earlier execution records support one OpenClaw Agent, a Kata Sandbox with
ReadWriteOncePod HOME, and shared Gateway model access. It uses the selected
Sandbox's resources/storage and the selected AgentPolicy's model entitlement
and budget. Per-Agent Gateway keys are projected through owned Secrets, not
embedded provider credentials. CIDR denies and supported TCP/UDP destination-port
denies are compiled into the owned Cilium policy. FQDN rules, internal-network
exceptions, required mTLS, Kubernetes/identity grants, Copilot sidecars, desktop,
Tailscale, Teams and arbitrary Agent Helm overrides are rejected before start.
Existing baseline network controls are retained.

Platform registration must state the guarantee it actually provides. A bounded
registration uses `registrations.kata.Capabilities.profile: "native-single-start"`
in `platform.json`, with `platforms.kata.adapter` set to
`{"name":"cilium","revision":"<probed-version>","qualified":false,"available":true}`.
This generic Cilium identifier does not assert Azure CNI or Azure topology.
The profile requires actual Kata resource limits and an explicit, nonempty
`homeAccessModes` list containing only `ReadWriteOncePod` and/or `ReadWriteOnce`.
New common-executor lifetimes can use either registered mode; retained
single-start lifetimes still require `ReadWriteOncePod`. Both retain
the existing guest-external network, deny-precedence, node-traffic and DNS
IP restrictions. Declare `hostResourceLimits`, `guestExternalNetwork`,
`denyPrecedence`, `nodeTraffic` and `dnsIPRestrictions` only after establishing
those controls. TCP and UDP, the actual IP families, revisioned complete
topology, DNS/Gateway peers, and pinned RuntimeClass/StorageClass identities
remain required; read-only root and ingress support must match the selected
policy. The configuration harness allow-list supports `openclaw` and `hermes`;
the earlier single-start executor itself remains OpenClaw-only.
`enforcementObservation`, `restrictiveFailure`, per-container guarantee flags,
FQDN support, mTLS, Kubernetes access and Entra ID flags must be
`false`; identity/token grant declarations are unsupported. These are not
physical fencing or guest-external runtime-observation guarantees.

`network.blockedCIDRs` uses the registered address families and the existing
guest-external deny baseline. `network.blockedPorts` additionally requires
`registrations.kata.Capabilities.blockedPorts: true`; it is never enabled by
default or inferred from TCP/UDP connectivity. Declare that support only after
verifying actual deny behavior on the selected Cilium/runtime combination.
Native port denies support TCP and UDP only; an omitted protocol means TCP.
SCTP is rejected even if a broader registration lists it. Rules that block
required DNS/Gateway destinations or ports are rejected, not silently removed.
Admission checks these conflicts against current topology when a policy is
bound to a native Sandbox, when a referenced policy changes, and when an Agent
is authorized. An unreferenced policy definition alone does not select a
platform. Capability-only injected registrations cannot authorize native deny
rules without the matching topology. Configure port support before creating
consumers; changing platform inventory is not a live capability upgrade.
Public destinations outside the deny rules retain the existing egress posture.
These controls do not enable live policy rollout: selected configuration remains
reserved for the single-start lifetime, and actual source drift still holds it.

The empty/omitted capabilities profile retains the existing qualified contract,
including mandatory `enforcementObservation`, `restrictiveFailure` and the
qualified `azure-cni-overlay-cilium` adapter. Unknown profiles fail closed.
Native-only registrations require the explicit native option in both controller
and configuration admission; they cannot authorize the default held/external
execution path. Admission checks the option for an Agent's selected Sandbox
and for all referenced Sandboxes when validating Team policy. An unused native
registration does not block requests using qualified Sandboxes.
Disabling the option blocks new native authorization/start, but does
not disable recorded lifetime cleanup. No profile qualifies a platform merely
by declaring its capabilities.

When this native profile has no `sandboxSources.skillsCatalogConfigMapName` and
the effective skills `(Team skills + Agent additions) - Agent removals` are
empty, no skills PVC or published catalog is required. The internal empty-skills
source mounts a fresh `emptyDir` read-only at `/home/vibe/.aks/skills`, only in
the main container. It creates no publication receipt, PVC, PV, package installer
or additional grant. `skillsClaimName` is unused for that selection and may be
empty. HOME and the other native prerequisites remain required.
Any remaining selected skill still requires the configured skills PVC. A
configured catalog retains its existing validated publication path, even for an
empty selection; catalog errors or a recorded publication cannot fall back to
empty skills. Default held and non-native behavior is unchanged.

The controller stages the protected chart, records a one-shot start marker,
releases admission and starts one StatefulSet replica. The first Pod carries a
lifetime finalizer, so an early deletion cannot free its name before the
controller records its UID. Only after that durable record may the finalizer
be removed; admission rejects subsequent Pod creation for that lifetime.
Finalizer-only cleanup remains available after a hold or Agent deletion, and
admission remains read-only.
The chart conveys the same native opt-in to Agent admission. Before the
controller removes an Agent's hold, it persists the selected source snapshot
while Pod creation is still held. Admission permits only the configured
controller's exact hold-finalizer removal after rechecking the current native
receipt/start marker, owned stopped workload, source inputs and configured
coordination reservation. Other finalizer changes keep their existing rules;
neither a controller identity alone nor a status value grants native startup.
The recorded expectation is also retained through foreground garbage collection
and released only after owned namespace cleanup, so early Pod deletion cannot
lose the record needed for finalization.
Ready requires the current application readiness probe, Node readiness,
matching workload and effective source inputs, and the current Gateway grant.
Native HOME and PVC-backed skills checks pin ownership and storage inputs rather than
PVC resourceVersion. Ordinary delayed binding, status updates and PVC-protection
finalization do not block first creation; changed ownership/profile or lost
claims are still rejected.
Native owner access uses the existing `/ui/<agent>/` route and authentication
deployment. An unauthenticated sign-in challenge alone is not an access test.
For authenticated native ingress, the protected renderer replaces OpenClaw's
`gateway.trustedProxies` with the sorted approved ingress addresses from the same
platform snapshot. By default these are exact peer IPs selected with the protected
peer-UID label. The native single-start profile can explicitly register stable
Pod labels and bounded proxy Pod CIDRs instead; see the
[managed ingress example](../../examples/native-sandbox/README.md#rollout-stable-managed-ingress).
The compiler revision binds the selector, namespace and trust ranges through the
canonical topology, and the rendered configuration must match the derived list.
No wildcard or loopback fallback is added. The ingress proxy must overwrite
or safely rebuild forwarded-client headers. Proxy trust does not bypass Gateway
authentication, owner authorization or device authentication. The
existing `sandboxWorkload.trustedProxyIPs` hook carries either exact IPs or the
explicitly registered Pod CIDRs and is supplied
only for authenticated native execution; held and legacy rendering without that
hook preserve their existing proxy configuration.
Native Agents awaiting preparation can retain chart pins predating this hook.
For those charts, the protected renderer supplies the exact proxy list through
the existing `openclaw.config.gateway.trustedProxies` value and omits the
unsupported hook. It retains the original chart schema and final proxy-list
validation without rewriting the Agent's pin or authorization.
Within the single-policy API contract, adding this render projection does not
change the existing `sandbox-controls-v1` digest for otherwise unchanged inputs.
An already-running native lifetime keeps its old runtime configuration, workload, route, expectation
and consumed start marker across an Operator upgrade; it is not automatically
re-rendered or restarted to adopt new proxy settings. Actual policy, topology
or execution-source changes still fail closed. Use normal deletion and a new
Agent lifetime when an existing workload needs the new rendering.

With coordinated configuration admission enabled, native startup reserves the
bound Team, selected Sandbox and effective policy in the same coordination
store before its one-shot start marker. Pending configuration changes prevent
that reservation; relevant configuration writers are blocked while it is active
or stopping. Pod admission and readiness recheck the reservation read-only.
Ordinary cleanup releases it only after observing quiescence. Unused Sandbox
and unrelated AgentPolicy edits do not become native execution inputs.
Once first-Pod admission is released, an unobserved creation outcome keeps the
reservation held even if replicas are zero and no Pod is currently listed.
The initial Pod must be observed and ordinarily terminated; an uncertain start
is not permission to unlock configuration or force-remove lifecycle state.
The authority name/UID is derived from the existing admission configuration;
keep that authority configured until native cleanup finishes. This reservation
does not enroll a qualified writer or claim physical-fencing evidence.

After native start, HOME and skills prerequisite rechecks use the same
binding-tolerant claim digest as Pod admission. Ordinary delayed binding, claim
status updates and the Kubernetes PVC-protection finalizer do not stop the
single-start lifetime even when they occur between reads. Claim UID, ownership,
storage requests and protected metadata still have to match; lost, missing,
terminating or unreadable claims fail closed. Held preparation retains its
strict snapshot checks, and this does not authorize replacement or start replay.

This is **Kubernetes workload readiness, not guest-external enforcement proof or
physical failover fencing**. ReadWriteOncePod and ordinary Pod termination do
not establish the stronger writer-exclusion contract below. Do not force-delete
Pods or use this mode for node failover, old-HOME handoff or production recovery.
After a hold, source change, suspension or uncertain start, delete and recreate
the Agent instead of attempting resume/replay. In-Pod restarts remain Kubernetes
behavior; this profile does not qualify failure recovery.

Normal deletion retains the existing ownership checks and finalizers, withdraws
the route, independently revokes the Gateway grant and waits for ordinary workload termination
before removing owned storage/namespace resources. A failed cleanup stays
visible. Disabling native execution does not waive existing cleanup obligations.
Cleanup selects ordinary native quiescence from the protected preparation
receipt's recorded execution mode, not from the current new-start option.
Earlier started native receipts retain their controller-written start marker.
Non-native or unknown lifetimes still require their qualified cleanup provider.
Keep the operator state namespace, enforced admission, Gateway cleanup credentials
and any configured coordination authority until the recorded lifetimes finish.
An empty `skillsClaimName` without a catalog remains accepted when disabling
native starts, so that configuration does not prevent the controller restarting.
Component tests and API observations do not replace an authorized end-to-end
create/connect/model/tool/delete run on the selected runtime.
See the [native Sandbox example](../../examples/native-sandbox/) for the minimal
configuration, explicit-target run steps and UID-conditional cleanup.

### Durable writer-exclusion orchestration

The [writer lifecycle package](internal/sandboxwriter/) records an explicitly
enrolled Agent/namespace/HOME lifetime and its immutable historical writers in
protected controller state. Enrollment must precede every possible writer;
neither a missing journal nor a list of currently visible Pods proves an empty
history. Each writer records the workload, Pod, Node, physical machine
incarnation, epoch and storage identity. Conflicts, missing identities and
history bounds fail rather than adopting or discarding old entries.
The journal retains its own finalizer and exact Agent UID ownership, with
`blockOwnerDeletion: false`. It remains available for historical cleanup when
garbage collection requests its deletion, without blocking foreground Agent
deletion after the Agent's independent cleanup obligations are complete.
A terminating journal cannot reserve another writer.

Revocation intent is persisted before calling the external provider. An
uncertain reply or controller restart permits observation only, not another
destructive submission. Completion requires current evidence for the exact
recorded operation and every historical writer, a durable restart barrier and
storage release. A one-shot CRI stop, Pod absence, zero replicas, Node readiness,
or a PVC access mode cannot supply that evidence. Cleanup uses the recorded
historical tuple even when current policy references are unavailable.

The [controller bridge](internal/controller/sandbox_writer_exclusion.go) can
supply the existing `SandboxWriterExclusionSource` interface when explicitly
configured by a trusted platform integration. It does not install a provider,
adopt an existing workload, weaken placement checks, issue a start permit or
change the default activation hold. An independently qualified provider must
implement the guest-external barrier, unreachable-node handling and storage
release. The standalone CRI and empty-pool experiments are not such providers.

Unit, race and isolated API-server tests use synthetic provider responses.
Run `make test-sandbox-lifecycle` with preinstalled `KUBEBUILDER_ASSETS` for the
API persistence tests. These checks are not live runtime qualification.

### Start and shared-change coordination

The [coordination package](internal/sandboxcoordination/) provides the ordered
protocol for a participating platform integration. The
[native preparation bridge](internal/controller/sandbox_execution_intent.go)
requires current held preparation, a pinned ControllerRevision and grant
journal, complete lifetime placement, current authorization and compiled
policy, and an independently enrolled Pod/Node/epoch, physical machine,
volume and writer-journal UID. The complete writer tuple is persisted with
the coordination ticket and must still match before reservation or cleanup.
It rechecks sources without reading Secret contents or treating Agent status
as authorization.
Preparation still validates every permitted Team Sandbox against the Team policy.
An execution reserves its selected Sandbox and the Team policy, so valid
changes to an unused Sandbox or another Team's policy do not require it to stop.
Non-secret sources use immutable UIDs and effective-input digests rather than
treating the original observation resource version as lifetime configuration.
Digests retain desired content, owner references, finalizers and recognized
runtime, ownership, network and admission-control metadata. Unrelated labels,
annotations, managed fields and status do not invalidate execution. StatefulSet
zero/one-replica transitions remain controlled separately. Replacement,
termination, lost storage, changed content or more than one desired replica
still fail. Secret contents are never read by these checks, so Secret versions
remain conservative pins; explicit grant-approval requirements are unchanged.
Every execution recheck also refreshes whole-generation prerequisite approval
and compares it with the protected preparation receipt. Kubernetes prerequisite
revisions cover effective inputs, not incidental observation versions.
Publication-source content and identities remain checked even when their
observation versions change. Withdrawal, missing providers, changed approvals
and stale observations fail without restaging Helm or quiescing a running writer.
Preparation and final-Pod admission retain the 128-source expectation limit.
Execution and coordination reserve room for all 128 prepared inputs, the
preparation-state ConfigMap, and up to 130 access inputs: one Service, one Gateway
and 128 distinct ingress workload namespaces. The combined limit is 259 source
identities; duplicate dependencies use a single slot. Oversized inventories
fail without truncation, and the existing 512-KiB coordination-object and compiler
budgets still apply. Older readers enforcing the 129-source execution limit
must be upgraded before larger execution records are written.

One UID-pinned controller-state object serializes shared-source readers and
exclusive changes with API resource-version comparisons. Running and stopping
executions retain their source reservations. There is no expiry, lock stealing,
automatic state recreation or adoption of a same-name replacement. A start or
source mutation records intent before its only submission; restart and lost
replies are observation-only. Unknown mutation outcomes retain their reservation,
including when the API still shows the old object.

`AcquireTeamChange` derives the affected execution selections from an
authoritative before image and the complete proposed Team mutation. Changing
only the creation default can coexist with unaffected executions.
Changing the Team policy, removing their Sandbox
membership, replacing the Team or changing shared runtime inputs cannot.
Team mutations still serialize with one another, pin the exact before
UID/resource version and full desired mutation digest, and retain unknown
outcomes. Normal configuration admission and whole-Team validation still apply;
the mutation adapter must enforce the full digest at its actual write boundary.

The lifecycle reserves the physical writer through the durable journal, then
requires a fresh, exact pre-start acknowledgement of an armed guest-external
gate. Current runtime, Pod, control and epoch evidence precedes route publication.
The route-publication boundary guard rechecks the same evidence's freshness
after coordination and source reads, so delayed publication cannot use an
expired observation. An additional post-publication observation precedes the
adapter's atomic applied-binding/readiness commit. Source, freshness or
observation loss clears readiness, withdraws routes and attempts durable writer exclusion; failure in
one cleanup step does not skip the others. Reservations are released only after
all cleanup succeeds. Historical cleanup does not rediscover targets from
current policy or storage configuration.

**This protocol does not activate the shipped controller.** The adapter must
enforce the same operation at the actual runtime, mutation and publication
boundaries, including delayed submissions, restart and observation loss.
Calling a read-only guard before an asynchronous write is not sufficient.
Every shared-source writer must participate; direct writes must be excluded
before enablement. No concrete production start, observation, routing
or status-commit adapter is installed here, and neither a coordination record
nor a successful callback qualifies one. The existing controller and admission
holds are unchanged. Live governed Agent execution, model traffic and platform
qualification remain separate, uncompleted integration work.

The focused lifecycle tests cover synthetic ordering, identity/freshness
failures, writer-history integration and real isolated API persistence/CAS.
They do not start agent containers, contact a model service or qualify a
provider.

### Coordinated configuration updates

The [Kubernetes configuration adapter](internal/sandboxcoordination/kubernetes_change.go)
and [explicit command](cmd/sandbox-config-change/) submit real Team, Sandbox and
AgentPolicy updates and deletions through the coordination protocol. They are not
a runtime adapter or an execution switch. Creation, Agent changes and other
execution sources remain outside this command.
The public [configuration coordination client](configurationcoordination/) is
the narrow in-process facade for trusted platform writers such as the Portal;
it reuses these adapters and does not define a second protocol.

Both before and proposed objects must retain the original name, UID and resource
version. The adapter verifies the authoritative before image, pins the complete
proposed write, reserves the affected scope and records submission intent before
one resource-version-conditional Update. Governed-to-governed Team changes retain
selected-Sandbox scoping; legacy updates and transitions into or out of
governance conservatively reserve the entire Team. Schema defaults (including
omitted port protocols) are applied to independent input copies before pinning
and submitting the write. Explicit values, list order and duplicates are not
normalized away. No-op updates, including changes that only omit defaulted
values, are rejected before reservation because Kubernetes need not advance
their resource version. Configuration admission and Kubernetes authorization
still apply.

Sandbox and AgentPolicy updates separately compare the existing effective
execution digest before and after the proposed write. Typed and unstructured
AgentPolicy inputs share the same canonical representation of schema-defaulted
false booleans and empty network arrays; optional empty grant lists use their
omitted form. Unknown authoritative fields and explicit control values remain
part of the digest. When it is unchanged, the reservation records that digest:
existing and new executions with the same source UID and digest can coexist with
harmless metadata updates. Spec,
ownership, finalizer and control-metadata changes still exclude affected
executions. The complete mutation digest continues to authorize every field,
and competing updates of the same source still serialize.
Older reservations without this optional digest remain conservative during
recovery; their scope is never rewritten. Upgrade all coordination readers
before issuing the new records: older strict readers reject the extra field.
Previously recorded mismatched digests are not rewritten or accepted as aliases.
This does not enable execution or replace source rechecks.

After a lost Update reply, use the same input files to observe the original operation.
A matching current object with the same UID and an advanced resource version
proves the desired write is present and an old-version request cannot commit
again. An unchanged, missing, replaced or differently changed object does not
prove completion or cancellation. Such outcomes keep their reservations; do not
edit the journal, change the original files or automatically repeat the write.
An existing reservation with a different, older digest is not rewritten or
released by defaulting; it remains subject to the same recovery boundary.
Observation never releases a reservation. After matching committed content is
reported, explicit `--action update` can finalize that same changing operation
without resubmitting it.

For deletion, use `--action delete --before FILE` without `--after`.
The [deletion adapter](internal/sandboxcoordination/kubernetes_delete.go) reserves
the entire source and persists submission
intent before one background Delete with exact UID and resourceVersion
preconditions. Existing reference checks and Kubernetes authorization still
apply. It neither removes finalizers nor adopts a replacement object.

After an uncertain Delete result, retain the original before file and use
`--action observe-delete --before FILE`. Portal deletion recovery may instead
use the recorded operation ID with `--action observe-delete --operation-id ID`,
without retaining the body: the UID-pinned journal supplies only the recorded
source identity and deletion-content hash. An unchanged object or failed read
keeps the outcome unknown and its reservation retained. An authoritative
observation of the original UID terminating at a different resourceVersion,
or of that UID's absence, establishes deletion acceptance: a delayed old
conditional Delete cannot change it or a same-name replacement. Observation
alone does not release the reservation. Repeating explicit `--action delete`
with the same `--before` or `--operation-id` then finishes the same recorded
operation without resubmission. This proves neither physical cleanup nor
completion of finalizers or dependent cleanup.
The CLI reports `deletion-accepted`, not resource or dependent cleanup completion.
Release after the terminating transition allows normal finalizer cleanup to
finish. Missing authority remains an error, never permission to delete.
Deletion observation reads authoritative metadata only, so a replacement's
larger or newer specification cannot block recovery for the original UID.
Reservation, admission and submission still validate the complete original
before image and enforce the same conditional Delete.

Deletion records include a content hash for metadata-only recovery, not a body.
Existing records without that optional metadata require `--before`. Update
tickets cannot authorize deletion. Upgrade all strict journal readers before
issuing deletion records with the new metadata, and keep a deletion-capable
client available until its outstanding operations complete.

Build locally:

```bash
make -C app/cetirun-operator build-sandbox-config-change
```

Using the command requires separate authorization for the exact cluster and
mutation. It requires explicit kubeconfig/context and authority namespace/name/UID.
Updates require the original before file plus after input; deletion requires the
original before file or a recorded operation ID. There is no implicit
current-cluster fallback. The default action observes an update; deletion
observation requires `--action observe-delete`. Mutation requires explicit
`--action update` or `--action delete`. Inputs are
bounded, strict YAML/JSON configuration objects, not arbitrary Kubernetes
resources (256 KiB per image). Verified HTTPS is required; redirects and
exec/auth-provider login plugins and automatic mutation retries are refused.
Output identifies the operation
and phase, not the configuration contents. Run `sandbox-config-change --help`
for the exact argument list.

Whitespace and object-key order inside `managedFields.fieldsV1` do not change
field ownership and are normalized before comparing configuration images.
Malformed or duplicate-key field sets are rejected. Other server metadata,
including managers, timestamps, UID and resourceVersion, must still be preserved;
normalization does not permit refreshing an original operation's inputs.

The UID-pinned authority must already have been installed and protected before
execution, using the existing coordination state format. The command does not
create missing state, grant privileges, bypass admission, qualify fencing or
enable a workload. All other source writers and the physical runtime still need
their own integrations before governed execution can be enabled.

With coordinated configuration admission enabled, finalizer-only cleanup of an
already-terminating Team, Sandbox or AgentPolicy uses normal authorized API
updates, not `sandbox-config-change`. Admission checks the current source and
UID-pinned authority read-only, and refuses cleanup while any recorded execution
or change uses that source. Only existing finalizers may be removed; other
content and all outstanding reservations stay unchanged. Missing authority is
not a cleanup bypass. Ordinary configuration updates still require
nonterminating inputs.
This opt-in also requires every primary Team/Sandbox/AgentPolicy Delete to carry
the exact submitted deletion reservation and conditional background options,
including unreferenced configurations and legacy Teams. Upgrade operational
deletion clients before enabling it. Default-off behavior, Agent deletion,
status writes and the narrow Kubernetes garbage-collector finalizer exclusion
are unchanged. This is not a complete all-source execution barrier.

### Running-input verification

`CheckSandboxExecutionSources` remains the held-preparation check. A qualified
runtime integration can use `SandboxRunningStatusInput` after its controlled
handoff: the Agent and persisted expectation must both be unheld, the workload
must still match its enrolled UID/template with exactly one desired replica,
and the actual Pod and Node must match the separately enrolled execution.
The returned current resolution and plan contain no observation or start permit.
Pod readiness and Node health are not enforcement evidence.

Held intent capture now pins a separate `runningPreparationDigest`. It permits
only the expected hold transition, the independently rechecked current Agent
intent, and refreshed observation versions for sources whose effective content
is checked separately. Ownership, namespace/HOME identity, template, revision,
source identities and non-effective version pins remain protected. Existing
held tickets without the optional field remain readable, but cannot use the
running-input path without a newly verified, stopped enrollment. Older strict
readers cannot read new records containing the field; keep compatible readers
until these records have completed cleanup.

The concrete Kubernetes prerequisite source has an explicit `ResolveRunning`
path. It retains metadata-only Secret reads and exact input/approval checks
without requiring a preparation hold, re-running Helm or calling writer
quiescence. Other prerequisite adapters must explicitly support running reads;
the controller does not silently reuse their held-only resolver.
Placement checks share the existing scheduled-Pod topology validation.

The existing controller still dispatches governed Agents to held preparation.
No code here changes finalizers, expectations or replicas to start an Agent.
Real runtime gating, independent observations, all-source mutation coordination
and model/tool validation remain prerequisites for enabling execution.

### Guarded Sandbox status persistence

`SandboxExecutionStatusWriter` provides the status component for a separately
qualified runtime adapter. `CommitReadiness` requires the exact current Running
ticket, running-input verification and independent complete, fresh enforcement
evidence. It uses the existing status helper and one UID/resource-version-pinned
merge patch to record Sandbox enforcement and applied history. The observation
age is explicitly bounded to at most one minute and rechecked before and after
the write. Unchanged status is revalidated without rewriting it.

This component does not create routes, assign a new access URL, set global Agent
`Ready=True` or change the phase to Ready. Disabled ingress keeps the URL empty.
An acknowledged status write followed by failed revalidation is returned as an
error, not reported as a verified transition. Status persistence is not an
atomic transaction with the coordination object or the physical runtime; the
caller must execute coordinated cleanup on uncertainty.

`ClearReadiness` requires the exact Stopping ticket but not current policy or
prerequisite availability. It clears reported readiness/access, follows the
current suspension/deletion generation and preserves applied history. An opaque
stop marker forces the first clear to advance the Agent resource version, even
if it was already nonready, so pending positive writes using the old version
cannot overwrite it. Repeated identical clears are no-ops. This is status
invalidation, not route withdrawal, process termination or writer fencing.

Stock startup does not install this component as an execution adapter. Its
unit and local API-server tests use synthetic runtime inputs and observations;
they do not establish live enforcement or an end-to-end governed Agent run.

### Governed native access lifecycle

`SandboxExecutionAccess` supplies concrete Istio API operations for the first
OpenClaw bring-up; it is not installed by stock startup. `Enroll` accepts only
verified held preparation and creates a nonforwarding, Agent-owned route slot.
The returned coordination intent pins that route UID and the current Service,
HTTPS Gateway, host and native port. It separately resolves the ingress workload
namespaces from the trusted platform ingress topology, reads their exact UIDs,
and includes them in the same effective-source digests and reservations.
The VirtualService references the Gateway resource by its namespace/name but
exports only to these enrolled consumer namespaces, which need not contain the
Gateway resource. Multiple approved consumers are sorted and deduplicated;
more than 128 distinct ingress namespaces are rejected before namespace reads
or route creation. Missing or replaced namespaces fail closed rather than
exporting mesh-wide.
An existing foreign, changed or used route is never adopted.

`Publish` requires the exact Running ticket, current running-input checks and
separate fresh guest-external enforcement evidence. It updates only the enrolled
route UID, preserving the native `/ui/<agent>` redirect, HTTP and WebSocket
behavior. It does not expose terminal, desktop, sign-in helper or webhook routes.
No new access URL, global Ready condition or runtime permission is written.
Shared gateway authentication and its ongoing enforcement must already be
independently qualified; an HTTPS Gateway object is not authentication evidence.

`Withdraw` uses the recorded Stopping ticket and route ownership even when
current policy, gateway or ingress namespaces are gone. It uses the enrolled
consumer namespace names and first makes the exact route
nonforwarding, advancing its resource version, then conditionally deletes that
UID and verifies absence. This invalidates delayed publication updates without
recreating routes or touching a same-name replacement. Unknown results retain
the coordination reservation; route removal is not writer fencing or a reason
to release it. Disabled ingress requires route absence and creates no slot.

The optional `access` intent field and its `ingressNamespaces` inventory require
updated readers while such records exist. Legacy intents without access or
consumer pins cannot publish authenticated access. Previously enrolled routes
without consumer pins remain eligible only for nonforwarding cleanup using their
original Gateway-namespace export scope, followed by conditional deletion. This
component does not install a physical start/fencing provider or replace the
controller's held dispatch. Hermes and helper-route support are not included in
this first native access slice. Offline tests exercise API behavior with
synthetic runtime evidence, not a running proxy or an end-to-end model request.

### Current activation boundary

Legacy Agents and Teams remain supported while Sandbox execution is opt-in.
The CRD makes Agent `team`/`sandboxRef` and Team policy/Sandbox fields optional.
Without policy intent, the controller continues the existing built-in or Custom
runtime path, including provisioning, suspension, readiness, and deletion.
Legacy Gateway model sources default on unless explicitly disabled. New legacy
keys receive the deployment default budget; later administrator budget changes,
spend, reset periods, and emergency blocks are preserved. Legacy shared catalog
entries and current per-deployment shared-model groups remain usable.

Setting any Team policy/Sandbox field opts that Team into the full binding
contract: one policy, permitted Sandboxes, and a creation default. Partial
configuration is not a legacy fallback. Policy-bound Agents must explicitly
request model sources. They require a permitted Sandbox and current
authorization, and Custom remains unsupported in that policy contract.
Portal creation accepts legacy Teams without a default Sandbox. With the
Gateway disabled, it also accepts no Team, as before. With the Gateway enabled,
it still requires an existing Team and supported model routing. Portal support
for managing policy configuration is delivered separately. Admission continues
to default governed creation and protect exact Team/Sandbox/policy UIDs.
Later governed assignment or budget changes require an authorized administrator
and a suspended Agent.

This compatibility path does not clear existing hold/preparation finalizers,
authorization records, or recorded policy/Sandbox status. Such lifetimes remain
governed even after spec fields are removed. If an earlier mandatory-policy
version has already held an old Agent, review its state and data-retention needs
before recovery; do not strip authority markers or delete its namespace as an
upgrade shortcut. Legacy mode does not claim AgentPolicy enforcement.

An administrative Team is a separate Team with its own context and skills; it
does not create a privileged policy lane for other Teams. A person's permission
to manage Teams or AgentPolicies is a control-plane authorization distinct from
workload authorization. The latter comes only from the Agent's selected Team,
that Team's `agentPolicyRef`, and the Agent's explicit requests. Existing
policy-enabled resources require controlled stop and cleanup before a changed
binding can be explicitly reauthorized. No lossless rolling migration between
policies is supported.

The pre-release `normalAgentPolicyRef`, `adminAgentPolicyRef` and `policyClass`
fields are removed from the API and CRD schemas. New resources use only the
Team's `agentPolicyRef`; there are no compatibility fields, policy-class
branches, or migration readers for the former dual-policy design. This contract
targets a new system, not an upgrade of an existing governed deployment.
Authorization snapshots and Pod expectations use `v1`; coordination records
use version `1`. Policy foundation and compiled-control revisions also use `v1`
domains. These are the initial single-policy formats, not version upgrades of
the undeployed dual-policy design. Strict decoding, ownership checks and
explicit reauthorization remain fail-closed; no migration readers are added.

The controller holds Agents that request Sandbox policy, belong to a Team
with policy settings, or already have Sandbox status or a hold finalizer.
It never passes them to legacy built-in or Custom runtime preparation or application.
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

Portal Team edits preserve policy fields that the Portal does not expose.
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
enabled, policy configuration checks also read the selected platform
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

- Explicit administrator usernames/groups for shared policy configuration,
  and trusted Team assignments for policy-bound member Agents. Kubernetes RBAC
  must also authorize the operation.
  Portal administrators do not automatically acquire this independent authority.
- Creation-only Agent selection defaulting, followed by validation against the
  current Team. Existing Agents are never silently re-defaulted. Portal writes
  use its administrator identity and may preserve the Team selected by the
  Agent owner. Non-administrator direct Kubernetes writes require an exact
  authenticated identity-to-Team assignment configured on the webhook; that
  cluster access rule is separate from the product's Team selection model.
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
  Ordinary Portal Team context edits can preserve unchanged policy.
  Deleting a policy-free Team retains the existing RBAC boundary, even with
  member Agents. Removing policy from, or deleting, a policy-configured Team requires
  rebinding its members to another policy-configured Team or deleting them first.
  Members inherit policy even without an explicit Agent `sandboxRef`;
  clearing the Team fields must not release those Agents into legacy execution.
- Protected lifecycle finalizers. Only the configured controller identity may
  change known lifecycle finalizers in a metadata-only update; it has no blanket
  exemption for spec changes. Removal also requires an already-deleting Agent.
  Kubernetes' exact authenticated controller-manager or generic
  garbage-collector username may remove only the built-in `foregroundDeletion`
  finalizer from an already terminating object in an otherwise metadata-only
  update.
  The legacy controller's chart-version and Harness image/port pinning writes
  remain behind the execution hold. A future policy-bound path must authorize those
  specific writes or move them into authorized creation before releasing it.

There are **no production capability registrations**. Standalone Sandbox and
AgentPolicy definitions can be validated, but policy-bound Team/Agent bindings are
rejected without a qualified registration. The tests inject synthetic
registrations; enabling this process does not make those production evidence
or release the controller hold.

Protected rendering, final-Pod checks and policy compilation are connected by
an opt-in **held controller preparation** path, described below. It does not serialize
concurrent shared-configuration changes, coordinate rollout/start barriers,
produce enforcement observations, or permit policy-bound execution. Reference reads
are checks against current objects, not an atomic cross-resource transaction.
Those boundaries must be completed before production adoption.

#### Direct Kubernetes writer assignments and explicit reauthorization

For non-administrator callers that write Agent CRs directly, configure
assignments using the Kubernetes authenticated identity rather than an Agent
field or a Portal role:

```yaml
sandboxAdmission:
  teamAssignments:
    - team: research
      users: ["member@example.com"]
      groups: ["research-members"]
```

Every matching user/group must identify the same Team; conflicting memberships
reject instead of picking a precedence order. Assignments do not grant Kubernetes
RBAC permissions or independently approved workload grants. An unassigned
caller cannot create or update a policy-bound member Agent. Do not map a shared
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
application-owned annotations preserve policy. If a field manager has
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
unrecorded policy-bound Agents and changed reference UIDs require an administrator
to update `cetirun.microsoft.com/sandbox-reauthorize` with a fresh `requestID`
and the exact current `binding` tuple: Agent, Team, Sandbox and AgentPolicy UIDs.
The mutator validates that request and replaces the snapshot
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
switch. Policy-bound Custom rendering is explicitly unsupported by this slice.

Prepared manifests protect resource accounting (including init/helper containers
and RuntimeClass overhead), runtime/security settings, owned HOME, read-only
skills, scratch mounts and identity projections. Existing HOME and skills claims
must match their trusted bindings; the native-only empty-skills source has no
skills claim identity. An incompatible storage or StatefulSet
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
production registrations. With no source, policy-bound Agents retain their existing
execution hold. The opt-in Kubernetes sources below wire authoritative reads of
platform configuration and existing owned resources into the shipped binary.
Without the separate native execution opt-in above, they do not supply a
qualified writer-exclusion implementation, so held staging cannot complete
through stock startup. Tests inject that separate
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
still does. This is expectation format `v1`, separate from the render contract.
Controller and admission use the same initial format; malformed or unsupported
expectations fail closed. Do not edit stored JSON or lift holds to bypass these
checks.

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

These references use the `v1` expectation source format; they are not
another intent journal or an execution permit. Held expectations without the
current pins are rebuilt through guarded preparation. A start action must
explicitly require the complete current identities rather than infer
qualification from format version alone.
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

Tailscale, Teams, workload identity and policy-bound Custom configurations remain
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
Pod-admission RuntimeClass/StorageClass `get` permissions. Enabled sources also
grant the controller `get`, `list`, `watch`, `create`, `update`, `patch` and
`delete` on namespaced `ciliumnetworkpolicies.cilium.io`, which guarded Helm
staging needs for the compiler's policy resources. The controller's ClusterRole
grants these permissions cluster-wide; the staging path still verifies exact
Agent ownership and namespace identities. Admission remains read-only, and no
cluster-wide Cilium policy or endpoint write permissions are granted.
Sources also grant the controller `get` on Istio Gateways for exact access-source
pinning; the shared Gateway is not created, updated or deleted by this path.
The optional skills publication catalog described below adds only
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
Missing, inaccessible, malformed or drifted platform inventory blocks policy-bound
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
claims (or the native-only empty-skills source described above), verifies
ownership and current identities, and reads only
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
enforcement. In particular, the default prerequisite source has
no writer-exclusion adapter and returns an explicit error before preparation
creates its qualified-preparation cleanup state. Bootstrap-only ownership state
is separate and does not claim a writer fence. An Agent rejected by this
preflight remains deletable, including after sources are disabled; existing
preparation state never bypasses fencing.
Kubernetes Pod absence, PVC access modes and zero replicas cannot replace it.
Qualified fencing, successful held staging, controlled starts and running
enforcement observations remain separate lifecycle work. Do not remove holds
or finalizers to bypass these missing guarantees. The separately enabled native
profile uses ordinary Kubernetes quiescence, never this qualified fence.

##### Owned bootstrap (separate opt-in)

With `sandboxSources.bootstrapOwnedPrerequisites: true`, new native policy-bound
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
content. The only exception is the explicitly enabled native profile with an
empty effective selection and no catalog: it uses the read-only in-Pod directory
described above, not an empty replacement for published content. Without native
execution, the stock binary still stops at unavailable qualified writer
exclusion. The native authentication Secret is not evidence that a
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

Both Cilium adapter registrations require `capabilities.blockedFQDNs=false`,
even when the current policy requests no hostname rules. Declaring support
rejects platform validation, standalone policy validation and compilation.
`blockedFQDNs` accepts exact lowercase ASCII hostnames, not suffixes, wildcards,
URLs or IP literals. DNS-derived IP sets and the shared Istio SNI blacklist are
not equivalent implementations of that hostname control.

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
policy-bound start path until the producer, scoped read permissions, source
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
Those gates and the actual policy-bound Agent end-to-end demonstration remain
separate requirements.

#### Durable placement binding during held preparation

`sandboxSources.placementConfigMapName` is a separate, default-empty controller
opt-in. It requires enabled sources and must select the same registry as
`sandboxAdmission.placementConfigMapName`. It adds only Node `get` permission
to the controller and does not write the placement registry.

After validating the owned zero-replica StatefulSet, preparation reads its
registered reservation and the current Node identities. Before publishing the
held Pod expectation, it records the exact Agent, namespace, StatefulSet, HOME,
registry UID and Node UID/provider-ID set in the existing preparation receipt.
The expectation pins both that receipt and the registry's current UID/version.
The platform must enroll the actual staged workload UID; a missing reservation
leaves the workload held and preparation incomplete, not implicitly assigned.

The recorded identity is immutable for this namespace/HOME lifetime. A changed
Node set, replaced registry or workload, or disabled reader cannot silently
reassign it. Node status and unrelated metadata updates do not append history
or rewrite the binding. Cleanup passes the recorded tuple to the separately
qualified writer-exclusion provider, even if the current registry, Node or
configuration is unavailable; it does not rediscover old writers by name.

Opted-in receipts use the internal `v2` preparation format, which requires the
placement payload. Existing unselected preparation keeps `v1`; losing a `v2`
payload is an error, not an unassigned fallback. Upgrade readers before selecting
this option and do not downgrade binaries while such receipts require cleanup.

This is durable identity plumbing, not proof of lifetime exclusivity or
Compute VM destruction. No automatic enrollment, qualified fence adapter,
cloud deletion, execution permit or hold release is added. Those requirements
and a real policy-bound Agent run remain open.

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
   `enabled=true, enforce=false`. Include the Portal's service account,
   `system:serviceaccount:<portal-namespace>:cetirun-portal` by default: the
   Portal writes Agents, Teams, Sandboxes and AgentPolicies with it, and the
   webhook otherwise refuses its writes to policy-bound Agents, such as saving
   an owner's GitHub Copilot models after a sign-in. The platform chart refuses
   to enforce admission without it. Verify TLS, readiness, API read permissions,
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
| `cetirun_sandbox_agents{runtime,status,reason}` | Counts explicit or Team policy-bound Sandbox intent and retained-held/history Agents by their reported status. Ordinary policy-free Agents are excluded. The obsolete `class` label is removed. |
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

Labels are bounded: `runtime` is `kata`, `gvisor` or `unknown`;
`status` is `True`, `False` or `Unknown`;
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
current readiness. Cached Team policy fields count as intent only: they
never select a Sandbox or establish an authorization binding.

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

#### Exact-hostname deny projection experiment

[`TestHostnameBlockDNSProjectionFeasibility`](internal/sandboxpolicy/hostname_feasibility_test.go)
tests one proposed backend reduction: resolve a blocked hostname and deny its
IPv4/IPv6 host CIDRs. Go's real resolver consumes synthetic A, AAAA and CNAME
responses over in-memory connections; separate CIDR-only inputs then pass through
the existing compiler. Assertions use the serialized Cilium policy and the
existing synthetic L3/L4 evaluator, not a running Cilium dataplane.

The result is **NO-GO for exact-hostname blocking through DNS-to-CIDR denies**:

- The exact blocked name is denied, but so are an unlisted name, a CNAME alias
  and a child name sharing its addresses. An unrelated address remains allowed.
  The [API](api/v1alpha1/agentpolicy_types.go) specifies exact names, not address
  equivalence or suffix rules; wildcard policy input still rejects.
- Literal IPv4/IPv6 access requires no DNS query and is indistinguishable from
  named access at this layer. This observation does not assign a new API
  permission to direct-IP access or establish a DNS-query-filtering backend.
- After an answer changes, a stale projection permits the new addresses.
  Replacing the projection releases the old addresses; retaining both denies
  both, including unlisted names on shared addresses. Recompilation changes the
  control revision, but these tests do not exercise TTL scheduling, policy
  propagation, existing connections or failure recovery.

This is not Cilium [`toFQDNs`](https://docs.cilium.io/en/stable/security/policy/layer3/#dns-based):
those rules generate additive IP **allows**, not hostname denies, and do not
remove the compiler's public-egress allowance. Nor is this the shared
[Istio sidecar SNI blacklist](../../infra/istio/templates/egress/blacklist.yaml):
that filter matches a TLS requested server name, not a guest-external control
covering arbitrary traffic. No hostname capability, schema, resource ownership
or execution gate changes. Production `blockedFQDNs` rejection remains intact
for both adapters; passing this experiment is not M3 completion.

Run this bounded offline check with the documented pinned Go toolchain:

```bash
go -C app/cetirun-operator test ./internal/sandboxpolicy -run '^TestHostnameBlockDNSProjectionFeasibility$' -count=1
go -C app/cetirun-operator test -race ./internal/sandboxpolicy -run '^TestHostnameBlockDNSProjectionFeasibility$' -count=1
```

The smallest separately authorized packet trial is a disposable protected
client, controlled DNS and a dual-stack test endpoint serving a blocked and an
unlisted name on the same addresses. Compare the baseline with CIDR denies on
the explicitly selected Cilium version using new named and direct-IP TCP/UDP
connections, then change one DNS answer and observe the update. Record both
denied and allowed controls from outside the guest. This can check the predicted
counterexample, not qualify hostname support; a different hostname-aware backend
still needs a separately agreed design and qualification.

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
