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
deploys the operator. The chart packages CRDs under `chart/crds/`; Helm installs
them initially but does not upgrade or delete them. Follow
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
network policy, or prove enforcement. There is no production adapter or capability
registration yet. The registration revision must represent the qualified
runtime/chart/topology/grant inventory when that integration is implemented.
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
access to the four configuration kinds. It does not mount operator cloud
credentials, registry credentials, or workload Secrets.
Agent validation fetches only its Team and that Team's named Sandbox/policy
references. Shared-configuration checks scan consumers in pages of 200, reuse
named reference reads only within the current request, and fail on incomplete
or expired page reads. There is no cross-request reference cache.

The chart defaults `sandboxAdmission.enabled` and `sandboxAdmission.enforce` to
`false`. Default installations retain their existing behavior. Enabling the
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

This is configuration admission, not complete governed-Pod admission. It does
not yet validate final Pod/extra-container contents, serialize concurrent
shared-configuration changes, coordinate rollout barriers, or permit governed
execution. Reference reads are checks against current objects, not an atomic
cross-resource transaction. Those boundaries must be completed before
production adoption.

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

#### Schema and binary upgrades

Admission deliberately rejects object fields the serving binary does not
understand. The API server's schema validation cannot prove that an older
webhook enforces a newly introduced policy field. Do not treat an unknown field
as allowed or disable fail-closed validation to work around version skew.

For additive upgrades, roll out a backward-compatible admission binary to
**all replicas first**, verify readiness and existing-field requests, then apply
the matching CRDs explicitly, and only then use newly introduced fields.
Helm does not upgrade files under `crds/`. A default added by a newer schema can
reach admission even if clients do not explicitly send it, so applying that
schema before every serving replica understands it is unsupported.
Rollback to an older binary requires first removing unsupported fields and
defaults through a reviewed migration while the newer validator still runs.
Breaking schema changes require a separately reviewed transition.

API-server tests simulate a newer schema and an older handler: objects carrying
the unrecognized field are rejected explicitly, while known-field objects
remain writable. This is not an exemption for new policy semantics.

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
There are no Pod admission rules in this increment, including on Pod deletion.
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
rules. It covers schema/default persistence, strict unknown fields, authority,
reference deletion, outage/recovery and the native cleanup predicate. A separate
loopback serving test exercises the actual admission process and overlapping
certificate rotation. These checks require pinned envtest assets and Helm 3;
see [development setup](../../DEVELOPMENT.md#generated-files-and-dependencies).
They never use an existing cluster, execute an Agent, or qualify a production
issuer, replica-loss recovery, runtime, CNI or CSI.
