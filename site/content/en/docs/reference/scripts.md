---
title: "Repository scripts"
description: "Commands for validation, container builds, deployment, and publication."
weight: 30
---

Run repository scripts from the repository root. Read inputs and side effects
before using one; scripts in this repository do not all implement `--help`.

## Validation and license helpers

The root [Makefile]({{< repo "Makefile" >}}) delegates to the offline
[`scripts/ci/validate.py`]({{< repo "scripts/ci/validate.py" >}}) runner.
Use `python3 scripts/ci/validate.py --help` for gate names.
`--root PATH` selects a checkout; `--component operator` or `--component portal`
only applies to `go-test` and `go-lint`.

| Helper | Action and boundary |
| --- | --- |
| `scripts/check-license-headers.py --root PATH` | Check in-scope first-party headers; accepts a Git worktree or source export |
| `scripts/add-license-headers.py --root PATH --dry-run` | Preview mechanical header additions; writing requires an explicit `--confirm-first-party` |
| `scripts/ci/install_tools.py --bin-dir PATH TOOL...` | Download selected checksum-pinned CI tools into a task-local directory; Linux amd64/arm64 only |
| `scripts/ci/scan_image.py --image IMAGE` | Scan the already-built local `cetirun-operator:validation` or `cetirun-portal:validation` image; Docker downloads the pinned Trivy image and vulnerability database |

The header checker has explicit generated/upstream exclusions. Passing it does
not establish dependency or container redistribution compliance; see
[NOTICE]({{< repo "NOTICE" >}}) and [security and data boundaries](../../security/).
The CI Python requirements are for auth/Teams tests, not a Hermes runtime lock.
The image helper checks the local image before downloading the scanner. It
fails on fixable HIGH/CRITICAL findings, skips unfixed findings, and does not
build or push an image. Review the report for lower-severity and unfixed findings.

`make check` runs application/infrastructure lint and tests. `make docs-check`
is separate. See [local checks](../../getting-started/local-checks/) for the
tool prerequisites and exact boundaries.

### License headers

Run `make license-check` from the repository root. It checks supported source
formats and Makefiles across the checkout, including site templates and
release/E2E sources. It does not execute those files.

Preview missing headers and review authorship before explicitly adding them:

```bash
python3 -B scripts/add-license-headers.py --dry-run
python3 -B scripts/add-license-headers.py --confirm-first-party
make license-check
```

Both helpers accept `--root PATH`, `--list-exclusions`, and `--help`.
They check tracked and non-ignored untracked files, or a source export without
Git. The adder refuses to replace existing notices and does not follow
symlinks. Retain the upstream/generated exceptions in
[the checker]({{< repo "scripts/license_headers.py" >}}).
Markdown, YAML, JSON, lockfiles, binary assets, dependencies, build outputs,
and hidden paths other than `.github/` are outside this mechanical check.
Exclusion does not waive a file's license obligations.

### Portal third-party notices

The Portal frontend build generates `web/dist/THIRD_PARTY_LICENSES.txt`, which
the Go server embeds and serves at `/THIRD_PARTY_LICENSES.txt` through the
existing access path. After installing the frontend's locked dependencies,
inspect the notice without building the UI:

```bash
npm --prefix app/cetirun-portal/web run licenses -- --output /tmp/cetirun-portal-notices/THIRD_PARTY_LICENSES.txt
```

The generator reads local production-package metadata and license files without
network calls or executing dependency code. Missing notices fail unless the
exact input has a checksum-verified supplement. Review supplements whenever
dependencies change; do not carry approvals across versions automatically.
Keep generated output out of Git. These notices do not cover Go dependencies,
OS/image packages, artwork, or the documentation site's assets.

See [the development guide]({{< repo "DEVELOPMENT.md" >}}),
[generator and tests]({{< repo "app/cetirun-portal/web/scripts" "tree" >}}),
and [supplement guidance]({{< repo "app/cetirun-portal/web/licenses/README.md" >}}).

## Local chart validation

