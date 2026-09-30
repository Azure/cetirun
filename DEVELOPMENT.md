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
| `app/cetirun-chart/` | Runtime Helm chart, container definitions, and runtime plugins |
| `infra/` | Infrastructure definitions and Istio configuration |
| `scripts/` | Component validation and operational helpers |
| `docs/` | Documentation pointers and project assets |
| `site/` | Hugo/Docsy documentation site, when included in the checkout |

The portal's Go module uses a local `replace` directive for the operator module.
Keep both directories in the same checkout when building or testing the portal.
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

- Git, Bash, and Make for repository commands.
- Go 1.26.8, matching `.go-version`, all module minimums, and the pinned
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

`npm --prefix app/cetirun-portal/web run dev` starts a local frontend server.
Its API proxy expects a backend on localhost and injects development identity
headers. Keep it local; it is not a production authentication configuration.
Starting the Go backend or operator uses Kubernetes configuration and is
outside the offline validation loop.

### Runtime chart and Python helpers

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
python3 -B -m unittest discover -s scripts/tests -v
```

The Helm scripts lint and render the runtime chart for OpenClaw and Hermes,
including invalid-value cases. They use dummy inputs and do not install
resources. They do not validate every chart in the repository or prove that a
live deployment works.

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
typed platform bindings, both HTTP and headless rendering, final manifest
validation, the common HOME initializer, and a local mock Gateway:

```bash
python3 -B examples/custom-agent/qualify.py
```

This source-level qualification starts only loopback mock servers and uses
synthetic files and credentials. It does not run Docker or contact a model
provider. The optional container mode requires a separate, explicit invocation;
see [the example](examples/custom-agent/README.md). No production Custom binding
provider is configured by these tests.

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
  including the Operator chart's CRD copies, without downloading tools.
  `make -C app/cetirun-operator check-crds` checks the packaged copies.
  `verify-generated` reruns generation and checks both copies and deepcopy
  against Git.
  Review generator upgrades separately. Operator,
  Portal, and operations-agent RBAC remain source Helm/YAML files and must
  name the matching API resources; do not hand-edit generated output.
- Agent API defaulting, strict decoding and CEL transitions, plus Sandbox
  configuration admission and its outage/cleanup boundary, are covered by
  `make -C app/cetirun-operator test-envtest`. Set `KUBEBUILDER_ASSETS` to local
  Kubernetes 1.37 test binaries prepared separately; Helm 3 is also required
  to render the actual admission matching rules. The target starts isolated
  API server/etcd processes with RBAC, refuses missing binaries and never uses
  an existing cluster. Loopback TLS tests cover the independent serving process
  and certificate replacement. Version-skew fixtures verify that fields known
  only to a newer CRD fail closed at the older handler without blocking
  known-field requests. Normal `go test ./...` runs unit and
  schema/serialization tests, not envtest.
  The same target tests trusted Team assignment and metadata-only UID
  reauthorization through the rendered webhooks. These are isolated API-server
  tests, not runtime qualification or permission to lift the execution hold.
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
