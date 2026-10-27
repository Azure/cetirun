# CetiRun agent chart

The chart deploys an OpenClaw or Hermes instance. Read the
[Helm configuration reference](../../site/content/en/docs/reference/helm.md)
for settings and defaults, and the
[local checks guide](../../site/content/en/docs/getting-started/local-checks.md)
for credential-free rendering.
Maintainers updating the bundled Hermes release should also follow
[the Hermes runtime upgrade procedure](agents/hermes/README.md).

`values.yaml`, `values.schema.json`, and `templates/` are the source of truth.
Operator-resolved values differ from standalone chart defaults. A local render
does not establish storage, RuntimeClass, image access, or cloud readiness.

From the repository root:

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
make -C site serve
```

See [package registry overrides](../../site/content/en/docs/reference/scripts.md#container-build-helpers)
for public npm/PyPI defaults and alternate mirrors, and
[configuration and skills](../../site/content/en/docs/concepts/configuration.md)
for runtime package and Git settings.

## Protected Sandbox rendering

The Operator's `sandboxworkload` adapter consumes the shared Sandbox resolution
and the policy compiler's desired outputs. `helm.PrepareSandboxChart` validates
original per-Agent overrides **before** merging, then applies the complete
protected bundle last. `sandboxWorkload` is an internal chart contract, not a
tenant override or a way to enable Sandbox execution.

This path supports OpenClaw and Hermes. It renders zero replicas and the Helm
client refuses to apply the prepared Sandbox release. Governed Custom runtimes
and the separate platform Copilot helper are explicitly unsupported; neither
falls back to an ungoverned runtime.

The protected inventory includes:

- The main container, HOME initializer, and selected chart-owned doctor, logger
  and desktop helpers. Each gets CPU, memory and ephemeral-storage requests and
  limits. Pod-level CPU/memory declarations use effective init/helper accounting;
  RuntimeClass overhead is accounted separately and checked after API defaulting.
- Non-root security contexts, dropped capabilities, disabled privilege
  escalation and RuntimeDefault seccomp. Explicit policy
  `readOnlyRootFilesystem: false` changes only that control.
- An existing private HOME claim and a distinct read-only skills claim, plus a
  size-limited writable scratch volume. `skills.existingClaim` suppresses chart
  PV/PVC creation on the native path. The adapter requires confirmed HOME UID
  ownership and unchanged class, access mode and size. The opt-in native
  OpenClaw profile may instead use an explicit internal empty-skills source
  when the effective selection is empty and no catalog is configured.
- Disabled Istio injection and Sidecar discovery, enabled compiler-owned network
  controls, and independent explicit identity projections mounted only in the
  main container. Compiler resources are emitted unchanged by this chart.
- Only compiler-owned CiliumNetworkPolicy, dedicated ServiceAccount,
  RoleBinding and ClusterRoleBinding resources are accepted. Explicitly requested
  cross-namespace RoleBindings and cluster-wide ClusterRoleBindings retain their
  scopes and must target only the workload's exact ServiceAccount. Shared Role
  and ClusterRole objects are never emitted or adopted. Ordinary mode uses the
  namespace's default ServiceAccount without creating it or projecting identity.
- `dnsPolicy: None` with exactly the compiler's one to three trusted DNS server
  IPs. Node/cluster resolver defaults, fallback servers, search suffixes and DNS
  options are not inherited or user-configurable. No cluster-domain suffix is
  invented; use fully qualified names where needed. The compiler's bound ingress
  mode must also match the shared resolution.

The legacy pre-upgrade orphan/re-adoption hook is absent from protected renders.
An existing StatefulSet using `volumeClaimTemplates` requires a separately
approved stopped-workload transition; rendering never migrates it, changes PVC
ownership or claims writer exclusion. Scratch limits are accounting/eviction
controls, not an independent hard disk quota.

Only model provider/name, authorized existing Secret selection and non-reserved
typed environment inputs are accepted as governed user overrides. Final-Pod
validation compares all containers, volumes, identity, scheduling and other
PodSpec fields against the rendered expectation, allowing only sanctioned
Kubernetes/StatefulSet defaults. Admission additionally requires current
controller-owned persisted UID/revision state; a rendered template alone cannot
authorize a Pod.

The admission contract also checks the live Namespace's
`cetirun.microsoft.com/sandbox-protected: "true"` label, Agent name/UID annotations
and canonical Agent owner reference against that state. Installation must make
this namespace identity immutable and restrict initial marking to the exact
controller identity; labels alone never authorize a workload.

Admission needs reads of the protected state ConfigMap and name-scoped reads of
referenced workload ConfigMaps and authorized Secrets in their namespaces. This
includes the platform-managed `kube-root-ca.crt` when Kubernetes access is
projected; it is not copied into an Agent-owned CA resource. Metadata-only reads
still require ordinary resource `get` permission, so do not grant cluster-wide
Secret access.

For common-executor Kata lifetimes, the controller records workload binding
intents before this chart is applied and confirms their live UIDs afterward.
Current Pod admission requires those confirmed grants and exact ClusterRole
source pins. A suspended common render validates the compiler output but omits
workload RoleBindings/ClusterRoleBindings and requires every component to have
zero replicas; it cannot recreate revoked access during suspension.
Kubernetes identity volumes and mounts must be present unchanged on the main
container and cannot be added to helpers. See the
[administrator setup](../../examples/native-sandbox/README.md#kubernetes-api-access);
this chart does not approve roles or enable capabilities.

Offline regression checks include:

```bash
go -C app/cetirun-operator test ./internal/sandboxworkload/... ./internal/helm ./internal/admission -count=1
# Requires separately prepared local Kubernetes test binaries:
go -C app/cetirun-operator test -tags=envtest ./internal/admission -run '^TestPodAdmissionAPIServerBoundaries$' -count=1
```

These checks do not qualify runtime isolation, host resource enforcement, CSI
writer exclusion, network enforcement or identity federation. Legacy chart
defaults remain unchanged when the protected contract is absent.

### Native OpenClaw Gateway packaging

The native OpenClaw MVP reuses the protected renderer and the existing
OpenClaw image. It does not add an alternative image, an Istio sidecar, or a
Helm value that authorizes execution. The controller owns Sandbox/AgentPolicy
selection, owner access, activation and deletion; the chart still prepares a
held workload. Rendering alone does not test any of those live operations.

The Operator supplies this contract:

| Values | Runtime use |
| --- | --- |
| `agentType: openclaw`, `sandboxWorkload.contractVersion: v1` | Explicit protected OpenClaw profile; preparation retains `executionHeld: true` and `replicaCount: 0` |
| `runtimeClassName`, `sandboxWorkload` resources, DNS, security and enforcement objects | The selected Sandbox's compiled runtime controls, never legacy network defaults |
| `persistence.existingClaim`, `skills.existingClaim` | Separately owned HOME and read-only skills storage |
| `sandboxWorkload.skillsMode: empty` (internal, otherwise omitted) | Replaces only the skills claim with an unpopulated `emptyDir`, mounted read-only at `/home/vibe/.aks/skills` in the main container |
| `openclaw.image.repository`, `tag`, optional `digest` | The same existing OpenClaw image for the Agent and its initializers; shared registry/version overrides remain supported |
| `aiGateway.enabled: true`, `keySecretName`, `keySecretKey` | The Agent's virtual key, passed as `OPENAI_API_KEY` through a Secret reference only in the main container |
| `aiGateway.models`, `secrets.provider: openai`, `secrets.model`, `secrets.apiBase` | The authorized model list, default and Gateway endpoint; missing endpoints and defaults outside that list fail rendering |
| `openclaw.config.gateway` | Controller-selected ingress/auth configuration, copied without persisted auth overrides on each native start |

Protected Gateway traffic uses compiler-owned network policy, so
`sidecarScope.enabled` remains false. A legacy Gateway render still requires
the existing Istio Sidecar scope.

Only the controller's existing `sandboxSources.nativeExecution` opt-in can
select the empty-skills mode, after resolving Team skills and Agent additions
and removals. It requires no new public flag. HOME remains a PVC, the chart
remains held, and the initial Pod lifetime finalizer is preserved.
The schema and renderer reject combining the empty mode with a shared claim,
package requests, another mount path, a non-OpenClaw runtime or missing native
Gateway/lifetime inputs. No helper can populate the empty skills volume.
Requested skills and configured catalogs continue to use the existing validated
PVC/publication path; errors never select an empty fallback.

Protected OpenClaw startup, readiness and liveness probes use the image's
existing Node runtime to request `/startupz`, `/readyz` and `/healthz` over
`127.0.0.1` inside the main container. The helper is packaged in the read-only
ConfigMap mount at `/etc/cetirun/health-probe.cjs`; no new image is needed.
It accepts HTTP 200-399 without following redirects, sends no model credentials,
and exits nonzero on HTTP errors, connection failures or its bounded deadline.
Explicit probe timing, thresholds and enablement settings are preserved.
With `startupProbe.failureThreshold: null` (the default), common-executor
OpenClaw Sandboxes allow 60 failed startup probes at the existing 10-second
interval, rather than restarting a still-initializing gateway after 30 failures.
Other built-in paths, including retained single-start OpenClaw and Hermes, keep
30 failures. An explicit positive integer, including `30`, overrides either
default. Liveness/readiness checks and their timings remain unchanged.
Legacy OpenClaw and held Hermes renders retain their existing HTTP probes.

The startup probe starts after init containers finish; it is not a deadline for
doctor or credential retirement and does not extend a lifecycle operation's
acceptance bound. Qualify the complete startup and retained operations on the
target runtime/storage, not just the probe window. Existing pinned charts and
explicit overrides are not changed by this default; use the normal retained
update flow to adopt the new chart.

These exec probes require no host/remote-node ingress exception or network
compiler change. They report Kubernetes container health only, not
guest-external observations, runtime qualification or authorization to lift an
execution hold.

The chart adds `CETIRUN_NATIVE_SANDBOX=true` to OpenClaw's HOME initializer only
on this protected Gateway path. The initializer replaces saved model/provider,
model fallback and ingress-auth settings with the current platform configuration.
It removes persisted provider credential/endpoint assignments from OpenClaw's
dotenv file while preserving unrelated settings and user files. A subsequent
`retire-direct-model-auth` initializer uses the existing OpenClaw CLI to retire
saved default-agent auth profiles after any doctor/import step. Invalid source
configuration, unsupported named-agent stores, unresolved profile removal,
compaction errors and linked auth paths fail initialization instead of starting
with a different credential source. Named-agent configurations are outside this
single-agent MVP. Legacy bootstrap behavior is unchanged.
Retirement still reads the auth store on every start and compacts existing
databases. An initially empty store needs no second expensive CLI read; whenever
logout removes a profile, successful empty-store readback remains mandatory.

The Gateway deployment must enable `operatorIntegration` and configure shared
models in its chart so the Operator can bind AgentPolicy grants to the
[chart-derived model identities](../cetirun-ai-gateway/chart/README.md#cetirun-agentpolicy-model-bindings).
Provider credentials stay with the Gateway. Arbitrary direct provider routes,
unimplemented policy restrictions, identity extensions and other deferred
profiles must be rejected by the controller, not passed through as ordinary
runtime values.

Focused offline checks (with the documented dependencies, Node and Helm 3 or 4):

```bash
python3 -B -m unittest discover -s scripts/tests -p test_native_gateway_chart.py -v
python3 -B -m pytest -p no:cacheprovider -q \
  app/cetirun-chart/tests/test_init_home.py \
  app/cetirun-chart/tests/test_retire_copilot_sign_in.py
```

`make ci-test` includes the rendering tests; `make python-test` includes the
initialization tests with the repository's offline guards. The CLI fixtures are
synthetic; they do not qualify a built image, Kata, Cilium, storage, a live owner
sign-in or an actual model request.
