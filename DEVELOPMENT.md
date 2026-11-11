# Developing CetiRun

This guide is for contributors who change the CetiRun source. It describes the
repository layout, the tools, and the checks to run for each change. Most
checks run offline. They do not need an Azure subscription, cluster access, or
a live Agent account. The download of tools and dependencies is a separate
step that needs network access.

For the contribution process, refer to [CONTRIBUTING.md](CONTRIBUTING.md). For
a short list of the root checks, refer to
[Run local checks](site/content/en/docs/getting-started/local-checks.md). For
documentation changes, refer to
[Contribute to documentation](site/content/en/docs/contributing/site.md).

## Quick start

1. Install the tools and dependencies in
   [Tools and dependencies](#tools-and-dependencies).
2. Create a branch with a meaningful name, for example
   `git switch -c fix/instance-status`.
3. Make your change.
4. Run the checks for the paths that you changed. Refer to
   [Checks for each change](#checks-for-each-change).
5. Run the full gate. If you changed only `site/`, `docs/`, or root Markdown
   files, `make docs-check` is the full gate. For all other changes, run
   `make check`, and also run `make docs-check` if you changed documentation.
6. Do the steps in [Before review](#before-review).

## Repository layout

This table shows the source directory of each component. For what each
component does and how the components work together, refer to
[Architecture](site/content/en/docs/concepts/architecture.md).

| Path | Source |
| --- | --- |
| `app/cetirun-operator/` | Operator, API types and generated CRDs, Operator chart, and Custom terminal |
| `app/cetirun-portal/` | Portal backend (Go) and Portal chart |
| `app/cetirun-portal/web/` | Portal frontend (React and TypeScript) |
| `app/cetirun-agent-broker/` | Agent Identity Broker |
| `app/cetirun-connections-mcp/` | Connections MCP |
| `app/cetirun-memory-context/` | Memory Context helper (Go) and its native adapters |
| `app/cetirun-ai-gateway/` | AI Gateway image and chart |
| `app/cetirun-observability/` | ADX-Mon image and chart for logs, events, and metrics |
| `app/cetirun-platform/` | Platform chart that `make deploy` installs |
| `app/cetirun-chart/` | Runtime chart, Agent image definitions, and runtime plugins |
| `app/internal/` | Shared Go packages |
| `infra/` | Bicep templates, Istio chart, storage, and cert-manager configuration |
| `scripts/` | Validation runner, release and deployment helpers, and their tests |
| `examples/` | [Examples](site/content/en/docs/reference/examples.md) |
| `docs/` | Pointers to site pages, and project assets |
| `site/` | Documentation site (Hugo and Docsy) |

Put application packages in `internal/`, not in `pkg/`.

`app/internal` holds shared Go packages: agent tools and credential policy
(`agenttools`), chat access (`chataccess`), Pod templates (`podtemplate`),
registry authentication (`registryauth`), terminal policy (`terminalpolicy`),
and TLS reload (`tlsreload`). The Operator, the Portal, the Agent Identity
Broker, and Connections MCP import these packages. Go allows imports of this
module only from modules under `github.com/Azure/CetiRun/app`.

These four modules use a local `replace` directive for `app/internal`.
`app/internal`, the Portal, and the Agent Identity Broker also replace the
Operator module for its API types. Keep all of these modules in one checkout.

## Tools and dependencies

Install only the tools for the components that you change. The table shows
where each version is pinned and which checks use the tool.

`make check` needs Linux or a Linux container. The Memory Context module
builds only on Linux, and `make go-test`, `make go-lint`, and
`make memory-native-test` include it.

| Tool | Version source | Used by |
| --- | --- | --- |
| Git, Bash, Make, curl | Not pinned | All repository commands. Loopback download tests also use curl. |
| Go | [`.go-version`](.go-version) | Go tests and lint, the Memory native test, and the Hugo installation |
| Node.js 24 and npm | CI configuration | Portal frontend, Memory native test (`make test`), examples, and the site |
| Python 3.11 or later | CI uses 3.12 | All Python gates and helpers. `make license-check` needs 3.10 or later. The site validator needs 3.11 or later. |
| Python packages | [`scripts/ci/requirements.txt`](scripts/ci/requirements.txt) | `make ci-test`, `make chart-check`, and `make python-test` |
| Helm 3 or 4 | [`scripts/ci/tools.json`](scripts/ci/tools.json) (CI uses this version) | `make chart-check`, `make set-version`, and the envtest suites |
| Bicep, ShellCheck, actionlint | [`scripts/ci/tools.json`](scripts/ci/tools.json) | `make bicep-check`, `make shell-check`, and `make workflow-check` |
| envtest binaries (`kube-apiserver`, `etcd`, `kubectl`) | [`scripts/ci/tools.json`](scripts/ci/tools.json) | API-server tests. Refer to [Prepare envtest assets](#prepare-envtest-assets). |
| controller-gen | `CONTROLLER_GEN_VERSION` in [`app/cetirun-operator/Makefile`](app/cetirun-operator/Makefile) | CRD and deepcopy generation |
| Hugo extended | `HUGO_VERSION` in [`site/Makefile`](site/Makefile) | `make docs-check`. The site build compiles it with Go and a C/C++ compiler. |
| Docker | Not pinned | Image builds and the optional image tests |

When you update a tool version, change its source file and the CI
configuration. Keep dependency changes separate from unrelated work.

### Install the pinned tools

The [pinned tool installer](scripts/README.md#pinned-test-tools) supports only
Linux on amd64 and arm64. It downloads checksum-pinned binaries into a new,
empty directory and does not change global tools. On macOS, install the same
versions with a package manager.

```bash
tools="${XDG_CACHE_HOME:-$HOME/.cache}/cetirun/tools"
python3 -B scripts/ci/install_tools.py --bin-dir "$tools" actionlint shellcheck bicep helm
export PATH="$tools:$PATH"
```

The installer refuses to overwrite a file. To install the tools again, for
example after a version pin changes, delete the `$tools` directory first. The
same rule applies to the `$assets` directory in
[Prepare envtest assets](#prepare-envtest-assets).

Install the Python packages in a virtual environment outside the repository.
Most Python tests and the chart render scripts import PyYAML from these
packages:

```bash
python3 -m venv "${XDG_CACHE_HOME:-$HOME/.cache}/cetirun/venv"
. "${XDG_CACHE_HOME:-$HOME/.cache}/cetirun/venv/bin/activate"
python3 -m pip install -r scripts/ci/requirements.txt
```

### Download dependencies

The offline gates set `GOPROXY=off`, `GOTOOLCHAIN=local`,
`GOFLAGS=-mod=readonly`, and npm offline mode. They fail when a dependency is
not in the local cache. They never start a download. To run a single Go command
the same way, start it with `GOPROXY=off GOTOOLCHAIN=local GOFLAGS=-mod=readonly`.

Download the dependencies one time when network access is available. Run only
the lines for the checks that you need:

```bash
# All Go checks
for module in app/internal app/cetirun-operator app/cetirun-portal \
  app/cetirun-agent-broker app/cetirun-connections-mcp app/cetirun-memory-context; do
  go -C "$module" mod download
done
# Portal frontend checks and make test
make -C app/cetirun-portal frontend-install NPM_REGISTRY=https://registry.npmjs.org
# Custom terminal, example, and Pi Memory tests
npm --prefix examples/pi-agent ci --ignore-scripts --no-audit --no-fund
# Documentation checks only
make -C site build
```

For the site setup and the `HUGO` override, refer to
[Check and preview](site/content/en/docs/contributing/site.md#check-and-preview).

Package downloads use public npm and PyPI by default. To use a different
mirror, set the `NPM_REGISTRY`, `OPENCLAW_NPM_REGISTRY`, or `PYPI_INDEX_URL`
build input, or the `skills.npmRegistry` chart value. Do not supply production
credentials to get a development dependency.

### Prepare envtest assets

API-server tests start a local `kube-apiserver` and `etcd`. They need the
pinned envtest binaries in `KUBEBUILDER_ASSETS` and Helm 3 or 4 on `PATH`. The
pinned tools above include Helm. Install the envtest binaries into their own
directory:

```bash
assets="${XDG_CACHE_HOME:-$HOME/.cache}/cetirun/envtest"
python3 -B scripts/ci/install_tools.py --bin-dir "$assets" envtest
export KUBEBUILDER_ASSETS="$assets"
```

This command installs `kube-apiserver`, `etcd`, and `kubectl`. It does not
install a cluster. The API-server tests never connect to a cluster that
already exists.

## Validate a change

Run the commands in this section from the repository root. These commands do
not log in to Azure, connect to a cluster, or run deployment scripts.

### Root gates

`make check` runs `make lint` and `make test`. Documentation is separate. Most
gates are targets of the [validation runner](scripts/ci/validate.py). To see
them, run `python3 -B scripts/ci/validate.py --help`. `version-check`,
`artifact-check`, and `memory-native-test` are recipes in the root `Makefile`.

| Command | What it runs |
| --- | --- |
| `make test` | `ci-test`, `python-test`, `go-test`, `portal-test`, and the Memory Context native test (`memory-native-test`) |
| `make lint` | `version-check`, `artifact-check`, `license-check`, `python-check`, `go-lint`, `portal-lint`, `chart-check`, `bicep-check`, `shell-check`, and `workflow-check` |
| `make ci-test` | Python `unittest` suites in `scripts/ci/tests`, `scripts/tests`, and `app/cetirun-operator/internal/cetiadmin/tests`. Needs PyYAML. |
| `make python-test` | `pytest` suites in `app/cetirun-chart/tests`. Needs all packages in `scripts/ci/requirements.txt`. |
| `make go-test` | Tests of each Go module. The Operator uses `make -C app/cetirun-operator test`. |
| `make go-lint` | `go vet ./...` in each Go module |
| `make portal-test` | `npm test` in `app/cetirun-portal/web` |
| `make portal-lint` | The TypeScript project check (`tsc -b`) in `app/cetirun-portal/web` |
| `make memory-native-test` | `node --test app/cetirun-memory-context/tests/native.test.mjs`. It builds the Memory helper offline with Go. |
| `make chart-check` | `helm lint --strict` and `helm template` for every chart in `CHARTS` of the validation runner, with synthetic values. Then the render regression scripts of the charts. Needs PyYAML. |
| `make bicep-check` | `bicep build --no-restore` for every Bicep file |
| `make shell-check` | `bash -n` and `shellcheck --severity=error` for every shell script |
| `make workflow-check` | `actionlint` for the workflows in `WORKFLOWS` of the validation runner. It needs ShellCheck. Workflows that are not in that list are not linted. |
| `make python-check` | A syntax parse of every Python file |
| `make license-check` | The source header check. Refer to [Generated files and dependencies](#generated-files-and-dependencies). |
| `make version-check` | The consistency of the release version fields |
| `make artifact-check` | The release inventory against the repository charts and build scripts |
| `make docs-check` | `make -C site check` in offline mode. Run `make -C site build` one time with network access first. |

`chart-check` runs `helm dependency build --skip-refresh` for the platform
chart before it renders. This command uses the local subcharts and writes the
ignored `app/cetirun-platform/charts/` directory.

`make lint` does not run a separate Go linter. Do not use the `lint` targets
in `app/cetirun-portal/Makefile` and `app/cetirun-operator/Makefile`. The
Portal target needs `golangci-lint` and an `npm run lint` script that does not
exist. The Operator target has no recipe and does nothing.

### Checks for each change

While you work, run the narrow checks for the paths that you changed. The full
gate in the [Quick start](#quick-start) runs the rest.

| If you change | Run |
| --- | --- |
| `app/cetirun-operator/api/` | [Generated files and dependencies](#generated-files-and-dependencies), then [Operator](#operator) |
| Other Operator code | [Operator](#operator). For admission, Sandbox, and webhook code, also [Operator API-server tests](#operator-api-server-tests). |
| Operator chart | `make chart-check`. It also runs `scripts/cetirun-platform-helm-test.sh`, which compares the chart CRD copies with the generated CRDs. |
| `app/internal/` | [Shared internal packages](#shared-internal-packages). The full gate then tests every consumer module. |
| Portal frontend or Go code | [Portal](#portal) |
| `app/cetirun-agent-broker/` or `app/cetirun-connections-mcp/` | [Agent Identity Broker](#agent-identity-broker) |
| `app/cetirun-memory-context/` | [Memory Context](#memory-context) |
| `app/cetirun-observability/` | `bash scripts/cetirun-observability-helm-test.sh` and `python3 -B -m unittest scripts.tests.test_observability_chart -v`. CI also builds and scans the image. |
| `app/cetirun-ai-gateway/` | [AI Gateway](#ai-gateway) |
| `app/cetirun-chart/` | [Runtime chart and Python helpers](#runtime-chart-and-python-helpers) |
| `app/cetirun-platform/` or `infra/` | [Platform chart and infrastructure](#platform-chart-and-infrastructure) |
| `scripts/` | The test module of the script, for example `python3 -B -m unittest scripts.tests.test_release_inventory -v` |
| `.github/workflows/` | `make workflow-check` |
| `examples/` | [Examples](#examples) |
| `site/`, `docs/`, or a root Markdown file | `make docs-check` |
| Markdown under `app/` | The checks of that component. Some of these files are chart or test inputs. |

`python3 -B scripts/ci/validate.py go-test --component <module>` runs the tests
of one Go module. The `go-lint` gate accepts the same option. The module names
are `internal`, `operator`, `portal`, `agent-broker`, `memory-context`, and
`cetirun-connections-mcp`. The full `make test` runs every module. The Operator
suites alone use the `GO_TEST_TIMEOUT` and `GO_ENVTEST_TIMEOUT` limits of the
Operator Makefile, 30 and 50 minutes for each package.

### CI jobs and local commands

Pull requests show CI job names. Use this table to find the local command for a
job that failed. The table gives the main command of each job. For the exact
steps, read the `run:` steps of the job in its workflow.

| CI job | Workflow | Local command |
| --- | --- | --- |
| `CetiRun license headers` | `cetirun-validation.yml` | `make license-check` |
| `CetiRun policy and helper tests` | `cetirun-validation.yml` | `make version-check python-check ci-test` |
| `CetiRun auth and runtime tests` | `cetirun-validation.yml` | `make python-test` |
| `CetiRun Bicep shell and workflows` | `cetirun-validation.yml` | `make bicep-check shell-check workflow-check` |
| `CetiRun Connections MCP offline Go checks` | `cetirun-validation.yml` | `go -C app/cetirun-connections-mcp test -race ./... -count=1` |
| `Helm all charts` | `helm-lint-test.yml` | `make chart-check` |
| `Operator Go unit and build checks (N/4)` | `operator-ci.yml` | `make -C app/cetirun-operator test GO_TEST_SHARD_INDEX=N GO_TEST_SHARD_COUNT=4`. The job also runs the `app/internal` tests, `go vet`, the terminal reconnect test, and the builds. |
| `Operator API admission checks (N/4)` | `operator-ci.yml` | `make -C app/cetirun-operator verify-generated`, then `make -C app/cetirun-operator test-envtest GO_TEST_SHARD_INDEX=N GO_TEST_SHARD_COUNT=4`, then the Portal API-server test in [Portal](#portal) |
| `Operator Go checks` | `operator-ci.yml` | None. This job passes only when every unit and API shard passes. |
| `Portal frontend checks` | `portal-ci.yml` | `npm ci`, `npm test`, and `npm run build` in `app/cetirun-portal/web`. The job also checks the license supplements and the generated notice. |
| `Portal Go checks` | `portal-ci.yml` | `go -C app/cetirun-portal test ./... -count=1`. CI uses a placeholder frontend. |
| `Agent Identity Broker Go checks` | `agent-broker-ci.yml` | `go -C app/cetirun-agent-broker test ./... -count=1` |
| `Memory Context Go checks` | `memory-context-ci.yml` | `go -C app/cetirun-memory-context test -race ./... -count=1 -timeout=20m`. For the native host tests, refer to [Memory Context](#memory-context). |
| `CetiRun docs checks` | `cetirun-docs-validation.yml` | `make -C site check` |
| `Install and upgrade on kind` | `ai-gateway-cluster-test.yml` | No offline command. Refer to [AI Gateway](#ai-gateway). |

These CI checks have no `make check` equivalent: the image CVE scans, the
`govulncheck` steps, CodeQL, and the dependency review. `make check` also runs
the Connections MCP and Memory Context tests without `-race`.

## Run components locally

These commands start processes on your workstation. The Portal backend and the
Operator connect to the cluster of your current kubeconfig context. These
commands are not offline checks.

### Start the Portal frontend

Start the frontend development server:

```bash
DEV_USER=alice make -C app/cetirun-portal frontend-dev
```

The server sends `/api` requests to a backend at `http://localhost:8080`. It
adds `X-Auth-Request-*` identity headers for `alice@example.com`. In a
deployment, OAuth2 Proxy sets these headers. Keep this server on your
workstation. It is not an authentication configuration for a deployment.

### Start the Portal backend

> **Warning:** The backend uses your current kubeconfig context. It reads and
> changes CetiRun resources in that cluster. Use only a development cluster
> that you have authorization to change.

The backend binary embeds `web/dist`, which a new checkout does not have. The
first time, build the frontend and start the backend with one command:

```bash
make -C app/cetirun-portal run
```

After the frontend is built, `make -C app/cetirun-portal backend-run` starts
only the backend. The backend reads its settings from the environment
variables in
[`internal/config/config.go`](app/cetirun-portal/internal/config/config.go).
It listens on `LISTEN_ADDR` (default `:8080`) and `INTERNAL_LISTEN_ADDR`
(default `:8081`). The `FEATURE_*` settings are off unless you set them to
`true`.

### Start the Operator

> **Warning:** A local Operator reconciles the CetiRun resources in the
> cluster of your current kubeconfig context. Use only a disposable
> development cluster that has the CetiRun CRDs. Make sure that no other
> Operator runs in that cluster. Give the local Operator the same environment
> variables as the deployed Operator. Without the AI Gateway settings, it
> scales every built-in Agent to zero and stops the Sandbox Agents.

```bash
go -C app/cetirun-operator run ./cmd/main.go \
  --metrics-bind-address=:8090 --health-probe-bind-address=:8091
```

The default metrics address of the Operator is `:8080`, which is also the
default Portal address. The flags above prevent a port conflict. To see all
flags, add `--help`. In a deployment, the Operator chart sets the environment
variables in `chart/templates/deployment.yaml`. With `AI_GATEWAY_ENABLED=true`,
the Operator also needs `AI_GATEWAY_URL`, `AI_GATEWAY_MASTER_KEY_SECRET`, and
`AI_GATEWAY_DEFAULT_BUDGET`, or it stops at startup. `AI_GATEWAY_URL` must be
a cluster Service URL, `http://<service>.<namespace>.svc.cluster.local:<port>`.
The Operator sends requests to this URL, so your workstation must be able to
reach it.

## Add an agent integration

To run a different agent, package it as a Custom runtime image. A Custom image
does not change this repository. For the image contract, refer to
[Custom runtime](site/content/en/docs/reference/agent-harness.md#custom-runtime).
For working images, refer to [Examples](site/content/en/docs/reference/examples.md).

A new built-in agent type, such as OpenClaw or Hermes, changes several
components:

- The `spec.agent.type` enum in `app/cetirun-operator/api/v1alpha1/harness_types.go`
  and the generated CRDs.
- The Operator registry in
  `app/cetirun-operator/internal/agents/builtin/registry.go` and a harness
  package.
- The runtime chart: `app/cetirun-chart/agents/<name>/` and its templates.
- The release inventory in `scripts/release-artifacts.json`.
- The Portal, which has its own list of agent types.

These changes affect the API contract and add runtime dependencies. Before you
start, open an issue with a proposal, as [GOVERNANCE.md](GOVERNANCE.md)
describes.

## Component checks

While you change a component, run its focused checks instead of the full
suite. The full gate in the [Quick start](#quick-start) runs before review.

Many of these checks are API-server tests. They need the assets from
[Prepare envtest assets](#prepare-envtest-assets). The scheduler, kubelet,
Gateway, storage, and CSI observations in these tests are synthetic. A pass
does not show that a workload runs on a real cluster. It is not a
qualification of the runtime, Cilium, or CSI.

### Shared internal packages

```bash
go -C app/internal test ./... -count=1
go -C app/internal vet ./...
go -C app/internal test -race ./agenttools ./tlsreload -count=1
```

### Operator

```bash
make -C app/cetirun-operator test
go -C app/cetirun-operator vet ./...
make -C app/cetirun-operator build
```

`make test` tests every package with `go list ./...`. It tests the controller
package in two groups: the tests that match `GO_CONTROLLER_TEST_GROUP` in the
Makefile, and then all other tests. Every test runs one time, with the
`GO_TEST_TIMEOUT` limit. The build writes `bin/manager` and
`bin/skill-marketplace`. It does not start the Operator.

`make test-envtest` runs the packages in `GO_ENVTEST_PACKAGES` in two passes:
first the tests whose names contain `APIServer`, and then all other tests. Each
pass has the `GO_ENVTEST_TIMEOUT` limit for each package. A failure in either
pass fails the target.

Operator CI divides both targets into four shards. To run one CI shard, set its
one-based index and the shard count:

```bash
make -C app/cetirun-operator test GO_TEST_SHARD_INDEX=1 GO_TEST_SHARD_COUNT=4
make -C app/cetirun-operator test-envtest GO_TEST_SHARD_INDEX=1 GO_TEST_SHARD_COUNT=4
```

To cover the full suite, run indexes 1 through 4. The shared runner
[`scripts/ci/go_test.py`](scripts/ci/go_test.py) lists the tests with
`go test -json -list .`. It assigns each top-level test name to one shard with
a stable hash. New tests are included automatically. A discovery failure, an
invalid index, an empty shard, or a test failure fails the command.

To check the format of changed Go files, use `gofmt -l`. Use `gofmt -w` only on
files that you intend to change.

### Operator API-server tests

`make -C app/cetirun-operator test-envtest` covers these items:

- Agent API defaulting, compatible and strict decoding, and CEL transitions.
- Sandbox configuration admission and its outage and cleanup behavior.
- Trusted Team assignment, metadata-only reauthorization,
  [final-Pod admission](app/cetirun-operator/README.md#optional-final-pod-admission),
  and the native namespace guard.
- Loopback TLS serving and certificate replacement.
- CRD upgrades through Helm while requests continue, and CRD retention after an
  uninstall.
- Native controller cases for the single-policy Team contract, with real Agent
  and Pod admission and Helm SDK actions.

`go test ./...` without the `envtest` tag does not run these tests.

Two focused targets run faster subsets of these packages. Use them while you
work on the Sandbox bootstrap or the lifecycle coordination:

```bash
make -C app/cetirun-operator test-sandbox-integration
make -C app/cetirun-operator test-sandbox-lifecycle
```

`test-sandbox-integration` tests the protected namespace and HOME bootstrap,
the ownership of published skills, and the rejection of unsupported held
profiles. `test-sandbox-lifecycle` tests the durable writer history and the
coordination of starts and shared changes, including restart recovery and
unknown outcomes. The provider responses and the machine and storage
identities are synthetic.

The coordination package and `cmd/sandbox-config-change` tests cover
conditional configuration updates and deletions, and a recovery that only
observes the current state. To build the configuration-change command, run
`make -C app/cetirun-operator build-sandbox-config-change`. Do not run this
command against a cluster as an offline check. It needs authorized before and
after inputs and an explicit authority pin.

The native-start tests and the ingress tests of the
[common executor](app/cetirun-operator/README.md#common-sandbox-reconciliation-explicit-opt-in),
the shared reconciliation flow for new Sandbox Agents, run from the Operator
module:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/controller \
  -run '^(TestNativeSandboxStartAPIServer|TestReconciledSandboxIngressMTLSAPIServer)$' -count=1
```

For the platform conditions that these tests do not cover, refer to the
[held controller integration](app/cetirun-operator/README.md#held-controller-integration).

### Operator Sandbox regressions

Each command below is a focused API-server test. Run the test when you change
the feature in its heading.

Planned migration:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/admission -run '^TestPlannedMigrationServedAPIAndBinding$' -count=1
```

This test checks the migration schema, finalizer authorization, and the Pod
binding APIs. Unit tests cover the order of CSI acknowledgements, repeated
moves, lost writes, suspension, and deletion.

HOME access mode of the common executor:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxRWOHomeAPIServer$' -count=1
```

This test checks Agent authorization, the chart render, Pod admission, and that
the HOME access mode cannot change. Unit tests cover `ReadWriteOnce` and
`ReadWriteOncePod` restarts, shutdown and resume, and migration.

Ordinary-container (`container`) backend:

```bash
go -C app/cetirun-operator test ./api/v1alpha1 ./internal/sandbox ./internal/sandboxplatform ./internal/sandboxworkload -count=1
go -C app/cetirun-operator test ./internal/controller -run '^TestContainerSandbox' -count=1 -timeout=20m
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestContainerSandbox(Common|IngressMTLS)APIServer$' -count=1 -timeout=20m
```

These tests cover registration and handler pins, render and admission,
per-container resources without VM overhead, both built-in harnesses, both HOME
modes, restart, suspension, and migration. They do not run runc or Kata. For
the runtime selector in the Portal, run the frontend tests and build.

Rate-limit updates of common Sandbox policies:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxPendingRateLimitsAPIServer$' -count=1
```

This test adds and removes rate limits before the first Ready observation. It
checks that the workload, Pod, key, and reservation identities do not change.

Kata internal CIDR grants:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxInternalNetworkAPIServer$' -count=1
```

This test checks the configuration binding, default TCP ports, the owned
Cilium policy, protected network conflicts, and Pod admission after policy
drift. For checks on an authorized cluster, refer to the
[internal-egress example](examples/native-sandbox/README.md#scoped-internal-egress).

Kubernetes API access for Kata Agents:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxKubernetes(Access|PostSubmitRecovery|RejectedGrantRecovery|FirstInstallRejection)APIServer$' -count=1
```

This test uses real RBAC and admission for OpenClaw and Hermes. It covers
namespace and cluster grants, token projection, role-version drift, access
withdrawal, and cleanup after a restart. Unit tests cover missing and foreign
approvals, lost acknowledgements, first-install recovery, and Helm submission
checks. Token rotation by the kubelet, API requests from a running Agent, and
Cilium enforcement need a live check. Refer to the
[administrator setup](examples/native-sandbox/README.md#kubernetes-api-access).

Registered Kata SCTP rules:

```bash
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxSCTPNetworkAPIServer$' -count=1
```

This test checks the stored SCTP and default TCP fields, Cilium compilation,
conflicting bindings, malformed requests, and Pod admission after drift. It
does not test SCTP traffic. For the requested and qualified contract, refer to
the [SCTP example](examples/native-sandbox/README.md#governed-sctp-egress).

### CetiAdmin binding cleanup

Run these checks when you change the deletion of an Agent that has a CetiAdmin
exec binding:

```bash
go -C app/cetirun-operator test ./internal/admission ./internal/controller -run '^TestCetiAdmin' -count=1
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestCetiAdminBoundAgentDeletionAPIServer$' -count=1
```

The API-server test uses the rendered admission rules and real RBAC. It
deletes suspended Custom, OpenClaw, and Hermes Agents that keep older image
and chart pins and an earlier native-auth rotation marker. The OpenClaw and
Hermes fixtures also keep a rotation receipt on their native auth Secret. The
test deletes the Secrets with a separate ServiceAccount that has Secret
permissions only in the Agent namespace. It does not use the Operator or an
administrator identity for this. The test checks that the binding is withdrawn
while the Agent is deleting, that a Portal-style service account with only
Agent API rights cannot edit the binding, and that the Operator finalizer
cleanup completes. It simulates the namespace and PVC controllers that envtest
does not run. It does not test storage reclamation, runtime images, or a live
cluster.

Receipt annotations protect rotation writes. They do not protect the Secret
from deletion. An identity that can delete Secrets can also delete a Secret
that has a receipt in a live namespace. The admission unit tests cover this
limit and the data updates that keep the receipt. They also check that the
admission rejects an unauthorized creation, replacement, or removal of a
receipt. The deletion restrictions of CetiAdmin and the protection of the
installation marker still apply.

### Custom terminal

The Custom terminal uses its own image and
`app/cetirun-operator/Dockerfile.terminal`. Its Go entrypoint is in the
Operator module, so that it can use the Agent API types. It runs as a separate
Deployment with its own ServiceAccount.

```bash
go -C app/cetirun-operator test ./internal/terminal -count=1
node --test app/cetirun-operator/internal/terminal/reconnect.test.cjs
make -C app/cetirun-operator build-terminal
```

The build target also builds `bin/loopback-probe`, a static HTTP probe in the
terminal image for governed Custom HTTP workloads. When you change Custom
rendering, admission, or the terminal, also run these checks:

```bash
go -C app/cetirun-operator test ./cmd/loopback-probe ./internal/helm ./internal/sandboxpolicy ./internal/controller -run 'TestCommonCustom|TestCustomSandbox|TestCompileCustom|TestProbe' -count=1 -timeout=20m
go -C app/cetirun-agent-broker test ./internal/broker -run '^TestCustom' -count=1
npm --prefix examples/pi-agent test
python3 -B -m unittest discover -s scripts/tests -p test_custom_initialization.py -v
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestCommonCustom(Terminal)?APIServer$' -count=1 -timeout=20m
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^(TestNativeEntraCommonRuntimeAPIServer|TestNativeConnectionsServedCurrentSourceProof|TestReconciledSandboxIngressMTLSAPIServer)/custom$' -count=1 -timeout=20m
```

`npm --prefix examples/pi-agent test` needs the dependencies from
[Download dependencies](#download-dependencies). These checks cover both
registered backends, all three Custom endpoint modes, the policy schema, RBAC,
final-Pod admission, restart, suspension, migration, and terminal isolation.
When you change Custom owner access, also run the Portal backend, frontend, and
chart checks.

Portal mTLS Chat tests use a fixture that the policy compiler generates. After
an intentional contract change, generate the fixture again with the pinned Go
toolchain, and then test the Portal:

```bash
CETIRUN_UPDATE_CHAT_FIXTURE=true go -C app/cetirun-operator test ./internal/sandboxpolicy -run '^TestCustomMTLSPortalFixture$' -count=1
go -C app/cetirun-portal test ./internal/kube ./internal/handler -run 'TestCommonChat|TestChatOwnerBindingAndForwarding' -count=1
```

These checks do not test Istio credentials or TLS handshakes.

The image build is a separate step. It can download pinned base images and Go
dependencies:

```bash
REGISTRY=registry.example.com PUSH=false bash scripts/terminal-docker-build.sh
```

### Portal

For a frontend change, run the tests and the production build:

```bash
npm --prefix app/cetirun-portal/web test
npm --prefix app/cetirun-portal/web run build
```

The build runs the TypeScript checks and creates `web/dist`. The Go entrypoint
embeds that directory. When `web/dist` does not exist, `make go-test` and
`make go-lint` copy the Go modules to a temporary directory and add a
placeholder `index.html`. Portal CI also uses a placeholder. These runs test
the Go code only. Build the frontend before you test or build the complete
Portal module:

```bash
go -C app/cetirun-portal test ./... -count=1
go -C app/cetirun-portal vet ./...
make -C app/cetirun-portal build
```

For backend-only work without frontend dependencies, test only the internal
packages. This check does not compile the embedded UI. Report this narrower
scope.

```bash
go -C app/cetirun-portal test ./internal/... -count=1
go -C app/cetirun-portal vet ./internal/...
```

The lifecycle and configuration APIs of the Portal have API-server tests.
Operator CI also runs them:

```bash
go -C app/cetirun-portal test -tags=envtest ./internal/kube ./internal/handler -run '^Test(RetainedRestartThroughServedAPI|SandboxDefaultsThroughPortalAndServedAPI|TeamGenerationThroughPortalAndServedAPI)$' -count=1
```

They test the stored restart identity, conditional suspend and resume,
duplicate requests, stale UIDs and versions, CRD validation, and Sandbox
defaults through the Portal and the API server. The Team case checks
status-only resource-version changes, configuration generations, replacement
UIDs, saves without changes, and deletion through both direct and coordinated
writes. The tests do not run Pods or connect to a model provider.

The frontend build generates `web/dist/THIRD_PARTY_LICENSES.txt` from the
installed production dependencies. The Portal serves this file at
`/THIRD_PARTY_LICENSES.txt`. If a dependency has no license file, the build
fails unless `web/licenses/supplements.json` records a checksum-verified
supplement for that exact version. When you update dependencies, follow the
[supplement update procedure](app/cetirun-portal/web/licenses/README.md).
`npm test` includes the generator tests.

To generate the notice without a frontend build, run this command after
`npm ci`. Keep the output out of Git:

```bash
npm --prefix app/cetirun-portal/web run licenses -- --output /tmp/cetirun-portal-notices/THIRD_PARTY_LICENSES.txt
```

The generator reads local package metadata and license files. It does not use
the network or run dependency code. This notice covers only the JavaScript
dependencies of the Portal. The root `NOTICE` keeps the manual attributions and
the open provenance items.

### Managed Agent ingress mTLS

Run these checks when you change the ingress mTLS endpoint of common-executor
Agents. The transport test (`-tags=envoy`) needs an Envoy 1.36.3 binary. The
test rejects other versions. On Linux x86-64, download and verify the release
binary outside the repository first. This step needs network access:

```bash
mkdir /tmp/cetirun-envoy-1.36.3
curl -fsSL -o /tmp/cetirun-envoy-1.36.3/envoy \
  https://github.com/envoyproxy/envoy/releases/download/v1.36.3/envoy-1.36.3-linux-x86_64
echo "6dcacec73f0ff18d2f68f605f9f041cd0e1c4d30d3c38977058cd923daa2dea6  /tmp/cetirun-envoy-1.36.3/envoy" | sha256sum --check
chmod +x /tmp/cetirun-envoy-1.36.3/envoy
```

Then run the checks:

```bash
go -C app/cetirun-operator test ./internal/sandboxpolicy ./internal/controller -run 'TestIngressMTLS|TestReconciledSandboxIngressMTLS' -count=1
python3 -B -m unittest discover -s app/cetirun-chart/tests -p test_hermes_ingress.py -v
python3 -B -m unittest discover -s app/cetirun-chart/tests -p test_desktop_ingress.py -v
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxIngressMTLS((Preflight|FinalGuard)Recovery)?APIServer$' -count=1
CETIRUN_ENVOY_BINARY=/tmp/cetirun-envoy-1.36.3/envoy \
  go -C app/cetirun-operator test -tags=envoy ./internal/ingressmtls -count=1
```

The transport tests use real loopback TLS with short-lived test keys. They
remove their listeners, processes, and files.

Optional image tests run the native servers in cached images. They use Docker
with `--pull=never`, no network, a read-only root, and a synthetic HOME. They
send no model requests. If a variable is not set, its test skips:

- `CETIRUN_OPENCLAW_IMAGE`: an approved, cached OpenClaw image. It tests the
  native API, the Control UI, and ttyd.
- `CETIRUN_HERMES_IMAGE`: an approved, cached Hermes image. It tests the API,
  the Webapp, and ttyd.
- `CETIRUN_DESKTOP_IMAGE`: a cached desktop image. It tests websockify and
  noVNC TLS with synthetic backends.

Use images of the versions in `nativeVersion` of
`app/cetirun-chart/agents/openclaw/profile.yaml` and
`app/cetirun-chart/agents/hermes/profile.yaml`. For the usage and rotation
contract, refer to
[Managed Agent ingress mTLS](site/content/en/docs/guides/agent-ingress-mtls.md).

### Managed observed-SNI egress

The [managed egress contract](app/cetirun-operator/README.md#managed-observed-sni-egress)
uses the same Envoy as ingress mTLS and a capture init container. Run these
checks when you change it:

```bash
go -C app/cetirun-operator test ./internal/egresscapture ./internal/sandboxpolicy -count=1
go -C app/cetirun-operator test ./internal/controller -run '^TestReconciledSandboxObservedSNI' -count=1
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestReconciledSandboxObservedSNIAPIServer$' -count=1
```

The controller tests cover source drift, the final apply check, restart,
suspend and resume, and deletion. If you set `CETIRUN_EGRESS_PROOF_DIR`, the
controller test exports synthetic Pod, configuration, and probe fixtures for a
separately authorized Docker check. It does not export live data.

### Agent Identity Broker

The Broker owns owner Connections, per-instance ConnectionBindings, OAuth and
OBO, and encrypted credential storage. Connections MCP in
`app/cetirun-connections-mcp/` is a separate native remote MCP server. It
verifies a Pod-bound workload token and asks the Broker to check the instance
and its authorization. Then it sends JSON REST requests to fixed Azure, GitHub,
Microsoft Graph, and Google service profiles. Provider tokens never enter Agent
workloads.

```bash
go -C app/cetirun-agent-broker test ./... -count=1
go -C app/cetirun-agent-broker vet ./...
make -C app/cetirun-agent-broker build
bash scripts/cetirun-agent-broker-helm-test.sh
make -C app/cetirun-connections-mcp test vet chart-lint
python3 -B -m unittest scripts.tests.test_native_mcp_runtime -v
python3 -B -m unittest discover -s app/cetirun-chart/tests -p test_connections_mcp.py -v
```

These checks do not call Microsoft Entra, Key Vault, ARM, Resource Graph,
GitHub, Google, or a cluster. For the TLS reload, the Broker identity, CA
distribution, and Agent bindings, refer to [CetiRun Connections MCP](app/cetirun-connections-mcp/README.md)
and [Agent Connections and Identity](site/content/en/docs/guides/agent-identity.md).

The Operator rejects Agent Identity requests for Custom Agents before it
provisions the identity. These tests cover that boundary:

```bash
go -C app/cetirun-operator test ./internal/runtimebindings ./internal/helm ./internal/controller -run 'TestCustomAgentIdentity' -count=1
```

While you work on the credential policy or the native integration, run these
focused tests. They cover the application and readback of the credential
policy and the native integration of both harnesses:

```bash
go -C app/internal test ./agenttools -run 'TestCredentialPolicy|TestNative' -count=1
go -C app/cetirun-agent-broker test ./internal/broker -run 'TestNative|TestCredentialPolicy' -count=1
go -C app/cetirun-connections-mcp test ./internal/identity ./internal/tools -run 'TestNative|TestCredentialPolicy' -count=1
go -C app/cetirun-operator test ./internal/controller -run 'TestCredentialPolicy|TestNativeConnections' -count=1
go -C app/cetirun-operator test -tags=envtest ./internal/controller -run '^TestNativeConnectionsServedCurrentSourceProof$' -count=1
```

The API-server test uses the real chart render, API-issued Pod-bound tokens,
TokenReviews, and Pod admission for both harnesses. It rejects a token from a
recreated ServiceAccount. These checks do not test Kata or Cilium routing, TLS
delivery, Entra federation, provider permissions, or an Azure tenant.

### Skill Marketplace

The Operator runs the Skill Marketplace. It publishes approved skills from one
Git repository to the native Sandbox Agents of policy-bound Teams. The code has
two parts:

- `app/cetirun-operator/internal/skillmarketplace/` runs in the Operator. It
  lists the policy-bound Teams for the worker and creates the read-only skills
  claim of each of their Agents.
- `app/cetirun-operator/cmd/skill-marketplace/` is the worker, a second binary
  in the Operator image. It runs in a separate Deployment. It syncs the
  repository, publishes the selection of each Team to Blob NFS storage, and
  writes the catalog.

```bash
go -C app/cetirun-operator test ./internal/skillmarketplace/... ./cmd/skill-marketplace/ -count=1
python3 -B -m unittest scripts.tests.test_skill_marketplace_platform \
  scripts.tests.test_full_deployment.SkillMarketplaceTests -v
```

These checks do not contact a Git remote, mount storage, or call a cluster. For
the repository, storage, and security requirements, refer to
[Skill Marketplace](site/content/en/docs/operations/skill-marketplace.md).

### Sandbox Entra projection core

The `AgentPolicy.spec.entraID` projection is different from Broker Agent
Identity. Run these checks when you change it:

```bash
go -C app/cetirun-operator test ./internal/sandboxpolicy ./internal/sandboxworkload ./internal/helm ./internal/controller -run '^Test(Entra|NativeEntra)' -count=1 -timeout=20m
go -C app/cetirun-operator test -race ./internal/sandboxpolicy ./internal/sandboxworkload ./internal/helm ./internal/controller -run '^Test(Entra|NativeEntra)' -count=1 -timeout=20m
go -C app/cetirun-operator test -tags=envtest ./internal/helm ./internal/controller -run '^(TestEntraIdentityBindingAPIServer|TestNativeEntraCommonRuntimeAPIServer)$' -count=1 -timeout=20m
```

The API-server test uses synthetic identity approvals. It checks namespace and
ServiceAccount UIDs, metadata drift, ServiceAccount replacement, and that no
unrequested RBAC is created. The native case checks Pod admission, a lost
create response, a controller restart, and denial after drift. It does not
request an Entra token or connect to a cloud API. For the integration that is
still necessary, refer to the
[Entra runtime projection core](app/cetirun-operator/README.md#entra-runtime-projection-core).

### Runtime chart and Python helpers

The [shared base image version notes](app/cetirun-chart/base-image.md) record
the selected tool releases and compatibility limits.

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
python3 -B -m unittest discover -s scripts/tests -v
make python-test
```

The Helm scripts lint and render the runtime chart for OpenClaw and Hermes,
including values that are not valid. They use synthetic inputs and install
nothing. They do not check the other charts. For all charts, run
`make chart-check`.

The Istio mesh tests use the PyYAML version in
`infra/istio/requirements.txt`. `make python-test` runs the
initialization scripts on synthetic HOME directories, with synthetic network
responses and outbound connections blocked. OpenClaw initialization tests also
need `jq`.

Operator render tests keep the original configuration and resource baselines.
Do not replace a baseline fixture with changed output only to make a test pass.

The Hermes Webapp storage test needs a prepared Playwright module and Chromium.
It was last run with Playwright 1.64.0:

```bash
PLAYWRIGHT_MODULE=/path/to/node_modules/playwright \
CHROMIUM_EXECUTABLE=/path/to/chrome \
node scripts/tests/hermes_webapp_storage.cjs
```

This test checks storage isolation between instance prefixes on loopback
pages. It needs no Hermes image or model endpoint.

CI builds the runtime images and scans each one with Trivy, with a 10-minute
timeout for each image. A scan error or a timeout fails the job. The Hermes
image build allows 30 minutes for each source download and at most three
retries. It verifies the pinned source checksum before extraction. `make check`
does not build or scan images.

### AI Gateway

Run these checks when you change `app/cetirun-ai-gateway/`:

```bash
python3 -B -m unittest discover -s scripts/tests -p 'test_ai_gateway_*.py' -v
bash scripts/cetirun-ai-gateway-helm-test.sh
```

The unit tests replace LiteLLM with a test double. The chart test renders the
chart with synthetic values.

For AI Gateway changes, CI also installs and upgrades the Gateway on a
disposable kind cluster
([`ai-gateway-cluster-test.yml`](.github/workflows/ai-gateway-cluster-test.yml)).
This test needs Docker, kind, and a cluster that only the test uses. Do not
run [`scripts/cetirun-ai-gateway-cluster-test.sh`](scripts/cetirun-ai-gateway-cluster-test.sh)
against a shared cluster.

### Platform chart and infrastructure

Run these checks when you change `app/cetirun-platform/` or `infra/`:

```bash
bash scripts/cetirun-platform-helm-test.sh
bash scripts/cetirun-istio-helm-test.sh
make bicep-check
make deploy-plan DEPLOY_CONFIG="$PWD/app/cetirun-platform/ci/deployment-example.yaml"
```

`make deploy-plan` renders the complete platform offline from the
[synthetic environment file](app/cetirun-platform/ci/deployment-example.yaml).
It does not connect to the target in the file. `make bicep-check` compiles the
templates without a registry restore. It does not check Azure resources.

### Memory Context

```bash
make -C app/cetirun-memory-context test lint build
```

`make test` runs the Go tests offline and the native adapter tests with Node.

The adapter tests also check the owner access rules in
[OWNER_ACCESS.md](app/cetirun-memory-context/OWNER_ACCESS.md) for both
adapters. They use a real helper and synthetic host APIs. They cover these
items:

- Default access, and the platform, sender, and chat restrictions
- The intersection of profile and environment settings
- The Memory modes
- Grants for child sessions, interruption, and resume
- The memory scope and malformed inputs

The CLI tests cover the flags and environment controls for native events,
direct requests, legacy context, and MCP.

The native host tests load pinned upstream Hermes, OpenClaw, and Pi code. If
their variables are not set, the tests skip, and `make test` can pass without
them:

| Variable | Tests |
| --- | --- |
| `MEMORY_HERMES_SOURCE` and `MEMORY_HERMES_PYTHON` | Hermes host tests. They include `tests/hermes_owner_host.py`, which uses the real hooks, session metadata, middleware, and registry in a disposable marked profile. It sends no model requests. `MEMORY_HERMES_PYTHON` is the `.venv/bin/python` that `app/cetirun-chart/agents/hermes/pm/build.py` creates in the source directory. |
| `MEMORY_OPENCLAW_SOURCE` | OpenClaw host tests. They load the plugin in the real host and check the prompt injection for interactive, channel, and scheduled turns. |
| `MEMORY_PI_NATIVE=true` | Pi SDK tests. They need the Pi dependencies from [Download dependencies](#download-dependencies). |

The tests of child sessions and restriction propagation use synthetic session
metadata. They do not replace real incoming messages or a child conversation
that a model drives.

To prepare the hosts, follow the "Install pinned native host test
dependencies" step in
[`memory-context-ci.yml`](.github/workflows/memory-context-ci.yml). That step
downloads the pinned sources, verifies their checksums, and runs `build.py`
with a synthetic `HERMES_HOME`. This preparation needs network access. Then
run, with the variables set:

```bash
node --test app/cetirun-memory-context/tests/native.test.mjs
"$MEMORY_HERMES_PYTHON" -B scripts/tests/hermes_mcp_host.py --source "$MEMORY_HERMES_SOURCE"
```

The second command tests the native MCP SDK over loopback TLS, including token
rotation, CA verification, and the rejection of redirects. Both commands use
synthetic HOME directories and do not access an existing Agent.

To test a separately installed Memory helper, run the CLI recovery check. It
works only on Linux. It creates and removes synthetic stores in the directory
that you select:

```bash
python3 -B app/cetirun-memory-context/tests/cli_recovery.py \
  --helper /path/to/cetirun-memory --work-dir /path/to/writable-test-directory
```

### Examples

```bash
node --test examples/vscode-server/test/*.test.mjs
python3 -B -m unittest discover -s scripts/tests -p test_vscode_example.py -v
npm --prefix examples/pi-agent test
make -C examples/coding-agents/app test check
```

These tests use loopback servers and synthetic files and credentials. They do
not start VS Code, run Docker, or connect to GitHub or a model provider. For
image builds and live checks, refer to each example README and to
[Examples](site/content/en/docs/reference/examples.md).

The example image permission suite needs a cached image with Node.js 24 and a
POSIX shell. If the variable is not set, the suite skips:

```bash
CETIRUN_PERMISSIONS_TEST_IMAGE=local/node:24 \
  python3 -B -m unittest scripts.tests.test_example_image_permissions -v
```

## Integration and E2E tests

Some scripts deploy, publish, or connect to a live cluster. They are not
development checks.

> **Warning:** Before you run a deployment script, a release command, or a
> live test, get explicit authorization for the target environment. A local
> check, a pull request, or a review does not give this authorization.

- `make deploy-plan` renders the platform offline. `make deploy` installs the
  platform, and `make deploy-verify` makes read-only checks of a live
  installation. Both connect to the target. `make deploy` requires
  `DEPLOY_CONFIG`. Refer to
  [Install CetiRun](site/content/en/docs/getting-started/platform.md).
- [`scripts/e2e-test.sh`](scripts/e2e-test.sh) creates a test Agent on an
  explicit cluster, checks its resources and routing, and deletes it. For its
  inputs, refer to
  [Application E2E lifecycle](site/content/en/docs/reference/scripts.md#application-e2e-lifecycle).
  The E2E CI workflow is disabled. All its jobs have `if: false`.
- [Operator manual verification](app/cetirun-operator/test/TESTING.md)
  describes manual checks on an installed platform.
- `scripts/cetirun-dev-deploy.sh` builds images, pushes them to `REGISTRY`,
  publishes the chart, and applies an Agent for the alias that you supply. To
  see its modes, run it with `--help`.

Maintainers run these checks in an authorized environment.

## Generated files and dependencies

Run `make license-check` to check the source headers of supported first-party
files and Makefiles in the repository. The `CetiRun license headers` CI job
runs this check on pull requests and on pushes to `main`. To add a header to a
file, refer to [the header helper commands](scripts/README.md#license-headers).
Before you add a header, examine the authorship of the file. The third-party,
generated, and scaffold exceptions in `scripts/license_headers.py` keep their
own notices.

The Operator API types are in `app/cetirun-operator/api/v1alpha1/`. The
deepcopy methods (`zz_generated.deepcopy.go`), the CRD YAML in
`config/crd/bases/`, and the chart copies in `chart/templates/crds/` are
generated from these types. Include the generated files with their source
changes. Do not edit generated files by hand.

To generate the files:

1. Install controller-gen. This step needs network access. The command
   installs the version in the Makefile into `app/cetirun-operator/bin`:

   ```bash
   make -C app/cetirun-operator tools
   ```

2. Add `app/cetirun-operator/bin` to `PATH`, or set `CONTROLLER_GEN` to the
   absolute path of the binary.
3. Generate the deepcopy methods and the CRDs. The targets check the
   controller-gen version and do not download tools:

   ```bash
   GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local make -C app/cetirun-operator generate manifests
   ```

4. Stage the API types and the generated files with `git add`.
5. Check the files. The target generates the files again and fails if the
   result differs from the staged files. Operator CI runs the same target:

   ```bash
   make -C app/cetirun-operator verify-generated
   ```

The metadata markers in the API types generate the CRD retention annotations.
Do not add these annotations to the generated YAML. Review generator upgrades
separately.

For the envtest binaries that the API-server tests need, refer to
[Prepare envtest assets](#prepare-envtest-assets).

The RBAC rules of the Operator, the Portal, and CetiAdmin are Helm templates.
They are not generated. When you add an API resource, add it to the applicable
rules.

Other rules for checked-in files:

- Helm files in `templates/` are source templates. Rendered manifests are
  validation output. They do not replace the templates.
- When dependencies change, keep `go.mod` and `go.sum` together. Keep each
  `package.json` and its `package-lock.json` consistent. Use `npm ci` to install.
- Dependabot groups the shared Go builder updates across the application
  directories. The runtime base has its default image in the `go-toolchain`
  stage. `GO_BUILD_IMAGE` accepts an explicit override. Add new Go components
  to the Docker and Go module update directories. CI makes sure that every Go
  builder uses the pinned digest.
- A Go release upgrade changes `.go-version` and the `go` directive of every
  module. Grouped Docker updates do not change them.
- Do not commit binaries, `web/dist`, `node_modules`, Python caches or virtual
  environments, coverage output, rendered secrets, or local configuration.
- Keep third-party copyright, license, and source attribution.

## Release and publication

This section is for maintainers. Contributors do not publish artifacts.

### Release steps

1. Run `make set-version VERSION=X.Y.Z` on a branch. This command needs Helm.
2. Run `make check`.
3. Open a pull request with the version change. After approval, merge it into
   `main`.
4. With release authorization, push the tag `vX.Y.Z` on the merged commit.
   The tag starts the [MCR release pipeline](.pipelines/release-mcr.yml) in
   Azure Pipelines.

`make set-version` runs `scripts/cetirun-version.py set`. The script updates
the version of every chart in `scripts/release-artifacts.json`, the Operator
and Portal defaults, the Go module references to the Operator, and the npm
package versions. For the exact files, read `set_version()` in
[`scripts/cetirun-version.py`](scripts/cetirun-version.py). Then the target
runs `helm dependency update --skip-refresh app/cetirun-platform`, which writes
`app/cetirun-platform/Chart.lock`, and checks the versions again.

Component GitHub workflows also start on tags, for example `operator-v*` and
`hermes-v*`. They push to the registry in the `ACR_REGISTRY` repository
secret, not to MCR. The CetiRun, Operator, Portal, and Istio release workflows
also push to the registry in the `E2E_ACR_REGISTRY` secret.

### Unified release inventory

[`scripts/release-artifacts.json`](scripts/release-artifacts.json) lists the
images and charts for the root Make targets and the MCR release pipeline. These
include the Broker, the Pi demo (`cetirun-pi-agent`), VS Code Server
(`cetirun-vscode`), Memory Context, and ADX-Mon (`cetirun-observability`)
artifacts. `make artifact-check` checks the source
paths and the chart coverage, and prints the number of images and charts. The
release driver calls the image builders, stamps the version and the source
SHA, and builds the base image before the images that use it. It never logs in
or installs tools.

`REGISTRY` defaults to `mcr.microsoft.com/aks/ai-runtime/cetirun`, including
the repository prefix. `VERSION` defaults to the Runtime Chart version. You
can set both as environment variables or Make arguments. A Make argument takes
precedence. For a local test build:

```bash
REGISTRY=registry.example.com/team/test VERSION=0.1.1-dev.1 \
  make build-images
```

The `build-images`, `publish-images`, and `publish` targets build
`cetirun-vscode`. `ACCEPT_VSCODE_SERVER_LICENSE_TERMS` defaults to `true`, so
these builds accept the
[VS Code Server license](https://code.visualstudio.com/license/server). An
explicit `ACCEPT_VSCODE_SERVER_LICENSE_TERMS=false` stops a build that includes
VS Code Server. To publish the other artifacts without it, run
`scripts/cetirun-release.py publish --exclude-image cetirun-vscode`. For the
complete command, refer to
[Install CetiRun](site/content/en/docs/getting-started/platform.md).

MCR is a public pull endpoint, not a push destination. Local builds use its
default only for image tags and chart packaging. Local builds can hide official
tags in the local Docker store. Use an explicit test registry prefix to keep
them separate. `make deploy-plan` renders the source offline and does not check
published artifacts. `make deploy` checks that the artifacts exist before it
writes, and it prints the effective registry, the version, and their origins.

Deployment needs a complete published release that matches the source. The
source version alone does not show that the artifacts exist or that they are
compatible. An MCR tag can exist and still hold an older build. Until the
release is synchronized to MCR, use the explicit registry and version of a
complete, compatible test release.

The publish targets (`make publish*`, the component chart publishers,
`PUSH=true`, and component `docker-push`) need an explicit writable registry.
They reject MCR before a push. Do not use the backing ACR of MCR as the public
pull address of a deployed chart.

Build and publish targets do not edit the checkout. Image builds copy the
tracked and nonignored working-tree files to a temporary directory. When the
version differs, the driver runs the version setter in that copy. The build
therefore includes uncommitted changes. `GIT_SHA` identifies the base commit,
not a clean tree. Only `make set-version` changes the checked-in version.
`make version-check` checks the source, not a proposed build.

Chart packages contain the selected registry and version defaults. These
include the Runtime Chart URL and version of the Operator, the embedded
subcharts, and the first-party runtime images. The Operator keeps a registry
prefix, such as `registry.example.com/team/test`, when it resolves the
OpenClaw, Hermes, Desktop, Terminal, and Memory images. Third-party images and
explicit Custom image pins keep their own coordinates. Charts use the fixed
`helm` namespace. Put all repository prefixes in `REGISTRY`.

Existing Agents keep their pinned Runtime Chart and image. An explicit chart
upgrade changes a default image only in the same registry. To change the
registry, refer to [Migrating an existing Agent registry](#migrating-an-existing-agent-registry).

The component `*-helm-release.sh` scripts use the same packager and publish one
chart. `CHART_VERSION` and `APP_VERSION` can override `VERSION`. `CHART_DIR`
selects a source override. `VALUES_FILE` supplies more lint and render values,
not packaged secrets. These scripts use the shared registry preflight and the
chart pull comparison, so they need Python and Helm. They use the Helm and
Docker registry authentication that already exists. They do not log in.
`VERIFY_PULL=false` does not disable the pull comparison.

After registry authentication and publication authorization:

```bash
make publish REGISTRY=registry.example.com VERSION=0.1.1 \
  GIT_SHA="$(git rev-parse HEAD)"
```

`publish` checks every destination image and chart tag before it builds or
pushes. An existing tag or an unknown registry error stops it.
`FORCE_PUBLISH=true` allows the replacement of tags. After a partial
publication, use a new version, unless a replacement is authorized. Every
chart is pulled back and compared with its package. The unified release
rejects `VERIFY_PULL=false`. `build-images` builds locally and does not
publish. `publish-images` and `publish-charts` select one artifact class.

For an official MCR release, use the Azure Pipelines release. It pushes to the
backing ACR, which synchronizes to MCR. The pipeline uses
`REGISTRY=mcr.microsoft.com/...` only to package the charts with public pull
coordinates. It stages and promotes images and pushes charts to the backing
ACR. It promotes `latest` only after all versioned images and charts succeed.
It passes `ACCEPT_VSCODE_SERVER_LICENSE_TERMS=true` to the shared driver, the
same value as the local default. Only a manual run can set `forcePublish`.

Offline release and deployment tests use synthetic APIs and local Helm:

```bash
python3 -B -m unittest scripts.tests.test_release_inventory scripts.tests.test_full_deployment -v
python3 -B -m unittest scripts.tests.test_connections_mcp_release -v
make version-check artifact-check python-check license-check
```

These tests never build an image with Docker or connect to a registry or a
cluster. They test the image build wrappers with a command double. They do not
test production builds, publication, OAuth, provider calls, or a live
deployment.

### Migrating an existing Agent registry

When you change the Runtime Chart URL of the Operator, you also change the
registry that the Operator accepts for built-in Agent images. The Operator does
not rewrite existing `spec.agent.image` pins. If a pin belongs to the previous
registry, reconciliation rejects it with `AgentConfigInvalid` before Helm
applies a workload. A Portal chart-version upgrade alone does not migrate the
pin.

Before you upgrade an installation from an earlier source revision, read the
chart URL and version (`cetirun.chart.repoURL` and `cetirun.chart.version`) and
the image coordinates that are actually deployed. Earlier revisions used a
different default registry. Keep the deployed values, including any custom
overrides. With a registry-prefixed chart URL, the Operator uses the full
prefix, not only the host name. A chart URL alone therefore does not keep the
old image registry.

To move registries:

1. Publish and qualify compatible images and charts at the new coordinates.
2. In an authorized migration window, update the chart URL of the Operator.
3. Update the `spec.agent.image` of each built-in Agent together with its
   `spec.helm.version`. Use `<target-registry>/openclaw-gateway:<appVersion>`
   or `<target-registry>/hermes-gateway:<appVersion>` from the selected chart.

For Sandbox Agents, use the coordinated configuration change. Do not use a raw
patch to bypass admission or lifecycle controls. Keep Custom images and
explicitly qualified digest pins, unless their owners authorize a separate
migration. A registry change does not show that the images of the two
registries are equivalent.

### Release-agent build storage

The Azure Pipelines release job prepares Docker storage before it builds
images. It puts Docker layers, the BuildKit cache, and build scratch files on
`dockerStorageMount` (default `/mnt`), not on the OS disk.

[`scripts/ci/prepare_docker_storage.py`](scripts/ci/prepare_docker_storage.py)
is an operational helper for **disposable Linux CI agents only**. It is not a
local check. It runs as root. It logs the file system sizes, free space, and
inode use. It needs at least 64 GiB of free space on the mount and on both
write destinations. This threshold is a guardrail, not a measured build size.
The helper refuses to interrupt running containers, keeps unrelated daemon
settings, and restores the original configuration if activation fails. It uses
the `overlay2` image store, because the containerd image store does not follow
the Docker `data-root`.

The release starts with a new local Docker store and prunes the cache between
images. It does not delete or move old image or cache directories. Later tasks
get scratch storage through `TMPDIR`. If a temporary disk is too small,
configure a larger data disk on the hosted pool. Then set `dockerStorageMount`
to its Linux mount path. The helper does not change the hosted pool, resize
disks, or change other agents.

`make ci-test` runs the offline tests of this helper and the pipeline contract.
They replace Docker and service operations with test doubles. Only an
authorized run on a Linux agent can validate the mount layout, the daemon
activation, and the disk space of a release.

## Before review

1. Run `git status --short` and examine your diff, including new files.
2. Run `git diff --check` to find whitespace errors.
3. Include tests and documentation for behavior changes.
4. Examine your contribution against the
   [public-data boundary](CONTRIBUTING.md#keep-contributions-safe-to-publish).
5. In the Validation section of the pull request template, list the exact
   checks that you ran and their results. Keep offline checks separate from
   integration, deployment, or release checks. List the checks that you did
   not run.

A limited test run does not show that every security check passed.
