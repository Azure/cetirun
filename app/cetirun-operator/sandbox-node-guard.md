# Standalone sandbox node guard prototype

**Production enablement is blocked pending independent review and
unreachable-node fencing.** This is a one-shot, trusted-node CRI experiment,
not a shipped controller integration, pre-start gate, or writer-exclusion
provider. It does not implement `CheckQuiesced`, release an execution hold, or
authorize a replacement writer.

The executable talks directly to the CRI v1 runtime service over a local Unix
socket. It defaults to inspection. Only an explicit `--action=stop` can issue
`StopPodSandbox`, and only for the full sandbox ID whose namespace, Pod name,
Pod UID and runtime handler have all been validated against current CRI data.
There is no name-only stop, remote endpoint, runtime endpoint discovery, or
batch mode.

## Dependency boundary

The operator did not previously declare `k8s.io/cri-api`. This prototype needs
its official CRI v1 protobuf messages and generated gRPC client to call the
real runtime service, plus its server bindings for the local wire-protocol
tests. It adds exactly `k8s.io/cri-api v0.37.1`, aligned with the operator's
existing Kubernetes module versions. Generated CRI bindings are imported from
that module, not copied or maintained in this repository.

The already-selected `google.golang.org/grpc v1.82.1` is now declared directly;
its version and the existing protobuf version remain unchanged. No other
existing dependency versions are upgraded. Dependency setup used Go's scoped
`mod edit` and, after the missing-cache failure, a checksum-verified
`go mod download k8s.io/cri-api@v0.37.1` for only that pinned module. The guard
does not invoke an external runtime CLI or substitute an unpinned library.

## Build and offline tests

Use the repository's pinned Go toolchain and already prepared dependencies.
From the repository root:

```bash
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  CGO_ENABLED=0 make -C app/cetirun-operator build-node-guard

GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator test ./internal/sandboxguard ./cmd/sandbox-node-guard -count=1

GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly \
  go -C app/cetirun-operator vet ./internal/sandboxguard ./cmd/sandbox-node-guard
```

The build produces `bin/sandbox-node-guard` within the operator directory.
It does not build a container image, deploy a Pod, start the operator, or
contact a cluster. The tests use synthetic CRI gRPC services on temporary
local Unix sockets, not a host runtime.

## Explicit invocation

Obtain the expected identity from a separately authorized, trusted source.
The caller must select the correct node and runtime socket; this process
does not query Kubernetes or verify node scheduling or API-server identity.
The following values are synthetic and must not be used to guess a real target:

```bash
app/cetirun-operator/bin/sandbox-node-guard \
  --socket=/run/containerd/containerd.sock \
  --namespace=sandbox-example \
  --pod-name=guard-example \
  --pod-uid=00000000-0000-4000-8000-000000000001 \
  --sandbox-id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --runtime-handler=runc \
  --timeout=15s
```

That command only inspects. For an explicitly approved stop experiment,
append **`--action=stop`** to the same exact-identity command. The guard does
not retry a failed stop RPC, substitute a target, or stop a replacement sandbox.

For safe sandbox ID discovery, omit `--sandbox-id` **only in inspect mode**:

```bash
app/cetirun-operator/bin/sandbox-node-guard --action=inspect \
  --socket=/run/containerd/containerd.sock \
  --namespace=sandbox-example --pod-name=guard-example \
  --pod-uid=00000000-0000-4000-8000-000000000001 \
  --runtime-handler=kata --timeout=15s
```

Discovery still requires the exact namespace, Pod name, Pod UID and expected
handler. It selects only one complete match, pins its full sandbox ID for
status and inventory rechecks, and returns `target.sandboxID` only on success.
Missing, ambiguous, mismatched or changing matches fail without returning a
discovered ID. It never stops a sandbox. For a later approved stop, supply
that returned ID explicitly with `--sandbox-id=FULL_ID --action=stop`;
the full identity is revalidated rather than trusting the earlier receipt.
Stop without an ID is rejected before any CRI call.

All other identity flags and `--socket` are required. The runtime handler flag
must be explicitly supplied, including `--runtime-handler=""` if an empty
handler is expected. Empty is compared literally; it does not mean "any
handler" or infer a default. A handler missing from a runtime response cannot
satisfy a nonempty expected handler.

For a Kubernetes RuntimeClass, pass its top-level `handler`, not its
`metadata.name`. The guard neither fetches nor translates RuntimeClass names.

`--socket` accepts only a clean, absolute, printable-ASCII local Unix socket
path of at most 107 bytes, not `unix://`, TCP, HTTP, DNS or abstract-socket
endpoints. Namespace and Pod name follow Kubernetes DNS naming rules. UID
and sandbox ID are bounded to 128 ASCII characters, start with an
alphanumeric character, and otherwise allow alphanumerics, `.`, `_` and `-`.
Matching is exact and case-sensitive; prefixes are never accepted as IDs.

