# CRD Reference & Platform Topology

OpenClawInstance CRD specification, namespace layout, service ports, and Kubernetes API permissions.

---

## 1. OpenClawInstance CRD

### Full Specification

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: OpenClawInstance
metadata:
  name: <alias>                     # Must match spec.user.alias
spec:
  user:
    alias: string                   # Required. Pattern: ^[a-z][a-z0-9-]{0,62}$
    displayName: string             # Required. Human-readable name
    team: string                    # Required. Team identifier (e.g., "aks-core")
    email: string                   # Optional. Notification email
  gatewayTokenSecretRef: string     # Required. Secret name for OPENCLAW_GATEWAY_TOKEN
  tailscale:                        # Optional.
    authKeySecretRef: string        # Secret name containing TAILSCALE_AUTH_KEY
  msteams:                          # Optional. MS Teams bot integration.
    appId: string                   # Required. Azure AD app (client) ID (UUID).
    tenantId: string                # Required. Azure AD tenant ID (UUID).
    authType: string                # Optional. "secret" (default), "certificate", or "managedIdentity".
    certificateSecretRef: string    # Optional. Secret name with PEM cert (key: tls.pem). Required for certificate auth.
    certificateThumbprint: string   # Optional. SHA-1 thumbprint (hex). Required for certificate auth.
    webhookPort: int32              # Optional. Webhook listener port. Default: 3978.
  helm:                             # Optional.
    version: string                 # Chart version override
    values: {}                      # Arbitrary Helm values merged with defaults
```

### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `spec.user.alias` | string | Yes | User alias. Must match `^[a-z][a-z0-9-]{0,62}$`. Must equal `metadata.name`. |
| `spec.user.displayName` | string | Yes | Human-readable display name |
| `spec.user.team` | string | Yes | Team identifier for grouping and spend tracking |
| `spec.user.email` | string | No | Contact email for notifications |
| `spec.gatewayTokenSecretRef` | string | Yes | Name of Secret containing `OPENCLAW_GATEWAY_TOKEN` |
| `spec.tailscale.authKeySecretRef` | string | No | Name of Secret containing `TAILSCALE_AUTH_KEY` |
| `spec.msteams.appId` | string | Yes (if msteams set) | Azure AD Application (client) ID (UUID) |
| `spec.msteams.tenantId` | string | Yes (if msteams set) | Azure AD tenant ID (UUID) |
| `spec.msteams.authType` | enum | No | Authentication method: `secret` (default), `certificate`, or `managedIdentity` |
| `spec.msteams.certificateSecretRef` | string | No | Secret name containing PEM cert (key: `tls.pem`). Required when `authType=certificate` |
| `spec.msteams.certificateThumbprint` | string | No | SHA-1 thumbprint of certificate (hex, no colons). Required when `authType=certificate` |
| `spec.msteams.webhookPort` | int32 | No | Port the Teams webhook listener runs on (default: 3978) |
| `spec.helm.version` | string | No | Chart version override (defaults to operator's configured version) |
| `spec.helm.values` | object | No | Helm values merged over defaults |

### Status Fields

```yaml
status:
  phase: string                     # Pending | Provisioning | Ready | Failed | Deleting
  accessUrl: string                 # e.g., "https://alice.<your-domain>"
  namespace: string                 # e.g., "cetirun-alice"
  helmReleaseName: string           # e.g., "openclaw-alice"
  observedGeneration: int64
  observedOperatorHash: string
  lastReconcileTime: timestamp
  conditions:
    - type: Ready                   # Overall readiness
      status: "True" | "False" | "Unknown"
      reason: string
      message: string
      lastTransitionTime: timestamp
    - type: HelmDeployed            # Helm chart deployment status
    - type: IngressReady            # VirtualService configuration status
    - type: PodReady                # StatefulSet pod readiness
    - type: TailscaleReady          # Tailscale configuration status (if applicable)
```

| Status Field | Description |
|-------------|-------------|
| `status.phase` | Current lifecycle phase |
| `status.accessUrl` | HTTPS URL for the instance |
| `status.namespace` | Kubernetes namespace created for the instance |
| `status.helmReleaseName` | Helm release name for diagnostics |
| `status.observedGeneration` | Last spec generation reconciled |
| `status.observedOperatorHash` | Hash of operator config at last reconcile |
| `status.lastReconcileTime` | Timestamp of last successful reconciliation |
| `status.conditions` | Array of condition objects tracking sub-resource states |

### Phase Transitions

```
                 ┌──────────┐
                 │ Pending  │  CR created, waiting for operator
                 └────┬─────┘
                      │
                 ┌────▼──────────┐
                 │ Provisioning  │  Operator creating namespace, Helm release, VirtualService
                 └────┬──────────┘
                      │
              ┌───────┴───────┐
              │               │
        ┌─────▼─────┐   ┌────▼────┐
        │   Ready   │   │  Failed │  All conditions met / something went wrong
        └─────┬─────┘   └─────────┘
              │
        ┌─────▼─────┐
        │ Deleting  │  CR deletion triggered
        └───────────┘
