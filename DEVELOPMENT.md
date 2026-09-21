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
- Go 1.26.8, matching `.go-version`, both module minimums, and the pinned
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

The Python command tests repository helpers with local fixtures. It does not
run the Teams runtime plugin suite. That suite imports Hermes runtime modules
and pytest; it needs an environment providing those dependencies. Keep any
live Teams or other service integration test separate and explicitly authorized.

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
  `go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.21.0` when
  network setup is authorized, then run
  `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local make -C app/cetirun-operator generate manifests`.
  Set `CONTROLLER_GEN=/path/to/controller-gen` if it is not on PATH. These
  targets check the version and regenerate deepcopy methods and CRD YAML,
  including the Operator chart's CRD copies, without downloading tools.
  `make -C app/cetirun-operator check-crds` checks the packaged copies.
  Review generator upgrades separately. Operator,
  Portal, and operations-agent RBAC remain source Helm/YAML files and must
  name the matching API resources; do not hand-edit generated output.
- Helm files under `templates/` are source templates. Rendered Kubernetes
  manifests are validation output, not replacements for those templates.
- Keep `go.mod` and `go.sum` together when dependencies change. Keep the portal's
  `package.json` and `package-lock.json` consistent; use `npm ci` for installation.
- Do not commit binaries, `web/dist`, `node_modules`, Python caches or virtual
  environments, coverage output, rendered secrets, or local configuration.
- Preserve third-party copyright, license, and source attribution.

## Before review

Inspect `git status --short` and your diff. Include tests and documentation for
behavior changes, check the [public-data boundary](CONTRIBUTING.md#keep-contributions-safe-to-publish),
and list the exact validation performed. Separate a passing offline check from
an unrun integration, deployment, or release check. Never describe a limited
test run as proof that every security check passed.