`--timeout` must be positive and no greater than one minute; the default is
15 seconds. One deadline covers connection setup and all CRI calls together.
Cancellation, deadline expiry and any unsuccessful CRI call fail the
operation. In particular, a stop error or timeout may mean a stop happened
without a confirmed outcome; do not treat it as a no-op or retry automatically.

## What is checked

1. List **all** CRI sandboxes, including non-ready ones. Locate exactly the
   supplied ID and complete Pod identity (or the unique complete match during
   inspect-only discovery). Reject extra records with that ID,
   the same Pod UID, or the same namespace and Pod name. Historical matching
   records are deliberately treated as ambiguous, even if non-ready.
2. Read non-verbose status for the full ID. Require identical Pod metadata,
   runtime handler, creation time, attempt and state across list and status.
   Repeat the inventory/status checks before any stop, retaining the original
   creation time and attempt to reject a changed sandbox.
3. In stop mode, issue one exact-ID `StopPodSandbox`. A successful RPC alone
   is never a successful stopped observation.
4. To report stopped, require fresh inventories and exact status checks to
   report `NOTREADY`, with a filtered container inventory between them.
   Only exited containers or an empty container list are acceptable.
   Created, running, unknown-state, duplicate, malformed or wrong-sandbox
   container records fail the observation.
5. Recheck the complete sandbox inventory and exact status after inspecting
   containers. A replacement ID, active matching sandbox, changed identity,
   handler or attempt rejects the result. No replacement is stopped.

Inspection of a ready sandbox reports `observed_ready` and never issues a
stop. Inspection of a non-ready sandbox uses the same stopped-observation
checks. An already absent sandbox, a missing status, an unsupported response,
or `NotFound` from status/stop is inconclusive, not successful fencing.
Unexpected malformed inventory data also fails closed.

## Receipt and exit codes

Except for `--help`, stdout is a single JSON object followed by a newline,
bounded to 2,048 bytes. CRI response messages are limited to 4 MiB; exceeding
that limit fails rather than accepting a partial inventory. There is no
verbose output, runtime information dump, container metadata dump, environment
dump, log retrieval, or credential output. Errors use fixed reason codes;
raw CRI error messages are never printed.

| `status` | Exit | Meaning |
| --- | --- | --- |
| `observed_ready` | 0 | Exact identity was observed ready; inspect only |
| `observed_stopped` | 0 | Observed stopped **at that moment only** |
| `unknown` | 1 | Insufficient evidence, including an absent sandbox |
| `rejected` | 1 | Identity, replacement, state or container checks failed |
| `error` | 1 | CRI call, cancellation or deadline failure |
| `rejected` with `invalid_input` | 2 | Invalid arguments; no CRI call |

The version-1 receipt includes `action`, typed `status` and `reason`,
`stopAttempted`, `observationScope: "point_in_time_only"`, and the validated
input `target` when available. `observedAt` is the local UTC completion time
of a successful observation, **not an atomic runtime snapshot timestamp**.
The stopped reason is `observed_stopped_at_that_moment`. The following fields
are always false: `durableWriterExclusion`, `preStartAuthorization`,
`completeEnforcement`, and `productionEligible`.

`stopAttempted` means the client invoked the exact-ID stop RPC; on transport
failure it does not establish that the runtime received it. The receipt
contains only the validated caller-supplied Pod identity and, after successful
discovery, the rechecked sandbox ID, never the socket path or runtime-provided
diagnostic payloads. Failed discovery leaves `target.sandboxID` empty.
Treat identity receipts as
operational data; do not publish real workload identifiers.

## Privilege and remaining integration

Access to the node's root-owned CRI socket is effectively host-runtime
privilege. This tool belongs only in a separately authorized, trusted test
process on the selected disposable node, never in an agent workload. Socket
access does not require host networking or other host filesystem mounts.
It neither adds those privileges nor supplies a deployment manifest.

The observations span sequential CRI calls and are not atomic. Kubelet can
restart a container or recreate a sandbox with the **same Pod UID**, including
immediately after the final recheck. A runtime can also remove a sandbox
before its stopped status can be observed; that outcome stays unknown.
No number of finite rechecks makes the receipt durable writer exclusion.
Trust in the local runtime and node is a prerequisite, not something this
prototype establishes.

Operator hold, termination ordering, fallback handling and prevention of
replacement starts still require independent integration and validation.
An unreachable or compromised node cannot be safely excluded by this local
process; independent unreachable-node fencing is still required. Neither
successful offline tests nor a disposable-cluster experiment permits
production enablement or lifting the operator's execution hold.
