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
30-second overall deadline, including DNS/TLS/header collection. Output is one
small JSON object with fixed reason codes, never the origin, response headers,
cookies, redirect state, identifiers or raw transport diagnostics.

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
  invocation; CEL match conditions and namespace policy; source semantics and
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
