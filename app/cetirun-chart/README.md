# CetiRun Helm Chart

CetiRun is an open-source managed runtime for hosting AI agents and AI applications.

This chart deploys isolated OpenClaw or Hermes agent instances with persistent storage and supporting services.

## Prerequisites

- Kubernetes 1.26+
- Helm 3.x

## Deploy from Local Chart

```bash
helm upgrade cetirun app/cetirun-chart --namespace cetirun-<alias> --install --create-namespace \
  --set secrets.openclawGatewayToken="<token>" \
  --set secrets.model=gpt-6-astra \
  --set persistence.accessMode=ReadWriteMany \
  --set persistence.storageClass=azureblob-nfs-premium
```

## Deploy from Released Chart

```bash
helm upgrade cetirun oci://cetirun.azurecr.io/helm/cetirun --version 0.1.0 --namespace cetirun-<alias> --install --create-namespace \
  --set secrets.openclawGatewayToken="<token>" \
  --set secrets.model=gpt-6-astra \
  --set persistence.accessMode=ReadWriteMany \
  --set persistence.storageClass=azureblob-nfs-premium
```

## Uninstalling the Chart

```bash
helm uninstall cetirun --namespace cetirun-<alias>
```

## Development

```bash
# Lint the chart
scripts/cetirun-helm-lint.sh

# Render templates for inspection
helm template cetirun app/cetirun-chart -f app/cetirun-chart/values.yaml \
  --set secrets.openclawGatewayToken=dev-token \
  --set secrets.model=gpt-6-astra

# Run tests
scripts/cetirun-helm-test.sh

# Build Docker image (default: cetirun.azurecr.io/openclaw-gateway:latest)
scripts/cetirun-docker-build.sh

# Override the package mirror for base and Hermes image build stages.
NPM_REGISTRY=https://packages.example.invalid/npm/ scripts/cetirun-docker-build.sh

# Override the public registry used only for the OpenClaw image.
OPENCLAW_NPM_REGISTRY=https://packages.example.invalid/npm/ scripts/cetirun-docker-build.sh

# Build and push to ACR
PUSH=true scripts/cetirun-docker-build.sh
```

`NPM_REGISTRY` and `OPENCLAW_NPM_REGISTRY` both default to
`https://registry.npmjs.org/`. The latter remains a separate override for
OpenClaw image builds. Runtime-installed npm skills use `skills.npmRegistry`,
with the same public default. CI and release workflows accept the corresponding
repository or organization variables.

Hermes's `PYPI_INDEX_URL` build argument defaults to `https://pypi.org/simple/`.

The Azure DevOps Git credential helper configures only `dev.azure.com`.
`openclaw.git.azureDevOps.enabled=false` skips setup without deleting existing
configuration. Configure additional hosts in the instance's Git configuration;
initialization preserves those entries.

## Architecture

Each engineer gets a dedicated namespace (`cetirun-<alias>`) containing:
- OpenClaw StatefulSet (Gateway + Claude Code + tools)
- Headed Chrome instance with remote debugging (port 18800)
- ttyd web terminal for browser-based shell access (port 7681, opt-in)
- PVC for `/home/vibe` persistence (Azure Blob NFS Premium)

The following resources are managed by the `cetirun-operator`, not this chart:
- Istio VirtualService for host-based routing

### Shared Skills Azure Blob CSI

When `skills.shared=true`, the chart creates a static PV/PVC backed by Azure Blob CSI. NFS is the default protocol; Blobfuse (`fuse` or `fuse2`) is supported when the storage account requires it.

`skills.protocol`, `skills.secretName`, and `skills.mountOptions` are install-time-only settings. They become part of the static PV specification, so Helm cannot change them in place after the PV is created. To change any of these values, create a replacement PV/PVC (or deploy a replacement instance in a new namespace), verify the workload can mount it, then retire the old release.

### Model Provider Authentication

Configure the model provider via `secrets.provider` and `secrets.model`. The chart composes the auth profile and model config automatically.

**GitHub Copilot (default):**

```bash
helm upgrade ... \
  --set secrets.provider=github_copilot \
  --set secrets.model=<a-model-authorized-for-the-user>
```

Engineers run `copilot-auth` on first use to authenticate.

**OpenAI:**

```bash
helm upgrade ... \
  --set secrets.provider=openai \
  --set secrets.model=gpt-4.1 \
  --set secrets.apiKey="sk-..." \
  --set secrets.apiBase="https://api.openai.com/v1"
```

**Anthropic:**

```bash
helm upgrade ... \
  --set secrets.provider=anthropic \
  --set secrets.model=claude-sonnet-4-6 \
  --set secrets.apiKey="sk-ant-..." \
  --set secrets.apiBase="https://api.anthropic.com"
```

### Network Policy

The chart includes a NetworkPolicy (enabled by default) that enforces:

