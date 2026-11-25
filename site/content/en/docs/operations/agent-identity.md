---
title: "Prepare Agent connections"
description: "Prepare the encryption key, certificates, provider registrations, and platform values that Agent connections use."
weight: 25
---

Agent connections let an Agent call Azure, GitHub, Gmail, and Google Calendar
with the accounts that its owner connects. The Portal does not offer
Microsoft 365 (Microsoft Graph) connections. These components supply Agent
connections:

- The **Agent Identity Broker** stores the encrypted provider authorizations in
  the credential namespace, `cetirun-credentials`. It issues short-lived
  credentials for each request.
- The **Connections MCP** service gives Agents the connection tools. It sends
  each request to the provider with a credential from the Broker.

This guide is for platform administrators. After installation, users follow
[Agent Connections and Identity](../../guides/agent-identity/). For the
cluster, ingress, runtime, storage, images, and model prerequisites, start
with [Install CetiRun](../../getting-started/platform/). The cluster Bicep and
the platform chart do not create the external identities, provider
registrations, encryption key, or certificates that this guide describes.

## What `make deploy` requires

[`make deploy`](../../getting-started/platform/#platform-chart) installs the
Broker with Microsoft delegated access through the Entra Auth SDK. It also
enables the Agent Identity lifecycle of the Operator. If the environment file
disables one of these features, `make deploy` stops. As a result, the Entra
parts of this guide are not optional with `make deploy`. GitHub and Google
connections stay optional.

`make deploy` stops before it writes if one of these values is empty:

| Environment file field | Value | Where to get it |
| --- | --- | --- |
| `identity.tenantId` | Entra tenant ID | Your tenant |
| `identity.portalClientId` | Client ID of the Portal login application | [Istio authentication](../istio/#register-the-portal-login-application) |
| `identity.encryptionClientId` | Client ID of the Broker encryption identity | [Step 2](#2-create-the-encryption-foundation) |
| `identity.keyVaultKeyUrl` | Key Vault key URL | [Step 2](#2-create-the-encryption-foundation) |
| `identity.blueprintClientId` | Client ID of the Agent Identity Blueprint | [Prepare Microsoft Entra Agent Identity](../entra-agent-identity/#create-and-govern-the-blueprint) |
| `identity.authorizationClientId` | Client ID of the Authorization application | [Prepare Microsoft Entra Agent Identity](../entra-agent-identity/#add-microsoft-delegated-access-when-needed) |
| `platform.values.cetirun-operator.azure.clientId` | Client ID for the Operator ServiceAccount | [Prepare Microsoft Entra Agent Identity](../entra-agent-identity/#federate-the-aks-workloads-to-the-blueprint) |

For the Entra resources behind these IDs, refer to
[Prepare Microsoft Entra Agent Identity](../entra-agent-identity/#entra-resources-that-make-deploy-requires).

`make deploy` does not call Azure or Microsoft Graph. It does not find a wrong
ID, a missing federated credential, a missing consent or role assignment, or a
missing key. These problems occur only when a component uses the resource, for
example when a user connects a provider or an Agent requests its identity.

With `make deploy`, do steps 1 to 4 of this guide. Then, instead of steps 5 and
6, put the values above and the names of the step 3 Secrets in the environment
file. `make deploy` sets the Broker, Operator, Portal, and MCP values from
them. The synthetic example environment file uses these names. If you created
other names, use them:

| Secret from step 3 | Environment file field |
| --- | --- |
| `cetirun-credentials/broker-tls` | `broker.values.tls.secretName` |
| `cetirun-system/connections-mcp-tls` | `platform.values.cetirun-connections-mcp.tls.secretName` |
| `cetirun-system/broker-client-tls` | `platform.values.cetirun-connections-mcp.broker.clientTLSSecretName` |
| `cetirun-system/connections-mcp-ca` | `platform.values.cetirun-operator.connectionsMCP.caSecretName` |

`make deploy` also stops if a provider Secret that the environment file names
under `broker.values.providers` does not exist. Remove the entries of the
providers that you do not register in step 4.

## Choose the capabilities

These terms have one meaning on this page:

- Portal login identifies the person who manages an Agent.
- A **Connection** stores the provider authorization of that person. The
  Portal shows it under **Connections**.
- The **Identities** tab of an Agent associates selected Connections with
  that Agent.
- An **Agent Identity** is a separate identity of the Agent in Microsoft Entra
  Agent ID. Independent Azure access and Microsoft delegated access need it.
  GitHub and Google connections do not need it. But they still need
  authenticated Portal ownership and encrypted storage.

| Capability | Preparation beyond the common foundation |
| --- | --- |
| GitHub, selected repositories | [Shared GitHub App registration](../../guides/github-app/#register-the-platform-app), user authorization and installation |
| GitHub, personal account | [GitHub OAuth App](../identity-providers/#github-personal-oauth) |
| Gmail and Google Calendar | [Google Web OAuth client](../identity-providers/#google-personal-oauth) |
| Azure, independent Agent | [Entra Blueprint and lifecycle setup](../entra-agent-identity/), then Azure RBAC for each Agent principal |
| Azure or Microsoft Graph, personal account | Entra lifecycle plus [delegated authorization setup](../entra-agent-identity/), consent and downstream permissions |

The baseline below enables GitHub/Google connections and explicitly disables
both Broker Entra modes. Add the Entra overlays only after you complete their
prerequisites. The Broker chart defaults enable both modes. As a result, if
you omit those flags, you do not get a GitHub/Google-only deployment.

This choice applies to a direct Helm installation. `make deploy` always enables
Microsoft delegated access and the Operator lifecycle, as
[What `make deploy` requires](#what-make-deploy-requires) shows.

An Agent Policy controls whether its Agents can use an Agent Identity. **Disabled**
rejects an Agent that requests one. **Optional** lets an Agent request one without
enabling it on existing Agents. **Required** also applies to existing Agents and
stops an Agent that does not request an identity. Complete the Agent Identity and
Connections platform configuration before selecting Optional or Required.

After an identity-only Agent Policy update is accepted, switching between
Disabled and Optional does not restart Agents that do not request an identity.
Upgrading the Operator also preserves those running Agents when their
Agent Policy is already Optional. Installations with configuration
coordination still reject Agent Policy identity edits while the Agent Policy
has active execution reservations; use their normal stop-and-update workflow.
This behavior does not change the admission of Agent Policy updates or
recover Agents stopped by an earlier denial.

## 1. Prepare access and record the coordinates

Use an authorized Azure CLI session and an explicit Kubernetes context. The
platform administrator must have these permissions:

- Authority to provision the encryption resources and to assign roles for them
- Kubernetes permission to install the charts and Secrets
- Registration and consent authority that is specific to each provider

Entra directory roles and Azure subscription roles are different permissions.
The examples use Azure CLI, kubectl, Helm, Bash, OpenSSL 3, jq and curl.
Install these tools before you start. Keep environment values and credentials
outside the checkout.

The `az ad` commands and the `az rest` requests to Microsoft Graph in these
guides use the tenant of the active subscription of the Azure CLI. The
`--subscription` argument does not change this tenant. Before you run them,
make sure that this command prints the deployment tenant ID:

```bash
az account show --query tenantId --output tsv
```

If your Azure CLI is also signed in to other tenants, sign in to the
deployment tenant with a separate CLI configuration directory. Then the
default subscription of your other sessions does not change:

```bash
export AZURE_CONFIG_DIR=/absolute/private/azure-cli-cetirun
az login --tenant "<tenant-id>"
```

Before you enable the Portal feature, complete these prerequisites:

- AKS OIDC issuer and Workload Identity webhook. Keep the exact issuer URL,
  with its trailing slash. Refer to [AKS Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-deploy-cluster).
- Managed Istio, namespace injection, DNS, an HTTPS Portal that works, and the
  [Portal login app and group admission](../istio/#prepare-identity-dns-and-tls).
  OAuth2 Proxy must forward its verified ID token to the Portal. The Portal
  examines the token signature, tenant, audience and immutable owner identity.
  Email headers alone are not sufficient.
- A supported Hermes or OpenClaw workload and an operational platform, or a
  Custom Sandbox Agent image that implements the [Custom client contract]({{< repo "app/cetirun-connections-mcp/README.md" >}}#governed-custom-workloads).
  For Custom, `custom` must also be in each agent type allowlist below. The
  native common-executor configuration is also necessary for Custom. A change
  to a type list alone grants nothing. For Sandbox Agents, also do
  [Enable Agent connections for Sandbox Agents](#enable-connections-for-sandbox-agents).
- Network access from the Broker to Kubernetes, Entra sign-in, its Key Vault,
  and configured providers. Network access from MCP to Kubernetes, the Broker
  and the fixed provider API origins. DNS, mesh control-plane access and image
  pulls must also work. Examine [Istio egress](../istio/#egress-and-logging)
  and the Broker/MCP NetworkPolicies. A ServiceEntry alone does not grant
  network access.

Provider endpoints include `github.com` and `api.github.com`,
`accounts.google.com`, `oauth2.googleapis.com`, `openidconnect.googleapis.com`,
`gmail.googleapis.com`, `www.googleapis.com`, `login.microsoftonline.com`,
`graph.microsoft.com` and `management.azure.com`, as applicable. Allow the
deployment's Key Vault hostname. Different endpoints are necessary for browser
authorization and for server-side token exchange. Examine both paths.

The examples use these coordinates. If you rename anything, keep them
consistent. During upgrades, use the name of your current platform release.
Replace the ID placeholders with the actual GUIDs from your deployment.

| Resource | Example and configuration consumer |
| --- | --- |
| Portal origin | `https://portal.example.com`. Provider redirects and Broker Portal URLs |
| Tenant ID | `<tenant-id>`. Portal, Broker, optional Operator Entra configuration |
| Portal login application client ID | `<portal-client-id>`. OAuth2 Proxy and Portal `agentIdentity.portalClientId` |
| Broker encryption managed identity client ID | `<encryption-client-id>`. Broker `agentIdentity.encryptionClientId` |
| Encryption managed identity principal/object ID | Azure returns this ID. It is for the Key Vault role assignment, not for the client-ID Helm field |
| Broker namespace / ServiceAccount | `cetirun-credentials` / `cetirun-agent-broker` |
| Platform namespace | `cetirun-system`. Operator, Portal and MCP in the umbrella deployment |
| Operator / Portal / MCP ServiceAccounts | `cetirun-operator-controller-manager` / `cetirun-portal` / `cetirun-connections-mcp` |
| Kubernetes workload audience | `cetirun-connections-mcp`. It must match Broker, MCP and Operator |
| Entra federation audience | `api://AzureADTokenExchange`. It is different from the MCP workload audience |
| Istio revision | `asm-example` is a placeholder for the installed revision |

Prepare private files outside the repository. The commands below create cloud
or cluster resources only where they are explicitly marked. The offline render
is a separate step. Use `umask 077`. Avoid shell tracing. Keep credential
values out of command arguments, Helm values, logs and support attachments.

## 2. Create the encryption foundation

The Broker uses envelope encryption. Connection records are Kubernetes Secrets
that contain ciphertext, and a Key Vault RSA key wraps their data-encryption
keys. This foundation is necessary even when both Entra modes are disabled. A
Blueprint or a Microsoft delegated authorization client is not necessary for
this foundation.

Use a dedicated user-assigned managed identity for encryption. Its federated
credential trusts the Broker ServiceAccount, with the AKS issuer and
`api://AzureADTokenExchange` audience. Only the Key Vault crypto operations
that the Broker uses are necessary for the identity. Subscription-wide
Contributor or Graph application-management permissions are not necessary for
it.

**Connected, mutating setup example.** Replace the inputs. Each command names
the reviewed subscription with `--subscription`. As a result, the example does
not change the default subscription of the Azure CLI. Use an approved resource
group that exists. The example expects the cluster in the same subscription.

The person who creates the key must already have Key Vault data-plane
key-management authority (for example, Key Vault Crypto Officer).
Resource-group Contributor alone is not sufficient. Your network policy must
allow the Broker to reach the vault.

```bash
umask 077
IDENTITY_RESOURCE_GROUP=example-platform-rg
IDENTITY_LOCATION=your-approved-region
IDENTITY_VAULT_NAME=your-globally-unique-vault-name
IDENTITY_SUBSCRIPTION_ID=your-subscription-id
AKS_RESOURCE_GROUP=example-cluster-rg
AKS_CLUSTER_NAME=example-cluster
# Your user object ID, from Microsoft Graph in the tenant of the active subscription:
KEY_PROVISIONER_OBJECT_ID=$(az ad signed-in-user show --query id --output tsv)

AKS_OIDC_ISSUER=$(az aks show --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --resource-group "$AKS_RESOURCE_GROUP" --name "$AKS_CLUSTER_NAME" \
  --query oidcIssuerProfile.issuerUrl --output tsv)
test -n "$AKS_OIDC_ISSUER" && test "$AKS_OIDC_ISSUER" != null

az identity create --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name cetirun-broker-encryption --location "$IDENTITY_LOCATION" --output none
ENCRYPTION_CLIENT_ID=$(az identity show --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name cetirun-broker-encryption --query clientId --output tsv)
ENCRYPTION_PRINCIPAL_ID=$(az identity show --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name cetirun-broker-encryption --query principalId --output tsv)
az identity federated-credential create --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --identity-name cetirun-broker-encryption --name cetirun-broker \
  --issuer "$AKS_OIDC_ISSUER" \
  --subject system:serviceaccount:cetirun-credentials:cetirun-agent-broker \
  --audiences api://AzureADTokenExchange --output none

az keyvault create --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --resource-group "$IDENTITY_RESOURCE_GROUP" \
  --name "$IDENTITY_VAULT_NAME" --location "$IDENTITY_LOCATION" \
  --enable-rbac-authorization true --enable-purge-protection true --output none
IDENTITY_VAULT_ID=$(az keyvault show --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --name "$IDENTITY_VAULT_NAME" --query id --output tsv)
# If the provisioning user does not already have key-management authority:
az role assignment create --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --assignee-object-id "$KEY_PROVISIONER_OBJECT_ID" \
  --assignee-principal-type User --role 'Key Vault Crypto Officer' \
  --scope "$IDENTITY_VAULT_ID" --output none
# Allow the role assignment to propagate before creating the key:
az keyvault key create --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --vault-name "$IDENTITY_VAULT_NAME" \
  --name connection-records --kty RSA --size 3072 \
  --ops wrapKey unwrapKey --output none
az role assignment create --subscription "$IDENTITY_SUBSCRIPTION_ID" \
  --assignee-object-id "$ENCRYPTION_PRINCIPAL_ID" \
  --assignee-principal-type ServicePrincipal \
  --role 'Key Vault Crypto Service Encryption User' \
  --scope "$IDENTITY_VAULT_ID/keys/connection-records" --output none
```

`az ad signed-in-user show` reads your object ID in the tenant of the active
subscription. If that tenant is not the deployment tenant, read the ID from
the Microsoft Graph `/me` resource with a token for the deployment tenant.
This command gives the token to curl through standard input, not as a command
argument:

```bash
az account get-access-token --tenant "<tenant-id>" --resource-type ms-graph \
    --query accessToken --output tsv |
  sed 's/^/Authorization: Bearer /' |
  curl --silent --show-error --fail --header @- \
    'https://graph.microsoft.com/v1.0/me?$select=id' | jq -r .id
```

Allow several minutes for new role assignments to propagate. If key creation
returns an authorization error, examine the signed-in principal that
provisions the resources. Also examine the assignment scope and the vault
network access. After propagation, retry only the key-creation step. Do not
start the deployment until that step succeeds.

The [Key Vault Crypto Service Encryption User role](https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/security#key-vault-crypto-service-encryption-user)
includes key metadata read and wrap/unwrap operations. Scope it to the
intended key.

Configure `https://<vault-name>.vault.azure.net/keys/connection-records` as
`agentIdentity.keyVaultKeyUrl`. This versionless URL lets new records use a
rotated version. Old records keep the exact key version that is necessary for
decryption. While any saved records or backups depend on old versions, keep
these versions enabled and accessible. A Key Vault **secret** URL is not a key
URL.

With `make deploy`, put the same URL in `identity.keyVaultKeyUrl`.
`make deploy` gives this value to the Broker without a change. The Broker also
accepts a versioned key URL,
`https://<vault-name>.vault.azure.net/keys/connection-records/<version>`. With
a versioned URL, the Broker wraps each new record with that version, and it
decrypts only the records of that version.

If you later change the URL to another version, the Broker cannot decrypt the
older records. A change to the versionless URL keeps them readable. For these
reasons, use the versionless URL. The host of the URL must end in
`.vault.azure.net`.

For a vault with private access, provision its network path and DNS before
deployment. The example creates the vault, but it does not implement that
network design. If an approved vault already exists, use that vault instead.
Do not overwrite its access configuration with the creation example.

## 3. Prepare namespaces and TLS

Keep the Broker in its private credential namespace. The platform chart rejects
a Broker installation beside MCP. The Broker can read encrypted records and
provider Secrets. MCP has no Kubernetes Secret API access.

When `connectionsMCP.brokerNamespace` and `brokerServiceAccount` are set, the
Operator creates a Role and a RoleBinding in each Agent namespace. They give
the Broker ServiceAccount `get`, `list`, `create`, `update`, and `delete` on all
Secrets of that namespace, including the AI Gateway key of the Agent. Secret
names and labels do not limit this access. Keep those fields until cleanup is
complete.

Create the namespaces that do not exist. Label both namespaces for the
installed Istio revision. For a namespace that already exists, examine its
injection configuration before you change labels. Do not combine legacy
`istio-injection` selection with a different revision.
**These commands mutate the selected cluster:**

```bash
kubectl create namespace cetirun-credentials
# Run only when the platform namespace does not already exist:
kubectl create namespace cetirun-system
kubectl label namespace cetirun-credentials istio.io/rev=your-installed-revision
kubectl label namespace cetirun-system istio.io/rev=your-installed-revision
```

There are two TLS layers. Istio mTLS identifies workload peers. Application
TLS authenticates the MCP endpoint and its private Broker connection. Both
layers are necessary.

Portal/Operator use Broker HTTP port 8080 inside the authenticated mesh. MCP
uses Broker HTTPS service port 443, which targets container port 8443. Do not
expose Broker or MCP through the public ingress.

Request these PEM certificates from your private PKI. Leaf certificates must
have a valid chain, a private key that matches, and the extended key usage
that the table shows. Include Subject Key Identifier (SKI) on CA certificates
and Authority Key Identifier (AKI) on leaves. Native clients use strict X.509
validation; a chain that passes ordinary `openssl verify` can still fail
without these extensions. The charts do not issue certificates.

| Material | Required identity | Destination |
| --- | --- | --- |
| Broker server certificate | DNS `cetirun-agent-broker.cetirun-credentials.svc.cluster.local`, `serverAuth` | Broker Secret `broker-tls`: `tls.crt`, `tls.key`, `ca.crt` that trusts the MCP client issuer |
| MCP server certificate | DNS `cetirun-connections-mcp.cetirun-system.svc.cluster.local`, `serverAuth` | Platform Secret `connections-mcp-tls`: `tls.crt`, `tls.key` |
| MCP's Broker client certificate | Exactly one URI SAN: `spiffe://cluster.local/ns/cetirun-system/sa/cetirun-connections-mcp`, `clientAuth` | Platform Secret `broker-client-tls`: `tls.crt`, `tls.key`, `ca.crt` that trusts the Broker server issuer |
| MCP server CA bundle | Public CA certificates only, no private key | Operator namespace Secret `connections-mcp-ca`: `ca.crt`, which the Operator distributes to Agent workloads |

For a private environment that manages its own dedicated CA, this
**local file-generation example** supplies all three certificates. If your
organization owns the PKI, use its approved issuance process instead. Keep the
CA private key outside Kubernetes. Protect it independently of leaf keys.
Select a new directory under a private parent directory that exists. This
example refuses to overwrite a directory that exists.

```bash
umask 077
IDENTITY_TLS_DIR=/absolute/private/identity-tls
# Refuse to replace an existing CA or certificate directory.
mkdir -m 700 "$IDENTITY_TLS_DIR" || exit 1
openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 365 \
  -subj '/CN=CetiRun identity example CA' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -addext 'subjectKeyIdentifier=hash' \
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
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\nextendedKeyUsage=%s\nsubjectAltName=%s\n' \
    "$leaf_usage" "$leaf_san" > "$IDENTITY_TLS_DIR/$leaf.ext"
  openssl x509 -req -sha256 -days 90 -in "$IDENTITY_TLS_DIR/$leaf.csr" \
    -CA "$IDENTITY_TLS_DIR/ca.crt" -CAkey "$IDENTITY_TLS_DIR/ca.key" \
    -CAcreateserial -extfile "$IDENTITY_TLS_DIR/$leaf.ext" \
    -out "$IDENTITY_TLS_DIR/$leaf.crt"
  openssl verify -x509_strict -CAfile "$IDENTITY_TLS_DIR/ca.crt" \
    -purpose "$(if [ "$leaf_usage" = clientAuth ]; then echo sslclient; else echo sslserver; fi)" \
    "$IDENTITY_TLS_DIR/$leaf.crt"
done
```

Install the files as Secrets. **Connected, mutating commands** for the
single-CA example above. If you use separate issuers, use the correct CA bundle
in each place:

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

These are creation commands, not a rotation mechanism. If a Secret with the
same name exists, the creation fails. Update that Secret through your reviewed
Secret-management process. Do not copy the CA private key into any Secret or
Agent namespace.

## 4. Register the selected providers

Complete [Register GitHub and Google providers](../identity-providers/) for GitHub App, GitHub
OAuth and Google. For Azure/Microsoft access, also complete
[Prepare Microsoft Entra Agent Identity](../entra-agent-identity/). Store provider Secrets
in `cetirun-credentials`. Keep only their names in Helm values.

There are different browser return paths:

| Purpose | Exact example URL |
| --- | --- |
| Portal sign-in, OAuth2 Proxy | `https://portal.example.com/oauth2/callback` |
| Broker-managed provider authorization, for example Entra delegated sign-in | `https://portal.example.com/api/v1/connections/callback` |
| GitHub App installation setup | `https://portal.example.com/api/v1/connections/github-app/setup` |

The last two paths go through the Portal. They are not public Broker
addresses. Register the actual HTTPS origin and the exact path, without a
trailing slash.

## 5. Assemble values

Keep values outside the repository. Save this **Broker baseline** as
`broker-identity.yaml`. It supplies all necessary identity settings for the
GitHub/Google-only path. Replace the illustrative IDs, vault and Istio
revision. Select reviewed image versions/digests separately. Add only the
provider overlays that you configured in step 4.

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

Save this **platform overlay** as `platform-identity.yaml`. It adds to your
complete, reviewed platform values. It does not replace image/runtime,
registry, AI Gateway, model, admission, storage or administrator
configuration. For example, identity setup alone does not supply a usable
Agent model. It also does not qualify a Sandbox execution profile.

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

For a standalone component release, remove the umbrella key for the
component. Use the contents of that key as the values of the component. If
the Portal runs in `cetirun-portal` instead, adjust the Portal namespace in the
Broker's trust configuration. Match all ServiceAccount names, MCP certificate
SANs and URLs to the rendered resources. Keep the same agent type allowlist in
Portal, Broker and Operator.

## 6. Render, then deploy

From a checkout that contains the selected feature versions, build the local
chart dependencies of the umbrella. The **Offline rendering** below does not
create resources or resolve Secrets. Redirected manifests can include other
platform secrets from your base values. For this reason, store these manifests
privately. Never attach them to a PR.

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

`broker-providers.yaml` is the combined provider overlay from step 4. If no
provider overlay is configured yet, use `{}`. For Entra, append its Broker and
platform overlay files after these baseline files in
**both rendering and deployment**. Helm merges maps in order. Lists are
replaced. When you apply this baseline, an Entra deployment that already
exists must keep its Entra overlays.

When the cluster and credentials are ready, run the
**connected, mutating installation**:

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

For an upgrade, use the current release names. Pass the complete reviewed
values, with your pinned images. Do not create a second platform release. Do
not rely on `--reuse-values` to infer absent trust settings.

Direct Helm does not configure the shared Istio external authorization
provider. First, complete the [Istio installation](../istio/#direct-helm-installation).
For this layout, point its Portal ownership target at `cetirun-system`.

## 7. Verify the environment

Do acceptance checks with disposable resources and accounts. Readiness and a
successful chart render do not prove that provider authorization works.

1. Examine the Broker, MCP, Operator and Portal rollouts. Examine the Istio
   sidecars and the Broker's injected Azure workload token mount. Do not read
   tokens. Broker `/readyz` is a local check. It does not qualify Key Vault or
   GitHub.
2. Sign in to the Portal. Open **Connections** at `/connections`. Make sure
   that only the connections of the signed-in owner appear. It must not be
   possible for another owner to select them.
3. Connect each configured provider. Finish its callback. Successful
   persistence exercises the encryption foundation. A provider error page is
   not a completed connection. For GitHub App, also select the installation.
4. On a ready supported Agent, use **Identities → Add identity**. Explicitly
   confirm access. Examine `connections_status` and `services_list` through the
   native MCP integration. Creation of a Connection alone must not grant an
   Agent access.
5. Run a small allowed request. For GitHub App, make sure that an unselected
   private repository and an ungranted write are rejected. Personal OAuth is
   governed by its broader scopes, not by the App's repository selection
   controls.
6. If Entra is enabled, wait until the Agent Identity is Ready. Assign a narrow
   Azure role to its principal. Then test autonomous Azure separately from
   delegated sign-in. Test the actual Microsoft Graph permission that you
   granted.
7. Remove the Agent binding. Make sure that new requests stop. After consent
   revocation or refresh expiry, make sure that **Sign in again** works. Local
   connection removal does not revoke provider-wide consent. It also does not
   uninstall a GitHub App.

Live authorization is necessary for these checks. They are not part of
offline development tests. Local revocation does not recall provider requests
that were already sent, or tokens that were issued previously.

## Enable Agent connections for Sandbox Agents {#enable-connections-for-sandbox-agents}

The Agents of a policy-bound Team run in a Sandbox. The operator gives such an
Agent its Connections configuration only after the steps below. The agent type
of the Agent must also be in the agent type allowlists of the operator, the
Broker, and the Portal. A Sandbox Agent with a
[single-start lifetime](../../concepts/glossary/#single-start-lifetime) does
not get this configuration. Without this configuration, the Agent cannot use
Connections. The Portal can still show **Identities** for the Agent.

Do these steps after you
[enable Sandbox execution](../policy-teams/#enable-sandbox-execution), and
before you create Sandbox Agents. A later change to the platform registration
can affect the Sandbox Agents that exist. Refer to the operator README's
[coordinated configuration updates]({{< repo "app/cetirun-operator/README.md" >}}#coordinated-configuration-updates).

1. Add these values to the `platform.values` of the environment file. Keep the
   values that are already there:

   ```yaml
   platform:
     values:
       cetirun-operator:
         connectionsMCP:
           nativeEnabled: true
       cetirun-connections-mcp:
         nativeSandbox:
           enabled: true
           operatorNamespace: cetirun-system
           peerUID: connections-mcp
   ```

   `peerUID` is a name that you select and keep stable. The chart puts it on
   the MCP Pods and in the selector of the MCP Service, as the label
   `sandbox.cetirun.microsoft.com/peer-uid`. `operatorNamespace` is the
   namespace of the operator.
2. Run `make deploy`. A later run stops if it changes the peer identity or the
   operator namespace of the MCP.
3. Read the cluster IP of the MCP Service:

   ```bash
   kubectl get svc cetirun-connections-mcp -n cetirun-system -o jsonpath='{.spec.clusterIPs}'
   ```

4. In `platform.json`, add the Service to `topology.peers`. Use the same
   `uid` as `peerUID`, and list every cluster IP of the Service. Also change
   `topology.revision`:

   ```json
   "peers": [
     {"name": "cetirun-connections-mcp", "namespace": "cetirun-system",
      "uid": "connections-mcp", "ips": ["<MCP_SERVICE_IP>"]}
   ]
   ```

5. Update the `sandbox-platform` ConfigMap with the changed `platform.json`.
   Refer to [The platform registration](../policy-teams/#the-platform-registration).

To verify, create a Sandbox Agent of a supported agent type. In **Identities**,
add an identity. Then examine `connections_status` in the Agent. For the
complete contract, refer to the Connections MCP README's
[governed native workloads]({{< repo "app/cetirun-connections-mcp/README.md" >}}#governed-native-workloads).

## Troubleshooting

| Symptom | Check next |
| --- | --- |
| Connections absent or owner verification fails | Portal `agentToolsEnabled`, correct login app client ID/tenant, forwarded verified ID token, HTTPS origin and supported agent type |
| OAuth redirect mismatch | Exact provider callback, public origin and the distinction from `/oauth2/callback` |
| Broker cannot persist or decrypt | Encryption identity client ID, exact issuer/subject/audience, Key Vault key URL, wrap/unwrap role propagation, DNS/firewall and old key versions that are kept |
| Portal or Operator gets Broker 403 | Actual namespace/ServiceAccount against Broker `portal`/`operator`, sidecar injection and Istio policy |
| MCP cannot reach Broker | HTTPS service port 443, DNS server SAN, CA direction, client `clientAuth` and exact SPIFFE URI, Broker `toolService` identity |
| MCP rejects an Agent | Audience alignment, live Pod/ServiceAccount/Agent binding, supported agent type and current Agent state. Do not use a manually copied token as a substitute |
| Agent has no MCP connection | Operator URL/CA source, agent type allowlists, workload configuration and Agent namespace Broker permissions |
| Sandbox Agent has no Connections | `connectionsMCP.nativeEnabled`, the MCP `nativeSandbox` values, the MCP peer in `topology.peers`, and `sandboxAdmission.podsEnabled`. Refer to [Enable Agent connections for Sandbox Agents](#enable-connections-for-sandbox-agents) |
| Agent Identity is `Ready`, but a Sandbox Agent cannot use it | The `CredentialPolicyReady` condition of the Agent. `PolicyApplied` alone does not prove this access: the operator must also read back the current credential policy from the Broker, for the owner and the identity. For Sandbox Agents, the Broker also verifies the current Pod and the protected Sandbox sources |
| Entra identity or delegated exchange fails | Follow [Troubleshoot Agent Identity](../entra-agent-identity/#troubleshoot-agent-identity). Key Vault encryption authority does not grant Blueprint lifecycle authority |
| Provider returns 401/403 | Current installation, owner/Admin authority, scopes, consent, resource RBAC, organization policy and token expiry |

Use correlation IDs and sanitized status/errors. Do not collect Secret bodies,
token files, full Helm values or browser cookies for a public diagnostic bundle.

## Records and audit

Personal Connections belong to their Portal owner. The Broker keeps them
encrypted in its credential namespace. The namespace of each Agent stores
these records:

- the bindings of the Agent to Connections
- the independent Azure Connections of the Agent
- the references to personal Connections, without credentials

These records have immutable Agent owner references. The deletion of an Agent
removes them. The personal Connections of the owner stay.

Each credential issue examines the live Agent, the owner, the enabled binding,
and the service. After the Broker gets the credential, it examines the
revisions again. A request that was already sent cannot be recalled. There is
no migration or compatibility workflow for the earlier AgentGrant model.

The MCP has no Kubernetes Secret permissions and no operation-record store.
Labels do not give Kubernetes RBAC isolation.

The platform audit records these items for each request:

- the Agent UID, and the stable account ID or principal that runs the request
- the revisions of the Connection and the binding
- the service, the HTTP method, and the validated target path
- the duration, the stage, and the outcome
- the provider status and request IDs, when they are available

The same MCP correlation ID goes to the Broker and to the provider request.
The audit does not include queries, headers, credentials, or request and
response bodies. Paths can identify resources or mailboxes. For this reason,
restrict the access to the audit. The Broker also records identity, OAuth,
binding, and account-management events, with the verified owner and target
IDs.

These logs are not an execution ledger. If a request has no completion, or if
its result is `execution_unknown`, examine the provider state before you try
again.

## Broker and MCP operation

With a direct Helm installation, you can disable Microsoft delegated access
(`identityModes.microsoftDelegated.enabled: false`). Then the Broker chart does
not include the Entra Auth SDK container. Refer to
[Choose the capabilities](#choose-the-capabilities).

- The readiness of the Broker does not call the SDK. SDK failures do not block
  other providers. When the SDK is enabled, it runs in the same Pod as the
  Broker.
- If you disable a mode, CetiRun blocks its use but keeps the saved
  connections. Key Vault encryption is still necessary for personal
  credentials.
- Management inventory scans read pages of Agents. If a scan is not complete,
  it fails and returns no partial result.
- At startup and every five minutes, the Broker removes OAuth transactions
  that expired more than one hour before. It skips corrupt records. It does
  not remove Connections, account indexes, or binding tombstones.
- The Broker and the MCP read the mounted TLS certificates, keys, and trust
  bundles again every five seconds. If a replacement is not valid, they keep
  the last valid material. With an expired leaf certificate, new handshakes
  and outbound requests fail. Requests that already started can finish.

The MCP and the Broker authenticate a workload with three checks. They do not
use a list of all Agents instead:

- the live Pod and ServiceAccount
- the Agent owner reference of the namespace
- a read of the named Agent with its UID

Local tests use synthetic providers. Each deployment must still qualify its
identity, OAuth configuration, and native runtime on its own cluster. For the
component settings, refer to the
[Broker README]({{< repo "app/cetirun-agent-broker/README.md" >}}) and the
[Connections MCP README]({{< repo "app/cetirun-connections-mcp/README.md" >}}).

## Rotation, shutdown and recovery

- Update provider client-secret files and their Secrets. Then restart the
  Broker to reload environment variables. The GitHub App signing key is
  file-backed. Allow time for the Secret projection delay. Before you revoke
  the old key, make sure that the replacement works. Refer to the
  [provider guide](../identity-providers/).
- Mount TLS Secrets as whole volumes. The Broker and the MCP then reload
  them, as [Broker and MCP operation](#broker-and-mcp-operation) describes.
- Make a backup of encrypted records together with a recovery plan for their
  Key Vault key versions and identity permissions. Before you return a
  recovered environment to service, use the [restore procedure below](#restore-with-fresh-owner-authorization).
  Encryption does not prove that a backup reflects current owner consent. It
  also does not prevent the use of credentials by an administrator who has
  decryption authority.
- To retire Entra, follow
  [Verify, operate, and remove](../entra-agent-identity/#verify-operate-and-remove).
  Until the cleanup is complete, also keep the Operator's Broker
  namespace/ServiceAccount settings.
- Remove Agent grants before you remove unused Connections. When applicable,
  revoke provider consent or uninstall the App separately. Remove deployments,
  federation, certificates and encryption authority only after you handle the
  dependent records, Agents and backups that you keep. When you troubleshoot,
  do not delete the credential namespace or Key Vault as a first step.

To replace a CA, do these steps in this order:

1. Add the new CA to the trust of every peer, next to the old CA.
2. Propagate the combined MCP CA bundle to existing Agent workloads before
   replacing the server leaf. Updating the source Secret alone does not
   enqueue every Agent. Follow
   [Propagate a new MCP CA to existing Agents](../certificates/#propagate-a-new-mcp-ca-to-existing-agents).
3. Rotate the leaf certificates and verify that new connections work.
4. Remove the old CA and repeat the client trust propagation and verification.

Allow time for the Secret projection and the reload delays. A change of trust
does not close connections that exist.

### Restore with fresh owner authorization

Do not include saved authorization records in routine restores. Make fresh
owner authorization necessary. Encryption and revision checks do not protect
against a restore of older authorization state, for example superseded grants
and revocation tombstones. A successful decryption or readiness check is not
approval to resume access.

If a whole-cluster or namespace restore includes those records, use this order:

1. **Keep execution closed before you restore.** Block Agent-to-MCP requests
   and Broker credential issuance. This includes internal cluster traffic.
   Quiesce authorization writes and in-flight requests. Make sure that
   restored workloads and controllers cannot reopen these paths during
   recovery. If you close only the public Portal ingress, native MCP access
   does not stop.
2. **Inventory authorization records in both storage locations.** Include the
   Broker credential namespace, every Agent workload namespace, and any older
   bindings that are stored centrally. Broker-managed Secret metadata uses
   `app.kubernetes.io/managed-by=cetirun-agent-broker` and
   `agenttools.cetirun.microsoft.com/record-type`. The record types to exclude
   or clear are `connection`, `binding`, `grant` (legacy), `authorization`
   (OAuth transactions), `account-index`, and `connection-reference`. Examine
   the metadata, but do not export Secret bodies. These records are different
   from provider client configuration, TLS Secrets, Key Vault keys and Agent
   lifecycle metadata.

   The Broker namespace also has `credential-policy` records. They hold no
   credentials. The Broker uses such a record only when it matches the current
   Agent and its Agent Policy. Without a matching record, the Broker refuses
   the Agent Identity requests of that Sandbox Agent.
3. **Clear restored authorization state before you enable management writes.**
   Remove the restored records that you identified above from both storage
   locations. Do not resume old OAuth transactions or restore their
   indexes/references later. When you recover into an environment that
   exists, keep its newer revocation tombstones and never replace them with
   older snapshots. This is a recovery reset, not routine tombstone garbage
   collection.

   Keep the infrastructure credentials and lifecycle metadata that are
   necessary for current Entra identity reconciliation and cleanup. Do not
   delete whole namespaces.
4. **Rebuild access through current owners.** Restore management access, but
   keep general Agent execution blocked. Owners recreate the necessary
   Connections and sign in again where necessary. Owners also select GitHub
   App installations and explicitly grant each Agent its services,
   repositories and permissions. Before you grant access, examine the current
   Agent UIDs, owners and any Entra identity state. An old binding or a
   restored provider credential is not a substitute for this owner action.
5. **Validate before you reopen traffic.** Through isolated recovery checks,
   make sure that Agents without a new grant cannot use the services or get
   credentials. This also applies to Agents whose UID survived the restore.
   Make sure that a newly granted Agent has its allowed access and that
   **Remove access** blocks new requests. Reopen general execution only after
   the record inventory and these checks pass. Handle provider-side revocation
   separately, because this reset does not recall previously sent provider
   requests or issued tokens.

Configuration sources: [Broker values]({{< repo "app/cetirun-agent-broker/chart/values.yaml" >}}),
[MCP values]({{< repo "app/cetirun-connections-mcp/chart/values.yaml" >}}),
[Operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}),
[Portal values]({{< repo "app/cetirun-portal/chart/values.yaml" >}}),
and [platform values]({{< repo "app/cetirun-platform/values.yaml" >}}).
