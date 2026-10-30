---
title: "Agent Identity setup"
description: "Prepare the identity, encryption, certificates, provider registrations, and platform configuration used by Agent connections."
weight: 25
---

Use this guide to prepare a CetiRun environment for Agent connections. It is
for platform administrators; users follow [Agent identities](../../guides/agent-identity/)
after installation. Start with [Install CetiRun](../../getting-started/platform/)
for the cluster, ingress, runtime, storage, images, and model prerequisites.
The cluster Bicep and platform chart do not create the external identities,
provider registrations, encryption key, or certificates described here.

## Choose the capabilities

Portal login identifies the person managing an Agent. A **Connection** stores
that person's provider authorization. An Agent's **Identities** associates
selected connections with that Agent. A Microsoft Entra **Agent Identity** is
a separate directory identity, needed for independent Azure and Microsoft
delegated access. GitHub and Google connections do not require that directory
identity, but still require authenticated Portal ownership and encrypted storage.

| Capability | Preparation beyond the common foundation |
| --- | --- |
| GitHub, selected repositories | [Shared GitHub App registration](../../guides/github-app/#register-the-platform-app), user authorization and installation |
| GitHub, personal account | [GitHub OAuth App](../identity-providers/#github-personal-oauth) |
| Gmail and Google Calendar | [Google Web OAuth client](../identity-providers/#google-personal-oauth) |
| Azure, independent Agent | [Entra Blueprint and lifecycle setup](../entra-agent-identity/), then Azure RBAC for each Agent principal |
| Azure or Microsoft Graph, personal account | Entra lifecycle plus [delegated authorization setup](../entra-agent-identity/), consent and downstream permissions |

The baseline below enables GitHub/Google connections and explicitly disables
both Broker Entra modes. Add the Entra overlays only after completing their
prerequisites. Broker chart defaults enable both modes, so omitting those flags
does not produce a GitHub/Google-only deployment.

## 1. Prepare access and record the coordinates

Use an authorized Azure CLI session and an explicit Kubernetes context. The
administrator needs resource provisioning and role-assignment authority for
the encryption resources, Kubernetes permission to install the charts and
Secrets, and provider-specific registration/consent authority. Entra directory
roles and Azure subscription roles are different permissions. The examples
use Azure CLI, kubectl, Helm, Bash, OpenSSL 3 and jq; install these tools before
starting and keep environment values and credentials outside the checkout.

Complete these prerequisites before enabling the Portal feature:

- AKS OIDC issuer and Workload Identity webhook; retain the exact issuer URL,
  including its trailing slash. See [AKS Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster).
- Managed Istio, namespace injection, DNS, a working HTTPS Portal, and the
  [Portal login app and group admission](../istio/#prepare-identity-dns-and-tls).
  OAuth2 Proxy must forward its verified ID token to the Portal. The Portal
  checks the token signature, tenant, audience and immutable owner identity;
  email headers alone are insufficient.
- A supported Hermes or OpenClaw workload and an operational platform, or a
  governed Custom image implementing the [Custom client contract]({{< repo "app/cetirun-connections-mcp/README.md" >}}#governed-custom-workloads).
  Custom additionally requires `custom` in each runtime allowlist below and the
  native common-executor configuration; changing a type list alone grants nothing.
- Network access from the Broker to Kubernetes, Entra sign-in, its Key Vault,
  and configured providers; from MCP to Kubernetes, the Broker and the fixed
  provider API origins. DNS, mesh control-plane access and image pulls must
  also work. Review [Istio egress](../istio/#egress-and-logging) and the Broker/MCP
  NetworkPolicies; a ServiceEntry alone does not grant network access.

Provider endpoints include `github.com` and `api.github.com`,
`accounts.google.com`, `oauth2.googleapis.com`, `openidconnect.googleapis.com`,
`gmail.googleapis.com`, `www.googleapis.com`, `login.microsoftonline.com`,
`graph.microsoft.com` and `management.azure.com`, as applicable. Permit the
deployment's Key Vault hostname. Browser authorization and server-side token
exchange need different endpoints; check both paths.

The examples use the following coordinates. Keep them consistent if you rename
anything, and use your existing platform release name during upgrades. Replace
the ID placeholders with the actual GUIDs from your deployment.

| Resource | Example and configuration consumer |
| --- | --- |
| Portal origin | `https://portal.example.com`; provider redirects and Broker Portal URLs |
| Tenant ID | `<tenant-id>`; Portal, Broker, optional Operator Entra configuration |
| Portal login application client ID | `<portal-client-id>`; OAuth2 Proxy and Portal `agentIdentity.portalClientId` |
| Broker encryption managed identity client ID | `<encryption-client-id>`; Broker `agentIdentity.encryptionClientId` |
| Encryption managed identity principal/object ID | Returned by Azure; used for the Key Vault role assignment, not the client-ID Helm field |
| Broker namespace / ServiceAccount | `cetirun-credentials` / `cetirun-agent-broker` |
| Platform namespace | `cetirun-system`; Operator, Portal and MCP in the umbrella deployment |
| Operator / Portal / MCP ServiceAccounts | `cetirun-operator-controller-manager` / `cetirun-portal` / `cetirun-connections-mcp` |
| Kubernetes workload audience | `cetirun-connections-mcp`; must match Broker, MCP and Operator |
| Entra federation audience | `api://AzureADTokenExchange`; distinct from the MCP workload audience |
| Istio revision | `asm-example` is a placeholder for the installed revision |

Prepare private files outside the repository. Commands below create cloud or
cluster resources only where explicitly marked; offline rendering is separate.
Use `umask 077`, avoid shell tracing, and keep credential values out of command
arguments, Helm values, logs and support attachments.

## 2. Create the encryption foundation

The Broker uses envelope encryption: connection records are Kubernetes Secrets
containing ciphertext, and a Key Vault RSA key wraps their data-encryption keys.
This foundation is required even with both Entra modes disabled. It does not
require a Blueprint or Microsoft delegated authorization client.

Use a dedicated user-assigned managed identity for encryption. Its federated
credential trusts the Broker ServiceAccount, with the AKS issuer and
`api://AzureADTokenExchange` audience. The identity only needs the Key Vault
crypto operations required by the Broker; it does not need subscription-wide
Contributor or Graph application-management permissions.

**Connected, mutating setup example.** Replace the inputs, select the reviewed
subscription, and use an existing approved resource group. The person creating
the key must already have Key Vault data-plane key-management authority
(for example, Key Vault Crypto Officer); resource-group Contributor alone is
insufficient. Your network policy must permit the Broker to reach the vault.

```bash
umask 077
IDENTITY_RESOURCE_GROUP=example-platform-rg
IDENTITY_LOCATION=your-approved-region
IDENTITY_VAULT_NAME=your-globally-unique-vault-name
IDENTITY_SUBSCRIPTION_ID=your-subscription-id
KEY_PROVISIONER_OBJECT_ID=your-administrator-user-object-id
AKS_RESOURCE_GROUP=example-cluster-rg
AKS_CLUSTER_NAME=example-cluster
az account set --subscription "$IDENTITY_SUBSCRIPTION_ID"

AKS_OIDC_ISSUER=$(az aks show --resource-group "$AKS_RESOURCE_GROUP" \
  --name "$AKS_CLUSTER_NAME" --query oidcIssuerProfile.issuerUrl --output tsv)
test -n "$AKS_OIDC_ISSUER" && test "$AKS_OIDC_ISSUER" != null

az identity create --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name cetirun-broker-encryption --location "$IDENTITY_LOCATION" --output none
ENCRYPTION_CLIENT_ID=$(az identity show --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name cetirun-broker-encryption --query clientId --output tsv)
ENCRYPTION_PRINCIPAL_ID=$(az identity show --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name cetirun-broker-encryption --query principalId --output tsv)
az identity federated-credential create --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --identity-name cetirun-broker-encryption --name cetirun-broker \
  --issuer "$AKS_OIDC_ISSUER" \
  --subject system:serviceaccount:cetirun-credentials:cetirun-agent-broker \
  --audiences api://AzureADTokenExchange --output none

az keyvault create --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name "$IDENTITY_VAULT_NAME" --location "$IDENTITY_LOCATION" \
  --enable-rbac-authorization true --enable-purge-protection true --output none
IDENTITY_VAULT_ID=$(az keyvault show --name "$IDENTITY_VAULT_NAME" \
  --query id --output tsv)
# If the provisioning user does not already have key-management authority:
az role assignment create --assignee-object-id "$KEY_PROVISIONER_OBJECT_ID" \
  --assignee-principal-type User --role 'Key Vault Crypto Officer' \
  --scope "$IDENTITY_VAULT_ID" --output none
# Allow the role assignment to propagate before creating the key:
az keyvault key create --vault-name "$IDENTITY_VAULT_NAME" \
  --name connection-records --kty RSA --size 3072 \
  --ops wrapKey unwrapKey --output none
az role assignment create --assignee-object-id "$ENCRYPTION_PRINCIPAL_ID" \
  --assignee-principal-type ServicePrincipal \
  --role 'Key Vault Crypto Service Encryption User' \
  --scope "$IDENTITY_VAULT_ID/keys/connection-records" --output none
```

Allow several minutes for new role assignments to propagate. If key creation
returns an authorization error, confirm the signed-in provisioning principal,
assignment scope and vault network access, then retry only the key-creation
step after propagation. Do not proceed to deployment until it succeeds.

The [Key Vault Crypto Service Encryption User role](https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/security#key-vault-crypto-service-encryption-user)
provides key metadata read and wrap/unwrap operations. Scope it to the intended
key. Configure `https://<vault-name>.vault.azure.net/keys/connection-records`
as `agentIdentity.keyVaultKeyUrl`; this versionless URL allows new records to
use a rotated version while old records retain the exact key version needed
for decryption. Keep old versions enabled and accessible while any saved
records or backups depend on them. A Key Vault **secret** URL is not a key URL.

For a vault with private access, provision its network path and DNS before
deployment. The example creates the vault but does not implement that network
design. Reuse an approved vault instead when one already exists; do not
overwrite its access configuration with the creation example.

## 3. Prepare namespaces and TLS

Keep the Broker in its private credential namespace. The platform chart rejects
installing it beside MCP. The Broker may read encrypted records and provider
Secrets; MCP has no Kubernetes Secret API access. The Operator creates scoped
Broker record permissions in Agent namespaces when `connectionsMCP.brokerNamespace`
and `brokerServiceAccount` are set. Keep those fields until cleanup is complete.

Create missing namespaces and label both for the installed Istio revision.
For an existing namespace, inspect its injection configuration before changing
labels; do not combine legacy `istio-injection` selection with a different
revision. **These commands mutate the selected cluster:**

```bash
kubectl create namespace cetirun-credentials
# Run only when the platform namespace does not already exist:
kubectl create namespace cetirun-system
kubectl label namespace cetirun-credentials istio.io/rev=your-installed-revision
kubectl label namespace cetirun-system istio.io/rev=your-installed-revision
```

There are two TLS layers: Istio mTLS identifies workload peers, and application
TLS authenticates the MCP endpoint and its private Broker connection. Both are
required. Portal/Operator use Broker HTTP port 8080 inside the authenticated
mesh; MCP uses Broker HTTPS service port 443, targeting container port 8443.
Do not expose Broker or MCP through the public ingress.

Request the following PEM certificates from your private PKI. Leaf certificates
need a valid chain, matching private key, and the indicated extended key usage.
The charts do not issue certificates.

| Material | Required identity | Destination |
| --- | --- | --- |
| Broker server certificate | DNS `cetirun-agent-broker.cetirun-credentials.svc.cluster.local`, `serverAuth` | Broker Secret `broker-tls`: `tls.crt`, `tls.key`, `ca.crt` trusting the MCP client issuer |
| MCP server certificate | DNS `cetirun-connections-mcp.cetirun-system.svc.cluster.local`, `serverAuth` | Platform Secret `connections-mcp-tls`: `tls.crt`, `tls.key` |
| MCP's Broker client certificate | Exactly one URI SAN: `spiffe://cluster.local/ns/cetirun-system/sa/cetirun-connections-mcp`, `clientAuth` | Platform Secret `broker-client-tls`: `tls.crt`, `tls.key`, `ca.crt` trusting the Broker server issuer |
| MCP server CA bundle | Public CA certificates only, no private key | Operator namespace Secret `connections-mcp-ca`: `ca.crt`, distributed by the Operator to Agent workloads |

For a private environment that manages its own dedicated CA, the following
**local file-generation example** supplies all three certificates. Use your
organization's approved issuance process instead if it owns the PKI. Keep
the CA private key outside Kubernetes and protect it independently of leaf keys.
Choose a new directory under an existing private parent; this example refuses
to overwrite an existing directory.

```bash
umask 077
IDENTITY_TLS_DIR=/absolute/private/identity-tls
# Refuse to replace an existing CA or certificate directory.
mkdir -m 700 "$IDENTITY_TLS_DIR" || exit 1
openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 365 \
  -subj '/CN=CetiRun identity example CA' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$IDENTITY_TLS_DIR/ca.key" -out "$IDENTITY_TLS_DIR/ca.crt"

for leaf in broker-server mcp-server mcp-client; do
  case "$leaf" in
    broker-server)
      leaf_usage=serverAuth
      leaf_san=DNS:cetirun-agent-broker.cetirun-credentials.svc.cluster.local ;;
    mcp-server)
      leaf_usage=serverAuth
      leaf_san=DNS:cetirun-connections-mcp.cetirun-system.svc.cluster.local ;;
    mcp-client)
      leaf_usage=clientAuth
      leaf_san=URI:spiffe://cluster.local/ns/cetirun-system/sa/cetirun-connections-mcp ;;
  esac
  openssl req -new -newkey rsa:3072 -nodes -subj "/CN=$leaf" \
    -keyout "$IDENTITY_TLS_DIR/$leaf.key" -out "$IDENTITY_TLS_DIR/$leaf.csr"
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=%s\nsubjectAltName=%s\n' \
    "$leaf_usage" "$leaf_san" > "$IDENTITY_TLS_DIR/$leaf.ext"
  openssl x509 -req -sha256 -days 90 -in "$IDENTITY_TLS_DIR/$leaf.csr" \
    -CA "$IDENTITY_TLS_DIR/ca.crt" -CAkey "$IDENTITY_TLS_DIR/ca.key" \
    -CAcreateserial -extfile "$IDENTITY_TLS_DIR/$leaf.ext" \
    -out "$IDENTITY_TLS_DIR/$leaf.crt"
done
```

Install the files as Secrets. **Connected, mutating commands** for the single-CA
example above; with separate issuers, use the appropriate CA bundle in each place:

```bash
kubectl --namespace cetirun-credentials create secret generic broker-tls \
  --from-file=tls.crt="$IDENTITY_TLS_DIR/broker-server.crt" \
  --from-file=tls.key="$IDENTITY_TLS_DIR/broker-server.key" \
  --from-file=ca.crt="$IDENTITY_TLS_DIR/ca.crt"
kubectl --namespace cetirun-system create secret tls connections-mcp-tls \
  --cert="$IDENTITY_TLS_DIR/mcp-server.crt" --key="$IDENTITY_TLS_DIR/mcp-server.key"
kubectl --namespace cetirun-system create secret generic broker-client-tls \
  --from-file=tls.crt="$IDENTITY_TLS_DIR/mcp-client.crt" \
  --from-file=tls.key="$IDENTITY_TLS_DIR/mcp-client.key" \
  --from-file=ca.crt="$IDENTITY_TLS_DIR/ca.crt"
kubectl --namespace cetirun-system create secret generic connections-mcp-ca \
  --from-file=ca.crt="$IDENTITY_TLS_DIR/ca.crt"
```

These are creation commands, not a rotation mechanism. A Secret with the same
name causes creation to fail; update it through your reviewed Secret-management
process. Do not copy the CA private key into any Secret or Agent namespace.

## 4. Register the selected providers

Complete [Identity providers](../identity-providers/) for GitHub App, GitHub
OAuth and Google. For Azure/Microsoft access, also complete
[Entra Agent Identity setup](../entra-agent-identity/). Store provider Secrets
in `cetirun-credentials` and retain only their names in Helm values.

There are distinct browser return paths:

| Purpose | Exact example URL |
| --- | --- |
| Portal sign-in, OAuth2 Proxy | `https://portal.example.com/oauth2/callback` |
| Broker-managed provider authorization, including Entra delegated sign-in | `https://portal.example.com/api/v1/connections/callback` |
| GitHub App installation setup | `https://portal.example.com/api/v1/connections/github-app/setup` |

The latter two go through the Portal; they are not public Broker addresses.
Register the actual HTTPS origin and exact path without a trailing slash.

## 5. Assemble values

Keep values outside the repository. The following **Broker baseline**, saved
as `broker-identity.yaml`, supplies all required identity settings for the
GitHub/Google-only path. Replace the illustrative IDs, vault and Istio revision;
select reviewed image versions/digests separately. Add only the provider
overlays you configured in step 4.

```yaml
identityModes:
  azureAgent:
    enabled: false
  microsoftDelegated:
    enabled: false
agentIdentity:
  tenantId: "<tenant-id>"
  encryptionClientId: "<encryption-client-id>"
  keyVaultKeyUrl: https://example.vault.azure.net/keys/connection-records
  redirectUri: https://portal.example.com/api/v1/connections/callback
  portalOrigin: https://portal.example.com
  portalBaseUrl: https://portal.example.com
portal:
  namespace: cetirun-system
  serviceAccount: cetirun-portal
operator:
  namespace: cetirun-system
  serviceAccount: cetirun-operator-controller-manager
toolService:
  namespace: cetirun-system
  serviceAccount: cetirun-connections-mcp
  runtimeTypes: [hermes, openclaw]
workloadTokenAudience: cetirun-connections-mcp
tls:
  enabled: true
  secretName: broker-tls
istio:
  enabled: true
  sidecarInjection: true
  revision: asm-example
```

Save the following **platform overlay** as `platform-identity.yaml`. It augments
your complete, reviewed platform values; it does not replace image/runtime,
registry, AI Gateway, model, admission, storage or administrator configuration.
In particular, identity setup alone does not supply a usable Agent model or
qualify a Sandbox execution profile.

```yaml
istio:
  enabled: true
cetirun-agent-broker:
  enabled: false
cetirun-connections-mcp:
  enabled: true
  tls:
    secretName: connections-mcp-tls
  broker:
    baseURL: https://cetirun-agent-broker.cetirun-credentials.svc.cluster.local
    clientTLSSecretName: broker-client-tls
  portal:
    baseURL: https://portal.example.com
  workloadTokenAudience: cetirun-connections-mcp
  istio:
    enabled: true
    sidecarInjection: true
    revision: asm-example
    brokerNamespace: cetirun-credentials
cetirun-operator:
  namespace: cetirun-system
  domainSuffix: portal.example.com
  istioRevision: asm-example
  agentIdentity:
    enabled: false
  connectionsMCP:
    url: https://cetirun-connections-mcp.cetirun-system.svc.cluster.local/mcp
    audience: cetirun-connections-mcp
    brokerNamespace: cetirun-credentials
    brokerServiceAccount: cetirun-agent-broker
    caSecretName: connections-mcp-ca
    runtimeTypes: [hermes, openclaw]
cetirun-portal:
  istio:
    host: portal.example.com
    sidecarInjection: true
  features:
    agentToolsEnabled: true
    agentIdentityEnabled: false
  agentIdentity:
    tenantId: "<tenant-id>"
    portalClientId: "<portal-client-id>"
    brokerURL: http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080
  agentTools:
    brokerURL: http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080
    runtimeTypes: [hermes, openclaw]
```

For standalone component releases, remove the corresponding umbrella key and
use its contents as that component's values. Adjust the Portal namespace in
the Broker's trust configuration if it runs in `cetirun-portal` instead.
Match all ServiceAccount names, MCP certificate SANs and URLs to the rendered
resources. Retain the same runtime allowlist in Portal, Broker and Operator.

## 6. Render, then deploy

From a checkout containing the selected feature versions, build the umbrella's
local chart dependencies. **Offline rendering** below does not create resources
or resolve Secrets. Redirected manifests may include other platform secrets
from your base values, so store them privately and never attach them to a PR.

```bash
IDENTITY_CONFIG_DIR=/absolute/private/cetirun-identity
helm dependency build app/cetirun-platform --skip-refresh
helm template cetirun-agent-broker app/cetirun-agent-broker/chart \
  --namespace cetirun-credentials \
  -f "$IDENTITY_CONFIG_DIR/broker-identity.yaml" \
  -f "$IDENTITY_CONFIG_DIR/broker-providers.yaml" > /dev/null
helm template cetirun-platform app/cetirun-platform \
  --namespace cetirun-system \
  -f "$IDENTITY_CONFIG_DIR/platform-base.yaml" \
  -f "$IDENTITY_CONFIG_DIR/platform-identity.yaml" > /dev/null
```

`broker-providers.yaml` is the combined provider overlay from step 4; use `{}`
if none is configured yet. For Entra, append its Broker and platform overlay
files after these baseline files in **both rendering and deployment**. Helm
merges maps in order; lists are replaced. An existing Entra deployment must
retain its Entra overlays when applying this baseline.

Once the cluster and credentials are ready, **connected, mutating installation**:

```bash
helm upgrade --install cetirun-agent-broker app/cetirun-agent-broker/chart \
  --namespace cetirun-credentials \
  -f "$IDENTITY_CONFIG_DIR/broker-identity.yaml" \
  -f "$IDENTITY_CONFIG_DIR/broker-providers.yaml" --wait --timeout 5m
helm upgrade --install cetirun-platform app/cetirun-platform \
  --namespace cetirun-system \
  -f "$IDENTITY_CONFIG_DIR/platform-base.yaml" \
  -f "$IDENTITY_CONFIG_DIR/platform-identity.yaml" --wait --timeout 10m
```

Use the existing release names for an upgrade and pass the complete reviewed
values, including your pinned images. Do not create a second platform release
or rely on `--reuse-values` to infer missing trust settings. Direct Helm does
not configure the shared Istio external authorization provider: complete the
[Istio installation](../istio/#direct-helm-installation) first, with its Portal
ownership target pointing at `cetirun-system` for this layout.

## 7. Verify the environment

Perform acceptance checks with disposable resources and accounts. Readiness
and a successful chart render do not prove provider authorization works.

1. Check Broker, MCP, Operator and Portal rollouts. Verify Istio sidecars and
   the Broker's injected Azure workload token mount without reading tokens.
   Broker `/readyz` is a local check and does not qualify Key Vault or GitHub.
2. Sign in to the Portal and open **Connections** at `/connections`. Verify
   only the signed-in owner's connections appear. Another owner must not be
   able to select them.
3. Connect each configured provider and finish its callback. Successful
   persistence exercises the encryption foundation; a provider error page is
   not a completed connection. For GitHub App, also select the installation.
4. On a ready supported Agent, use **Identities → Add identity** and explicitly
   confirm access. Check `connections_status` and `services_list` through the
   native MCP integration. Creating a Connection alone must not grant an Agent
   access.
5. Execute a small allowed request. For GitHub App, verify an unselected private
   repository and an ungranted write are rejected. Personal OAuth is governed
   by its broader scopes, not the App's repository selection controls.
6. With Entra enabled, wait for the Agent Identity to be Ready, assign a narrow
   Azure role to its principal, then test autonomous Azure separately from
   delegated sign-in. Test the actual Microsoft Graph permission you granted.
7. Remove the Agent binding and confirm new requests stop. Test reauthentication
   after consent revocation or refresh expiry. Local connection removal does
   not revoke provider-wide consent or uninstall a GitHub App.

These checks require live authorization and are not part of offline
development tests. Already-sent provider requests and previously issued tokens
are not recalled by local revocation.

## Troubleshooting

| Symptom | Check next |
| --- | --- |
| Connections absent or owner verification fails | Portal `agentToolsEnabled`, correct login app client ID/tenant, forwarded verified ID token, HTTPS origin and supported runtime |
| OAuth redirect mismatch | Exact provider callback, public origin and the distinction from `/oauth2/callback` |
| Broker cannot persist or decrypt | Encryption identity client ID, exact issuer/subject/audience, Key Vault key URL, wrap/unwrap role propagation, DNS/firewall and retained old key versions |
| Portal or Operator gets Broker 403 | Actual namespace/ServiceAccount against Broker `portal`/`operator`, sidecar injection and Istio policy |
| MCP cannot reach Broker | HTTPS service port 443, DNS server SAN, CA direction, client `clientAuth` and exact SPIFFE URI, Broker `toolService` identity |
| MCP rejects an Agent | Audience alignment, live Pod/ServiceAccount/Agent binding, supported runtime and current Agent state; do not substitute a manually copied token |
| Agent has no MCP connection | Operator URL/CA source, runtime allowlists, workload configuration and Agent namespace Broker permissions |
| Entra identity or delegated exchange fails | Follow the [Entra checks](../entra-agent-identity/); Key Vault encryption authority does not grant Blueprint lifecycle authority |
| Provider returns 401/403 | Current installation, owner/Admin authority, scopes, consent, resource RBAC, organization policy and token expiry |

Use correlation IDs and sanitized status/errors. Do not collect Secret bodies,
token files, full Helm values or browser cookies for a public diagnostic bundle.

## Rotation, shutdown and recovery

- Update provider client-secret files and their Secrets, then restart the Broker
  to reload environment variables. The GitHub App signing key is file-backed;
  account for Secret projection delay and verify the replacement before
  revoking the old key. See the [provider guide](../identity-providers/).
- Mount TLS Secrets as whole volumes. Broker/MCP reload mounted TLS material;
  invalid updates retain the last valid snapshot. Introduce overlapping CA
  trust first, rotate leaf certificates, distribute the MCP CA source to
  Agent workloads, verify new connections, then retire the previous CA.
  Check Agent reconciliation and native-client reload/restart requirements
  before removing old trust; server reload alone does not update every client.
- Back up encrypted records together with a recovery plan for their Key Vault
  key versions and identity permissions. Use the [restore procedure below](#restore-with-fresh-owner-authorization)
  before returning a recovered environment to service. Encryption does not
  establish that a backup reflects current owner consent or prevent an
  administrator with decryption authority from using credentials.
- To retire Entra, stop new identity creation while retaining Operator
  `agentIdentity.cleanupEnabled`, Blueprint federation and Broker access until
  existing identities and Agent records have been cleaned up. Preserve the
  Operator's Broker namespace/ServiceAccount settings during cleanup.
- Remove Agent grants before unused Connections. Revoke provider consent or
  uninstall the App separately when appropriate. Remove deployments, federation,
  certificates and encryption authority only after dependent records, Agents
  and retained backups have been handled; do not delete the credential namespace
  or Key Vault as a first troubleshooting step.

### Restore with fresh owner authorization

Exclude saved authorization records from routine restores and require fresh
owner authorization. Encryption and revision checks do not provide protection
against restoring older authorization state, including superseded grants and
revocation tombstones. A successful decryption or readiness check is not
approval to resume access.

If a whole-cluster or namespace restore includes those records, use this order:

1. **Keep execution closed before restoring.** Block Agent-to-MCP requests
   and Broker credential issuance, including internal cluster traffic. Quiesce
   authorization writes and in-flight requests; ensure restored workloads and
   controllers cannot reopen these paths during recovery. Closing only the
   public Portal ingress does not stop native MCP access.
2. **Inventory authorization records in both storage locations.** Cover the
   Broker credential namespace and every Agent workload namespace, including
   any older centrally stored bindings. Broker-managed Secret metadata uses
   `app.kubernetes.io/managed-by=cetirun-agent-broker` and
   `agenttools.cetirun.microsoft.com/record-type`. The record types to exclude
   or clear are `connection`, `binding`, `grant` (legacy), `authorization`
   (OAuth transactions), `account-index`, and `connection-reference`. Inspect
   metadata without exporting Secret bodies. These records are distinct from
   provider client configuration, TLS Secrets, Key Vault keys and Agent
   lifecycle metadata.
3. **Clear restored authorization state before enabling management writes.**
   Remove the restored records identified above from both storage locations;
   do not resume old OAuth transactions or restore their indexes/references
   later. When recovering into an existing environment, preserve its newer
   revocation tombstones and never replace them with older snapshots. This is
   a recovery reset, not routine tombstone garbage collection. Retain the
   infrastructure credentials and lifecycle metadata needed for current Entra
   identity reconciliation and cleanup; do not delete whole namespaces.
4. **Rebuild access through current owners.** Restore management access while
   keeping general Agent execution blocked. Owners recreate required
   Connections, sign in again where required, select GitHub App installations,
   and explicitly grant each Agent its services, repositories and permissions.
   Verify current Agent UIDs, owners and any Entra identity state before
   granting access. An old binding or a restored provider credential is not a
   substitute for this owner action.
5. **Validate before reopening traffic.** Through isolated recovery checks,
   verify that Agents without a new grant cannot use the services or obtain
   credentials, including Agents whose UID survived the restore. Verify a
   newly granted Agent's allowed access and that **Remove access** blocks new
   requests. Reopen general execution only after the record inventory and
   these checks pass. Previously sent provider requests or issued tokens are
   not recalled by this reset; handle provider-side revocation separately.

Configuration sources: [Broker values]({{< repo "app/cetirun-agent-broker/chart/values.yaml" >}}),
[MCP values]({{< repo "app/cetirun-connections-mcp/chart/values.yaml" >}}),
[Operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}),
[Portal values]({{< repo "app/cetirun-portal/chart/values.yaml" >}}),
and [platform values]({{< repo "app/cetirun-platform/values.yaml" >}}).
