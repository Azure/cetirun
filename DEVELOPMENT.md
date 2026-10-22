# Developing CetiRun

Start with local tests and rendering checks. Running these checks does not
require an Azure subscription, cluster access, or a live agent account.
Downloading tools and dependencies is a separate setup step that may need
network access.

## Repository layout

| Path | What to work on here |
| --- | --- |
| `app/cetirun-operator/` | Go Kubernetes operator, API types, and generated CRDs |
| `app/cetirun-portal/` | Go portal backend and its Helm chart |
| `app/cetirun-portal/web/` | React and TypeScript portal frontend |
| `app/cetirun-agent-broker/` | Agent Identity authorization sessions and workload-bound short-lived credential issuance |
| `app/agent-tools-mcp/` | Workload-authenticated MCP service for Azure JSON REST execution |
| `app/internal/` | Shared Go implementation packages for Registry credentials and terminal policy |
| `app/cetirun-chart/` | Runtime Helm chart, container definitions, and runtime plugins |
| `infra/` | Infrastructure definitions and Istio configuration |
| `scripts/` | Component validation and operational helpers |
| `docs/` | Documentation pointers and project assets |
| `site/` | Hugo/Docsy documentation site, when included in the checkout |

Application implementation packages belong in `internal/`, not `pkg/`.
Code shared by the Operator and Portal lives in the `app/internal` Go module;
Go restricts its imports to modules under `github.com/Azure/CetiRun/app`.
The modules use local `replace` directives for these packages and the Operator's
API types. Keep `app/internal`, `app/cetirun-operator`, and `app/cetirun-portal`
in the same checkout when building or testing them.
Not every script is a development check: deployment, release, and real-cluster
test scripts require a separately authorized environment.

### Deployment-specific configuration

`scripts/cetirun-dev-deploy.sh` requires an explicit `REGISTRY` and alias
argument and no longer inserts a default user team label; use `--help` for its
modes. Other build, deployment, and Helm publishing helpers retain their
registry defaults and overrides.

Package defaults use public npm and PyPI. The existing `NPM_REGISTRY`,
`OPENCLAW_NPM_REGISTRY`, `skills.npmRegistry`, and `PYPI_INDEX_URL` inputs remain
available for alternate mirrors. Image tags, chart versions, and the
`BASE_IMAGE` override remain available.

## Tools and setup

Install tools only for the component you are changing:

- Git, Bash, Make, and curl for repository commands and loopback download tests.
- Go 1.27.3, matching `.go-version`, all module minimums, and the pinned
  Microsoft Go builder images.
- Node.js 24 and npm for the portal frontend, matching its CI configuration.
- Helm for local chart linting and rendering.
- Python 3 for the standard-library tests under `scripts/tests/`.

Check the component manifests and CI when updating tool versions. Keep
dependency changes separate from unrelated work.

From your checkout, use a meaningful branch such as
`git switch -c fix/instance-status`. See [CONTRIBUTING.md](CONTRIBUTING.md) for
the issue and review process.

For Go components, fetch dependencies once when network access is available:

```bash
go -C app/internal mod download
go -C app/cetirun-operator mod download
go -C app/cetirun-portal mod download
go -C app/cetirun-agent-broker mod download
```

Install the frontend's locked dependencies from the public npm registry:

```bash
make -C app/cetirun-portal frontend-install NPM_REGISTRY=https://registry.npmjs.org
```

Dependency installation is not an offline check. Do not supply production
credentials to resolve a missing development dependency.

## Current validation entry points

Run the following commands from the repository root. Select checks for the
components affected by your change.

When the checkout includes the root Makefile, use `make check` for offline
validation, `make test` for unit tests, and `make lint` for static checks.
Install the required tools and dependencies separately before running them.
`make docs-check` runs the documentation site's checks when `site/` is present.
These commands do not authorize cluster or cloud access. For an earlier
checkout without these entry points, use the component commands below and
report the narrower validation scope.

### Shared internal packages

```bash
go -C app/internal test ./... -count=1
go -C app/internal vet ./...
go -C app/internal test -race ./agenttools ./tlsreload -count=1
```

The root `make go-test` and `make go-lint` targets include this module.
Run the consumer modules' tests too when changing shared behavior.

