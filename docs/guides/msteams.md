# Microsoft Teams Integration for OpenClaw

Connect your OpenClaw instance to Microsoft Teams so your AI assistant can respond to messages in Teams channels and direct chats.

## Prerequisites

- An Azure AD [App Registration](https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade) with a Bot Channel configured
- The App Registration's **Application (client) ID** and **Tenant ID** (both are UUIDs)
- One of the following authentication methods configured:
  - **Certificate**: A PEM certificate uploaded to the App Registration
  - **Managed Identity** (recommended): Azure Workload Identity federated credential on the App Registration

> **Note:** CetiRun does not support app secret (`appPassword`) authentication for MS Teams due to tenant security policy. Use **certificate** or **managed identity** auth instead.

## Architecture Overview

When MS Teams integration is enabled, the operator provisions:

1. **Helm values** — injects `msteamsWebhookPort`, `msteamsAuthType`, and credential env vars into the OpenClaw StatefulSet
2. **Service port** — exposes the Teams webhook listener port (default: 3978) on the Kubernetes Service
3. **Istio VirtualService route** — routes `POST /<instance>/api/messages` to the webhook port (inserted before the catch-all route). For non-Teams instances the operator instead inserts a deny route that returns 404 for `POST /<instance>/api/messages`, so the static gateway bypass cannot reach an instance that has no Teams adapter.
4. **Cluster-level Istio AuthorizationPolicy** (configured via the shared `infra/istio` chart, not per-instance by the operator) — bypasses OAuth2 Proxy for `POST */api/messages` only (Teams authenticates via Bot Framework, not Azure AD OAuth). Non-POST requests to the same suffix still go through OAuth2 Proxy. The bypass is a single static rule for all instances; per-instance enable/disable is enforced by the operator's VirtualService routes above.
5. **NetworkPolicy** — allows ingress on the webhook port from the Istio ingress gateway
6. **Gateway token Secret** — populates `MSTEAMS_APP_ID` and `MSTEAMS_TENANT_ID` keys

## 1. Create an Azure AD App Registration

1. Go to [Azure Portal → App Registrations](https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade)
2. Click **New registration**
3. Name it (e.g., `openclaw-teams-bot-<alias>`)
4. Set **Supported account types** to **Single tenant** (your org only)
5. Click **Register**
6. Note the **Application (client) ID** and **Directory (tenant) ID**

## 2. Configure Bot Channel