- **Default deny-all** ingress and egress
- **Ingress:** Only from pods in the Istio ingress gateway namespace on the gateway port
- **Egress allowed:**
  - DNS (kube-dns in `kube-system`, port 53)
  - Istiod mTLS cert signing (port 15012 in `istioNamespace`)
  - All external (out-of-cluster) traffic
  - Explicitly allowed internal IPs via `allowedInternalCIDRs` (bypasses blocked ranges)
- **Egress blocked:**
  - `10.0.0.0/8` — cluster/VNet internal ranges
  - `172.16.0.0/12` — RFC 1918 private range
  - `192.168.0.0/16` — RFC 1918 private range
  - `169.254.169.254/32` — Azure IMDS (prevents credential theft)

For **AKS managed Istio**, the operator sets `istioNamespace: aks-istio-system` and `istioIngressNamespace: aks-istio-ingress`. For **upstream Istio**, the defaults (`istio-system`) work out of the box.

### Chrome Sidecar

A headed Chrome instance is started automatically via supervisord with remote debugging on port 18800. It enables browser-based skills and MCP servers (e.g., Playwright, Puppeteer) out of the box. Chrome is configured with `--no-sandbox` and `--disable-gpu` for container compatibility and auto-restarts on crash.

### Web Terminal (ttyd)

