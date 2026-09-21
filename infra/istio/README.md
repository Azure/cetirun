# infra/istio/ — CetiRun Istio Authentication (Helm Chart)

Shared Istio-based authentication components for CetiRun. Deployed once per cluster to provide Azure AD (Entra ID) authentication + per-engineer alias enforcement for all instances on a wildcard domain (default: `*.example.com`).

See [docs/cetirun.aad-auth.design.md](../../docs/cetirun.aad-auth.design.md) for the full design.

## Chart Structure

```
infra/istio/
├── Chart.yaml                    # Helm chart metadata
├── values.yaml                   # Default configuration values
├── deploy.sh                     # Installation script + ext_authz patcher
├── templates/
│   ├── _helpers.tpl              # Template helpers
│   ├── NOTES.txt                 # Post-install notes
│   ├── tls-secret.yaml           # TLS secret (optional)
│   ├── oauth2-proxy-cookie-secret.yaml
│   ├── oauth2-proxy-serviceaccount.yaml
│   ├── oauth2-proxy-deployment.yaml
│   ├── oauth2-proxy-service.yaml
│   ├── oauth2-proxy-virtualservice.yaml
│   ├── gateway.yaml              # Istio Gateway (*.example.com)
│   ├── authorization-policy.yaml # Wildcard AuthorizationPolicy for *.domain
│   ├── envoy-filter-redirect.yaml # Pre-ext_authz redirect header
│   └── envoy-filter-alias.yaml   # Post-ext_authz alias enforcement
└── README.md                     # This file
```

## Prerequisites

Before running `deploy.sh`:

1. **AKS cluster** with Istio addon enabled and external ingress gateway running
2. **Entra ID app registration** — see design doc §3 for configuration:
   - Redirect URI: `https://oauth2.<domain>/oauth2/callback` (default: `oauth2.example.com`)
   - Group claims: "Groups assigned to the application" (NOT "All security groups")
   - Federated identity credential for `system:serviceaccount:cetirun-istio:oauth2-proxy`
   - **Important**: The redirect URI must match the chart's `domain` value in `values.yaml`
3. **DNS**: Wildcard `*.<domain>` → Istio gateway external LB IP (default: `*.example.com`)
4. **TLS certificate** for `*.<domain>` (cert-manager or manual)
5. **Tools**: `helm`, `kubectl`, `az`, `jq`, `openssl` installed

## Deployment

### Using deploy.sh (recommended)

The script handles both Helm chart installation and ext_authz configuration:

```bash
export CLIENT_ID="<entra-app-client-id>"
export TENANT_ID="<azure-ad-tenant-id>"
export ALLOWED_GROUPS="<group-oid-1>,<group-oid-2>"

# Optional: provide TLS cert paths
export TLS_CERT_PATH="/path/to/tls.crt"
export TLS_KEY_PATH="/path/to/tls.key"

./deploy.sh
```

The script will:

1. Install/upgrade the Helm chart (`cetirun-istio` in `cetirun-istio` namespace)
2. Deploy OAuth2 Proxy, Istio Gateway, VirtualService, and EnvoyFilters
3. Patch the Istio shared configmap with the ext_authz extension provider
4. Wait for OAuth2 Proxy rollout

### Manual Helm Installation

```bash
helm upgrade --install cetirun-istio . \
  --namespace cetirun-istio --create-namespace \
  --set clientId=<CLIENT_ID> \
  --set tenantId=<TENANT_ID> \
  --set allowedGroups=<OID1>\\,<OID2>
```

**Note**: The chart auto-generates a cookie secret if not provided. Manual installation requires you to **separately configure** the ext_authz extension provider. See `deploy.sh` for the patching logic.

### Upgrading

```bash
# With deploy.sh — re-export environment variables
export CLIENT_ID="..." TENANT_ID="..." ALLOWED_GROUPS="..."
./deploy.sh

# Or directly with Helm
helm upgrade cetirun-istio . -n cetirun-istio --reuse-values
```

## What This Chart Deploys

- **OAuth2 Proxy** (`cetirun-istio`): Deployment, Service, ServiceAccount
- **Istio Gateway** (`aks-istio-ingress`): Handles `*.example.com` on port 443
- **VirtualService** (`aks-istio-ingress`): Routes `oauth2.example.com` → OAuth2 Proxy
- **AuthorizationPolicy** (`aks-istio-ingress`): Wildcard policy for `*.example.com` with exempt prefixes
- **EnvoyFilter** (redirect): Sets `X-Auth-Request-Redirect` before ext_authz
- **EnvoyFilter** (alias): Alias ↔ hostname enforcement after ext_authz

## Configuration

### Admin Aliases

Users listed in `authPolicy.adminAliases` are granted admin access to the CetiRun Portal. They can access the `/admin` routes (Team configuration, all-instance view, etc.) in addition to their own instances. They still require OAuth2 Proxy authentication and cannot access other users' instances. The same alias list is projected into the `cetirun-portal-admins` ConfigMap by the portal chart, which the portal backend reads to enforce `RequireAdmin` on `/api/v1/admin/*`.

