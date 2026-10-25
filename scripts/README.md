# CetiRun scripts

The [script reference](../site/content/en/docs/reference/scripts.md) covers
local checks, container builds, cloud deployment, and publication tools,
including the platform umbrella and independent Istio helpers.

For local chart validation, run from the repository root:

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
```

Documentation validation is `make -C site check`. It does not run deployment,
publication, or end-to-end scripts.

## CetiRun Connections MCP artifacts

`cetirun-connections-mcp-docker-build.sh` builds the standalone MCP image from the
repository root. Set `REGISTRY`; `VERSION` defaults to the central release
metadata. `PUSH=false` loads a local image and `PUSH=true` publishes it.

`cetirun-connections-mcp-helm-release.sh` lints and packages the independent chart,
pushes it under `oci://$REGISTRY/${CHART_NAMESPACE:-helm}`, and verifies the
pull unless `VERIFY_PULL=false`. Neither helper logs in or deploys resources.
Image builds can download pinned dependencies and base images; publication
requires explicit authorization and registry authentication.

The root build/publish targets include these artifacts. They are not added
to the platform umbrella or Agent runtime by these helpers. For offline
validation without Docker or registry access, use:

```bash
make -C app/cetirun-connections-mcp test vet chart-lint
python3 -B -m unittest scripts.tests.test_connections_mcp_chart -v
```

## Explicit-target application E2E lifecycle

`e2e-test.sh` is a connected application smoke test, not an offline check.
Run it only after authorization for the exact environment and its test resources.
It now requires an absolute `E2E_KUBECONFIG`, an explicit `E2E_CONTEXT`, and an
independently obtained `E2E_CLUSTER_UID` (the `kube-system` namespace UID).
Use a trusted pre-provisioned HTTPS kubeconfig without exec/auth-provider login
plugins, and do not change that file or context during a run. Every kubectl and
Helm invocation carries the explicit target; the ambient current context is
not used.

```bash
E2E_KUBECONFIG="$TEST_KUBECONFIG" \
E2E_CONTEXT="$TEST_CONTEXT" \
E2E_CLUSTER_UID="$TEST_CLUSTER_UID" \
AGENT_TYPE=openclaw bash scripts/e2e-test.sh
```

The default alias is generated per invocation. If `TEST_ALIAS` is supplied,
both it and its derived namespace must be absent. Existing resources, forbidden
reads and failed observations stop the run; there is no leftover adoption,
pre-run deletion, `apply` takeover or forced namespace cleanup.

Creation records the API-issued Agent UID and a run label. Suspend/resume use
JSON Patch tests for the current UID and resourceVersion; conflicts are not
automatically rebased/retried. Cleanup rechecks cluster/Agent ownership and
any namespace owner reference, submits at most one conditional Agent Delete,
then observes both Agent and namespace absence through normal controller
finalization. A replacement identity, unknown response, read error or timeout
is reported as incomplete cleanup and causes a failing exit. It never removes
finalizers or deletes a namespace directly.

`READY_TIMEOUT` (default 600 seconds) and `CLEANUP_TIMEOUT` (default 180 seconds)
accept positive values up to 9999. Individual Kubernetes requests use a
15-second request timeout, which can extend the final polling iteration beyond
its deadline. An ambiguous creation without a returned UID is retained for
explicit recovery, never adopted by its name or label.
`SKIP_CLEANUP` retains resources only after successful application checks;
that explicit choice is reported and may incur charges. Keep the alias and
private run output for authorized cleanup.

The application checks retain their existing scope:
this does not implement governed creation, prove physical writer fencing,
qualify enforcement or complete a real model/tool acceptance run. The existing
Hermes check still uses Pod exec and is not a governed-Pod admission workaround.
All six live workflow jobs remain disabled. Before independently enabling the
test job, provision its `E2E_CONTEXT` and `E2E_CLUSTER_UID` secrets together with
its existing kubeconfig; the job uses a task-local file rather than overwriting
the runner's default kubeconfig.

Offline lifecycle regressions replace kubectl and Helm with synthetic processes:

```bash
python3 -B -m unittest discover -s scripts/tests -p test_e2e_lifecycle.py -v
```

### Offline lifecycle evidence assertions

