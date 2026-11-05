---
title: "Istio authentication and egress"
description: "Operate the shared gateway, OAuth2 Proxy, ownership checks, and egress policy."
weight: 30
---

The [infra/istio chart]({{< repo "infra/istio" "tree" >}}) is installed one time
for each deployment. It supplies OAuth2 Proxy, an Istio Gateway and
VirtualService, AuthorizationPolicy, redirect and ownership filters, security
headers, and mesh-wide egress resources.

## Prepare identity, DNS, and TLS

1. Enable AKS managed Istio and its external ingress gateway. The
   [cluster template](../infrastructure/) enables both.
2. Register a single-tenant Entra **Web** application for Portal sign-in, with
   the OAuth2 callback `https://<domain>/oauth2/callback` (for example,
   `https://example.com/oauth2/callback`). OAuth2 Proxy authenticates as this
   application through Workload Identity, so the application has no client
   secret. If Agent Identity is enabled, add the `login_hint` optional claim to
   the ID tokens of this app. `make deploy` always enables Agent Identity.
   Entra defines this opaque claim as the reliable value to carry SSO into
   another app. This includes B2B guest and cross-tenant scenarios.
3. Configure application-assigned group claims (`ApplicationGroup`). Create the
   service principal of the application, and assign each allowed group to it.
   With `ApplicationGroup`, the ID token contains only the groups that are
   assigned to the application. Without an assignment, OAuth2 Proxy refuses
   every sign-in. Use the object IDs of these groups as the allowed group IDs.
4. Add a federated credential to the Portal login application. Its issuer is
   the cluster’s OIDC issuer URL, with the slash at the end. Its subject is
   `system:serviceaccount:cetirun-istio:oauth2-proxy`, and its audience is
   `api://AzureADTokenExchange`.
5. Configure the apex to refer to the ingress address. Add wildcard DNS only
   for subdomain services that you configure. The portal, instance UI, and
   OAuth routes use the apex. For one exact hostname, use
   [Public DNS and TLS](../public-ingress/) and configure `publicTLS` to
   prepare Azure Public IP DNS or verify a user-managed record during
   deployment.
6. Supply a certificate that covers the actual names. These names include the
   apex. Optional `publicTLS` in the normal platform deployment can issue and
   renew the external Gateway certificate with HTTP-01; existing certificate
   workflows remain supported.

The [OAuth2 Proxy deployment]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}})
sets the apex callback. Its
[VirtualService]({{< repo "infra/istio/templates/ingress/oauth2-proxy-virtualservice.yaml" >}})
routes apex `/oauth2` requests. An `oauth2.<domain>` exemption in the values
does **not** create a callback route that works on that host.