When `ttyd.enabled` is set to `true`, a [ttyd](https://github.com/tsl0922/ttyd) web terminal is started via supervisord on port 7681 (configurable via `ttyd.port`). This provides lightweight browser-based shell access — faster than noVNC for terminal-only work.

**Disabled by default.** Enable per-engineer via the operator or Helm values:

```bash
helm upgrade ... --set ttyd.enabled=true
```

Security considerations:
- ttyd runs with `--check-origin` to prevent cross-origin WebSocket hijacking
- Access is gated by the cluster's network boundary (ClusterIP service, Istio routing)
- The entrypoint validates port range (1024-65535) and shell executable before starting
- Port collisions with gateway (18789) or noVNC (6080) are rejected at template render time
- For production, ensure ttyd is only reachable through authenticated ingress (OAuth2 Proxy / Istio AuthorizationPolicy)

### Health Probes

Liveness and startup probes use HTTP GET on the `/health` endpoint, while the readiness probe uses the dedicated `/ready` endpoint (all on the gateway port).

## Parameters

### General

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of replicas (typically 1, stateful) | `1` |
| `image.repository` | Image repository | `cetirun.azurecr.io/openclaw-gateway` |
| `image.pullPolicy` | Image pull policy | `Always` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets for private registries | `[]` |
| `nameOverride` | Override the chart name | `""` |
| `fullnameOverride` | Override the chart fullname | `""` |

### Service

| Parameter | Description | Default |
|-----------|-------------|---------|
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `18789` |
| `service.targetPort` | Target port on the container | `18789` |
| `service.nodePort` | NodePort (when type is NodePort) | `null` |
| `service.annotations` | Service annotations | `{}` |

### Ingress

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `""` |
| `ingress.annotations` | Ingress annotations (includes nginx WebSocket defaults) | see `values.yaml` |
| `ingress.hosts` | Ingress hosts | `[{host: openclaw.local, paths: [{path: /, pathType: ImplementationSpecific}]}]` |
| `ingress.tls` | TLS configuration | `[]` |

### Resources and Scheduling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.limits.cpu` | CPU limit | `2000m` |
| `resources.limits.memory` | Memory limit | `8Gi` |
| `resources.requests.cpu` | CPU request | `250m` |
| `resources.requests.memory` | Memory request | `1Gi` |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |
| `topologySpreadConstraints` | Pod topology spread constraints | `[]` |

### Autoscaling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `autoscaling.enabled` | Enable HPA | `false` |
| `autoscaling.minReplicas` | Minimum replicas | `1` |
| `autoscaling.maxReplicas` | Maximum replicas | `3` |
| `autoscaling.targetCPUUtilizationPercentage` | Target CPU utilization | `80` |
| `autoscaling.targetMemoryUtilizationPercentage` | Target memory utilization | `80` |

### Persistence

| Parameter | Description | Default |
|-----------|-------------|---------|
| `persistence.enabled` | Enable persistent storage | `true` |
| `persistence.storageClass` | Storage class (empty for default) | `""` |
| `persistence.accessMode` | PVC access mode | `ReadWriteOnce` |
| `persistence.size` | PVC size | `100Gi` |
| `persistence.existingClaim` | Use an existing PVC | `""` |
| `persistence.annotations` | PVC annotations | `{}` |
| `persistence.retainPolicy` | PVC retain policy | `Retain` |
| `persistence.useStatefulSetVolumeClaim` | Use StatefulSet volumeClaimTemplates | `true` |

### Secrets

| Parameter | Description | Default |
|-----------|-------------|---------|
| `secrets.existingSecret` | Use an existing secret | `""` |
| `secrets.annotations` | Secret annotations | `{}` |
| `secrets.provider` | Model provider: `github_copilot`, `openai`, or `anthropic` | `"github_copilot"` |
| `secrets.model` | Model name selected by the instance or deployment | required |
| `secrets.apiKey` | API key for openai/anthropic providers | `""` |
| `secrets.apiBase` | Custom API base URL (uses provider default if empty) | `""` |
| `secrets.openclawGatewayToken` | Gateway authentication token | `""` |
| `secrets.msteamsAppId` | Microsoft Teams app ID | `""` |
| `secrets.msteamsAppPassword` | Microsoft Teams app password | `""` |
| `secrets.msteamsTenantId` | Microsoft Teams tenant ID | `""` |
### MS Teams Integration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `msteamsWebhookPort` | MS Teams webhook listener port (0 = disabled, set by operator) | `0` |
| `msteamsAuthType` | MS Teams auth type: `""` (secret), `"managedIdentity"`, or `"certificate"` | `""` |
| `azureWorkloadIdentity.enabled` | Enable Azure Workload Identity for managed identity auth | `false` |
| `azureWorkloadIdentity.clientId` | Azure AD client ID for workload identity | `""` |
| `secrets.braveApiKey` | Brave Search API key | `""` |
| `secrets.perplexityApiKey` | Perplexity API key | `""` |

### OpenClaw Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `openclaw.config.gateway.port` | Gateway server port | `18789` |
| `openclaw.config.logging.level` | Log level | `"info"` |
| `openclaw.config.browser.enabled` | Enable browser integration | `true` |
| `openclaw.config.browser.headless` | Run Chrome headless | `false` |
| `openclaw.config.browser.remoteCdpTimeoutMs` | CDP connection timeout | `1500` |

### Web Terminal (ttyd)

| Parameter | Description | Default |
|-----------|-------------|---------|
| `ttyd.enabled` | Enable ttyd web terminal (opt-in) | `false` |
| `ttyd.port` | ttyd listening port (must be >= 1024) | `7681` |
| `ttyd.shell` | Absolute path to the shell to launch | `/bin/zsh` |

### Health Probes

| Parameter | Description | Default |
|-----------|-------------|---------|
| `livenessProbe.enabled` | Enable liveness probe | `true` |
| `livenessProbe.initialDelaySeconds` | Delay before first probe | `30` |
| `livenessProbe.periodSeconds` | Probe interval | `180` |
| `livenessProbe.timeoutSeconds` | Probe timeout | `180` |
| `livenessProbe.failureThreshold` | Failures before restart | `3` |
| `readinessProbe.enabled` | Enable readiness probe | `true` |
| `readinessProbe.initialDelaySeconds` | Delay before first probe | `30` |
| `readinessProbe.periodSeconds` | Probe interval | `30` |
| `readinessProbe.timeoutSeconds` | Probe timeout | `10` |
| `readinessProbe.failureThreshold` | Failures before unready | `6` |
| `startupProbe.enabled` | Enable startup probe | `false` |
| `startupProbe.initialDelaySeconds` | Delay before first probe | `10` |
| `startupProbe.periodSeconds` | Probe interval | `10` |
| `startupProbe.failureThreshold` | Failures before kill | `30` |

### Network Policy

| Parameter | Description | Default |
|-----------|-------------|---------|
| `networkPolicy.enabled` | Enable NetworkPolicy | `true` |
| `networkPolicy.istioNamespace` | Namespace where istiod runs (mTLS egress) and default ingress source | `istio-system` |
| `networkPolicy.istioIngressNamespace` | Override namespace for Istio ingress gateway (falls back to `istioNamespace`) | `""` |
| `networkPolicy.dnsNamespace` | Namespace of kube-dns (egress DNS) | `kube-system` |
| `networkPolicy.allowedInternalCIDRs` | Internal IPs/CIDRs to explicitly allow despite `blockedCIDRs` (each with `cidr` and `ports`) | `[{ cidr: 10.230.0.4/32, ports: [80] }]` |
| `networkPolicy.blockedCIDRs` | CIDRs blocked from external egress | `[10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.169.254/32]` |

### Pod Disruption Budget

| Parameter | Description | Default |
|-----------|-------------|---------|
| `podDisruptionBudget.enabled` | Enable PDB | `false` |
| `podDisruptionBudget.minAvailable` | Minimum available pods | `1` |

### Extra Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `extraEnv` | Extra environment variables | `[]` |
| `extraEnvFrom` | Extra envFrom sources | `[]` |
| `extraVolumeMounts` | Extra volume mounts | `[]` |
| `extraVolumes` | Extra volumes | `[]` |
| `initContainers` | Init containers | `[]` |
| `sidecars` | Additional sidecar containers | `[]` |
| `podAnnotations` | Pod annotations | `{}` |
