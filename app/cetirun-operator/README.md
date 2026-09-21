# cetirun-operator

Kubernetes operator that manages long-lived OpenClaw and Hermes agents via
`Agent` and `Team` resources in `cetirun.microsoft.com/v1alpha1`.

## API contract

| Kind | Resource | CRD name | Short name | Scope |
| --- | --- | --- | --- | --- |
| `Agent` | `agents` | `agents.cetirun.microsoft.com` | `cra` | Cluster |
| `Team` | `teams` | `teams.cetirun.microsoft.com` | `crt` | Cluster |

An Agent is a long-lived, user-owned environment, not a template or a single
execution. A Team supplies common configuration and skills, not shared access
to members' private data. Pod replacement does not create a new Agent.

This API replaces `OpenClawInstance` and `OpenClawTeam` in
`cetirun.microsoft.com/v1alpha1`, retaining the same API group and version.
It is a breaking resource-name change, not an in-place conversion or an
existing-cluster upgrade procedure.
The operator and Portal register only the new API. Installing the new CRDs
does not convert, adopt, or delete old objects.

Do not delete legacy CRs or CRDs as a migration shortcut: the old finalizer
may uninstall workloads and delete their namespaces, and deleting a CRD
removes its stored objects. Existing environments need a separately reviewed
data-retention and ownership-handover plan.

This change preserves the existing spec/status fields, cluster scope, finalizer,
namespace/release naming and PVC behavior. It does not implement the separate
`spec.agent` configuration redesign; this source snapshot still uses
`spec.agentType`. The Kubernetes CRD definition itself continues to use
`apiextensions.k8s.io/v1`.

## Responsibilities

- Watch for `Agent` CR create/update/delete events
- Generate authentication tokens (Gateway Token) on CR creation
- Deploy the runtime Helm chart into per-agent namespaces (`cetirun-<name>`)
- Configure Istio VirtualService for path-based routing (`https://<domain>/<instance>`)
- Update CR status with access URL
- Clean up resources via finalizers on CR deletion

## Directory Structure

```
cetirun-operator/
├── api/v1alpha1/  # Agent and Team API types and generated DeepCopy methods
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

The chart packages the generated CetiRun CRDs in `chart/crds/`. Helm creates
them before the Operator resources on the first installation. Helm does not
upgrade or delete CRDs; perform a reviewed CRD migration before an Operator
release that changes their schemas.

When installing the Operator chart directly with a Gateway workload outside the
Gateway resource namespace, set the runtime NetworkPolicy ingress namespace as
well:

```bash
helm upgrade --install cetirun-operator app/cetirun-operator/chart \
  --namespace cetirun-system \
  --set-string istioGateway=gateway-resources/cetirun-gateway \
  --set-string cetirun.helmValues.networkPolicy.istioIngressNamespace=gateway-workloads
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

- #12: Define Agent CRD
- #13: Implement operator scaffold with Create reconciler
- #14: Implement Update/Delete reconcilers with finalizers
- #15: Auto-generate and inject credentials
- #16: Deploy OpenClaw Helm chart per engineer
- #17: Update CR status with access URL