1. In your App Registration, go to **Manage → Expose an API**
2. Set the Application ID URI if prompted
3. Go to [Azure Bot Service](https://portal.azure.com/#create/Microsoft.AzureBot)
4. Create a new Azure Bot and link it to your App Registration
5. Under **Channels**, add **Microsoft Teams**
6. Set the **Messaging endpoint** to: `https://<your-domain>/<instance>/api/messages`

## 3. Enable MS Teams on Your Instance

### Option A — Managed Identity (Azure Workload Identity) — Recommended

Uses Azure Workload Identity — no secrets to manage. The operator creates a ServiceAccount annotated for workload identity and enables IMDS access in the NetworkPolicy.

1. Set up a [federated credential](https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-create-trust) on the App Registration that trusts the AKS cluster's OIDC issuer
2. Configure the CR:

```yaml
apiVersion: cetirun.microsoft.com/v1beta1
kind: Agent
metadata:
  name: alice
spec:
  user:
    alias: alice
    displayName: "Alice Smith"
    team: aks-core
  gatewayTokenSecretRef: "gateway-token"
  msteams:
    appId: "<your-app-id>"
    tenantId: "<your-tenant-id>"
    authType: managedIdentity
```

The operator will:
- Set `azureWorkloadIdentity.enabled=true` and `azureWorkloadIdentity.clientId=<appId>` in Helm values
- Create a ServiceAccount annotated with `azure.workload.identity/client-id`
- Configure the projected service account token volume for federated token exchange with Azure AD

### Option B — Certificate Authentication

Use a client certificate instead of a shared secret. Suitable when workload identity is not available.

1. Generate or obtain a PEM certificate (private key + cert combined)
2. Create a Kubernetes Secret with the cert:

```bash
kubectl create secret generic msteams-cert \
  -n cetirun-alice \
  --from-file=tls.pem=./combined-cert.pem
```

3. Get the SHA-1 thumbprint of the certificate (hex, no colons):

```bash
openssl x509 -in cert.pem -fingerprint -noout | tr -d ':' | cut -d= -f2
```

4. Configure the CR:

```yaml
apiVersion: cetirun.microsoft.com/v1beta1
kind: Agent
metadata:
  name: alice
spec:
  user:
    alias: alice
    displayName: "Alice Smith"
    team: aks-core
  gatewayTokenSecretRef: "gateway-token"
  msteams:
    appId: "<your-app-id>"
    tenantId: "<your-tenant-id>"
    authType: certificate
    certificateSecretRef: "msteams-cert"
    certificateThumbprint: "ABC123DEF456..."
```

The operator will mount the certificate at `/etc/msteams-cert/tls.pem` inside the container and set `MSTEAMS_CERTIFICATE_PATH` and `MSTEAMS_CERTIFICATE_THUMBPRINT` environment variables.

## 4. CRD Reference — MSTeamsSpec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `appId` | string | Yes | — | Azure AD Application (client) ID (UUID format) |
| `tenantId` | string | Yes | — | Azure AD tenant ID (UUID format) |
| `authType` | enum | No | `secret` | Authentication method: `certificate` or `managedIdentity` |
| `certificateSecretRef` | string | No | — | Secret name containing PEM cert (key `tls.pem`). Required when `authType=certificate` |
| `certificateThumbprint` | string | No | — | SHA-1 thumbprint (hex, no colons). Required when `authType=certificate` |
| `webhookPort` | int32 | No | `3978` | Port the Teams webhook listener runs on inside the container |

> **Note:** The CRD schema also accepts `authType: secret` for compatibility, but CetiRun's tenant policy does not allow app secret authentication. Use `certificate` or `managedIdentity`.

## 5. Helm Values Added by the Operator

When `spec.msteams` is set, the operator injects these Helm values:

| Value | Description |
|-------|-------------|
| `msteamsWebhookPort` | Webhook listener port (default 3978) |
| `msteamsAuthType` | `"certificate"` or `"managedIdentity"` |
| `extraEnv` | Certificate path/thumbprint env vars (certificate auth only) |
| `extraVolumes` / `extraVolumeMounts` | Certificate Secret mount (certificate auth only) |
| `azureWorkloadIdentity.enabled` | `true` (managed identity auth only) |
| `azureWorkloadIdentity.clientId` | App ID (managed identity auth only) |
| `serviceAccount.create` | `true` (managed identity auth only) |

## 6. Troubleshooting

### Bot not responding to messages

1. Check the Teams webhook listener is running:
   ```bash
   kubectl logs -n cetirun-alice sts/openclaw -c openclaw | grep -i msteams
   ```

2. Verify the Istio route exists:
   ```bash
   kubectl get vs -n istio-system cetirun-alice -o yaml | grep -A5 'api/messages'
   ```

3. Test the webhook endpoint directly:
   ```bash
   # POST reaches the Teams adapter (expect 401/Bot Framework auth, not a 302 login redirect):
   curl -X POST https://<your-domain>/<instance>/api/messages \
     -H "Content-Type: application/json" \
     -d '{"type":"message","text":"hello"}'

   # Non-Teams instance: POST returns 404 (operator deny route).
   # Non-POST to a Teams instance: redirected to OAuth2 Proxy login (method guard).
   ```

### Certificate auth errors

- Ensure the PEM file contains both the private key and certificate
- Verify the thumbprint matches: `openssl x509 -in cert.pem -fingerprint -noout`
- Check the Secret key is `tls.pem`: `kubectl get secret msteams-cert -n cetirun-alice -o jsonpath='{.data}' | jq -r 'keys'`

### Managed Identity token errors

- Verify the federated credential trust is configured correctly on the App Registration
- Verify the projected Workload Identity token is present and readable:
  ```bash
  kubectl exec -n cetirun-alice sts/openclaw -- ls -l /var/run/secrets/azure/tokens
  kubectl exec -n cetirun-alice sts/openclaw -- head -c 200 /var/run/secrets/azure/tokens/azure-identity-token
  ```
- Check egress to Azure AD (for token acquisition):
  ```bash
  kubectl exec -n cetirun-alice sts/openclaw -- curl -sSf https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration | head -c 200
  ```
- Verify the ServiceAccount annotation:
  ```bash
  kubectl get sa -n cetirun-alice -o yaml | grep azure.workload.identity
  ```
