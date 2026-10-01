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

- `infra/cluster/deploy.sh`: cloud provisioning, preview registration, and
  kubeconfig changes; see [infrastructure](../../operations/infrastructure/).
- `infra/storage/deploy.sh`: subnet endpoint changes and Blob deployment;
  see [storage](../../operations/storage/).
- `infra/istio/deploy.sh`: identity/TLS, Helm, and shared mesh configuration;
  see [Istio](../../operations/istio/).
- `scripts/cetirun-platform-deploy.sh`: verify independent Istio prerequisites,
  label the target namespace, install the operator/portal umbrella, and wait for
  its deployments. It applies the operator's CRDs around the upgrade: new ones
  before it, changes to existing ones after the operator and Sandbox admission
  roll out and before the portal wait.
- `app/cetirun-operator/deploy/deploy.sh`: check StorageClass/gateway prerequisites,
  install the operator chart, and apply its CRDs in the same order.
- `scripts/operator-crds.sh`: the CRD steps both helpers source.
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

Source: [scripts directory]({{< repo "scripts" "tree" >}}).