`sandbox-platform-qualify.py acceptance-check --receipt FILE` checks a local,
sanitized JSON receipt. It reuses the qualification helper's bounded, strict
JSON parser and fixed reason codes. It does **not** collect evidence, run another
lifecycle runner, read Kubernetes configuration, sign in, call a model, or change
resources. The receipt fields are validation inputs, not new Agent status fields,
annotations or API requirements.
For a narrow current-resource collector, see
[read-only lifecycle snapshots](#read-only-lifecycle-snapshots) below.

Run the offline regression entry point from the repository root:

```bash
PYTHONPATH=scripts/tests python3 -B -m unittest \
  test_lifecycle_acceptance test_lifecycle_snapshot test_e2e_lifecycle \
  test_sandbox_platform_qualify test_sandbox_deployment_preflight -v
make python-check license-check
```

The existing `make ci-test` entry point discovers these tests as well. The
[synthetic fixture](tests/fixtures/lifecycle_acceptance.json) intentionally has
no authorization or observations:

```bash
python3 -B scripts/sandbox-platform-qualify.py acceptance-check \
  --receipt scripts/tests/fixtures/lifecycle_acceptance.json
```

**Expected result: exit 1, `status: blocked`.** The executable
[test receipt builder](tests/test_lifecycle_acceptance.py) adds synthetic
measurements for positive and negative assertions; those measurements are never
live evidence. No test sends fabricated identity headers to a service.

#### Coverage and support boundary

This is an **assertion coverage matrix**, not a new runtime support declaration.
Product availability must be checked on the exact selected source, images,
charts, feature configuration and platform tuple before collecting a receipt.
In particular, retained native operations and native Copilot/helper combinations
need an integrated common-execution candidate; this main-based helper does not
deliver that candidate or enable its features.

| Dimension | Fixture coverage | Connected acceptance |
| --- | --- | --- |
| OpenClaw / Hermes; ordinary / retained `sandbox-native`; shared / Copilot-only / mixed | All 12 core tuples | Blocked until the exact supported artifact, target, owners and providers are authorized |
| Each core tuple with no optional identity / Entra OBO / Azure app-only, and each of skills / Memory / doctor / logger / desktop | 180 single-helper assertion tuples, plus a combined-helper case | Conditional on actual runtime integration and helper/identity availability; no native parity inferred from ordinary support |
| Owner create, restart, suspend/resume, delete; native HTTP and WebSocket | Required owner-path and distinct-owner negative assertions | Blocked without real OIDC owner and peer sessions and available owner controls |
| Healthy-source native migration | At least two administrator-only moves, each with source termination and CSI release/attach observations | Separate bounded authorization required; no owner migration action |
| Custom images, gVisor, failed/unreachable-source recovery, physical fencing | Not covered; unknown runtime/execution values are blocked | Outside this checker; no implied support |

Each tuple needs its own receipt. Helper combinations beyond the tested
single-helper rows and combined case, other platforms, and optional identity
integrations are not qualified by neighboring rows. The existing
[Harness contract](../site/content/en/docs/reference/agent-harness.md),
[identity availability](../site/content/en/docs/guides/agent-identity.md) and
[protected renderer](../app/cetirun-chart/README.md#protected-sandbox-rendering)
remain the product support sources.

#### Required observations

The fixture supplies the v1 envelope. The test builder shows the complete
snapshot, operation, request and cleanup shapes. The reusable
`assess_lifecycle_receipt` function returns ten independent check results:

| Check | Required evidence; rejected substitutes |
| --- | --- |
| `artifacts` | Full source commit **and tree**, digest-pinned component images and charts, and a digest of the reviewed non-secret configuration/tuple manifest. Every snapshot, request case and cleanup must carry the same recomputed fingerprint of the artifacts **and case**. Tags, tuple relabeling, a shared run label or receipts from different builds are insufficient. |
| `authorization` | Explicit target, owner, peer, provider, bounded spend and fault authorization, and owner-control availability. Missing prerequisites stay blocked; `true` in a file does not grant permission. |
| `owner-access` | Real OIDC owner REST/Portal access, stable distinct owners, denied peer reads/mutations, native HTTP 200 and WebSocket 101. Administrator patches, unauthenticated redirects and synthetic identity headers cannot replace these observations. |
| `home` | Retained Agent/namespace/StatefulSet/PVC/PV identities, a nonempty known synthetic file checksum manifest, matching contents and continued read/write through an allowed owner/application path after every operation. PVC identity alone or protected Pod exec is insufficient. |
| `accounting` | The same key and budget, strictly positive spend before the journey, nondecreasing cumulative spend across operations, and additional spend from each subsequent model call within the budget. Decimal strings avoid floating-point comparisons. `0 -> 0`, key replacement or budget reset fails. Copilot-only must not silently use a shared priced model; lack of an authorized priced path is a blocker. |
| `recovery` | Separate apply/request acceptance, stop request, observed shutdown/source termination and actual current-workload Ready witnesses bound to the retained Agent UID, current generation and Pod UID. Every operation identifies the preceding source Pod/node. Each selected model source needs a reply and a fresh, independently checked tool challenge after every operation; reusing an earlier checksum fails. Native moves additionally require a healthy source, distinct destination and CSI witnesses. Generation or a status label alone cannot establish completion. |
| `peers` | Another owner's independent Agent, Pod, HOME PVC/PV, key and budget remain unchanged and Ready through cleanup; peer spend cannot decrease. No recovered owner Pod or retained HOME volume may be the peer's. An unused peer may have zero spend. |
| `requests` | Duplicate, stale UID/version, unknown reply, same-version takeover, suspend/delete precedence, and native in-flight replacement/destination-failure cases. Preserve the original request fingerprint and accepted operation's ID/Agent UID/generation/source Pod, bound time and apply counts, and compare reported state with authoritative, current-workload observations. Completion needs a nonzero apply count and a fresh Ready Pod; pending/retryable and terminal failure remain distinct. |
| `cleanup` | Ordinary owner deletion of the same retained Agent UID, independently observed absence of Agent/namespace/HOME volumes/attachments/control records/reservations/key, and retained peer health. A retained suspended Agent, an unobserved timeout or forced finalizer/detach operation is not cleanup. |
| `extensions` | For selected helpers: read-only unchanged skills; private usable Memory; ready doctor/logger; owner-only desktop. Optional identity adds authorized use, peer denial and revocation observations; Azure app-only also requires the MCP path. Merely configuring a connection does not establish these facts. |

Use local consistent pseudonym digests for object/owner/key comparisons, never
raw identities, credential hashes, tokens, cookies, endpoints, conversations or
diagnostic bundles. HOME/tool entries are bounded synthetic witness labels and
checksums, not user files. Artifact fingerprints are SHA256 over
`{"artifacts": artifacts, "case": case}` serialized as sorted-key compact JSON,
with `case.helpers` sorted; use
`lifecycle_artifact_fingerprint` rather than inventing a label. Reconfirm deployed
pins at every observation; the checker cannot authenticate a collector's claims.

The following fields bind the measurements; they are receipt projections, not
new product API fields:

| Record | Binding |
| --- | --- |
| Snapshot `resources` | `agent`, `namespace`, `statefulSet`, `homePvc`, `homePv` are consistent pseudonym digests of the retained Kubernetes UIDs, not names. |
| Snapshot `workload` | `agent` equals `resources.agent`; `pod` and `node` identify the current execution. `generation` is its selected Agent generation, not a local step counter. `observedGeneration` must equal it, and `observedPod` must equal `pod`; `ready` must be `true`. Successful recovery snapshots require fresh Pod UIDs and increasing generations. |
| Operation | `sourcePod` and `sourceNode` must equal the preceding snapshot's workload. All observation flags are JSON booleans, including CSI flags on non-move operations; use `false` when those observations are not applicable. |
| Model witness | `expectedToolChecksum` pins a new synthetic challenge for that source and observation. `observedToolChecksum` is read independently of the model reply. Both must match and the checksum must not have appeared in any earlier model witness. |
| Peer | `home` is the peer HOME PVC UID digest; `homePv` is its backing PV UID digest. Both remain unchanged and differ from the owner's retained volumes. |
| Request `operationBefore` / `operationAfter` | Each has exactly `id`, `agent`, `generation`, `sourcePod`. The ID, Agent UID and source Pod UID are pseudonym digests; generation is a positive integer. Both records must be identical and belong to the retained Agent. For rejected retries, observe the original protected operation, not an invented replacement for the rejected request. |
| Request `workload` | Exactly `agent`, `generation`, `observedGeneration`, `pod`, `observedPod`, `ready`; bound to the original operation's Agent/generation. Pod fields must match; both are `null` when no current Pod is observed. Ready requires the current generation and a Pod. Completion additionally requires a Pod different from the original source. Suspended/deleted outcomes require no current Pod; pending/failed outcomes cannot report Ready. |
| Request failure | `retryable` is `true` only for a pending outcome. `failureObserved` is `true` only for the destination-failure case, including when it remains pending/retryable. These are independent observations, not an inference from submission acceptance or timeout. |
| Cleanup | `agent` identifies the retained Agent being deleted. All `remaining` values are observed integer zeroes, never booleans or inferred absence following an unsuccessful read. |

To print a complete, machine-readable **synthetic** shape without collecting
evidence or running a lifecycle:

```bash
PYTHONPATH=scripts/tests python3 -B -c \
  'import json; from test_lifecycle_acceptance import receipt; print(json.dumps(receipt(), sort_keys=True))'
```

The builder accepts `runtime`, `execution`, `model`, `identity` and `helpers`
arguments for the matrix above. Never relabel this example as live evidence.
Missing binding fields in older receipts remain blocked; do not fill them with
invented identities or observations to obtain a match.

Unknown submission must retain its original intent until an authoritative
observation shows pending or completed work; absence is not rejection or
cancellation. Confirmed stale requests remain rejected, not silently rebased.
A pending failure can match its bounded negative-case assertions without being
a successful migration. `retryable` describes internal operation recovery, not
permission to replay an unknown submission. Retained stop is neither destruction
nor physical writer exclusion. Reuse existing controller/unit/envtest cases for fault
injection rather than adding a second implementation here.

Output contains only validated case enums, the artifact fingerprint, fixed check
names/reasons and the supplied evidence kind. It never echoes input paths,
observed resource identifiers or exception text. `status: matched` and exit 0
mean only that the **reported scoped measurements** meet these assertions.
`qualifiesExecution` is always `false` and `liveAcceptance` always
`not-established`, including receipts labeled `live`: independent evidence
review and the complete supported matrix are still required. Missing evidence
returns `blocked`; contradictory evidence returns `failed`; both exit 1.
Malformed command lines exit 2. Input must be an explicit local regular file
(not a pipe or device) and is capped at 1 MiB; duplicate JSON fields
and nonfinite numbers are rejected. Keep live receipts and private validation
reports outside public PRs; never merge historical versions into a new pass.

### Read-only lifecycle snapshots

`sandbox-platform-qualify.py lifecycle-snapshot` collects a small set of current
Kubernetes facts for **one explicitly selected Agent and run**. It is not a
lifecycle runner, an acceptance receipt, or authorization to use an environment.
Only run it after separately authorizing read access to that exact target:

```bash
python3 -B scripts/sandbox-platform-qualify.py lifecycle-snapshot \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --scope "$REVIEWED_SNAPSHOT_SCOPE"
```

Supply a trusted, absolute, static-credential kubeconfig and independently obtained
pins; never adopt discovered resources or the ambient current context. Both local
inputs must be regular files, not symlinks, directories, devices or pipes. Keep
real inputs private. The scope JSON is capped at 64 KiB and has **exactly** these
fields:

| Field | Required input |
| --- | --- |
| `version` | `"v1"` |
| `run` | One explicit, nonempty DNS-style run-label value, at most 63 characters |
| `case`, `artifacts` | The existing acceptance receipt shapes above, including full source commit/tree, configuration digest, and all required image/chart digests; no tags |
| `resources` | The seven entries shown below; no additional discovery inputs |

Example **synthetic `resources` fragment**, not a runnable live scope:

```json
{
  "agent": {
    "name": "selected-agent", "uid": "agent-uid",
    "resourceVersion": "1", "generation": 3
  },
  "namespace": {"name": "snapshot-example", "uid": "namespace-uid"},
  "statefulSet": {"name": "selected-runtime", "uid": "statefulset-uid"},
  "pod": {"name": "selected-runtime-0", "uid": "pod-uid"},
  "homePvc": {"name": "selected-home", "uid": "pvc-uid"},
  "homePv": {"name": "selected-pv", "uid": "pv-uid"},
  "attachment": {"name": "selected-attachment", "uid": "attachment-uid"}
}
```

Use `attachment: null` explicitly when no attachment identity has been pinned.
This leaves CSI attachment unknown and exits 1; it never discovers attachments
cluster-wide or interprets the missing pin as absence/release. Names and UIDs
are mandatory for every other entry. Agent generation and resourceVersion are
also mandatory; replaced or stale pins are not refreshed automatically.

The supported collection footprint is deliberately strict:

- The Agent and its controller-owned namespace must both carry the exact
  `qualification.cetirun.microsoft.com/run` label. Workload/storage objects are
  scoped by the pinned identities and ownership/binding chain; any run label
  they carry must agree. The namespace must be Active and the Agent report Ready
  for the pinned generation and selected runtime. No owner authentication is
  inferred from the Agent's user information or from administrative access.
- There must be exactly one StatefulSet in that namespace, controller-owned by
  the Agent, at one current Ready replica. The main Pod must have the pinned UID,
  that StatefulSet controller owner, matching selector/revision, Running/Ready
  status and one ready `cetirun` container. The selector must use precisely the
  chart's `app.kubernetes.io/name` and `app.kubernetes.io/instance` labels.
  A namespace-scoped StatefulSet list and a selector-scoped Pod list each use
  `limit=2`; empty, multiple, paginated or truncated results stop collection.
  Other layouts, suspended workloads and in-progress rollouts remain blocked.
- The template and Pod must declare the same digest-pinned main image matching
  `artifacts.images.runtime`; the reported main-container `imageID` digest must
  also match. A differing platform-image digest is **unknown**, not silently
  adopted from a multi-architecture tag/index. No registry lookup occurs.
- Both template and Pod must explicitly mount volume `data` from the pinned
  HOME PVC at `/home/vibe`, without subpaths or a read-only declaration.
  Implicit claim-template layouts are outside this slice. PVC and PV must be
  Bound, filesystem-mode and linked by the exact claim UID/name/namespace.
  Only the explicitly named CSI VolumeAttachment is checked, including its UID,
  PV, driver, Pod node name, reported attached flag and absence of reported errors.
  This neither inventories other attachments nor proves physical attachment,
  detachment, writer exclusion or actual HOME read/write.

This mode reuses the deployed-preflight POSIX runner: 5-second request timeout,
10-second command deadline, 90-second total deadline, 64-command ceiling,
1 MiB stdout cap per command and a separate one-second process-cleanup wait.
Only redacted local `kubectl config view --minify` and allowlisted API GETs run:
the pinned `kube-system` namespace, Agent, namespace, StatefulSet, Pod, HOME
PVC/PV, optional VolumeAttachment and the two bounded lists above. It never reads
Secrets, Nodes, application logs or HOME contents; no exec, attach, port-forward,
proxy, watch, mutation, login, model call or retry is available. Credential plugins,
token-file/impersonation indirection and external client-key/certificate files are
rejected before API reads. Do not replace the trusted kubeconfig or `kubectl`
during collection.

Every selected object and candidate set is rechecked, together with the redacted
context selection and kubeconfig file identity. UID, resourceVersion, content,
run-label or configuration drift blocks the snapshot. These checks detect
observed changes; they are not an atomic API snapshot or a promise of future
state. Failed/forbidden GETs never establish absence or cleanup.

One sanitized JSON object below 8 KiB is emitted. `checks` use the existing
`observed` / `unknown` / `blocked` statuses and fixed reason codes:

- Exit 0 and `status: observed` mean only that the selected current-object
  checks matched. `qualifiesExecution` stays `false` and `liveAcceptance` stays
  `not-established`. Exit 1 reports a blocker/unknown; malformed arguments exit
  2, and cancellation exits 130.
- After stable reads, `snapshot` reuses the receipt's `artifactSet`, `resources`
  and `workload` projections. Resource UIDs, the Pod UID, node **name** and
  cluster/context/run scope are pseudonymized with SHA256 of compact JSON
  `[clusterUID, role, value]`, without raw names, identifiers, endpoints, paths,
  credentials or diagnostics. Keep the same convention when assembling later
  observations; node-name equality does not establish node/provider identity.
- `workload.observedPod` identifies the independently read Pod, but
  `observedGeneration` and `ready` remain `null`. The controller's
  `reported.agentObservedGeneration` and the independently read
  `reported.podReady` do not establish generation-bound runtime readiness.
  `storage` reports only the checked bindings/attachment; `csiReleased` is
  always `null`. With no attachment pin, the other stable projections are
  retained, the attachment check is unknown and the overall result is blocked.
  Other failures leave `snapshot: null`; earlier checks are only partial reads.
- `owner`, `home`, `accounting`, `models`, `peer` and `extensions` remain `null`.
  No owner/provider authorization, model/tool witness, HOME checksum, nonzero
  spend, peer continuity, source shutdown or cleanup proof is manufactured.

The artifact fingerprint binds the **supplied** case/pins, not proof that the
source, charts, other component images or effective case are deployed. Those
facts and collector authenticity remain explicitly `unverified`, as do the
acceptance observations above. An edited JSON file or successful administrator
snapshot is not real-owner acceptance, billing evidence, a signed attestation
or live qualification. Independently authorized collection and review of the
remaining evidence are still required.

Offline regressions replace every kubectl subprocess and use synthetic JSON:

```bash
PYTHONPATH=scripts/tests python3 -B -m unittest \
  test_lifecycle_snapshot test_lifecycle_acceptance \
  test_sandbox_deployment_preflight test_sandbox_platform_qualify test_sandbox_udp_probe
make python-check license-check
```

### Native ingress sign-in challenge

The application runner uses `e2e-ingress-check.py` to request the canonical
`https://<domain>/ui/<agent>/` route, not an instance subdomain. `DOMAIN` is an
explicit override of the operator release's `domainSuffix`; missing/unreadable
values fail instead of falling back to an example domain. `E2E_OIDC_CLIENT_ID`
can supply the expected public application identifier; otherwise the runner
reads `clientId` from the selected cluster's Istio release.

The helper sends one unauthenticated GET with certificate and hostname
verification. It does not read an Agent Secret, send a token/cookie, follow a
redirect, contact the identity provider, use ambient HTTP proxies or enable
ambient TLS key logging. System CA trust is the default; `E2E_CA_FILE` selects
an absolute PEM CA bundle when the approved target needs a private test CA.
There is no insecure verification option.

For a separately authorized HTTPS target, the helper can also run directly:

```bash
python3 -B scripts/e2e-ingress-check.py \
  --origin "$TEST_HTTPS_ORIGIN" --agent-name "$TEST_AGENT_NAME" \
  --client-id "$EXPECTED_OIDC_CLIENT_ID"
```

It accepts only HTTP 302 with a single Location naming the exact public
`https://login.microsoftonline.com/<tenant>/oauth2/v2.0/authorize` endpoint,
the expected client ID, `response_type=code`, and the exact
`<origin>/oauth2/callback` redirect URI. Missing/duplicate/mismatched query
parameters fail. Other statuses, including 200, 401 and 403, are not evidence
that the expected sign-in challenge is wired correctly. Sovereign-cloud issuers,
intermediate redirects and other layouts are outside this narrow check.

Accepted response headers are limited to 16 KiB and Location to 8 KiB; Python's
HTTP parser also applies its native line/count limits. The body is not read.
Socket operations have a ten-second timeout and the POSIX command has a
30-second worker deadline, including DNS/TLS/header collection. A separate
supervisor enforces that deadline without relying on Python signal delivery
inside the resolver. On timeout or SIGINT/SIGTERM it kills the worker and waits
up to one additional second to confirm reaping before returning. Unavailable
process isolation, abnormal worker exit or unconfirmed cleanup fails the check.
The helper requires POSIX `fork` support. Output is one small JSON object with
fixed reason codes, never the origin, response headers, cookies, redirect state,
identifiers or raw transport diagnostics.

Exit zero means only `status: observed` for that unauthenticated challenge.
`authenticatedAccess` remains `not-tested` and `qualifiesExecution` remains
`false`. Owner sign-in, authorized native HTTP/WebSocket access, downstream
application readiness and model/tool execution require separate acceptance.
Transport/validation failure exits 1, invalid inputs exit 2, and cancellation
exits 130/143. The runner retains its ordinary conditional cleanup on failure.

Offline tests use response doubles and ephemeral local TLS servers/certificates:

```bash
PYTHONPATH=scripts/tests python3 -B -m unittest \
  test_e2e_ingress_check test_e2e_lifecycle -v
```

On Linux/glibc, a local C compiler (`cc`) also enables native `getaddrinfo`
regressions for the deadline and SIGINT/SIGTERM cleanup. A temporary fixture
directs only the worker's resolver to a nonresponding loopback DNS socket with
30-second retries. These tests do not change host resolver configuration or
contact external DNS. They are explicitly skipped without Linux/glibc or `cc`.

## Read-only deployed Sandbox preflight

`sandbox-platform-qualify.py deployment-preflight` inspects a small, explicitly
selected deployment of the existing Operator chart. It is separate from the
older RuntimeClass/storage/Cilium `preflight` and the mutating `network-smoke`.
It does not start an Agent, qualify a provider, or authorize execution.
Stock startup leaves `WriterExclusion` unavailable and execution held; enabling
Kubernetes sources does not change that.

Only run this connected command after authorization for the named environment.
Supply a trusted, static-credential kubeconfig, an explicit context, the expected
`kube-system` namespace UID, and independently obtained Deployment and
ServiceAccount UIDs and SHA256-pinned image references. Do not obtain pins by
implicitly adopting whatever the preflight discovers. Keep real inputs local.
The helper does not log in or invoke credential plugins.

```bash
python3 -B scripts/sandbox-platform-qualify.py deployment-preflight \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --namespace cetirun-system \
  --operator-deployment cetirun-operator --operator-uid "$OPERATOR_UID" \
  --operator-image "$PINNED_OPERATOR_IMAGE" \
  --operator-service-account cetirun-operator-controller-manager \
  --operator-service-account-uid "$OPERATOR_SA_UID" \
  --admission-deployment cetirun-sandbox-admission --admission-uid "$ADMISSION_UID" \
  --admission-image "$PINNED_ADMISSION_IMAGE" \
  --admission-service-account cetirun-sandbox-admission \
  --admission-service-account-uid "$ADMISSION_SA_UID"
```

The supported footprint is deliberately narrow:

- A stable Kubernetes version at least 1.37 (prereleases are blocked).
- The selected Deployments in one explicit namespace, distinct pinned service
  accounts, exact digest-pinned images for the native `manager`/`admission`
  containers and their process modes. Desired and observed rollout generations and
  replica counts must agree. Operator replicas are bounded to 1-4 and admission
  replicas to 2-4; this helper does not wait for rollouts.
- Inline `CETIRUN_SANDBOX_SOURCES` and `CETIRUN_SANDBOX_ADMISSION`, matching
  controller/admission identities, controller state namespace, strict admission,
  explicit administrator lists and distinct HOME/skills claim names. Platform
  and placement ConfigMap references must agree between the two processes.
  Only the named ConfigMaps' identities and bounded `platform.json` /
  `placement.json` v1 document envelopes are checked. Their declarations,
  capabilities and references are **not** validated as runtime evidence.
- The chart's fixed `cetirun-sandbox-admission` Service, its selector and
  443-to-9443 listener, readiness probe and TLS Secret **reference**, plus its
  exact-name core/v1 Endpoints object. No Secret is fetched. Two to four ready
  endpoint Pods must retain their UIDs, match the selected admission image,
  service account and relevant process configuration, and trace through named
  ReplicaSets to the selected Deployment UID. Their reported node names must
  span at least two nodes; no Node is read or qualified.
- The chart's fixed validating/mutating `cetirun-sandbox-admission` webhooks
  and validating `cetirun-sandbox-pods` / `cetirun-sandbox-placement` webhooks:
  service/path/CA-encoding wiring, matching CA bundles, resource rules and
  selectors, fail-closed settings and bounded request timeouts. The admission
  process's explicit `ms` or `s` request budget must be shorter than each
  webhook timeout.

The exact legacy two-rule Pod webhook remains supported when
`sandboxSources.plannedMigration` is absent or `false`. The current three-rule
form adds `CREATE` for both `pods/binding` and `bindings` and requires the
public chart's garbage-collector cleanup predicate. `plannedMigration: true`
requires native execution, that complete Binding rule, and the chart's
controller and Pod cleanup guards. The flag must be a JSON boolean.
Nonempty match-condition lists must contain exactly the applicable public
`_sandbox-admission.tpl` predicate, with the pinned controller identity where
needed; unknown, duplicate or altered conditions fail. Only legacy,
non-migration wiring may omit guards. Matching predicate text does not verify
CEL execution, namespace policy or migration readiness.

Unsupported layouts, indirect environment configuration, missing/forbidden
resources, identity/version drift, malformed responses and timeouts stop the
inspection with a blocked or unknown check. There is no fallback to Secret
reads, EndpointSlice discovery, alternative workloads or the current context.
The helper rechecks object UID/resourceVersion pairs, API version and redacted
kubeconfig selection before accepting the observations. This detects changes
during collection, not an atomic snapshot or a promise about future state.
The trusted local kubeconfig and `kubectl` must not be replaced during a run.

### Requests, bounds and output

This mode uses only local, redacted `kubectl config view --minify` and exact
`kubectl get --raw` GETs. The allowlist is `/version`, the `kube-system`
Namespace, the two selected Deployments/ServiceAccounts, the two referenced
ConfigMaps, the fixed Service/Endpoints and four webhook configurations, and
only the endpoint Pods and their ReplicaSets. It does not use API discovery,
resource lists, watches, logs, exec, attach, port-forward, service proxies,
mutations or test resources. Mutation-mode arguments are rejected.

The POSIX subprocess runner has a 5-second Kubernetes request timeout,
10-second command deadline, 90-second collection deadline, 64-command ceiling
and 1 MiB stdout limit per command. Cleanup has a separate one-second wait.
Inline JSON is capped at 16 KiB; platform and placement documents at 512 KiB
and 256 KiB. Stderr is discarded, stdin is closed, and failed, oversized,
timed-out or cancelled processes are terminated and reaped. SIGINT/SIGTERM
cancellation stops further requests. `kuberc` preferences are disabled.
Exec/auth-provider credentials, token-file indirection, impersonation and
insecure/non-HTTPS targets are rejected before cluster requests.

One JSON object is printed, below 4 KiB, with no observed names, UIDs, versions,
images, addresses, administrator identities, kubeconfig data or raw diagnostics:

- `version: "v1"`, `mode: "deployment-preflight"`.
- `status: "completed"` means the **scoped observations** were collected and
  matched, not that all deployment prerequisites or admission behavior passed.
  Otherwise `status` is `"blocked"`.
- `checks` is the fixed 11-entry checklist, in execution order. Each entry has
  `check`, `status` (`observed`, `blocked`, or `unknown`) and a fixed `reason`
  code. After the first failure, remaining entries stay `unknown/not-checked`.
- `unverified` always contains five `unknown` entries: TLS and actual admission
  invocation; effective CEL evaluation and namespace policy; source semantics and
  namespace write protection; running Operator Pods; and runtime/network/storage
  enforcement and provider evidence.
- `qualifiesExecution` is always `false`, `executionReadiness` always `unknown`,
  and `stockExecutionHold` always `writer-exclusion-unavailable`. The last field
  describes stock source wiring, **not** an observation of deployed execution.

Exit codes are 0 for a completed scoped inspection, 1 for a blocker/unknown
required observation, 2 for command-line syntax errors, and 130 for cancellation.
Syntax errors produce a minimal sanitized `version/status/qualifiesExecution/error`
object instead of a checklist. `--help` prints usage without any requests.
Do not use exit 0, rollout status, endpoint readiness or source booleans as an
execution gate. Certificate validity, effective webhook matching/authorization,
protected placement and provider enforcement still require independently
authorized verification. There is no execution override.

Offline tests substitute every `kubectl` subprocess with a synthetic Python
process and retain the existing helper regressions:

```bash
PYTHONPATH=scripts/tests python3 -B -m unittest \
  test_sandbox_deployment_preflight test_sandbox_platform_qualify -v
make python-check license-check
```

## Protocol-specific network smoke

The existing `network-smoke` mode defaults to TCP. On a separately authorized
disposable target, select `--network-protocol UDP` to run the same destination
port-denial and overlapping-allow sequence with UDP datagrams:

```bash
python3 -B scripts/sandbox-platform-qualify.py network-smoke \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --storage-class managed-csi \
  --probe-image "$PINNED_PYTHON_IMAGE" --allow-test-resources \
  --network-protocol UDP
```

Each invocation creates its own synthetic server/probe Pods and policies, then
deletes only its UID-pinned, run-labelled namespace. The protocol flag is
rejected for both read-only preflight modes before client setup. No new workload
credentials, host access, capability registration or execution override is added.

UDP uses connected sockets to the selected server's primary IPv4/IPv6 Pod IP.
Every datagram carries a fresh 16-byte random suffix and must receive the exact
echo; mismatched, empty, stale or oversized responses fail the probe. Only a
receive timeout counts as no response. Connect/send failures, short sends and
connection refusals are errors, not successful policy denials. Each receive has
a two-second timeout; server readiness requires an echo from all three listeners
with one second per listener inside the existing five-second readiness probe.

The baseline requires all ports to respond. The Cilium policy then allows 8081,
denies 8080 and leaves 8082 blocked. After an overlapping allow, 8082 must respond
while 8080 still times out. Each sample checks 8082 before 8080, so an earlier
denial cannot be reused across that allow transition.

Results include `networkProtocol` (`TCP` or `UDP`) and retain
`qualifiesExecution: false`. UDP timeouts are observed loss of replies, not an
independent proof of which device or policy dropped a datagram. Neither protocol
qualifies full network isolation, DNS semantics, other IP families, physical
fencing or a running governed Agent. These are synthetic primitive probes, not
permission to exec into governed Pods or enable a live E2E workflow.

Offline validation covers both the existing TCP behavior and UDP policy wiring,
strict response handling, and actual echo traffic over IPv4/IPv6 loopback:

```bash
PYTHONPATH=scripts/tests python3 -B -m unittest \
  test_sandbox_udp_probe test_sandbox_platform_qualify \
  test_sandbox_deployment_preflight -v
```

## Pinned test tools

`ci/install_tools.py` is a separate, networked setup command, not part of the
offline validation targets. Its manifest pins Linux amd64/arm64 downloads by
SHA256. The `envtest` bundle installs Kubernetes 1.37 `kube-apiserver`/`kubectl`
and its bundled etcd into a new explicit directory. The installer verifies the
archive before reading only its named regular members, refuses existing outputs
and symlink destinations, and removes only files it created if writing fails.
It does not start the binaries or contact a cluster.

Downloads (including raw binaries) are capped at 128 MiB, each selected archive
member or raw binary at 128 MiB, and the total selected payloads across one
invocation at 384 MiB. Oversized advertised lengths fail before reading the
response; absent or understated lengths still use a bounded read. Selected
member sizes are checked before any member payload is read. Verified archives
and selected binaries remain in memory within these limits; streaming to disk
is not implemented.

Operator CI uses this same manifest and installer for both `envtest` and Helm 3,
with an explicit run/attempt-specific directory under `RUNNER_TEMP`. It sets
`KUBEBUILDER_ASSETS` to that binary directory and adds it to `PATH`, rather than
duplicating pins or relying on the runner's Helm version. Release provenance
for both Linux architectures is recorded in `ci/tools.json`.

See [envtest setup](../DEVELOPMENT.md#generated-files-and-dependencies) for the
separate provisioning and isolated API-server test commands.

## License headers

Run the same offline check used by CI:

```bash
make license-check
```

Checks include supported source files and Makefiles across the repository,
including `site/`, release, and E2E sources. Keep upstream and generated
exceptions aligned with [NOTICE](../NOTICE).

Follow the [header review and addition procedure](../site/content/en/docs/reference/scripts.md#license-headers)
before adding a notice. The tools do not replace existing notices, follow
symlinks, deploy resources, or access the network.

## Agent tool authorization metadata inventory

`agent-tools-inspect.py` is a read-only command-line inventory for Agent tool
authorization records. It separates the central namespace's delegated personal
Connections from Agent-local independent Connections and ConnectionBindings. An
Agent-local `connection-reference` is shown in its own table: it contains no
credential and is not a duplicate Connection. It links an Agent-local Binding to
an owner-matched central delegated Connection.

Record identity is the Secret namespace plus record ID. The command joins each
record's immutable Agent UID with the live cluster-scoped Agent resource, so a
deleted or recreated Agent is shown as `deleted/unresolved` rather than being
matched by name. A delegated personal connection can be shared by several
Agent-local references and bindings; an unreferenced connection is shown as
`unassigned/unknown`. Binding validity is always reported as
`unknown/not evaluated`: this command does not decrypt its encrypted payload
and record presence does not establish an active permission.

Use an explicit kubeconfig. `--context` is optional, and the command does not
change either the kubeconfig or its current context. The central credential
namespace defaults to `cetirun-credentials`; pass `--namespace` when the
Broker was configured differently. Without an Agent filter, the command lists
each live Agent's `status.namespace` individually; it never uses `kubectl get
secrets -A`. A Pending Agent may not have `status.namespace`; it is listed in
the output's `scope.unqueriedAgents` with its UID, name, and reason, and no
Agent-local Secret query is attempted. With `--agent`, it queries only the
selected live Agent namespace plus the central namespace. A selected Pending
Agent therefore produces a clearly incomplete central-only view. This
intentionally does not claim visibility of references in other Agent
namespaces. A deleted UID has no safely discoverable namespace, so
`--agent-uid` then reports central records only.

An Agent-filtered view includes only its independent Connections, Connections
resolved by its Bindings, and central delegated Connections reached through its
own local references. Other owners' central accounts are not shown.

```bash
python3 -B scripts/agent-tools-inspect.py \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT"

python3 -B scripts/agent-tools-inspect.py \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --agent my-agent --output json
```

The command receives a fixed JSONPath projection of selected Secret labels and
display annotations from `kubectl`. Kubectl can fetch complete Secret objects
for that client-side projection, requiring normal Secret list permission. The
script does not receive, decrypt, or print the payload, and does not contact
Key Vault or provider APIs. Each query has a 15-second request timeout; more
than 8 MiB of projected output is rejected. A missing `status.namespace` is
reported as unqueried; a namespace that does exist but is missing or forbidden
when queried stops the inspection and is never reported as an empty record set.
The inventory is not an atomic snapshot across the separate metadata queries.

Old central Connections and Bindings remain visible as legacy central relations.
Connections without a mode label show an unknown mode rather than an inferred
delegated identity. New display labels and annotations are only display hints;
UID and owner matches remain authoritative.

Offline coverage uses synthetic kubectl subprocess results only:

```bash
python3 -B -m unittest scripts/tests/test_agent_tools_inspect.py -v
```
