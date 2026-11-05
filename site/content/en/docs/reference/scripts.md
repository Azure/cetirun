---
title: "Repository scripts"
description: "Commands for validation, container builds, deployment, and publication."
weight: 30
---

Run repository scripts from the repository root. Before you use a script, read
its inputs and side effects. Not all scripts in this repository implement
`--help`.

## Validation and license helpers

The root [Makefile]({{< repo "Makefile" >}}) delegates to the offline
[`scripts/ci/validate.py`]({{< repo "scripts/ci/validate.py" >}}) runner.
For the gate names, use `python3 scripts/ci/validate.py --help`.
`--root PATH` selects a checkout. `--component operator` or
`--component portal` applies only to `go-test` and `go-lint`.

| Helper | Action and boundary |
| --- | --- |
| `scripts/check-license-headers.py --root PATH` | Examines the in-scope first-party headers. It accepts a Git worktree or a source export |
| `scripts/add-license-headers.py --root PATH --dry-run` | Shows a preview of the mechanical header additions. To write the headers, an explicit `--confirm-first-party` is necessary |
| `scripts/ci/install_tools.py --bin-dir PATH TOOL...` | Downloads the selected checksum-pinned CI tools into a task-local directory. Linux amd64/arm64 only |
| `scripts/ci/scan_image.py --image IMAGE` | Scans the already-built local `cetirun-operator:validation` or `cetirun-portal:validation` image. Docker downloads the pinned Trivy image and the vulnerability database |

The header checker has explicit generated/upstream exclusions. A successful
header check does not establish compliance for the redistribution of
dependencies or containers. See [NOTICE]({{< repo "NOTICE" >}}) and
[security and data boundaries](../../security/). The CI Python requirements are
for auth and runtime helper tests. They are not a Hermes runtime lock.

The image helper examines the local image before it downloads the scanner. It
fails on fixable HIGH/CRITICAL findings and skips unfixed findings. It does not
build or push an image. Examine the report for lower-severity and unfixed
findings.

`make check` runs the application/infrastructure lint and tests.
`make docs-check` is separate. For the tool prerequisites and the exact
boundaries, see [local checks](../../getting-started/local-checks/).

### License headers

Run `make license-check` from the repository root. It examines the supported
source formats and the Makefiles in the full checkout. This scope also includes
site templates and release/E2E sources. It does not execute those files.

Preview the headers that are not present. Examine the authorship before you
explicitly add the headers:

```bash
python3 -B scripts/add-license-headers.py --dry-run
python3 -B scripts/add-license-headers.py --confirm-first-party
make license-check
```

Both helpers accept `--root PATH`, `--list-exclusions`, and `--help`.
They examine tracked files and untracked files that are not ignored, or a
source export without Git. The adder refuses to replace notices that exist, and
it does not follow symlinks. Keep the upstream/generated exceptions in
[the checker]({{< repo "scripts/license_headers.py" >}}).
Markdown, YAML, JSON, lockfiles, binary assets, dependencies, build outputs,
and hidden paths other than `.github/` are outside this mechanical check.
An exclusion does not waive the license obligations of a file.

### Portal third-party notices

The Portal frontend build generates `web/dist/THIRD_PARTY_LICENSES.txt`. The Go
server embeds this file and serves it at `/THIRD_PARTY_LICENSES.txt`, through
the access path that already exists. After you install the locked dependencies
of the frontend, examine the notice without a build of the UI:

```bash
npm --prefix app/cetirun-portal/web run licenses -- --output /tmp/cetirun-portal-notices/THIRD_PARTY_LICENSES.txt
```

The generator reads the local metadata and license files of production
packages, without network calls or execution of dependency code. A notice that
does not exist causes a failure, unless the exact input has a checksum-verified
supplement. Examine the supplements each time that dependencies change. Do not
carry approvals across versions automatically. Keep the generated output out of
Git. These notices do not cover Go dependencies, OS/image packages, artwork, or
the assets of the documentation site.

See [the development guide]({{< repo "DEVELOPMENT.md" >}}),
[generator and tests]({{< repo "app/cetirun-portal/web/scripts" "tree" >}}),
and [supplement guidance]({{< repo "app/cetirun-portal/web/licenses/README.md" >}}).

## Local chart validation