```

### Conditions Reference

| Condition | True When | Common Failure Reasons |
|-----------|-----------|----------------------|
| `Ready` | All sub-conditions are True | Any sub-condition is False |
| `HelmDeployed` | Helm release is in `deployed` status | Chart download failed, values validation error |
| `IngressReady` | VirtualService exists and is valid | Gateway reference missing, host conflict |
| `PodReady` | StatefulSet pod is Running and Ready | CrashLoopBackOff, ImagePullBackOff, OOMKilled |
| `TailscaleReady` | Tailscale auth key is valid and connected | Auth key expired, connectivity issues |

---

## 2. What the Operator Creates Per Instance

When the operator reconciles an `OpenClawInstance` CR, it creates:

| Resource | Name Pattern | Namespace |
|----------|-------------|-----------|
| Namespace | `cetirun-<alias>` | (cluster-scoped) — with Istio injection label |
| Role | `cetirun-operator-ns-role` | `cetirun-<alias>` |
| RoleBinding | `cetirun-operator-ns-rolebinding` | `cetirun-<alias>` |
| Secret | `gateway-token` (contains `OPENCLAW_GATEWAY_TOKEN`) | `cetirun-<alias>` |
| Helm release | `openclaw-<alias>` (chart fullname for K8s resources: `openclaw-<alias>-cetirun`) | `cetirun-<alias>` |
| StatefulSet | (from Helm chart) | `cetirun-<alias>` |
| Service | (from Helm chart) | `cetirun-<alias>` |
| ConfigMap | (from Helm chart) | `cetirun-<alias>` |
| NetworkPolicy | (from Helm chart) | `cetirun-<alias>` |
| PVC | (from Helm chart) | `cetirun-<alias>` |
| VirtualService | `cetirun-<alias>` | `cetirun-<alias>` |

### VirtualService Pattern

```yaml
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: cetirun-<alias>
  namespace: cetirun-<alias>
spec:
  hosts: ["<alias>.<your-domain>"]
  gateways: ["aks-istio-ingress/cetirun-gateway"]
  http:
    - route:
        - destination:
            host: openclaw-<alias>-cetirun.cetirun-<alias>.svc.cluster.local
            port:
              number: 18789  # gateway
    # Additional routes for ttyd (7681) and noVNC (6080) if configured