### Operator

```bash
go -C app/cetirun-operator test ./... -count=1
go -C app/cetirun-operator vet ./...
make -C app/cetirun-operator build
```

The build writes `app/cetirun-operator/bin/manager`; it does not start the
operator. The component's `make test` and `make vet` targets are also available.
Use `gofmt -l` on changed Go files to check formatting, and `gofmt -w` only on
files you intend to change.

For a disconnected Go check with dependencies already cached, prefix the
command with `GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly`. A missing
toolchain or dependency should fail the check, not trigger an implicit download.

### Custom terminal

The Custom terminal uses its own image and
`app/cetirun-operator/Dockerfile.terminal`. Its Go entrypoint shares the Operator
module so it can use the same Agent API types, but runs as a separate
Deployment with its own ServiceAccount. It does not start the Operator.

```bash
go -C app/cetirun-operator test ./internal/terminal -count=1
node --test app/cetirun-operator/internal/terminal/reconnect.test.cjs
make -C app/cetirun-operator build-terminal
```

Image building is a separate step and can download pinned base images and Go
dependencies:

```bash
REGISTRY=registry.example.com PUSH=false bash scripts/terminal-docker-build.sh
```

`make build-images` and `make publish-images` include `cetirun-terminal`.
Neither local tests nor image building exercises a cluster exec, model login,
real gateway authentication or Pod restart. Use separately authorized
environment tests for those paths.

### Portal

For a frontend change, run the tests and production build:

```bash
npm --prefix app/cetirun-portal/web test
npm --prefix app/cetirun-portal/web run build
```

The build includes TypeScript checking and creates `web/dist`. The Go entry
point embeds that directory, so build the frontend before testing or building
the entire portal module:

```bash
go -C app/cetirun-portal test ./... -count=1
go -C app/cetirun-portal vet ./...
make -C app/cetirun-portal build
```

The frontend build also generates `web/dist/THIRD_PARTY_LICENSES.txt` from the
installed production dependency graph. The Portal embeds and serves this file
at `/THIRD_PARTY_LICENSES.txt`; no separate server or authentication rule is
added. Missing required dependency/license files fail the build unless an exact,
checksum-verified supplement is recorded in `web/licenses/supplements.json`.
Review supplements when updating dependencies; do not reuse old version
approvals. `npm test` includes offline generator regressions.

Portal CI checks known supplement versions before installing dependencies.
When a version needs a new entry, it verifies the public upstream metadata and
unchanged attribution files, then saves a proposed `portal-license-update`
patch for review. CI does not apply the patch or relax the build check. See the
[supplement update procedure](app/cetirun-portal/web/licenses/README.md) for the
local command, manual-review cases, and tarball checks.

To inspect the notice without building the frontend, after `npm ci`:

```bash
npm --prefix app/cetirun-portal/web run licenses -- --output /tmp/cetirun-portal-notices/THIRD_PARTY_LICENSES.txt
```

The generator reads local package metadata and upstream license files without
network access or executing dependency code. Keep generated output out of Git.
This JavaScript notice does not cover Go dependencies, image packages, artwork,
commercial software terms, or corresponding-source obligations. Root `NOTICE`
retains the manual attributions and unresolved provenance/distribution items.

For backend-only work without frontend dependencies or build output, this
narrower check covers the internal packages without compiling the embedded UI:

```bash
go -C app/cetirun-portal test ./internal/... -count=1
go -C app/cetirun-portal vet ./internal/...
```

Report that reduced scope; it does not validate the frontend or the complete
portal binary. Do not treat a placeholder embed directory as a frontend build.

The retained-restart API also has an isolated API-server test. With the pinned
Kubernetes 1.37 binaries described below already installed, run:

```bash
KUBEBUILDER_ASSETS=/path/to/local/envtest \
  go -C app/cetirun-portal test -tags=envtest ./internal/kube -run '^TestRetainedRestartThroughServedAPI$' -count=1
```

It verifies persisted request identity, conditional updates, and CRD validation.
It starts its own API server and etcd, never an existing cluster; it does not
run Pods, contact a model provider, or qualify storage/failure recovery.
Operator CI also runs this test using its existing pinned admission-test tools.