```bash
helm upgrade cetirun-istio . -n cetirun-istio --reuse-values \
  --set 'authPolicy.adminAliases[0]=alice' \
  --set 'authPolicy.adminAliases[1]=bob'
```

### Portal ownership endpoint

The ownership EnvoyFilter calls the Portal's `/internal/auth` endpoint before
routing an instance path. The standard umbrella deployment places the Portal
in `cetirun-system`, so `deploy.sh` targets
`cetirun-portal.cetirun-system.svc.cluster.local:8081` by default.

For an independently installed Portal, set its namespace and service when
deploying Istio:

```bash
export PORTAL_AUTH_NAMESPACE=cetirun-portal
export PORTAL_AUTH_SERVICE=cetirun-portal
./deploy.sh
```

`PORTAL_AUTH_CLUSTER_HOST` can be used instead when the service has a custom
cluster FQDN.

### Exempt Prefixes

The chart uses two separate exemption lists for fine-grained control:

| List | Purpose | Default Prefixes | Effect |
| --- | --- | --- | --- |
| `authExemptPrefixes` | Bypass OAuth2 Proxy authentication entirely | `oauth2`, `llmapi` | No auth; alias check still applies unless also in `aliasExemptPrefixes` |
| `aliasExemptPrefixes` | Bypass alias ↔ hostname enforcement only | `oauth2`, `llmapi` | Auth required (unless also in `authExemptPrefixes`), no alias check |

The **portal** is served directly on the apex `<domain>` (not a subdomain). It is handled by a dedicated rule in the AuthorizationPolicy that requires OAuth2 Proxy authentication, and the path-based ownership check (Lua filter) exempts portal-internal paths (`/`, `/admin`, `/instances/`, `/api/`, `/oauth2`, etc.) so it does not need to appear in `aliasExemptPrefixes`.

Configure via `values.yaml` → `authPolicy.authExemptPrefixes` and `authPolicy.aliasExemptPrefixes`.

## What This Does NOT Deploy

Per-instance resources are managed by the **cetirun-operator** during instance creation:

- `VirtualService` per instance — routes `<domain>/<instance>` to OpenClaw pod

See [docs/cetirun.operator.design.md](../../docs/cetirun.operator.design.md) and design doc §6 for operator integration.

## Managing the Chart

```bash
# Status
helm status cetirun-istio -n cetirun-istio

# List values
helm get values cetirun-istio -n cetirun-istio

# Upgrade with changed values
helm upgrade cetirun-istio . -n cetirun-istio \
  --set oauth2Proxy.replicas=3

# Uninstall
helm uninstall cetirun-istio -n cetirun-istio
```

**Note**: Uninstalling does NOT remove the ext_authz provider from the Istio configmap. To clean that up, manually edit:

```bash
kubectl edit configmap istio-shared-configmap-<revision> -n aks-istio-system
# Remove the oauth2-proxy entry under extensionProviders
```

## Authentication Flow

```
Browser → *.example.com
  → Istio Gateway (aks-istio-ingress)
    → [EnvoyFilter: set X-Auth-Request-Redirect]
    → [ext_authz → OAuth2 Proxy (Entra ID OIDC)]
    → [EnvoyFilter: alias ↔ hostname check]
    → VirtualService → OpenClaw pod
```

## Troubleshooting

```bash
# Check OAuth2 Proxy logs
kubectl logs -n cetirun-istio -l app=oauth2-proxy

# Verify Gateway
kubectl get gateway cetirun-gateway -n aks-istio-ingress

# Verify EnvoyFilters
kubectl get envoyfilter -n aks-istio-ingress

# Check ext_authz in Istio mesh config
kubectl get cm istio-shared-configmap-<revision> -n aks-istio-system -o yaml

# Verify Istio ingress listeners (ext_authz should appear)
istioctl proxy-config listener -n aks-istio-ingress <gateway-pod> --port 443
```


### Webhook Bypass Paths

The `authPolicy.webhookBypassPaths` value lists URL paths that are **completely exempted from OAuth2 Proxy authentication** at the Istio AuthorizationPolicy level. Requests to these paths are allowed through to the application without any mesh-level auth.

This is different from `authExemptPrefixes` (which exempts by hostname prefix) -- `webhookBypassPaths` exempts by **request path** across all hosts.

| Value | Purpose | Default |
| --- | --- | --- |
| `authPolicy.webhookBypassPaths` | Paths bypassing OAuth2 Proxy auth entirely | `["/api/messages"]` |

**Default**: `/api/messages` is bypassed to allow Microsoft Teams Bot Framework webhook callbacks. The Teams Bot Framework performs its own JWT token validation, so OAuth2 Proxy auth is not needed (and would block the webhook since Teams sends its own auth headers, not Azure AD cookies).

**Security considerations:**
- Paths listed here are **UNAUTHENTICATED** at the mesh edge
- The application behind these paths **must perform its own authentication** (e.g., Bot Framework token validation, HMAC signature verification)
- To disable all webhook bypasses: `webhookBypassPaths: []`