The [portal's Sign out action]({{< repo "app/cetirun-portal/web/src/components/Header.tsx" >}})
uses the apex `/oauth2/sign_out` route. It returns the user to the page from
which they signed out.

The chart does not issue or renew certificates. The helper can read supplied
PEM files, decode PFX content, read a PFX file, or download a certificate from
Key Vault, in that order. The helper parses PFX content with an empty password.
The helper has no input for a PFX password. Issuance and renewal are still
operator responsibilities.

The [Gateway template]({{< repo "infra/istio/templates/ingress/gateway.yaml" >}})
always declares HTTPS on port 443 with `credentialName: cetirun-tls`. By
default, `tls.enabled` is false. This value prevents the creation of the TLS
Secret. It does **not** prevent HTTPS. Supply `cetirun-tls` in the gateway
namespace out of band, or set `tls.enabled: true` with the certificate and key.
A render without that Secret is not a usable TLS deployment.

`make deploy` refuses the certificate and key in values. With `make deploy`,
supply the Secret out of band.

### Set the example inputs

The examples below do steps 2 to 6 with the Azure CLI, Microsoft Graph,
kubectl, and OpenSSL. They also use jq and Python 3. These **connected,
mutating** commands change Entra, Azure, and the cluster. Replace the example
values first. Run the examples in one Bash session, because later commands use
the variables of earlier ones.

The `az ad` commands and the `az rest` requests to Microsoft Graph use the
tenant of the active subscription of the Azure CLI. Before you run them, make
sure that this tenant is the deployment tenant. Refer to
[Agent Identity setup](../agent-identity/#1-prepare-access-and-record-the-coordinates).

```bash
SUBSCRIPTION_ID=your-subscription-id
AKS_RESOURCE_GROUP=example-cluster-rg
AKS_CLUSTER_NAME=example-cluster
ALLOWED_GROUP_ID="<allowed-group-object-id>"
AKS_OIDC_ISSUER=$(az aks show --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$AKS_RESOURCE_GROUP" --name "$AKS_CLUSTER_NAME" \
  --query oidcIssuerProfile.issuerUrl --output tsv)
```

### Name the ingress address

AKS creates a public IP address for the external ingress gateway, in the node
resource group of the cluster. Do this step first, because the application and
the certificate use the domain. Read the address:

```bash
INGRESS_IP=$(kubectl get service aks-istio-ingressgateway-external \
  --namespace aks-istio-ingress \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
```

For your own domain, create a DNS `A` record for the apex that points to this
address. Then set the domain:

```bash
DOMAIN=example.com
```

For a test environment without a domain, give the public IP address a DNS
label instead. The label must be unique in the region. The domain is then
`<label>.<region>.cloudapp.azure.com`:

```bash
NODE_RESOURCE_GROUP=$(az aks show --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$AKS_RESOURCE_GROUP" --name "$AKS_CLUSTER_NAME" \
  --query nodeResourceGroup --output tsv)
INGRESS_IP_NAME=$(az network public-ip list --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --query "[?ipAddress=='$INGRESS_IP'].name | [0]" --output tsv)
DOMAIN=$(az network public-ip update --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$NODE_RESOURCE_GROUP" --name "$INGRESS_IP_NAME" \
  --dns-name example-cetirun --query dnsSettings.fqdn --output tsv)
```

There are no DNS names below a `cloudapp.azure.com` label. Use only the apex
routes with it. If AKS replaces the public IP address, set the label again.

### Register the Portal login application

Create the application of steps 2 and 3, with the `login_hint` claim:

```bash
cat > portal-login-app.json <<EOF
{
  "displayName": "cetirun-portal-login",
  "signInAudience": "AzureADMyOrg",
  "web": {"redirectUris": ["https://$DOMAIN/oauth2/callback"]},
  "groupMembershipClaims": "ApplicationGroup",
  "optionalClaims": {"idToken": [{"name": "login_hint", "essential": false}]}
}
EOF
PORTAL_APP=$(az rest --method post \
  --url https://graph.microsoft.com/v1.0/applications \
  --body @portal-login-app.json)
PORTAL_APP_OBJECT_ID=$(jq -r .id <<< "$PORTAL_APP")
PORTAL_CLIENT_ID=$(jq -r .appId <<< "$PORTAL_APP")
```

`PORTAL_CLIENT_ID` is the client ID of the application. It is the `clientId`
of the Istio release, and `identity.portalClientId` of `make deploy`. Create
the service principal of the application. Then assign the allowed group with
the default access role. The ID of this role contains only zeros:

```bash
PORTAL_SP_ID=$(az ad sp create --id "$PORTAL_CLIENT_ID" --query id --output tsv)
DEFAULT_ACCESS_ROLE_ID=$(python3 -c 'import uuid; print(uuid.UUID(int=0))')
az rest --method post \
  --url "https://graph.microsoft.com/v1.0/servicePrincipals/$PORTAL_SP_ID/appRoleAssignedTo" \
  --body "$(jq -n --arg group "$ALLOWED_GROUP_ID" --arg sp "$PORTAL_SP_ID" \
    --arg role "$DEFAULT_ACCESS_ROLE_ID" \
    '{principalId: $group, resourceId: $sp, appRoleId: $role}')" --output none
```

Do the assignment for each allowed group.

OAuth2 Proxy requests the OIDC scopes `openid`, `email`, and `profile`. In the
Redis session mode, it also requests `offline_access`. Add the Microsoft Graph
delegated permissions `openid`, `profile`, `email`, and `User.Read` to the
application. OAuth2 Proxy uses `User.Read` to read the groups of a user from
Microsoft Graph when the ID token cannot contain all of them:

```bash
GRAPH_APP_ID=$(az ad sp list --filter "displayName eq 'Microsoft Graph'" \
  --query '[0].appId' --output tsv)
for scope in openid profile email User.Read; do
  scope_id=$(az ad sp show --id "$GRAPH_APP_ID" \
    --query "oauth2PermissionScopes[?value=='$scope'].id | [0]" --output tsv)
  az ad app permission add --id "$PORTAL_CLIENT_ID" --api "$GRAPH_APP_ID" \
    --api-permissions "$scope_id=Scope"
done
```

Then decide how users consent to these permissions:

- If the user consent settings of the tenant allow it, each user consents at
  the first sign-in.
- An administrator consents one time for all users. In the Microsoft Entra
  admin center, open **API permissions** of the application and select
  **Grant admin consent**. You can also run
  `az ad app permission admin-consent --id "$PORTAL_CLIENT_ID"`. A role that
  can grant tenant-wide consent is necessary, for example Cloud Application
  Administrator.

Add the federated credential of step 4. If the Istio release uses another
namespace, use that namespace in the subject:

```bash
az rest --method post \
  --url "https://graph.microsoft.com/v1.0/applications/$PORTAL_APP_OBJECT_ID/federatedIdentityCredentials" \
  --body "$(jq -n --arg issuer "$AKS_OIDC_ISSUER" '{name: "oauth2-proxy",
    issuer: $issuer, subject: "system:serviceaccount:cetirun-istio:oauth2-proxy",
    audiences: ["api://AzureADTokenExchange"]}')" --output none
```

### Supply the Gateway certificate

The Gateway reads its certificate from the `cetirun-tls` Secret in the gateway
namespace, `aks-istio-ingress` by default. The Secret has the keys `tls.crt`
and `tls.key`.

For a single exact public hostname on AKS managed Istio, prefer
[Public DNS and TLS](../public-ingress/). Set `publicTLS.enabled: true` in the
normal environment file. The platform deployment creates a separate port-80
Gateway API listener for cert-manager HTTP-01 while this chart keeps the
port-443 Gateway. It sets this chart's `tls.enabled: false` and platform
`istio.tlsMode: external`; cert-manager then owns `cetirun-tls`. This mode does
not support wildcard certificates, DNS-01, certificate ownership migration, or
an arbitrary proxy/CDN in front of the ingress.

The CetiRun Gateway listens only on HTTPS port 443. As a result, an ACME
HTTP-01 challenge through this Gateway fails. For a domain whose DNS you
control outside the `publicTLS` path, use DNS-01 validation, or a certificate
from the CA of your organization.

If you do not use the `publicTLS` path, provide a certificate yourself.
This example creates a self-signed certificate; browsers show a warning until
they trust its CA:

```bash
umask 077
INGRESS_TLS_DIR=/absolute/private/ingress-tls
# Refuse to replace an existing directory.
mkdir -m 700 "$INGRESS_TLS_DIR" || exit 1
openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 90 \
  -subj "/CN=$DOMAIN" -addext "subjectAltName=DNS:$DOMAIN" \
  -keyout "$INGRESS_TLS_DIR/tls.key" -out "$INGRESS_TLS_DIR/tls.crt"
```

If a CA issued the certificate, put its intermediate CA certificates after the
leaf certificate in `tls.crt`. Then create the Secret:

```bash
kubectl create secret tls cetirun-tls --namespace aks-istio-ingress \
  --cert="$INGRESS_TLS_DIR/tls.crt" --key="$INGRESS_TLS_DIR/tls.key"
```

Before it writes, `make deploy` makes sure that the certificate is valid now
and that it covers `domain`. With `publicTLS`, it issues that certificate before
the normal platform preflight. It does not examine if browsers trust the
certificate.

## Deploy helper inputs

To run [infra/istio/deploy.sh]({{< repo "infra/istio/deploy.sh" >}}), you must
have Bash, Helm, kubectl, jq, OpenSSL, Python 3, and the pinned PyYAML
dependency. Before deployment, install the Python dependency:

```bash
python3 -m pip install --requirement infra/istio/requirements.txt
```

For its normal path, the helper also must have these inputs:

| Variable | Purpose |
| --- | --- |
| `AZURE_SUBSCRIPTION_ID` | Subscription used for cluster validation |
| `CLIENT_ID`, `TENANT_ID` | OAuth2 Proxy app identity |
| `ALLOWED_GROUPS` | Comma-separated allowed group IDs |
| `RESOURCE_GROUP`, `AKS_CLUSTER_NAME` | Target cluster |
| `DOMAIN` | Deployment domain |
| `KEY_VAULT_NAME` | Necessary for the helper, even with supplied certificate files |

Optional inputs include `KEY_VAULT_CERT_NAME` (default `sslcert`),
`TLS_CERT_PATH` plus `TLS_KEY_PATH`, `PFX_BASE64`, `PFX_PATH`, `COOKIE_SECRET`,
`ADMIN_ALIASES`, and `RELEASE_NAME` (default `cetirun-istio`).
For the normal path, you also must have Azure CLI and an already-authorized
Azure session. To download a certificate, Key Vault secret access is necessary.
Permission to list certificates is not enough.

After you set the necessary variables, deploy the shared Istio resources:

```bash
bash infra/istio/deploy.sh
```

The helper selects the Azure subscription and examines the current context. It
installs or upgrades the Helm resources and waits for OAuth2 Proxy. Then it
patches the AKS Istio shared ConfigMap with the external authorization
provider. Context validation only tests if the context name contains
`AKS_CLUSTER_NAME`. Independently, make sure that the actual target is correct.

AKS managed Istio reads the mesh settings that you add from the shared
ConfigMap `istio-shared-configmap-<revision>` in `aks-istio-system`. On a new
cluster, this ConfigMap does not exist yet. A listing of the namespace shows
`istio-<revision>` instead. AKS manages that ConfigMap, so do not edit it. The
helper creates the shared ConfigMap when it does not exist. `make deploy` with
`istio.mode: manage` also creates it, with the name in `istio.meshConfigMap`.

`SKIP_AZ_VALIDATION` skips the Azure validation and the context validation, and
the subscription ID is then not necessary. The helper still must have the other
necessary inputs and the supplied certificate material. `SKIP_AZ_VALIDATION` is
not a dry-run flag.

The patch selects the revision on the first Istiod pod that matches. It parses
the mesh configuration as YAML. There must be at most one provider named exactly
`oauth2-proxy`. The patch adds this provider if it does not exist, or it adds
`authorization` to the upstream header list of that provider. The update uses
the ConfigMap `resourceVersion` as a compare-and-swap precondition. After a
conflict, it retries against the latest mesh configuration.

During Istio upgrades, examine every active revision. Revision discovery still
selects the first Istiod pod that matches.

## Direct Helm installation

**Offline render**, with only synthetic identity values:

```bash
helm template cetirun-istio infra/istio --namespace cetirun-istio \
  --set-string clientId=example-client --set-string tenantId=example-tenant \
  --set-string allowedGroups=example-group --set-string domain=example.com \
  > /dev/null
```

This command does not validate identity, TLS, Secrets that already exist, or
the managed mesh configuration. Do not apply this rendered example.

**Connected, mutating:** platform operators can install the local chart with
a separately prepared private values file:

```bash
helm upgrade --install cetirun-istio infra/istio \
  --namespace cetirun-istio --create-namespace \
  -f /absolute/path/reviewed-istio-values.yaml
```

This path must have **separate external authorization provider configuration**
in the AKS shared ConfigMap. A Helm install alone does not configure
`ext_authz`. The chart reuses a cookie Secret that already exists through a
lookup, or it generates one at the first installation. Offline `helm template`
cannot find that Secret and can generate a different cookie key. Do not use an
offline rendered Secret to update a live release.

### OAuth session storage

The default `oauth2Proxy.sessionStore.type: redis` mode stores encrypted login
sessions in a dedicated Valkey. The browser keeps a short session ticket.
OAuth2 Proxy requests `offline_access` and tries to refresh tokens on the first
HTTP request after the 30-minute refresh interval. The cookie lifetime is eight
hours and successful refreshes renew it. These settings are not an absolute
eight-hour login limit and do not override the identity provider's sign-in or
revocation policies. Entra must permit refresh tokens for the application.

The bundled `oauth2-proxy-sessions` Deployment has one replica, password
authentication, and a NetworkPolicy that accepts connections only from
OAuth2 Proxy Pods in the same namespace. It does not share the AI Gateway's
cache. Use the namespace's Istio injection and mesh encryption for transport
protection. The chart does not create a public Redis endpoint.

By default, `oauth2Proxy.sessionStore.redis.bundled.persistence.enabled` is
`true`. Valkey writes an append-only file to a 1 GiB PVC with the cluster's
default StorageClass. Configure `persistence.storageClass` and
`persistence.size` for the environment. The Deployment uses `Recreate` so that
two Pods do not write the same volume. The chart retains the PVC and generated
password Secret on uninstall. Remove retained objects only when their sessions
are no longer needed. Persistence reduces session loss after a restart. A single
replica is not highly available, and AOF flushes once per second.

Set `persistence.enabled: false` to use temporary storage. A Valkey restart then
requires users to sign in again. A storage outage prevents session lookup and
can also require a new sign-in after recovery. Keep backups and storage access
under the same controls as other credential-bearing data.

An explicit external `connectionURL` takes precedence over the bundled server.
The chart then creates no session Valkey, PVC, or password Secret:

```yaml
oauth2Proxy:
  sessionStore:
    type: redis
    redis:
      connectionURL: redis://oauth-session.example.svc.cluster.local:6379/0
      existingSecret: oauth-session-credentials
      passwordKey: redis-password
```

For an external service, configure availability, persistence, network access,
and encryption separately. Use `rediss://` for a TLS Redis endpoint. Keep
credentials out of `connectionURL` and Helm values. OAuth2 Proxy reads the
password from the referenced Secret. To use a pre-created password with the
bundled server, set `existingSecret` without setting `connectionURL`. This also
keeps offline GitOps renders from generating a different password each time.

After you rotate the referenced Secret for the Redis password, reconcile the
Istio Helm release. The chart hashes the live Secret data into both Pod
templates. Helm reconciliation restarts OAuth2 Proxy and the bundled Valkey
with the new password. Plan for sign-in interruption during this rotation.
A Secret update alone does not restart existing Pods.

When upgrading a cookie-mode installation, explicitly set
`oauth2Proxy.sessionStore.type: redis`. An existing values file can retain
`type: cookie` despite the new default. Keep the existing cookie Secret and
Entra configuration. Users must sign in again after the storage change.
Reconcile the mesh with `scripts/ensure-oauth2-proxy-id-token-header.py` through
the deployment entry point. The external authorization provider must include
`headersToDownstreamOnAllow: [set-cookie]` so that refreshed cookies reach the
browser. A Helm upgrade alone does not change that mesh setting.

Explicit `type: cookie` remains available. It does not request `offline_access`
and expires after 45 minutes without refresh. This avoids large refresh-token
cookies that can exceed common 8 KiB request header limits. Increasing that
cookie lifetime alone does not refresh the ID token sent to the Portal.

### Expired sessions

After a session expires, the next page load signs the user in again. OAuth2
Proxy keeps a separate CSRF cookie for each sign-in that is in progress. As a
result, sign-ins that start at the same time do not invalidate each other.
These sign-ins can come from a page reload and background requests, or from
several tabs. When a sign-in starts, OAuth2 Proxy deletes the oldest of these
cookies beyond `oauth2Proxy.cookie.csrfPerRequestLimit` (default 20).

Requests below `/api/`, `/ui/<instance>/api/`, `/ui/<instance>/assets/`, and
`/<instance>/cetirun-auth/`
answer `401` instead of a redirect to Entra. This is because a browser cannot
follow that redirect from a script. Also, an Agent UI can request dozens of
assets at one time. Otherwise, each of these asset requests starts a sign-in.
The Portal then asks the user to refresh the page.

## Authentication and ownership

```text
Browser → gateway → redirect header → OAuth2 Proxy / Entra login
        → ownership filter → portal or instance VirtualService
```

By default, OAuth2 Proxy uses Entra's `preferred_username` as its email claim.
It is not certain that local tenant users receive an `email` claim. But the
tenant-facing UPN stays available for Portal identity headers. Set
`oauth2Proxy.emailClaim: email` only when you know that every allowed user in
the tenant receives that claim. Without Agent Identity and Tools, Portal ownership
and administrator checks use the alias derived from these trusted identity
headers, and the ingress ownership endpoint accepts the owner's alias or email.
Providers must assign the claims, keep their local parts unique among allowed
users, and prevent users from changing them. For custom issuers and the supported
OIDC configuration, see [cloud and OIDC parameters]({{< repo "infra/istio/README.md" >}}#cloud-and-oidc-parameters).

Agent Identity and Tools require additional Entra ID-token verification and use
verified tenant and object IDs for Agent authorization. Generic OIDC does not
enable that verification.

When the Portal ID token includes Entra's optional `login_hint` claim, the
Portal passes that opaque value to the separate Agent authorization client.
It uses `preferred_username` instead only for app registrations where the
optional claim is not enabled. The authorization request does not force an
account picker, so Entra can reuse the hinted Portal session. The hint selects
a sign-in session, and it is never an authorization key. The callback must
still match the verified tenant and object IDs of the instance owner. As a
result, if you enable the claim, this does not relax owner isolation.

The Agent authorization client requests the `access_agent` scope of the
Blueprint and the OIDC scopes `openid`, `profile`, and `offline_access`. Entra's
`profile` scope supplies the immutable object ID that the callback owner check
uses. `openid` alone does not guarantee that claim. Do not replace the
object-ID check with a username or email fallback. For the consent to these
scopes, refer to
[Entra Agent Identity setup](../entra-agent-identity/#add-microsoft-delegated-access-when-needed).

For authenticated Portal routes, OAuth2 Proxy returns its validated ID token in
the internal `Authorization` header. The gateway filter first removes any
client-supplied `X-CetiRun-Portal-ID-Token`. Then it moves the validated token
to that header only for the Portal, and it removes `Authorization`. Agent
routes receive neither the Portal ID token nor its authorization header. Before
the Portal uses the token for Agent authorization ownership, it verifies the
token signature, issuer, audience, tenant, object ID, and timestamps.

Native Agent UIs, such as the OpenClaw Control UI, authenticate some of their
own HTTP requests with an `Authorization` header that ext-auth replaces. Before
ext-auth, the security headers filter records the value from the browser in
the request's dynamic metadata. This metadata never leaves the gateway. After
the ownership check, the ownership filter restores the value on owned
`/ui/<instance>/` routes, unless OAuth2 Proxy accepted that same credential as
the user's identity. The native Agent API routes below continue to accept only
their dedicated header. On other owned Agent routes, such as the Terminal and
Desktop, the filter removes `Authorization` unless one of the route-bound
credentials below applies.

The deployment helper also reconciles `authorization` in the
`headersToUpstreamOnAllow` of the canonical oauth2-proxy ext-auth provider.
When you upgrade an older shared Istio ConfigMap, this step is necessary. Helm
cannot change that AKS-managed ConfigMap.

The ownership filter extracts the instance from `/ui/<instance>/`, or from the
first path segment for other instance services. Then it calls the portal's
internal ownership endpoint. Before it forwards a normal or webhook request to
an instance workload, it removes the `_cetirun_oauth2` Portal session cookie
(with chunk suffixes). It also removes the sign-in (CSRF) cookies of OAuth2
Proxy, but it keeps the cookies that the Agent UI owns.

Portal routes keep the Portal cookie. The filter does not compare a subdomain
with an owner alias. The portal namespace must have actual sidecar injection,
enforced NetworkPolicy, and the authorization policy of its chart.

Model sign-in uses the Portal and AI Gateway. Hermes native API clients carry
`API_SERVER_KEY` in `X-CetiRun-Agent-API-Key`. After the ownership check, the
filter converts it to a bearer credential only for the owned
`/ui/<instance>/v1/models` and `/ui/<instance>/v1/chat/completions` routes (and
their legacy instance-subdomain forms). Transport headers are removed from
unrelated routes.

Portal port 8080 uses strict mTLS and gateway service-account identity.
Identity-bound Agent ownership also requires mTLS on port 8081. The Gateway's
dedicated TLS cluster sends the signed Portal ID token to the internal
ownership endpoint, where Portal checks the recorded tenant and object IDs.
An owner's display alias or email can change without changing ownership.
The token is never forwarded to the Agent.

The complete deployment helper enables both sides. Standalone deployments
must set `portalAuth.mtls.enabled: true` and an exact
`portalAuth.mtls.serverIdentity`, such as
`spiffe://cluster.local/ns/cetirun-portal/sa/cetirun-portal`, in the Istio chart,
and `istio.internalAuthMTLS: true` in the Portal chart. Use the actual Portal
namespace and service account. SDS supplies the mesh client certificate and
CA; the TLS cluster verifies that exact Portal identity, and Portal permits
only the configured Gateway service account. NetworkPolicy remains enforced.

For an upgrade, enable Portal's strict internal policy first, then deploy the
Istio TLS cluster, then update the Portal backend. Native Agent access can be
briefly unavailable between these steps; Portal port 8080 remains available.
The TLS cluster has a distinct name so partial xDS propagation fails closed.
Preserve the TLS settings when rolling back the backend. Chart defaults keep
the older plaintext alias-only transport for standalone compatibility; that
mode never forwards ID tokens and cannot authorize identity-bound Agents with
the updated backend. A configured Entra tenant and Portal client are required
for bound ownership even when Agent Identity and Agent Tools are disabled.

Sources: [ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}),
[portal peer authentication]({{< repo "app/cetirun-portal/chart/templates/peer-authentication.yaml" >}}),
and [portal NetworkPolicy]({{< repo "app/cetirun-portal/chart/templates/network-policy-internal-auth.yaml" >}}).
The operator owns per-instance VirtualServices, not this shared chart.

Portal `admins.aliases` enables administrative APIs. It is not blanket
authorization to read another user's instance. The Istio helper passes
`authPolicy.adminAliases`, but no Istio template uses it. Configure the
portal's reviewed admin list separately. If you run only the Istio helper, this
does not grant portal admin access.

### Agent UI content security policy

Applications served below `/ui/<instance>/` own their Content Security Policy
(CSP). The gateway keeps their `Content-Security-Policy` and
`Content-Security-Policy-Report-Only` response headers, with dynamic nonces and
hashes. If the application does not set a CSP, the gateway does not supply a
fallback CSP. Other routes keep the platform policy. The bare `/ui/<instance>`
redirect also keeps it. This is a response-header rule, not an authentication
exemption.

AAD authentication, instance ownership checks, and the removal of platform
credentials stay in effect. The gateway also keeps HSTS, `nosniff`,
Referrer-Policy, and the configured X-Frame-Options header. Applications must
set a CSP that is correct for their scripts, API calls, workers, and embedded
content. Platform operators do not keep per-application resource allowlists.

Path prefixes do not isolate browser origins. An Agent UI on the Portal's origin
can make same-origin requests with the visitor's platform session. The
server-side permissions of the visitor limit these requests. The removal of
cookies before a request goes to an Agent does not prevent those separate
browser requests. When you host mutually untrusted frontends, use separate
origins and the cookie, CORS, and CSRF controls for these origins. A storage
namespace adapter is not a security boundary.

### Portal ownership endpoint

If you change the portal service name or namespace, also update
`portalAuth.clusterHost`. By default, the standalone helper uses
`cetirun-portal.cetirun-portal.svc.cluster.local:8081`.
The platform umbrella installs the portal in `cetirun-system`. For this reason,
set `PORTAL_AUTH_NAMESPACE=cetirun-system` before you run the separate Istio
deployment helper.

Use `PORTAL_AUTH_SERVICE` for a custom service name or
`PORTAL_AUTH_CLUSTER_HOST` for a service FQDN override, without a port suffix.
The ownership endpoint uses port 8081. The umbrella
does not reconfigure the independent Istio release. Refer to the
[deployment helper]({{< repo "infra/istio/deploy.sh" >}}) and
[platform installation](../../getting-started/platform/#platform-chart).

## Exemptions

| Setting | Scope |
| --- | --- |
| `authPolicy.authExemptPrefixes` | Host prefixes that bypass OAuth2 Proxy |
| `authPolicy.aliasExemptPrefixes` | Present in values, but not used by the ownership filter |

Both prefix lists contain `oauth2` and `llmapi`. `authExemptPrefixes` bypasses
OAuth2 Proxy for those subdomain hosts. With `aliasExemptPrefixes`, the
ownership filter does not interpret those shared hosts as instance names.
Review if they are necessary. These values do not install an LLM gateway or
supply model request authorization.

The [authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}})
also exempts apex `/oauth2/*`, `/healthz`, and `/readyz`. The static
portal-path exclusions of the ownership filter are separate from OAuth
exemptions. If the filter skips an ownership call, this alone does not skip
sign-in.

## Egress and logging

The chart adds ServiceEntries and access logging in the
Istio root namespace. Access logging is enabled by default and can include
destination names. A ServiceEntry list alone does not create an egress
allowlist. The chart does not implement a complete outbound allowlist.
These audit resources do not enforce AgentPolicy. Guest-external network
controls and approved Pod-local visible-SNI capture belong to each Agent's
common Sandbox execution pipeline.

The agent NetworkPolicy allows external IPv4 egress, except to blocked ranges.
It does not restrict all external traffic to HTTPS. Its configured
`allowedInternalCIDRs` add more allow rules. As a result, private-range
exclusions are not absolute. Review or replace that environment-specific list.

### Retired shared blacklist

The chart no longer contains the mesh-wide `egress.blacklist` filter and
values. If you supply that retired values key, the chart render now fails.
This is also true when the key contains `enabled: false`. The chart does not
silently ignore a former enforcement setting. Remove the key from your reviewed
configuration.

Do not use `--reuse-values` from an older release. Upgrade with
`--reset-values -f <reviewed-values.yaml>`. Keep the necessary TLS/OIDC, owner,
routing, header, and logging configuration.

Before you remove the old enforcement, qualify the necessary AgentPolicy
network outcomes on your supported runtime profiles. The old filter has the
`helm.sh/resource-policy: keep` annotation. As a result, if you remove its
template, this does not prove that the filter is gone. If you uninstall the
release, this also does not prove that the filter is gone.

After you upgrade, run the explicit
[cleanup helper]({{< repo "scripts/cetirun-istio-cleanup.py" >}}) with your
approved target coordinates. Its default action is read-only:

```bash
python3 scripts/cetirun-istio-cleanup.py \
  --kubeconfig /path/to/test-kubeconfig --context test-cluster \
  --cluster-uid '<observed-kube-system-uid>' \
  --release cetirun-istio --release-namespace cetirun-istio \
  --legacy-namespace aks-istio-system
```

Review the object UID/resourceVersion that the helper returns. Then run the
helper again with `--apply`, `--expected-uid '<reviewed-uid>'`, and
`--expected-resource-version '<reviewed-resource-version>'`. Add
`--after-uninstall` only after Helm confirms that the exact release does not
exist. The helper examines cluster identity, release state, current manifest,
Helm ownership, and the shape of the retired filter. It issues one conditional
deletion and then makes sure that the filter is gone. The helper does not
silently retry a foreign/replaced/changed object, a failed release, a deletion
conflict, or a lost reply.

No cleanup hook runs automatically. The helper never lists or deletes unrelated
filters, removes finalizers, changes the shared authorization provider, or
removes the ServiceEntries/access logging that the chart keeps. Do not publish
its environment-specific receipt or Helm values. Record only sanitized outcomes.

## Inspect, upgrade, or remove

**Connected, read-only:**

```bash
helm status cetirun-istio -n cetirun-istio
kubectl get gateway.networking.istio.io cetirun-gateway -n aks-istio-ingress
kubectl get envoyfilter -n aks-istio-ingress
kubectl get pods -n cetirun-istio
```

For diagnosis, examine the specific shared ConfigMap revision and gateway
listeners with `istioctl`. Review OAuth2 Proxy logs privately. Helm values can
contain certificate or cookie material. For this reason, do not copy them into
reports.

An upgrade can affect every instance. Before you apply changed values and the
external authorization patch, review them. A Helm uninstall does not remove the
external authorization provider from the managed ConfigMap. For an older kept
blacklist, the explicit cleanup above is necessary. Make an explicit plan for
the resources that are left and for the effect on shared access. Do not remove a
shared provider without a review.