The current portal `make lint` target calls `npm run lint`, but the frontend
does not define that script. Use Go vet, formatting checks, and the frontend
build above until a working lint entry point is added.

### Agent Identity Broker

The Agent Identity Broker retains OBO authorization state, validates the
instance workload binding, and issues a short-lived credential for a fixed
downstream service. Service-specific tools run in the Agent workload and must
keep that credential out of model context, logs, files, command arguments, and
environment variables. Run the Broker's Go and chart checks with:

```bash
go -C app/cetirun-agent-broker test ./... -count=1
go -C app/cetirun-agent-broker vet ./...
make -C app/cetirun-agent-broker build
bash scripts/cetirun-agent-broker-helm-test.sh
```

These are offline checks. They do not call Microsoft Entra, the Auth SDK, Key
Vault, ARM, Resource Graph, or a Kubernetes cluster.

The Agent workload's `cetirun-agent az rest` wrapper has separate offline tests:

```bash
python3 -B -m unittest discover -s app/cetirun-chart/tests -p 'test_agent*.py' -v
python3 -B -m unittest discover -s scripts/tests -p test_agent_id_runtime_assets.py -v
```

The wrapper runs on Linux and uses `/usr/bin/az`, `/dev/stdin`, and anonymous
memory-backed file descriptors under `/proc/self/fd`. When Azure CLI is installed,
the tests exercise it against a loopback fixture using synthetic tokens and an
isolated configuration directory. Otherwise, the real-CLI test is explicitly
skipped; the mocked and subprocess tests still run. This does not qualify the
CLI version in a built runtime image or validate a real OBO session.

Custom Agent Identity also uses the Operator's typed runtime bindings and final
manifest validation. Run its focused checks with:

```bash
go -C app/cetirun-operator test ./internal/runtimebindings ./internal/helm ./internal/controller -run 'TestCustomAgentIdentity|TestAgentIdentityBrokerNamespace' -count=1
```

The Custom image supplies Python and Azure CLI; the platform mounts the shared
CLI and Azure skill read-only. Identity creation and cleanup use the existing
controller lifecycle, not image-owned credentials or new Azure role assignments.

`npm --prefix app/cetirun-portal/web run dev` starts a local frontend server.
Its API proxy expects a backend on localhost and injects development identity
headers. Keep it local; it is not a production authentication configuration.
Starting the Go backend or operator uses Kubernetes configuration and is
outside the offline validation loop.

### Agent Tools MCP core

The standalone MCP service consumes the Broker's opt-in Connection/Binding
APIs and executes JSON REST requests against Azure Resource Manager with an
independent Agent identity. Provider tokens stay in the service. Azure RBAC
controls resource operations; CetiRun checks the Agent's connection binding.

```bash
go -C app/agent-tools-mcp test ./... -count=1
go -C app/agent-tools-mcp test -race ./... -count=1
go -C app/agent-tools-mcp vet ./...
make -C app/agent-tools-mcp build chart-lint
python3 -B -m unittest scripts.tests.test_agent_tools_mcp_chart -v
```

The root Go and Helm gates include this module and its independent chart.
These checks use synthetic workload/provider fixtures and local rendering;
they do not call Azure or Kubernetes. Native runtime bootstrap, connected-service
skills, Portal connection management, and platform umbrella wiring are separate
follow-ups. Existing runtime and authorization paths remain available.
See [Agent Tools MCP](app/agent-tools-mcp/README.md) for its request contract,
TLS prerequisites, and execution limits.

### Runtime chart and Python helpers

