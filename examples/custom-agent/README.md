# Custom Agent example

This synthetic Python application demonstrates the Custom image, HOME, model
environment and optional HTTP contracts. It reads the approved context files,
discovers a model through a mock Gateway, and forwards a single request.
It is not a production agent, reasoning loop, Chat UI or history service.

The default operator does not yet have a production `CustomBindingProvider`.
A Custom Agent therefore waits for platform bindings. The example does not
create those bindings or enable Custom in a cluster.

Custom inherits the platform desktop and shared network, exec, disruption and
mesh controls. Desktop is enabled by default even when the Agent has no HTTP
port. The Agent need not provide a native web UI. Desktop keeps its platform
image, UID 1024 and `/home/vibe` mount; the Agent mounts the same PVC at its own
HOME path. Both runtime identities need qualified PVC permissions.

ttyd remains temporarily disabled. TODO: Re-visit ttyd for Custom Agents,
including tool delivery and startup inside the Agent's execution environment.
A separate container sharing HOME is not an equivalent Agent terminal.

## Source qualification

Run from the repository root:

```bash
python3 -B examples/custom-agent/qualify.py
```

The command runs the Custom initialization and protocol tests with Python's
standard library, local temporary files and loopback servers. It uses
`sk-dummy`, never a real model credential. No image is pulled or built.

The tests verify:

- First-copy, recovery and no-recopy behavior through the actual platform init
  script, using only its required basic commands.
- Read-only context mount requirements in the Go chart tests; the shell tests
  separately reject an image `.agents` symlink rather than deleting it.
- HTTP health and inbound model discovery without a Gateway call.
- Consumption of canonical context, the synthetic memory skill and the unique
  example-skill instruction in the actual outbound request.
- Non-streaming output saved in private HOME, normal streaming, interruption,
  cancellation during silent upstream work (including before headers), errors
  and no automatic request replay.
- Cancellation during TCP connection and TLS handshake, while retaining default
  certificate and hostname verification.
- Complete SSE chunk/termination structure and explicit rejection of unsupported
  request fields, including tools and tool selection.
- A headless one-shot check using the same model/context environment.

The mock returns a deterministic marker after inspecting the context it
received. That demonstrates input consumption, not real model reasoning.
The included `cetirun-memory` and `AGENTS.md` are synthetic fixtures, not
approved production material.

The Operator tests additionally cover both Custom chart modes, binding
ownership/revisions, blocked workload replacement, override injection, preserved
native baselines, and resource naming:

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator test ./... -count=1
```

## Image qualification

Docker execution is separate from source qualification. The Dockerfile declares
`USER 2000:2000`, `HOME=/home/worker`, basic copy tools, an initial HOME file and
the example entrypoint. Python is the example application's dependency; the
platform initializer itself does not require it.

After explicitly approving local image work, build the example with an approved
base image. Use an immutable base reference for repeatable image qualification:

```bash
docker build --build-arg "BASE_IMAGE=registry.example.com/python@sha256:<approved-digest>" \
  -t cetirun-custom-example:local examples/custom-agent
```

Prepare an empty synthetic directory writable by the image's UID/GID, then run:

```bash
python3 -B examples/custom-agent/qualify.py \
  --image cetirun-custom-example:local \
  --home-dir /path/to/empty-synthetic-home