| Script | Action |
| --- | --- |
| `scripts/cetirun-helm-lint.sh` | Lints both agent types with synthetic model/token values |
| `scripts/cetirun-helm-test.sh` | Renders chart scenarios and expected validation failures |
| `scripts/cetirun-platform-helm-test.sh` | Examines umbrella components, packaged CRDs, gateway settings, and invalid-value cases |
| `scripts/cetirun-istio-helm-test.sh` | Examines the defaults and overrides of the portal ownership endpoint |

`CHART_DIR` selects a different local chart. In the test helper,
`RELEASE_NAME` selects the render name. Azure and a cluster are not necessary
for these checks.

## Container build helpers

| Script | Purpose |
| --- | --- |
| `scripts/cetirun-docker-build.sh` | Builds the agent, base, and desktop images |
| `scripts/operator-docker-build.sh` | Builds the operator image |
| `scripts/portal-docker-build.sh` | Builds the portal image |
| `scripts/pi-agent-docker-build.sh` | Builds the `cetirun-pi-agent` demo |

Builds use Docker and download dependencies. They are not offline checks.
Registry access and network policy must be correct for the environment. The
agent helper builds for `linux/amd64`. Its inputs include:

| Variable | Purpose |
| --- | --- |
| `REGISTRY`, `IMAGE_NAME`, `IMAGE_TAG` | Image destination and identity |
| `PUSH` | `false` for a local load. `true` publishes externally |
| `DOCKERFILE`, `BASE_IMAGE` | Agent Dockerfile and base override |
| `BUILD_BASE`, `BUILD_DESKTOP` | Default `true` |
| `BUILD_HERMES` | Default `false` |
| `NPM_REGISTRY` | Registry that most build stages use |
| `OPENCLAW_NPM_REGISTRY` | Separate OpenClaw package registry |
| `PYPI_INDEX_URL` | Default Python package index supplied to Hermes' locked `uv` install |

`NPM_REGISTRY` and `OPENCLAW_NPM_REGISTRY` default to
`https://registry.npmjs.org/`. Runtime skill installation separately uses
`skills.npmRegistry` with the same public default. The Hermes image downloads a
checksum-pinned upstream commit and uses its PM locks for a private Python 3.14
and Node 26 toolchain. It installs the locked application environment with
`all`, `messaging`, `anthropic` and `azure-identity`, and builds the upstream
Webapp and TUI assets. Runtime startup uses those prebuilt assets without
installing dependencies or Electron. `PYPI_INDEX_URL` defaults to
`https://pypi.org/simple/`. An alternate index must contain the exact locked
artifacts.

## Connected deployment helpers

### Application E2E lifecycle

To run [`scripts/e2e-test.sh`]({{< repo "scripts/e2e-test.sh" >}}), you must
set `E2E_KUBECONFIG`, `E2E_CONTEXT`, and the `E2E_CLUSTER_UID` that you get
independently. The script confirms the explicit cluster. It creates a new test
Agent, and it does not adopt or delete leftovers. It pins the UID of that Agent
for lifecycle mutations and cleanup. The test refuses Agent or namespace names
that already exist.

Cleanup observes controller finalization. It fails if it cannot confirm the
ownership, the deletion outcome, or the absence. It never force-deletes a
namespace or strips finalizers.