The [shared base image version notes](app/cetirun-chart/base-image.md) record
the selected tool releases and compatibility ceilings.

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
python3 -B -m unittest discover -s scripts/tests -v
```

The Helm scripts lint and render the runtime chart for OpenClaw and Hermes,
including invalid-value cases. They use dummy inputs and do not install
resources. They do not validate every chart in the repository or prove that a
live deployment works.

The [Runtime image CVEs workflow](.github/workflows/docker-build-validate.yml)
uses an explicit ten-minute Trivy timeout per runtime image scan, within its
existing job time limit. Scan errors and timeouts still fail the vulnerability
gates; severity thresholds and enabled scanners are unchanged. These image
builds/scans run in CI, not as part of the offline commands above.

The Hermes image build allows up to 30 minutes per source transfer, with at most three
retries, a 15-second retry delay and a 30-minute retry-start window. This avoids
discarding a progressing download on slow runners while keeping retries bounded.
The pinned source checksum is verified before extraction, and the extracted
project version must match the selected release. Download, checksum and version
failures still fail the image build; the workflow's job timeout is unchanged.

The [Memory Context workflow](.github/workflows/memory-context-ci.yml) downloads
its pinned Hermes and OpenClaw qualification hosts separately. These downloads
retry transient failures such as HTTP 429 at most three times, with a 15-second
retry delay and a two-minute retry-start window. Their existing five-minute
transfer limit and the job's 30-minute limit remain unchanged; connections also
have a 15-second timeout. Permanent HTTP errors and integrity mismatches still
fail before host installation or qualification. Offline helper tests use real
curl requests against loopback fixtures to cover recovery and retry exhaustion.

The Python command tests repository helpers with local fixtures. The Istio mesh
reconciliation tests require the pinned PyYAML dependency in
`infra/istio/requirements.txt`. It does not run the Auth/Teams and native
initialization suite. For that suite, prepare the locked dependencies in
`scripts/ci/requirements.txt` separately, then run `make python-test`. It
executes the real init scripts against synthetic HOME
directories and uses test doubles for Hermes interfaces, cloud credentials
and network calls. OpenClaw initialization tests also require `jq`.
Keep live Agent, Teams or other service integration tests separately authorized.

Operator rendering tests retain the original native configuration and resource
baselines. Rewritten init assets and `CETIRUN_INIT_IMAGE` are checked separately
by wiring tests and executable initialization tests. Do not replace baseline
fixtures with changed output merely to make a regression pass.

Custom runtime tests run through the same Go and Python suites. They cover
typed platform bindings, Chat HTTP, Web UI and headless rendering, final manifest
validation, and the common HOME initializer. The VS Code example has a separate
offline suite for its prefix proxy, browser storage and restart behavior:

```bash
node --test examples/vscode-server/test/*.test.mjs
```

These tests start only loopback fixture servers and use synthetic files and
credentials. They do not start VS Code, run Docker or contact GitHub/Copilot.
Image builds, rendered IDE checks and real Copilot tasks are separate checks;
see [the example](examples/vscode-server/README.md).

`make build-images` and `make publish-images` include the `cetirun-pi-agent`
demo. See [its README](examples/pi-agent/README.md) for usage and tests.

### Release-agent build storage

The Azure Pipelines release job prepares Docker storage before building images.
It uses `dockerStorageMount` (default `/mnt`) for Docker layers, BuildKit cache,
and build scratch files instead of the OS disk. The helper logs filesystem sizes,
free space, and inode usage, requires at least 64 GiB free on the selected mount
and on both actual write destinations, and checks the active Docker data root
after restarting the daemon. Existing child mounts and an already-configured
Docker daemon receive the same capacity checks. This free-space threshold is a
guardrail, not a measured maximum release-build size.

`scripts/ci/prepare_docker_storage.py` is an operational helper for **disposable
Linux CI agents only**, not a local validation command. It requires root,
refuses to interrupt running containers, preserves unrelated daemon settings,
and restores the original configuration if activation fails. It uses Docker's
classic `overlay2` image store because the containerd image store does not follow
Docker's `data-root`. Old image/cache directories are not deleted or migrated;
the release starts with a fresh local Docker store and retains staged pushes and
cache pruning between images. Subsequent tasks receive scratch storage via
`TMPDIR`.

If a temporary disk is insufficient, configure a larger data disk on the hosted
pool and set `dockerStorageMount` to its Linux mount path. Adding a disk alone
or increasing the number of agents does not relocate Docker storage. The pipeline
helper does not change the hosted pool, resize disks, or modify other agents.

Offline helper and pipeline-contract tests run with `make ci-test`. They mock
Docker and service operations; only a separately authorized Linux-agent run can
verify the actual mount layout, daemon activation, and release disk headroom.

### Documentation

Check relative links, heading anchors, examples, and the commands you changed.
When the checkout includes `site/`, run `make -C site check` to build and
validate the documentation; the root `make docs-check` delegates to this
command. Site dependency setup may require network access. Run
`git diff --check` for whitespace errors, and inspect new files as well as
tracked changes.

## Generated files and dependencies

Run `make license-check` to check supported first-party source and Makefiles
across the repository, including release and E2E scripts. The `CetiRun license
headers` CI job runs this check on pull requests and pushes to `main`.
See [the header helper commands](scripts/README.md#license-headers) for adding
missing headers. Review authorship first: third-party and generated/scaffold
exceptions in `scripts/license_headers.py` must retain their own notices.

- Operator API definitions live in `app/cetirun-operator/api/v1alpha1/`.
  `zz_generated.deepcopy.go` and the YAML under `config/crd/bases/` are generated
  from those types. Include regenerated artifacts with their source changes.
- The operator requires `controller-gen v0.21.0`. Install it separately with
  `make -C app/cetirun-operator tools` when network setup is authorized.
  This installs into `app/cetirun-operator/bin`; add that directory to PATH
  or set `CONTROLLER_GEN` to its absolute path, then run
  `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local make -C app/cetirun-operator generate manifests`.
  These targets check the version and regenerate deepcopy methods and CRD YAML,
  including the Operator chart's copies under `chart/templates/crds/`, without
  downloading tools. The API type metadata markers generate CRD retention
  annotations; do not add those by editing generated YAML.
  `make -C app/cetirun-operator check-crds` checks the packaged copies.
  `verify-generated` reruns generation and checks both copies and deepcopy
  against Git.
  Review generator upgrades separately. Operator,
  Portal, and operations-agent RBAC remain source Helm/YAML files and must
  name the matching API resources; do not hand-edit generated output.
- Agent API defaulting, compatible/strict decoding and CEL transitions, plus Sandbox
  configuration admission and its outage/cleanup boundary, are covered by
  `make -C app/cetirun-operator test-envtest`. Set `KUBEBUILDER_ASSETS` to local
  Kubernetes 1.37 test binaries prepared separately; Helm 3 or 4 is also required
  to render the actual admission matching rules. The target starts isolated
  API server/etcd processes with RBAC, refuses missing binaries and never uses
  an existing cluster. Loopback TLS tests cover the independent serving process
  and certificate replacement. Version-skew fixtures compare strict rejection
  with compatible handling of new status fields. Helm tests upgrade retained
  CRD templates while requests continue, then verify retention on uninstall.
  Controller tests verify unknown status preservation and resource-version
  conflict handling. Normal `go test ./...` runs unit and
  schema/serialization tests, not envtest.
  The same target also tests trusted Team assignment and metadata-only UID
  reauthorization through the rendered webhooks, final-Pod validation, and the
  native namespace marker/ownership guard (including webhook outages and
  namespace status/finalize updates). These are isolated API-server tests, not
  runtime/Cilium/CSI qualification or permission to lift the execution hold.
  The focused `make -C app/cetirun-operator test-sandbox-integration` target
  exercises the native Sandbox/AgentPolicy controller preparation chain with
  synthetic platform inventory, real API object identities/defaulting and held
  admission expectations. It also checks current grant-journal and owned
  ControllerRevision pins, API resource-version conflicts, post-write source
  races and restart without adding another receipt/history format.
  The ControllerRevision producer is synthetic, not a running StatefulSet
  controller. It stages OpenClaw/Hermes resources at zero replicas;
  it does not start containers, install a production provider, or deploy to an
  existing cluster. These tests inject their platform and fencing dependencies.
  Their results do not qualify the opt-in Kubernetes-backed sources or a
  production writer-exclusion adapter. Stock startup leaves writer exclusion
  unavailable, so enabling sources alone cannot finish held preparation or
  lift execution holds.
  Normal unit tests also cover stale inputs, metadata-only
  refresh/reauthorization, partial writes, restart, grant revocation, suspension
  and ordered cleanup. Regression tests count read/write requests for unchanged
  preparation and ensure Agent status changes do not invalidate authorization
  intent, while generation changes, reauthorization and lifecycle holds do.
  The versioned expectation transition is fail-closed; it does not change CRDs.
  See the [operator integration boundary](app/cetirun-operator/README.md#held-controller-integration)
  for the platform prerequisites and remaining start/observation gates.
  The checksum-pinned Linux amd64/arm64 bundle can be provisioned into a new
  task-local directory after a missing-tool failure:

  ```bash
  # Networked setup, separate from offline validation; refuses overwrites.
  python3 -B scripts/ci/install_tools.py --bin-dir /tmp/cetirun-envtest-1.37.0 envtest
  KUBEBUILDER_ASSETS=/tmp/cetirun-envtest-1.37.0 \
    make -C app/cetirun-operator test-envtest
  ```

  This installs only the manifest-pinned `kube-apiserver`, `etcd` and `kubectl`
  binaries, not a cluster, global tool replacement or a runtime qualification.
  Native controller cases in `test-envtest` exercise the current single-policy
  Team contract through real Agent/Pod admission and Helm SDK actions. They cover
  concurrent HOME binding during readiness, two Agents sharing a policy with
  independent HOME and model keys, guarded policy mutation, and ordinary cleanup
  after restarting with new native starts disabled. Platform, Gateway, scheduler
  and kubelet observations remain synthetic; these are not native UI/model/tool
  acceptance, physical fencing or CSI qualification.
  The focused `make -C app/cetirun-operator test-sandbox-lifecycle` target
  exercises durable writer-history and start/shared-change coordination with
  those same isolated API servers. Provider responses and physical
  machine/storage identities are synthetic. It tests persistence, CAS races,
  restart recovery and unknown-outcome handling, not actual fencing, volume
  release or live Agent execution. Unit tests additionally cover the native
  held-preparation bridge, pre-start acknowledgement, current observations,
  route/readiness ordering and the journal-backed writer integration.
  No production provider is installed or implicitly qualified by these checks.
  Concrete configuration updates are covered by the coordination package and
  `cmd/sandbox-config-change` tests. Their API-server cases exercise real
  conditional writes and observation-only recovery, not a runtime provider.
  Build the explicit command with
  `make -C app/cetirun-operator build-sandbox-config-change`; do not run it against
  a cluster as part of an offline check. It requires separately authorized
  before/after configuration inputs and an explicitly pinned authority.
  The same suites cover conditional configuration deletion and observation-only
  recovery using the original before input or, for a Portal deletion with
  recorded recovery metadata, its operation ID without a body. Both paths retain
  the same UID/metadata safety and do not prove physical cleanup. Existing
  records without the optional metadata require `--before`; upgrade all strict
  journal readers before issuing new metadata. Real API cases exercise UID/version
  conflicts, replacement protection, served deletion admission and independent
  finalizer cleanup; no live cluster or physical writer is involved.
  Running-input tests additionally check a synthetic held-to-running handoff
  against real Pod creation, API binding/defaulting and final-Pod admission.
  They contain no scheduler, kubelet or guest-external runtime observer. Run the
  focused API test with `go test -tags=envtest ./test/integration -run
  '^TestSandboxRunningInputs$' -count=1` from the Operator module, with the same
  explicit local envtest assets. Its synthetic enrollment is not a runnable
  activation procedure.
- Helm files under `templates/` are source templates. Rendered Kubernetes
  manifests are validation output, not replacements for those templates.
- Keep `go.mod` and `go.sum` together when dependencies change. Keep the portal's
  `package.json` and `package-lock.json` consistent; use `npm ci` for installation.
- Dependabot groups shared Go builder updates across the application
  directories. The runtime base exposes its default image in the `go-toolchain`
  stage; `GO_BUILD_IMAGE` still accepts an explicit override. New Go components
  must be included in the Docker and Go module update directories. CI checks
  that every Go builder is discoverable and shares the pinned digest.
  A Go release upgrade also needs matching changes to `.go-version` and every
  module's `go` directive; grouping Docker updates does not make those changes.
- Do not commit binaries, `web/dist`, `node_modules`, Python caches or virtual
  environments, coverage output, rendered secrets, or local configuration.
- Preserve third-party copyright, license, and source attribution.

## Before review

Inspect `git status --short` and your diff. Include tests and documentation for
behavior changes, check the [public-data boundary](CONTRIBUTING.md#keep-contributions-safe-to-publish),
and list the exact validation performed. Separate a passing offline check from
an unrun integration, deployment, or release check. Never describe a limited
test run as proof that every security check passed.
