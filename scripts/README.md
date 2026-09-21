# scripts/

Development, build, and release scripts for CetiRun.

## Available Scripts

| Script | Purpose |
|--------|---------|
| `cetirun-docker-build.sh` | Local Docker build for CetiRun agent image |
| `cetirun-helm-lint.sh` | Lint CetiRun chart values/templates |
| `cetirun-helm-test.sh` | Render test scenarios for CetiRun chart |
| `cetirun-istio-helm-test.sh` | Render the Istio chart's portal ownership target defaults and overrides |
| `cetirun-helm-release.sh` | Lint/package CetiRun chart and push Helm OCI artifact to ACR |
| `platform-helm-release.sh` | Lint/package the self-contained platform umbrella chart and push it to ACR |
| `cetirun-platform-deploy.sh` | Install the local platform umbrella after verifying independent Istio prerequisites |
| `operator-docker-build.sh` | Build and push operator container image to ACR |
| `operator-helm-release.sh` | Lint/package operator chart and push Helm OCI artifact to ACR |
| `portal-docker-build.sh` | Build and push portal container image to ACR |
| `portal-helm-release.sh` | Lint/package portal chart and push Helm OCI artifact to ACR |
| `check-license-headers.py` | Check first-party source headers across the repository |
| `add-license-headers.py` | Preview or add missing headers after authorship review |

## License headers

Run the same offline check used by CI:

```bash
make license-check
```

Preview missing headers, verify that the listed files are Microsoft-authored,
then explicitly confirm before adding them:

```bash
python3 -B scripts/add-license-headers.py --dry-run
python3 -B scripts/add-license-headers.py --confirm-first-party
make license-check
```

Both helpers accept `--root PATH`, `--list-exclusions`, and `--help`. The default
root is the checkout containing the scripts, regardless of the caller's working
directory. Checks include tracked and non-ignored untracked source files,
Makefiles, and release/E2E scripts, and work in a source export without Git.
They do not run builds, deploy resources, or access the network.

Keep the explicit third-party and generated/scaffold exceptions aligned with
[NOTICE](../NOTICE). The adder never replaces existing notices or follows
symlinks. Markdown, YAML, JSON, lockfiles, binary assets, dependencies, build
outputs, and hidden paths other than `.github/` are outside the header check.
These exclusions do not waive their licensing obligations.

## Planned Scripts

| Script | Purpose |
|--------|---------|
| Build scripts | Container image builds for operator and OpenClaw |
| Helm scripts | Chart linting, testing, and publishing |
| Deploy scripts | Cluster bootstrapping and component deployment |
| Dev scripts | Local development environment setup |

## Related TODO Items

- #11: Set up CI/CD pipeline for container image, Helm chart, and operator builds
