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