See [inputs, timeouts, retention, and offline regression checks]({{< repo "scripts/README.md" >}}#explicit-target-application-e2e-lifecycle).
These run-lifecycle checks do not establish governed execution, runtime
fencing, or model/tool acceptance. The live E2E workflow stays disabled. It
must not be enabled as part of routine local validation.

The routing check of the runner uses the canonical `/ui/<agent>/` path over
verified HTTPS. Its credential-free `e2e-ingress-check.py` helper examines the
exact public Entra sign-in endpoint, the expected application ID, and the
callback. It does not follow redirects or print response data. When necessary,
`E2E_OIDC_CLIENT_ID` and `E2E_CA_FILE` supply explicit application/CA inputs.

Configuration that is not set, TLS failures, and unexpected/ambiguous
challenges cause a failure. A sign-in challenge is not proof of authenticated
owner access or of a model/tool task that runs. The POSIX helper supervises a
separate worker. On timeout or cancellation, it kills and reaps the worker, even
when native DNS resolution is stalled. For inputs, deadlines, the supported
layout, and offline TLS tests, see the
[native ingress challenge contract]({{< repo "scripts/README.md" >}}#native-ingress-sign-in-challenge).

### Sandbox platform probes

For
[`sandbox-platform-qualify.py`]({{< repo "scripts/sandbox-platform-qualify.py" >}}),
an explicit kubeconfig, context, and expected `kube-system` namespace UID are
necessary. It never uses the current context implicitly. It never logs
kubeconfig contents or upstream authentication diagnostics. Keep target
identities and credentials in local configuration, not in repository fixtures.

The read-only preflight examines the minimum Kubernetes version for admission,
the selected RuntimeClass handler and StorageClass, and the established Cilium
policy and endpoint APIs:

```bash
python3 -B scripts/sandbox-platform-qualify.py preflight \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --storage-class managed-csi
```

The default RuntimeClass is `kata-vm-isolation`, with the handler `kata`.
To name an explicitly approved alternative, use `--runtime-class` and
`--runtime-handler`. Existence and a configuration that matches do not
establish actual runtime, network, or storage enforcement.

For an Operator/admission installation that already exists, the separate
`deployment-preflight` mode examines these items:

- Explicit Deployment and ServiceAccount UID pins
- Digest-pinned images
- Native source bindings
- Named admission endpoints
- Webhook wiring

It does only bounded, named GETs, without discovery, Secret reads, or test
resources. It reports unverified prerequisites explicitly. Successful scoped
observations do not lift the stock writer-exclusion hold or qualify execution.
See the [deployed Sandbox preflight inputs, output contract,
and limits]({{< repo "scripts/README.md" >}}#read-only-deployed-sandbox-preflight).
Neither read-only mode authorizes a run of the network smoke test below.

On a disposable target that is separately authorized, the `network-smoke` mode
creates a unique namespace, a synthetic server, and a Kata probe. For this mode,
`--allow-test-resources` and a `--probe-image` are necessary. The image must be
pinned by SHA256 digest and contain Python 3. The mode tests three initially
reachable TCP ports, a Cilium destination-port deny, and deny precedence over an
overlapping Kubernetes allow policy:

```bash
python3 -B scripts/sandbox-platform-qualify.py network-smoke \
  --kubeconfig "$TEST_KUBECONFIG" --context "$TEST_CONTEXT" \
  --cluster-uid "$TEST_CLUSTER_UID" --storage-class managed-csi \
  --probe-image "$PINNED_PYTHON_IMAGE" --allow-test-resources
```

These fixtures have no workload credentials, host mounts, or host networking.
They are not governed Agent Pods, and they do not bypass protected-Pod
admission.

The server receives its primary Pod IP through the Downward API. It binds all
three listeners with the IPv4 or IPv6 family of that address. An exec readiness
probe examines all three listeners, with a one-second connection timeout and a
five-second total probe timeout. The baseline waits for server readiness within
the 180-second readiness window that already exists. Container startup alone is
not readiness. On dual-stack targets, this test exercises only the primary Pod
IP.

The helper keeps the UID of each server and probe Pod from its create response.
Every readiness observation and both final identity checks must match those
creation UIDs and must not be in the terminating state. The run fails if a
creation identity is not available or if there is a same-name replacement.
Readiness cannot authorize a replacement. The effective `spec.runtimeClassName`
of the probe must also match the requested class in its creation response,
every readiness observation, and the final check. If admission rewrites or
removes that field, the run fails, even when the Pod UID is unchanged.

TCP stays the default. Only a TCP connection timeout can count as blocked.
Connection refusal, unexpected HTTP responses, and failures after the
connection fail the probe.

To run that sequence for UDP, add `--network-protocol UDP`. The synthetic UDP
server echoes a new random payload over connected datagram sockets. For
readiness, the exact echo from every listener is necessary. A two-second
receive timeout counts as no response. Connect/send failures, connection
refusals, and incorrect or oversized responses fail. They do not count as
denial.

Readiness uses one second for each listener, within the five-second timeout
that already exists. The protocol flag is not valid for either read-only
preflight mode. See the
[protocol-specific probe contract]({{< repo "scripts/README.md" >}}#protocol-specific-network-smoke).

The Cilium policy initially allows port 8081, explicitly denies 8080, and
leaves 8082 blocked by default. After the overlapping allow policy is created,
8082 must become reachable while 8080 stays blocked. With this witness, an
unchanged pre-allow sample cannot falsely establish deny precedence. Each sample
examines the witness before the denied port. As a result, it cannot reuse a
denial that was observed before the allow took effect.

Cleanup examines the run label and the exact UID of the namespace. It uses
UID/resourceVersion preconditions on deletion. It waits for absence and never
removes finalizers. An ambiguous create or a failed cleanup reports the
generated namespace for explicit recovery. It does not adopt or delete a
same-name replacement.

Results identify the tested transport in `networkProtocol` and always keep
`qualifiesExecution: false`, even when the run is successful. A UDP receive
timeout is absence of a reply. It is not proof of which policy or device dropped
the datagram. The TCP/UDP smoke test does not qualify pre-start enforcement,
writer exclusion, node/metadata traffic, DNS restrictions, storage, resource
bounds, model access, or failure recovery. It does not create production
capability registrations or enable execution. These probes are separate from
offline checks and are not enabled in the live E2E workflow.

The separate [sandbox node guard prototype]({{< repo "app/cetirun-operator/sandbox-node-guard.md" >}})
uses the node's local CRI socket for exact-identity inspection or an explicitly
requested sandbox stop. Access to that socket is host-runtime privilege. This
is not an Agent tool or a default deployment. Its bounded receipt always
disclaims durable writer exclusion and production eligibility. After a
successful stop, Kubelet can recreate a sandbox with the same Pod UID. As a
result, this prototype must not be connected to execution authorization.

### Deployment commands

- `infra/cluster/deploy.sh`: cloud provisioning, preview registration, and
  kubeconfig changes. See [infrastructure](../../operations/infrastructure/).
- `infra/storage/deploy.sh`: subnet endpoint changes and Blob deployment.
  See [storage](../../operations/storage/).
- `infra/istio/deploy.sh`: identity/TLS, Helm, and shared mesh configuration.
  See [Istio](../../operations/istio/).
- `scripts/cetirun-platform-deploy.sh`, which `make deploy` runs: examine the
  target cluster, references, certificates, and published artifacts. Then
  install the CetiRun Istio, Broker, and platform releases, and wait for their
  workloads.
- `app/cetirun-operator/deploy/deploy.sh`: examine StorageClass/gateway
  prerequisites, then install the operator chart with its packaged CRDs.
- `app/cetirun-portal/deploy/deploy.sh`: portal namespace injection and Helm.

Operator deployment inputs include `REGISTRY`, `CHART_VERSION`, `NAMESPACE`,
`RELEASE_NAME`, `LOCAL_CHART`, `IMAGE_REPO`, `IMAGE_TAG`, `IMAGE_DIGEST`,
`DOMAIN`, `CETIRUN_REPO_URL`, `CETIRUN_CHART_VERSION`, and `HELM_SET_ARGS`.
`ISTIO_GATEWAY` selects the Gateway resource as `namespace/name`.
`ISTIO_INGRESS_NAMESPACE` selects the gateway workload namespace for agent
NetworkPolicies.

Portal deployment also uses `ISTIO_HOST`, `ISTIO_REV`, `NOTICE_BANNER`,
`ADMIN_ALIASES`, and `MAX_INSTANCES_PER_USER`. The helpers default to
deployment-specific OCI locations. Configure actual, accessible artifacts.

The platform helper reads one environment file: `--config <file>`. With
`--plan`, it validates the file and renders the charts offline. It does not
read `DOMAIN`, `VALUES_FILE`, or the other variables of the earlier helper. See
[platform installation](../../getting-started/platform/#platform-chart).

For the development helper `scripts/cetirun-dev-deploy.sh`, an explicit
`REGISTRY` and alias argument are necessary. It does not insert a default user
team label. For the available modes, use its `--help`.

## Publication and integration boundaries

The repository also contains these items:

- Helm publication helpers for the agent, operator, portal, platform, and Istio
- A development deployment helper
- An end-to-end test script

They are not part of documentation validation. Publication can push artifacts.
Deployment and end-to-end checks can mutate a live environment.

Do not run these as a substitute for local docs or chart checks. This site does
not establish a release process or automate publication.

`scripts/verify-mcr-image.py --image IMAGE --digest SHA256` examines the public
MCR digest and AMD64 pull before the release pipeline updates `latest`.

Source: [scripts directory]({{< repo "scripts" "tree" >}}).
