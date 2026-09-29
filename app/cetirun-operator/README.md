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

The chart defaults `sandboxAdmission.enabled` and `sandboxAdmission.enforce` to
`false`. Default installations retain their existing behavior. Enabling the
server does not install webhook rules; `enforce` is a separate bootstrap step.
Once enforced, even legacy Agent writes depend on webhook availability, although
healthy admission leaves their Sandbox selection unchanged.

This increment implements:

- Explicit administrator usernames/groups for governed Agent writes and shared
  governance configuration. Kubernetes RBAC must also authorize the operation.
  Portal administrators do not automatically acquire this independent authority.
  A human administrator's Agent still defaults to the `normal` policy class.
- Creation-only Agent selection defaulting, followed by validation against the
  current Team. Existing Agents are never silently re-defaulted. Governed member
  creation is rejected until trusted Portal Team assignment is implemented.
- Shared normalization and compatibility validation, including every referencing
  Team on Sandbox/policy edits, and reference checks before shared deletion.
  Ordinary Portal Team context edits can preserve unchanged governance.
- Protected lifecycle finalizers. Only the configured controller identity may
  change known lifecycle finalizers in a metadata-only update; it has no blanket
  exemption for spec changes. Removal also requires an already-deleting Agent.

There are **no production capability registrations**. Standalone Sandbox and
AgentPolicy definitions can be validated, but governed Team/Agent bindings are
rejected without a qualified registration. The tests inject synthetic
registrations; enabling this process does not make those production evidence
or release the controller hold.

This is configuration admission, not complete governed-Pod admission. It does
not yet validate final Pod/extra-container contents, assign member Teams,
authorize a pending UID binding, serialize concurrent shared-configuration
changes, coordinate rollout barriers, or permit governed execution. Reference
reads are checks against current objects, not an atomic cross-resource
transaction. Those boundaries must be completed before production adoption.

#### Availability and bootstrap

Use an explicitly authorized environment and platform certificate issuer:

1. Provision a `kubernetes.io/tls` Secret containing `tls.crt` and `tls.key` for
   `cetirun-sandbox-admission.<operator-namespace>.svc`. Supply its name through
   `sandboxAdmission.tlsSecretName`; no issuer is installed and no serving
   certificate is generated by this chart.
2. Configure exact `sandboxAdmission.adminUsers`/`adminGroups`, then deploy
   `enabled=true, enforce=false`. Verify TLS, readiness, API read permissions,
   two available replicas and their placement. The chart supplies a disruption
   budget and spreading constraints; rendering does not establish availability.
3. Supply the issuer's base64 PEM CA bundle as `sandboxAdmission.caBundle`.
   After request-path verification, enable `enforce=true` on Kubernetes 1.30+
   (the native match-condition API minimum; local integration tests use 1.37).
   The configurations use `failurePolicy: Fail` and a two-second deadline.
   Neither flag enables runtime execution.
4. During CA rotation, first trust both old and new CAs, rotate the mounted pair,
   verify every replica serves the new certificate, then retire the old CA.
   Each new TLS handshake loads the current pair; invalid or expired material
   fails the handshake and readiness instead of relying on a filesystem watcher
   to keep cached material current. Existing TLS connections may outlive a
   certificate replacement.

The rules do not intercept their own Deployment, Service, RBAC or certificate
Secret. They exclude configuration `/status`, Agent DELETE and Pod DELETE.
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
