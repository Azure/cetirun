# scripts/

Development, build, and release scripts for CetiRun.

## Available Scripts

| Script | Purpose |
|--------|---------|
| `cetirun-docker-build.sh` | Local Docker build for CetiRun agent image |
| `cetirun-helm-lint.sh` | Lint CetiRun chart values/templates |
| `cetirun-helm-test.sh` | Render test scenarios for CetiRun chart |
| `cetirun-helm-release.sh` | Lint/package CetiRun chart and push Helm OCI artifact to ACR |
| `operator-docker-build.sh` | Build and push operator container image to ACR |
| `operator-helm-release.sh` | Lint/package operator chart and push Helm OCI artifact to ACR |
| `portal-docker-build.sh` | Build and push portal container image to ACR |
| `portal-helm-release.sh` | Lint/package portal chart and push Helm OCI artifact to ACR |

## Planned Scripts

| Script | Purpose |
|--------|---------|
| Build scripts | Container image builds for operator and OpenClaw |
| Helm scripts | Chart linting, testing, and publishing |
| Deploy scripts | Cluster bootstrapping and component deployment |
| Dev scripts | Local development environment setup |

## Related TODO Items

- #11: Set up CI/CD pipeline for container image, Helm chart, and operator builds