```

---

## 3. Namespace Layout

```
cetirun-system        — CetiRun Operator (Deployment, 2 replicas, leader election)
cetirun-portal        — Portal web UI
cetirun-admin         — AdminClaw privileged instance
cetirun-<alias>       — Per-user OpenClaw instances (created by operator)
aks-istio-system      — AKS-managed Istio control plane
aks-istio-ingress     — AKS-managed Istio ingress gateway
```

### Namespace Responsibilities

| Namespace | Owner | AdminClaw Access |
|-----------|-------|-----------------|
| `cetirun-system` | Platform team | Read-only (logs, events, deployment status). **Never modify.** |
| `cetirun-portal` | Platform team | Read-only (health monitoring) |
| `cetirun-admin` | AdminClaw | Full access. **Never delete.** |
| `cetirun-<alias>` | Operator (per instance) | Full read/write for monitoring and remediation |
| `aks-istio-*` | AKS platform | Read-only. **Never modify Istio infra.** |

---

## 4. Service Port Reference

| Service | Port | Protocol | Description |
|---------|------|----------|-------------|
| OpenClaw Gateway | 18789 | HTTP | Main application gateway (primary VirtualService target) |
| ttyd (web terminal) | 7681 | HTTP/WebSocket | Web-based terminal access |
| noVNC (browser) | 6080 | HTTP/WebSocket | Browser access via noVNC |
| Chrome CDP | 18800 | HTTP | Chrome DevTools Protocol |

---

## 5. Kubernetes API Permissions

AdminClaw's `adminclaw-admin` ClusterRole grants the following permissions:

| Resource | API Group | Verbs | Purpose |
|----------|-----------|-------|---------|
| `OpenClawInstance` | `cetirun.microsoft.com/v1alpha1` | get, list, watch, create, update, patch | Instance lifecycle (**no delete**) |
| `Namespace` | core/v1 | get, list, watch, create | Instance namespace provisioning (**no delete**) |
| `Pod` | core/v1 | get, list, watch, delete | Diagnostics and remediation (restart) |
| `Pod/exec` | core/v1 | create | Diagnostics and storage cleanup |
| `StatefulSet` | apps/v1 | get, list, watch | Monitor instance workloads |
| `Service` | core/v1 | get, list, watch | Verify service endpoints |
| `Secret` | core/v1 | get, list, watch, create, update, patch | Token and credential management (**no delete**) |
| `ConfigMap` | core/v1 | get, list, watch, update, patch | Read and update configuration |
| `PersistentVolumeClaim` | core/v1 | get, list, watch, patch | Storage monitoring and expansion |
| `Event` | core/v1, events.k8s.io | get, list, watch, create, patch | Failure detection and audit logging |
| `VirtualService` | `networking.istio.io/v1beta1` | get, list, watch, create, update, patch | Ingress management (**no delete**) |
| `AuthorizationPolicy` | `security.istio.io/v1` | get, list, watch | Security audit |
| `NetworkPolicy` | networking.k8s.io/v1 | get, list, watch | Security audit |
| `Deployment` | apps/v1 | get, list, watch | Operator and portal health checks |
| `ClusterRoleBinding` | rbac.authorization.k8s.io/v1 | get, list, watch | RBAC audit |

### Key Constraints

- **No `delete` on `OpenClawInstance`**: This is intentional. Instance deletion must go through another principal.
- **No `delete` on Namespaces, Secrets, or VirtualServices**: Overwrite/re-create instead of delete. Namespace lifecycle is managed by the operator.
- **No `create`/`update` on Deployments**: AdminClaw cannot modify operator or portal deployments.
- **No write access to RBAC resources**: Prevents privilege escalation.
- **No write access to Istio infrastructure**: Gateway, mesh config, and control plane are off-limits.
- **Cluster-scoped reads**: AdminClaw can read nodes, CRDs, and cluster-level resources for diagnostics.

---

## 6. MSTeamsSpec Reference

The `spec.msteams` field configures Microsoft Teams bot integration for an instance. When set, the operator provisions webhook routing, credential injection, and (optionally) certificate mounts or Azure Workload Identity.

### MSTeamsSpec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `spec.msteams.appId` | string | Yes | — | Azure AD Application (client) ID. Must be a valid UUID. |
| `spec.msteams.tenantId` | string | Yes | — | Azure AD tenant ID. Must be a valid UUID. |
| `spec.msteams.authType` | enum | No | `secret` | Auth method: `secret`, `certificate`, or `managedIdentity` |
| `spec.msteams.certificateSecretRef` | string | No | — | Secret name containing PEM cert (key: `tls.pem`). Required when `authType=certificate`. |
| `spec.msteams.certificateThumbprint` | string | No | — | SHA-1 thumbprint of the certificate (hex, no colons). Required when `authType=certificate`. |
| `spec.msteams.webhookPort` | int32 | No | `3978` | Port the Teams webhook listener runs on inside the container. |

### Example CR with MS Teams

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: OpenClawInstance
metadata:
  name: bob
spec:
  user:
    alias: bob
    displayName: "Bob Builder"
    team: aks-ai
  gatewayTokenSecretRef: "gateway-token"
  msteams:
    appId: "<your-app-id>"
    tenantId: "<your-tenant-id>"
    authType: managedIdentity
```

### What the Operator Provisions for MS Teams

| Resource | Details |
|----------|---------|
| Gateway token Secret | Adds `MSTEAMS_APP_ID` and `MSTEAMS_TENANT_ID` keys |
| Helm values: `msteamsWebhookPort` | Exposes webhook port on Service and StatefulSet |
| Helm values: `msteamsAuthType` | Sets `MSTEAMS_AUTH_TYPE` and `MSTEAMS_USE_MANAGED_IDENTITY` env vars |
| VirtualService route | `POST /<instance>/api/messages` → webhook port (before catch-all). Non-Teams instances get a deny route returning 404 for the same path. |
| NetworkPolicy | Allows ingress on webhook port from Istio ingress gateway |
| ConfigMap (`openclaw.json`) | Registers msteams plugin with load path, enables msteams channel |
| Extra volumes (certificate only) | Mounts cert Secret at `/etc/msteams-cert/tls.pem` |
| Azure Workload Identity (managedIdentity only) | Annotated ServiceAccount; IMDS remains blocked by NetworkPolicy |

See [MS Teams Setup Guide](../../../docs/guides/msteams.md) for full setup instructions.