```

The helper refuses a missing or nonempty HOME, pulls no image, and never changes
HOME ownership or permissions. It uses the already-present image's local ID for
both init and main. A mounted inspection script checks actual UID, GID, username
and HOME, the completion marker, copied `seed.txt`, and the read-only context
mount. The helper changes the synthetic `seed.txt` to a known restart control,
initializes again, and verifies that both that change and the marker survive.
The headless check must complete one successful mock request and leave the same
result in private memory. The helper preserves the resulting synthetic HOME.
On failure, inspect that directory; the helper does not clear it for retry.

The helper uses Linux host networking only to reach its loopback mock Gateway.
The Kubernetes chart does **not** use host networking. Container HTTP behavior,
Kubernetes probe behavior, CSI permissions, network-policy enforcement, real
models and private Registry credentials remain separate qualifications.
Local image-ID equality is not a Registry digest or production pull attestation.

## Platform integration

`internal/agents/custom` returns logical environment and optional HTTP
contributions. `internal/runtimebindings` defines receipts from platform
capability owners; `internal/controller.CustomBindingProvider` supplies them.
These are internal Go interfaces, not additional CRD or user Helm fields.

| Owner | Required result |
| --- | --- |
| Governance | Exact Agent UID/generation, type and image approval, with a revision |
| Namespace/storage | Approved namespace UID, bound HOME PVC UID/revision and volume group; no new PVC owner reference |
| Model access | Dedicated Opaque Secret with one model-key entry, actual Agent owner UID, API base and revision |
| Context | Approved immutable ConfigMap, UID/revision and explicit key-to-path mappings |
| Network | Approved internal callers and Gateway namespace/pod selectors, and revision; the legacy DNS receipt is validated but shared DNS rules use platform settings |
| Lifecycle/storage | Authorization for the exact next plan, old-writer shutdown, stable ownership and verified storage handoff |

The provider must authorize the requested Team/skill selections and the actual
runtime/storage/network combination before returning its receipt. Reference
validation does not establish those policies on its own. The installed Operator
ClusterRole covers its own Pod get/list operations, referenced objects and
PDB/HPA management. Namespace checks in the controller are still required:
namespaced RoleBindings do not narrow permissions already granted cluster-wide.
The provider must separately qualify namespace identity and network integration;
a namespace UID/revision check does not prove mesh injection or enforcement.

Context mappings must include `AGENTS.md` and
`skills/cetirun-memory/SKILL.md`. All mapped content is read-only under
`$HOME/.agents`; private memory remains in HOME. The controller checks actual
object UIDs/revisions and file presence, while the context owner is responsible
for provenance and approval. The ConfigMap representation has Kubernetes size
limits and is intended for bounded text snapshots, not dependency bundles.

No receipt may come from `spec.helm.values`. The platform supplies shared
network/exec/disruption/scaling settings, Pod annotations, RuntimeClass,
nodeSelector, affinity, tolerations, topology spread and Desktop configuration.
Per-Agent overrides accept resources and Desktop enablement, logging, resolution
and resources. The explicit platform allowlist still rejects unrelated keys;
inapplicable global native settings are not silently consumed. The adapter
never selects arbitrary Secrets, volumes, service accounts, commands or sidecars.

For Pod-affecting updates, the provider must maintain exclusive lifecycle coordination through
application; a one-time observation of no writers is insufficient. The controller
also checks the existing StatefulSet's Agent owner, zero replicas, no active
HOME-using Pods, and unchanged binding objects. Missing coordination leaves
`CustomMaintenancePending` and does not modify the workload.
Image/HOME format compatibility is also a prerequisite for approval. An unknown
or incompatible format must stop the update, not clear the HOME flag.

`ApproveApply` receives a `CustomApplyOperation` containing a unique attempt ID,
the Agent generation and an input-and-manifest digest, and returns a
`CustomApplyLease`. Before returning that lease, the provider must patch the
supplied StatefulSet to zero replicas without changing its UID, owner or
protected annotations, and wait for Pods referencing HOME to terminate. This
requires StatefulSet patch and Pod get/list permissions in that namespace.
Callbacks must honor the controller's 30-second context deadline; long-running
storage coordination must persist progress and return a retryable result.
Its `Check` method rejects expired or revoked permission;
`Complete` receives `not-started`, `applied` or `unknown`, even if status writing
fails. The provider holds coordination until completion. The controller uses an
uncached reader to recheck Agent UID/generation, pause/deletion state and bindings
before application. These checks do not make separate Kubernetes writes a
transaction; the provider still owns physical writer/storage coordination.

An operator-owned completion outbox is written before requesting a lease. The
outcome becomes `unknown` before Helm starts, then records the result before
completion delivery. It remains until the provider acknowledges `Complete`.
After failure or restart, `ResumeApply` recovers a completion-capable handle for
the same Agent UID and operation ID; it does not acquire a new lease or repeat
handoff. Providers must persist operation identities, make completion idempotent,
and retain enough completed-operation state to acknowledge a repeated delivery.
An unallocated operation can only acknowledge `not-started` after the provider
confirms no lease was granted.

A separate workload digest covers the actual Agent/init, model/context and Pod
inputs. Changes only to NetworkPolicy, exec controls, PDB/HPA, mesh scope,
generation or namespace/HOME receipt metadata do not acquire a writer lease.
They still pass plan validation and API dry-run before being applied. Disabled
ttyd options do not enter either digest. Relevant cluster API selections enter
the full plan digest, not the writer digest.

Completion recovery runs before new configuration validation or the
already-applied shortcut, so a changed image or invalid new setting cannot hide
an earlier completion. Suspension can still request zero replicas while a
completion is pending; deletion first stops the owned workload and removes its
route, then waits for acknowledgement before namespace cleanup. Invalid records or
an unavailable provider block recovery; tenant annotations are not used to
reconstruct an operation. Providers must separately recover legacy operations
that have no recorded operation ID. These are internal provider
contracts, not new fields in the public Agent API.

Custom preflight uses the actual Helm action context. CRDs and hooks are
rejected. Case-sensitive decoding rejects unknown/duplicate fields and YAML
keys that collide when converted to JSON. The checked objects are stored as
canonical JSON documents so apply does not reinterpret ambiguous YAML.
Before handoff, the controller performs strict server-side dry-run creates or
patches of those objects; invalid values and insufficient permissions fail
without stopping the writer. Apply uses a
minimal chart containing only those bytes, with hooks/CRDs disabled and an
output check. It does not rerun the original templates or their dependencies.
A successful, matching Helm release record can restore lost Agent status
without another handoff or restart.

Platform RuntimeClass and node-selector values are syntax-checked both before
merging and after Chart defaults are resolved. Empty values retain their
clearing behavior. This does not prove a RuntimeClass exists or that the
selected nodes satisfy the required runtime/storage qualification.

Before the first workload application, the controller stores an immutable
ownership receipt in the configured operator namespace, outside the Agent's
namespace. It records Agent, namespace and HOME UIDs plus release identity,
not credentials. Tenants must not be able to alter these receipts. A finalizer
keeps the receipt available during foreground deletion. Custom cleanup deletes
only the recorded namespace with a UID precondition; it does not run chart
uninstall hooks. Lost or conflicting ownership blocks cleanup rather than
adopting resources by name. A never-bound Agent can finalize without deleting
foreign resources. Namespace deletion still removes its PVCs; this does not
change the requirement to back up data before deleting an Agent.

Registry credentials, Portal forms, Chat UI, chatproxy, production Gateway/Key
issuance, context publication and storage coordination are not implemented here.
The operator's default provider remains unset rather than synthesizing these
dependencies from native tokens or tenant-controlled annotations.

## Failure diagnosis and recovery

Dependency and chart/API outages are not Agent configuration errors. When a
previously applied Pod is still ready, its observed state remains visible while
a separate condition reports the desired update as waiting. Steady-state
reconciliation reuses a bounded in-memory plan cache and rechecks fully at
least every ten minutes; binding changes must trigger reconciliation for faster
rollout. Provider callbacks and API preflight have deadlines.

`ReleaseOperationPending` means Helm history still records a pending operation,
not that the Agent spec is invalid. A maintainer must preserve the relevant
history/outbox evidence, verify the Agent/namespace/HOME UIDs and writer state,
resolve the previous operation with the lifecycle owner, and repair only that
release's history under the normal recovery procedure. The controller does not
automatically delete pending release Secrets or replay an uncertain operation.

Orphan propagation can remove ownerReferences. During deletion, internal
records must still match the original Agent UID and identity; workload/route
cleanup additionally requires protected ownership markers. Foreign owners
remain rejected. Orphan propagation does not change the namespace/PVC deletion
policy described above; it is not a data-retention mechanism.

The example caps concurrent connections at 32 and bounds request/JSON-response/
SSE-event sizes at 1 MiB/4 MiB/256 KiB. Busy connections receive 429; client reads
and response headers time out after 30 seconds, while stream idle reads allow
120 seconds. HTTP and headless modes both handle termination signals.
Source qualification runs the Custom and shared HOME suites, uses fresh context
nonces, and checks actual context bytes reaching the mock. It does not qualify
arbitrary Agent reasoning, real model behavior, CNI/mesh or CSI permissions.
