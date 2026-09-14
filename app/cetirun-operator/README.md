# cetirun-operator

Kubernetes operator that manages the lifecycle of OpenClaw instances via the `OpenClawInstance` CRD (`cetirun.microsoft.com/v1alpha1`).

## Responsibilities

- Watch for `OpenClawInstance` CR create/update/delete events
- Generate authentication tokens (Gateway Token) on CR creation
- Deploy the OpenClaw Helm chart into per-engineer namespace (`cetirun-<alias>`)
- Configure Istio VirtualService for path-based routing (`https://<domain>/<instance>`)
- Update CR status with access URL
- Clean up resources via finalizers on CR deletion

## Directory Structure

```
cetirun-operator/
├── api/          # CRD type definitions (v1alpha1)
├── cmd/          # Main entry point
├── internal/     # Reconciler logic
├── chart/        # Helm chart for deploying the operator to cetirun-system namespace
├── Dockerfile
├── Makefile
└── go.mod
```

## Docker Image

The operator image is published to Azure Container Registry:

```
cetirun.azurecr.io/cetirun-operator:<tag>
```

### Build locally

```bash
scripts/operator-docker-build.sh
```

### Build and push to ACR

```bash
az acr login --name cetirun
IMAGE_TAG=0.1.0 PUSH=true scripts/operator-docker-build.sh
```

### Environment variables

| Variable      | Default                      | Description            |
| ------------- | ---------------------------- | ---------------------- |
| `REGISTRY`    | `cetirun.azurecr.io`        | Container registry     |
| `IMAGE_NAME`  | `cetirun-operator`           | Image name             |
| `IMAGE_TAG`   | `latest`                     | Image tag              |
| `PUSH`        | `false`                      | Push image after build |

## Helm Chart

The operator Helm chart is published to Azure Container Registry as an OCI artifact:

```
oci://cetirun.azurecr.io/helm/cetirun-operator
```

### Publish to ACR

```bash
# Login to ACR
az acr login --name cetirun

# Package and push
scripts/operator-helm-release.sh
```

### Verify

```bash
helm pull oci://cetirun.azurecr.io/helm/cetirun-operator --version 0.1.0
```

### Environment variables

| Variable          | Default              | Description                        |
| ----------------- | -------------------- | ---------------------------------- |
| `REGISTRY`        | `cetirun.azurecr.io` | OCI registry                       |
| `CHART_NAMESPACE` | `helm`               | Repository namespace in registry   |
| `VERIFY_PULL`     | `false`              | Pull chart after push to verify    |

## Related TODO Items

- #12: Define OpenClawInstance CRD
- #13: Implement operator scaffold with Create reconciler
- #14: Implement Update/Delete reconcilers with finalizers
- #15: Auto-generate and inject credentials
- #16: Deploy OpenClaw Helm chart per engineer
- #17: Update CR status with access URL