| Script | Action |
| --- | --- |
| `scripts/cetirun-helm-lint.sh` | Lint both agent types with synthetic model/token values |
| `scripts/cetirun-helm-test.sh` | Render chart scenarios and expected validation failures |
| `scripts/cetirun-platform-helm-test.sh` | Check umbrella components, packaged CRDs, gateway settings, and invalid-value cases |
| `scripts/cetirun-istio-helm-test.sh` | Check the portal ownership endpoint's defaults and overrides |

`CHART_DIR` selects a different local chart; `RELEASE_NAME` selects the render
name in the test helper. These checks do not need Azure or a cluster.

## Container build helpers

| Script | Purpose |
| --- | --- |
| `scripts/cetirun-docker-build.sh` | Build agent, base, and desktop images |
| `scripts/operator-docker-build.sh` | Build the operator image |
| `scripts/portal-docker-build.sh` | Build the portal image |
| `scripts/pi-agent-docker-build.sh` | Build the `cetirun-pi-agent` demo |

Builds use Docker and download dependencies; they are not offline checks.
Registry access and network policy must be appropriate for the environment.
The agent helper builds for `linux/amd64`. Its inputs include:

| Variable | Purpose |
| --- | --- |
| `REGISTRY`, `IMAGE_NAME`, `IMAGE_TAG` | Image destination and identity |
| `PUSH` | `false` for a local load; `true` publishes externally |
| `DOCKERFILE`, `BASE_IMAGE` | Agent Dockerfile and base override |
| `BUILD_BASE`, `BUILD_DESKTOP` | Default `true` |
| `BUILD_HERMES` | Default `false` |
| `NPM_REGISTRY` | Registry used for most build stages |
| `OPENCLAW_NPM_REGISTRY` | Separate OpenClaw package registry |
| `PYPI_INDEX_URL` | Default Python package index supplied to Hermes' locked `uv` install |

`NPM_REGISTRY` and `OPENCLAW_NPM_REGISTRY` default to
`https://registry.npmjs.org/`. Runtime skill installation separately uses
`skills.npmRegistry` with the same public default. The Hermes image downloads a
checksum-pinned upstream source tag, installs its checked-in `uv.lock`, and
builds the upstream Dashboard and TUI assets. `PYPI_INDEX_URL` defaults to
`https://pypi.org/simple/`; an alternate index must contain the exact locked
artifacts.

## Connected deployment helpers

### Application E2E lifecycle

[`scripts/e2e-test.sh`]({{< repo "scripts/e2e-test.sh" >}}) requires
`E2E_KUBECONFIG`, `E2E_CONTEXT` and the independently obtained
`E2E_CLUSTER_UID`. It verifies the explicit cluster, creates a new test Agent
instead of adopting/deleting leftovers, and pins that Agent's UID for lifecycle
mutations and cleanup. The test refuses existing Agent/namespace names.
Cleanup observes controller finalization and fails if ownership, deletion
outcome or absence cannot be confirmed; it never force-deletes a namespace or
strips finalizers.

