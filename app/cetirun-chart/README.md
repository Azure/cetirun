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
client refuses to apply the prepared Sandbox release. Policy-bound Custom runtimes
and the separate platform Copilot helper are explicitly unsupported; neither
falls back to a policy-free runtime.

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
  ownership and unchanged class, access mode and size.
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
typed environment inputs are accepted as policy-controlled user overrides. Final-Pod
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

Offline regression checks include:

```bash
go -C app/cetirun-operator test ./internal/sandboxworkload/... ./internal/helm ./internal/admission -count=1
# Requires separately prepared local Kubernetes test binaries:
go -C app/cetirun-operator test -tags=envtest ./internal/admission -run '^TestPodAdmissionAPIServerBoundaries$' -count=1
```

These checks do not qualify runtime isolation, host resource enforcement, CSI
writer exclusion, network enforcement or identity federation. Legacy chart
defaults remain unchanged when the protected contract is absent.
