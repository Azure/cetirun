# Disposable User-pool deletion qualification

This Linux-only command performs a real, conditional AKS **whole-pool DELETE**.
It is restricted to a newly reserved, single-use User pool containing one VM,
with autoscaling disabled, in a cluster with exactly one untouched system pool
and no other User pools.

**No Agent workloads are allowed in this API-only qualification.** The operator
must keep the task-owned cluster isolated from unrelated users and workloads
for the entire experiment. A run tag is ownership correlation, not admission
isolation. This command does not establish that isolation or supply a
`CheckQuiesced` provider, pre-start/data-plane verification, execution permit,
or trusted post-start observation. Production startup remains held. Any pilot
with Agent workloads needs separately qualified admission/start coordination
and writer-history registration; this receipt cannot substitute for them.

## Public API and dependency boundary

The fixed Azure Public ARM operation is:

```http
GET    {poolARMID}?api-version=2026-05-01
DELETE {poolARMID}?api-version=2026-05-01
If-Match: <exact properties.eTag from GET>
```

The [public AKS specification](https://github.com/Azure/azure-rest-api-specs/blob/497a43f48abaaefc756655cc703832e7d2cb7754/specification/containerservice/resource-manager/Microsoft.ContainerService/aks/stable/2026-05-01/managedClusters.json)
defines the ETag field and conditional DELETE. ManagedCluster `resourceUID`
distinguishes cluster incarnations; AgentPool has no published immutable UID.
The helper therefore pins an ETag and refuses any refresh or adoption after
drift. A pool name must never be reused for this registration.

The command uses the already-pinned `azcore` ARM transport. It does not add a
generated ContainerService SDK or change dependency versions. ARM provider
auto-registration and retries are disabled. A transport guard also prevents
DELETE replay through authentication handling; redirects are forbidden.
The 1 MiB response-body cap is applied by the transport before azcore can
buffer the body, including error responses and chunked transfers.
An oversized DELETE reply leaves the durable intent unknown, never completed
or eligible for resubmission.
The Azure CLI credential invokes `az account get-access-token` with a strict
argument array, explicit `--subscription` and the exact
`--resource https://management.azure.com/` audience, without a shell, login,
default-credential chain or alternate credential discovery. Azure CLI rejects
combining `--subscription` and `--tenant` on this command. The helper's
`--tenant-id` instead specifies the required tenant of the returned token;
both returned `tenant` and `subscription` must match the registration before
any ARM request. Missing/mismatched metadata fails without a fallback or
context switch. Tokens are memory-only, not journal or output fields.

Polling supports the documented `Azure-AsyncOperation` and `Location` response
headers, confined to the same subscription's HTTPS AKS operation endpoints.
The monitor API version can differ from the pool API version. A single valid
dated version is required, and the provider's bounded opaque query is retained
verbatim rather than rewritten. Origin, subscription, path and redirect
restrictions still apply.
Location completion follows the pinned SDK's
[Location polling semantics](https://github.com/Azure/azure-sdk-for-go/blob/sdk/azcore/v1.23.2/sdk/azcore/internal/pollers/loc/loc.go).
Compute GET uses `2025-04-01` and the read-only `properties.vmId`. VM resource
names and VMSS instance IDs alone are insufficient because
[instance IDs may be reused](https://learn.microsoft.com/en-us/azure/virtual-machine-scale-sets/virtual-machine-scale-sets-instance-ids).

## Build and offline validation

From the repository root, using the pinned Go toolchain:

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator test ./internal/sandboxfence ./cmd/sandbox-pool-fence -count=1

GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator vet ./internal/sandboxfence ./cmd/sandbox-pool-fence

GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator build -o /tmp/sandbox-pool-fence ./cmd/sandbox-pool-fence
```

Tests use synthetic identities, a local TLS HTTP server and a synthetic SDK
credential. They never invoke Azure CLI or access Azure. These tests do not
qualify the live provider. Live commands below require separately authorized
resources and authenticated access; they are not development checks.

### Test-only managed-disk observation probe

The [disk probe](internal/sandboxfence/disk_probe_test.go) exists only in Go test
code. It reuses the bounded ARM transport and synthetic TLS fixtures; neither
the command nor the Operator gains a disk reader or writer-exclusion provider.
It makes no cloud requests and does not change the empty-pool experiment scope.

The probe exercises the public
[Disk GET 2025-01-02 contract](https://learn.microsoft.com/en-us/rest/api/compute/disks/get?view=rest-compute-2025-01-02):
the resource ID and `properties.uniqueId` must match the separately pinned disk
incarnation, provisioning must be `Succeeded`, and disk state must be
`Unattached`. Top-level `managedBy` and `managedByExtended`, plus
`properties.shareInfo`, must contain no owners. This deliberately narrow
prototype requires explicit `maxShares: 1`; omitted, null or shared capacity
remains inconclusive rather than defaulting to a supported storage tuple.

Regressions reject a recreated disk at the same path, `Reserved` disks belonging
to deallocated VMs, attachment contradictions, duplicate fields, read errors
and disk absence. A completed pool receipt does not substitute for a disk read.
Conversely, an unattached disk and absent old resources do not repair a lost
DELETE reply: reopening the existing journal remains observe-only, with no
destructive replay or fabricated completion.

These are offline protocol checks, not positive physical-fencing evidence.
Even a matching `Unattached` response is only a point-in-time control-plane
observation; it neither prevents reattachment nor proves all historical writers
are excluded. Pre-writer enrollment, a durable start barrier, independent
current exclusion/storage evidence and real no-overlap/data-continuity checks
remain outside this probe. No execution permission or production integration
is introduced.

### Test-only writer-recovery composition

The [recovery probe](internal/sandboxfence/disk_recovery_test.go) composes that
disk reader with the existing `sandboxwriter.Provider` methods, `Store` JSON
journal and `Manager.Quiesce` / `CheckQuiesced` ordering. A fake ConfigMap API
retains the serialized intent across fresh Manager/Provider instances; disk
requests use the existing loopback ARM transport. This adds no production
provider, source activation or schema. The synthetic storage driver's opaque
handle carries the fixture disk incarnation; this is **not** a claim that an
Azure CSI handle pins a managed disk's `uniqueId`.

The additional boundary is evidence composition: a later disk read cannot
refresh stale exclusion evidence, and a delayed exclusion read cannot refresh
an earlier disk sample. The composite observation retains the oldest component
time for the Manager's existing freshness checks, including after journal
reads/writes. Future observations and a backward clock are rejected rather than
hidden by timestamp selection. Even a restamped proof from the previous
operation cannot cover a newly reserved writer epoch.

The tests lose a Revoke reply and, separately, a persisted quiescent-update
reply, then recover through fresh managers without another Revoke. They retain
the journal/operation/enrollment IDs and complete immutable writer/storage
history. Every recovery check reads the disk again, including an already
quiescent journal. Reattached or recreated disks, replaced machine/PV
incarnations, missing independent storage-release evidence, accepted stops,
NotReady nodes, Pod absence and missing durable barriers cannot pass.

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator test ./internal/sandboxfence -run '^TestDiskWriterRecovery' -count=1

GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator test -race ./internal/sandboxfence ./internal/sandboxwriter ./cmd/sandbox-pool-fence -count=1
```

**Production facility remains NO-GO.** Positive barrier, machine-exclusion and
HOME/volume-release assertions in these tests are independently supplied
synthetic fixtures, never inferred from `Unattached` or an empty-pool receipt.
The fake API does not prove API-server or power-loss durability. Before any live
recovery claim, a separately authorized provider experiment must establish
complete pre-writer enrollment, a still-installed guest-external start barrier,
immutable machine/disk binding, independent current storage release, and
no-overlap/data-continuity results across response loss and controller restart.
Passing this offline slice is not full M4 qualification or permission to start
workloads.

## Explicit workflow

The following placeholders must come from the authorized provisioning run:
`POOL_ARM_ID`, `TENANT_ID`, `CLUSTER_RESOURCE_UID` and a new UUID `RUN_ID`.
`TENANT_ID` is the expected tenant for the selected subscription's Azure CLI
account, not an instruction to switch accounts or tenants.
Use a private, durable directory outside the checkout as `PRIVATE_DIRECTORY`.

1. **Before creating the User pool**, reserve the absent ARM ID:

   ```text
   sandbox-pool-fence --action reserve --journal-dir PRIVATE_DIRECTORY \
     --pool-id POOL_ARM_ID --tenant-id TENANT_ID \
     --cluster-resource-uid CLUSTER_RESOURCE_UID --run-id RUN_ID
   ```

   The cluster must already exist. Existing pools are rejected even if they
   have the expected tag. A failed/unauthorized read is not an absence result.

2. The authorized operator creates the new pool, not this command: one
   **User** VM, autoscaling explicitly disabled, and pool tag
   `cetirun-fence-run=RUN_ID`. Do not create Agent workloads or give another
   actor access to the pool. Do not change the system pool.

3. Register the pool ETag and exact Machines-to-Compute `vmId` binding:

   ```text
   sandbox-pool-fence --action register --journal-dir PRIVATE_DIRECTORY
   sandbox-pool-fence --journal-dir PRIVATE_DIRECTORY
   ```

   The second command is the default inspect action. Registration is one-shot;
   missing ETag, identity changes, extra pools or ambiguous machines fail.

4. Explicitly authorize the destructive conditional-API probe:

   ```text
   sandbox-pool-fence --action test-condition --journal-dir PRIVATE_DIRECTORY \
     --confirm-delete-pool POOL_ARM_ID
   ```

   **This sends DELETE with a nonmatching ETag. A service that fails to enforce
   the condition could destroy the disposable pool.** Intent is persisted
   first. Only HTTP 412 followed by unchanged pool/VM identities qualifies the
   next step. Unexpected acceptance, lost response or ambiguous failure stops
   the experiment; never repeat the probe or discard its journal.

5. Submit the matching DELETE once:

   ```text
   sandbox-pool-fence --action delete --journal-dir PRIVATE_DIRECTORY \
     --confirm-delete-pool POOL_ARM_ID
   ```

   Exit 1 with a `deleting` or unknown phase is expected for asynchronous or
   uncertain submission. **Never repeat DELETE.** Observe instead:

   ```text
   sandbox-pool-fence --action observe --journal-dir PRIVATE_DIRECTORY
   ```

   Each observe is one bounded read pass; repeat it after pending/read errors.
   `--timeout` defaults to 30 seconds and cannot exceed five minutes. Cancellation
   does not cancel the cloud operation or authorize a new DELETE.

Completion requires a terminal successful operation, authoritative pool
absence, and authoritative absence at every recorded old Compute VM resource,
with the same cluster incarnation still present. A replacement at an old
resource path is left untouched and does not count as absence. Neither an
initial 204 nor a missing operation monitor establishes operation success.
Running/failed/canceled operations, VM deallocation, Node readiness and Pod
absence cannot produce completion. A completed journal is a historical receipt
for this exact empty-pool experiment, never permission to delete a later pool.

If a pending journal already contains the durable HTTP 202, a recognized poll
kind and an operation handle accepted by the current validator, `observe` may
resume polling after verifying the cluster incarnation. It never resubmits
DELETE. Missing response evidence or an invalid/foreign handle stays unknown.

For these fixed, identity-checked GETs, a 404 absence response must contain
`NotFound`, `ResourceNotFound` or `ParentResourceNotFound` in either the ARM
`error` envelope or the bare AKS error-detail object (`code`, `message`,
`details`). The
[public error-detail schema](https://github.com/Azure/azure-rest-api-specs/blob/497a43f48abaaefc756655cc703832e7d2cb7754/specification/common-types/resource-management/v6/types.json)
defines those detail fields. Synthetic HTTP fixtures cover the observed bare
AKS `NotFound` shape. Empty/HTML/malformed replies, authorization errors, wrong
HTTP status, duplicate fields and simultaneous bare/enveloped codes remain
unknown; they do not establish absence. Cluster-incarnation checks still
surround reservation, and no error response permits target adoption.

## Journal and recovery

The private directory is mode 0700; `lock` and `journal.json` are mode 0600.
The journal binds the fixed API/scope, tenant, subscription via pool ARM ID,
cluster resourceUID, run UUID, node resource group, ETag and Compute identities.
It records separate condition-probe and final DELETE intents, request IDs,
HTTP outcomes, operation handle/status, and final destruction observations.
It contains resource identities and operation handles, but no Azure CLI bearer
token or workload credentials/contents. Operation handles can include opaque or
signed query values. Treat the whole journal and command output as sensitive:
keep real receipts private and out of Git.

A nonblocking exclusive lock covers each invocation. Each transition writes a
bounded temporary file, fsyncs it, atomically renames it and fsyncs the directory
**before** a destructive side effect or completion output. Symlinks, unsafe
permissions, malformed state and phase rollback are rejected.
Every journal open also fsyncs its parent directory, including retries with an
already-existing directory and resumed operations. The directory entry must
be persisted before a command can succeed or contact ARM. Parent-sync failures
propagate without creating a success record or sending a destructive request.

An authentication failure before the first successful reservation can leave a
directory and lock file without `journal.json`. Retry reservation only after
confirming that the actual `journal.json` is absent; do not delete or reset an
existing journal to recover from an error. Keep stdout captures outside the
helper-owned journal files. A stdout error document is not a reservation or
mutation receipt, and must never be written over `journal.json`.

Retain the journal permanently on durable local storage. Do not copy it to
another host, run multiple copies, reset its phase, remove an intent, refresh
its ETag, or reuse the pool name. A crash after persisted intent but before a
confirmed response is intentionally ambiguous: there is no blind resubmission
or unsupported operation-discovery fallback. Missing operation handles require
separately authorized provider investigation; absence alone does not repair
that evidence gap. Networked/shared filesystems and multi-host coordination are
not supported by this local journal.