See [inputs, timeouts, retention and offline regression checks]({{< repo "scripts/README.md" >}}#explicit-target-application-e2e-lifecycle).
These run-lifecycle checks do not establish governed execution, runtime fencing
or model/tool acceptance. The live E2E workflow remains disabled and must not
be enabled as part of routine local validation.

The runner's routing check uses the canonical `/ui/<agent>/` path over verified
HTTPS. Its credential-free `e2e-ingress-check.py` helper checks the exact public
Entra sign-in endpoint, expected application ID and callback without following
redirects or printing response data. `E2E_OIDC_CLIENT_ID` and `E2E_CA_FILE`
provide explicit application/CA inputs when needed. Missing configuration,
TLS failures and unexpected/ambiguous challenges fail; a sign-in challenge is
not proof of authenticated owner access or a running model/tool task.
The POSIX helper supervises a separate worker, killing and reaping it on timeout
or cancellation even when native DNS resolution is stalled.
See the [native ingress challenge contract]({{< repo "scripts/README.md" >}}#native-ingress-sign-in-challenge)
for inputs, deadlines, supported layout and offline TLS tests.

### Sandbox platform probes

[`sandbox-platform-qualify.py`]({{< repo "scripts/sandbox-platform-qualify.py" >}})
requires an explicit kubeconfig, context, and expected `kube-system` namespace
UID. It never uses the current context implicitly or logs kubeconfig contents
and upstream authentication diagnostics. Keep target identities and credentials
in local configuration, not repository fixtures.

The read-only preflight checks the admission minimum Kubernetes version, the
selected RuntimeClass handler and StorageClass, and established Cilium policy
and endpoint APIs:

```bash
python3 -B scripts/sandbox-platform-qualify.py preflight \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --storage-class managed-csi
```

The default RuntimeClass is `kata-vm-isolation`, with handler `kata`.
Use `--runtime-class` and `--runtime-handler` to name an explicitly approved
alternative. Existence and matching configuration do not establish actual
runtime, network or storage enforcement.

For an existing Operator/admission installation, the separate
`deployment-preflight` mode checks explicit Deployment and ServiceAccount UID
pins, digest-pinned images, native source bindings, named admission endpoints
and webhook wiring. It performs bounded named GETs only, without discovery,
Secret reads or test resources. It reports unverified prerequisites explicitly;
successful scoped observations do not lift the stock writer-exclusion hold or
qualify execution. See the [deployed Sandbox preflight inputs, output contract
and limits]({{< repo "scripts/README.md" >}}#read-only-deployed-sandbox-preflight).
Neither read-only mode authorizes running the network smoke test below.

On a separately authorized disposable target, the `network-smoke` mode creates
a unique namespace, a synthetic server and a Kata probe. It requires
`--allow-test-resources` and a `--probe-image` pinned by SHA256 digest, containing
Python 3. It tests three initially reachable TCP ports, a Cilium destination-port
deny, and deny precedence over an overlapping Kubernetes allow policy:

```bash
python3 -B scripts/sandbox-platform-qualify.py network-smoke \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --storage-class managed-csi \
  --probe-image "$PINNED_PYTHON_IMAGE" --allow-test-resources
```

These fixtures have no workload credentials, host mounts or host networking.
They are not governed Agent Pods and do not bypass protected-Pod admission.
The server receives its primary Pod IP through the Downward API and binds all
three listeners using that address's IPv4 or IPv6 family. An exec readiness
probe checks all three listeners, with a one-second connection timeout and a
five-second total probe timeout. The baseline waits for server readiness within
the existing 180-second readiness window; container startup alone is not
readiness. On dual-stack targets this test exercises the primary Pod IP only.
The helper retains each server and probe Pod UID from its create response.
Every readiness observation and both final identity checks must match those
creation UIDs and must not be terminating. Missing creation identities or
same-name replacements fail the run; readiness cannot authorize a replacement.
The probe's effective `spec.runtimeClassName` must also match the requested
class in its creation response, every readiness observation and the final
check. Admission rewriting or removing that field fails the run even when
the Pod UID is unchanged.
TCP remains the default. Only a TCP connection timeout can count as blocked;
connection refusal, unexpected HTTP responses and failures after connecting
fail the probe.

To run that sequence for UDP, add `--network-protocol UDP`. The synthetic UDP
server echoes a fresh random payload over connected datagram sockets; readiness
requires the exact echo from every listener. A two-second receive timeout
counts as no response. Connect/send failures, connection refusals and incorrect
or oversized responses fail rather than count as denial. Readiness uses one
second per listener within the existing five-second timeout. The protocol flag
is not valid for either read-only preflight mode. See the
[protocol-specific probe contract]({{< repo "scripts/README.md" >}}#protocol-specific-network-smoke).

The Cilium policy initially allows port 8081, explicitly denies 8080, and leaves
8082 blocked by default. After the overlapping allow policy is created, 8082
must become reachable while 8080 stays blocked. This witness prevents an
unchanged pre-allow sample from falsely establishing deny precedence. Each
sample checks the witness before the denied port, so it cannot reuse a denial
observed before the allow took effect.
Cleanup checks the namespace's run label and exact UID and uses
UID/resourceVersion preconditions on deletion. It waits for absence and never
removes finalizers. An ambiguous create or failed cleanup reports the generated
namespace for explicit recovery; it does not adopt or delete a same-name
replacement.

Results identify the tested transport in `networkProtocol` and always retain
`qualifiesExecution: false`, even when successful. A UDP receive timeout is
absence of a reply, not proof of which policy or device dropped the datagram.
The TCP/UDP smoke test does not qualify pre-start enforcement, writer exclusion,
node/metadata traffic, DNS restrictions, storage, resource bounds, model access
or failure recovery. It does not create production capability registrations or
enable execution. These probes are separate from offline checks and are not
enabled in the live E2E workflow.

The separate [sandbox node guard prototype]({{< repo "app/cetirun-operator/sandbox-node-guard.md" >}})
uses the node's local CRI socket for exact-identity inspection or an explicitly
requested sandbox stop. Access to that socket is host-runtime privilege; this
is not an Agent tool or a default deployment. Its bounded receipt always
disclaims durable writer exclusion and production eligibility. Kubelet can
recreate a sandbox with the same Pod UID after a successful stop, so this
prototype must not be connected to execution authorization.

### Deployment commands

- `infra/cluster/deploy.sh`: cloud provisioning, preview registration, and
  kubeconfig changes; see [infrastructure](../../operations/infrastructure/).
- `infra/storage/deploy.sh`: subnet endpoint changes and Blob deployment;
  see [storage](../../operations/storage/).
- `infra/istio/deploy.sh`: identity/TLS, Helm, and shared mesh configuration;
  see [Istio](../../operations/istio/).
- `scripts/cetirun-platform-deploy.sh`: verify independent Istio prerequisites,
  label the target namespace, install the operator/portal umbrella, and wait
  for its deployments.
- `app/cetirun-operator/deploy/deploy.sh`: check StorageClass/gateway prerequisites,
  then install the operator chart with its packaged CRDs.
- `app/cetirun-portal/deploy/deploy.sh`: portal namespace injection and Helm.

Operator deployment inputs include `REGISTRY`, `CHART_VERSION`, `NAMESPACE`,
`RELEASE_NAME`, `LOCAL_CHART`, `IMAGE_REPO`, `IMAGE_TAG`, `IMAGE_DIGEST`, `DOMAIN`,
`CETIRUN_REPO_URL`, `CETIRUN_CHART_VERSION`, and `HELM_SET_ARGS`.
`ISTIO_GATEWAY` selects the Gateway resource as `namespace/name`;
`ISTIO_INGRESS_NAMESPACE` selects the gateway workload namespace for agent
NetworkPolicies.

Portal deployment additionally uses `ISTIO_HOST`, `ISTIO_REV`, `NOTICE_BANNER`,
`ADMIN_ALIASES`, and `MAX_INSTANCES_PER_USER`. The helpers default to
deployment-specific OCI locations; configure actual accessible artifacts.

The platform helper uses `DOMAIN`, `VALUES_FILE`, `NAMESPACE`, `RELEASE_NAME`,
`ISTIO_REVISION`, and separate Gateway resource/workload inputs. See
[platform installation](../../getting-started/platform/#platform-chart).
The development helper `scripts/cetirun-dev-deploy.sh` requires an explicit
`REGISTRY` and alias argument; it does not insert a default user team label.
Use its `--help` for the available modes.

## Publication and integration boundaries

The repository also contains agent, operator, portal, platform, and Istio Helm
publication helpers, a development deployment helper, and an end-to-end test
script. They are not part of documentation validation. Publication can push
artifacts; deployment and end-to-end checks can mutate a live environment.

Do not run these as a substitute for local docs or chart checks. This site does
not establish a release process or automate publication.

`scripts/verify-mcr-image.py --image IMAGE --digest SHA256` checks the public
MCR digest and AMD64 pull before the release pipeline updates `latest`.

Source: [scripts directory]({{< repo "scripts" "tree" >}}).
