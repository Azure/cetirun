---
title: "Istio authentication and egress"
description: "Operate the shared gateway, OAuth2 Proxy, ownership checks, and egress policy."
weight: 30
---

The [infra/istio chart]({{< repo "infra/istio" "tree" >}}) is installed once per
deployment. It supplies OAuth2 Proxy, an Istio Gateway and VirtualService,
AuthorizationPolicy, redirect and ownership filters, security headers, and
mesh-wide egress resources.

## Prepare identity, DNS, and TLS

1. Enable AKS managed Istio and its external ingress gateway.
2. Configure an Entra app for the OAuth2 callback
   `https://<domain>/oauth2/callback` (for example,
   `https://example.com/oauth2/callback`). Add the `login_hint` optional claim
   to this app's ID tokens when Agent Identity is enabled. Entra defines this
   opaque claim as the reliable value for carrying SSO into another app,
   including B2B guest and cross-tenant scenarios.
3. Configure application-assigned group claims and the permitted group IDs.
4. Configure federated trust for
   `system:serviceaccount:cetirun-istio:oauth2-proxy` and the cluster’s OIDC issuer.
5. Point the apex at the ingress address. Add wildcard DNS only for
   subdomain services you configure; the portal, instance UI,
   and OAuth routes use the apex.
6. Supply a certificate that covers the actual names, including the apex.

The [OAuth2 Proxy deployment]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}})
sets the apex callback, and its
[VirtualService]({{< repo "infra/istio/templates/ingress/oauth2-proxy-virtualservice.yaml" >}})
routes apex `/oauth2` requests. An `oauth2.<domain>` exemption in the values
does **not** create a working callback route on that host.

The [portal's Sign out action]({{< repo "app/cetirun-portal/web/src/components/Header.tsx" >}})
uses the apex `/oauth2/sign_out` route and returns the user to the page from
which they signed out.

The chart does not issue or renew certificates. The helper can read supplied
PEM files, decode PFX content, read a PFX file, or download a certificate from
Key Vault, in that order. PFX parsing uses an empty password; the helper has no
PFX-password input. Issuance and renewal remain operator responsibilities.

The [Gateway template]({{< repo "infra/istio/templates/ingress/gateway.yaml" >}})
always declares HTTPS on port 443 using `credentialName: cetirun-tls`.
`tls.enabled` defaults to false: that suppresses creation of the TLS Secret,
**not** HTTPS. Supply `cetirun-tls` in the gateway namespace out of band, or
set `tls.enabled: true` with the certificate and key. A render without that
Secret is not a usable TLS deployment.

## Deploy helper inputs

[infra/istio/deploy.sh]({{< repo "infra/istio/deploy.sh" >}})
requires Bash, Helm, kubectl, jq, OpenSSL, Python 3, and the pinned PyYAML
dependency. Install the Python dependency before deployment:

```bash
python3 -m pip install --requirement infra/istio/requirements.txt
```

The helper also requires these inputs for its normal path:

| Variable | Purpose |
| --- | --- |
| `AZURE_SUBSCRIPTION_ID` | Subscription used for cluster validation |
| `CLIENT_ID`, `TENANT_ID` | OAuth2 Proxy app identity |
| `ALLOWED_GROUPS` | Comma-separated allowed group IDs |
| `RESOURCE_GROUP`, `AKS_CLUSTER_NAME` | Target cluster |
| `DOMAIN` | Deployment domain |
| `KEY_VAULT_NAME` | Required by the helper, even with supplied certificate files |

Optional inputs include `KEY_VAULT_CERT_NAME` (default `sslcert`),
`TLS_CERT_PATH` plus `TLS_KEY_PATH`, `PFX_BASE64`, `PFX_PATH`, `COOKIE_SECRET`,
`ADMIN_ALIASES`, and `RELEASE_NAME` (default `cetirun-istio`).
The normal path also requires Azure CLI and an already-authorized Azure
session. Certificate download needs Key Vault secret access, not merely
permission to list certificates.

After setting the required variables, deploy the shared Istio resources:

```bash
bash infra/istio/deploy.sh
```

The helper selects the Azure subscription, checks the current context,
installs/upgrades Helm resources, waits for OAuth2 Proxy, then patches the AKS
Istio shared ConfigMap with the external authorization provider. Context
validation only tests whether its name contains `AKS_CLUSTER_NAME`; independently
verify the actual target. `SKIP_AZ_VALIDATION` skips Azure and context checks
and the subscription-ID requirement; it still needs the other required inputs
and supplied certificate material. It is not a dry-run flag.

The patch chooses the revision on the first matching Istiod pod, parses the
mesh configuration as YAML, and requires at most one provider named exactly
`oauth2-proxy`. It adds a missing provider or adds `authorization` to that
provider's upstream header list. The update uses the ConfigMap
`resourceVersion` as a compare-and-swap precondition and retries against the
latest mesh configuration after conflicts. Check every active revision during
Istio upgrades; revision discovery still selects the first matching Istiod pod.

## Direct Helm installation

**Offline render**, using only synthetic identity values:

```bash
helm template cetirun-istio infra/istio --namespace cetirun-istio \
  --set-string clientId=example-client --set-string tenantId=example-tenant \
  --set-string allowedGroups=example-group --set-string domain=example.com \
  > /dev/null
```

This does not verify identity, TLS, existing Secrets, or the managed mesh
configuration. Do not apply this rendered example.

**Connected, mutating:** platform operators can install the local chart with
a separately prepared private values file:

```bash
helm upgrade --install cetirun-istio infra/istio \
  --namespace cetirun-istio --create-namespace \
  -f /absolute/path/reviewed-istio-values.yaml
```

This path requires **separate external authorization provider configuration**
in the AKS shared ConfigMap. A Helm install alone does not wire `ext_authz`.
The chart reuses an existing cookie Secret via lookup, or generates one on
first installation. Offline `helm template` cannot look up that Secret and can
generate a different cookie key. Do not use an offline rendered Secret to
update a live release.

### OAuth session storage

The default `oauth2Proxy.sessionStore.type: cookie` mode does not request
`offline_access`. Its 45-minute cookie lifetime ends before the Entra ID token
expires, so users must sign in again instead of carrying a refresh token in an
encrypted browser cookie. Refresh-token cookies can exceed common 8 KiB request
header limits and break otherwise unrelated Portal actions.

For refreshable sessions, provision Redis separately and configure the chart
to reference it:

```yaml
oauth2Proxy:
  sessionStore:
    type: redis
    redis:
      connectionURL: redis://oauth-session.example.svc.cluster.local:6379/0
      existingSecret: oauth-session-credentials
      passwordKey: redis-password
```

Redis mode requests `offline_access`, stores the session server-side, refreshes
it every 30 minutes, and keeps only a short session key in the browser. The
chart does not deploy, persist, back up, or secure Redis. Use an environment-
appropriate highly available service, NetworkPolicy, encryption in transit,
and a pre-created Secret. Keep credentials out of `connectionURL` and Helm
values; OAuth2 Proxy reads the password from the referenced Secret.

After rotating the referenced Redis password Secret, reconcile the Istio Helm
release. The chart hashes the live Secret data into the OAuth2 Proxy Pod
template, so that Helm reconciliation rolls the Pods and loads the new
password. A Secret update by itself does not restart existing Pods.

### Expired sessions

After a session expires, the next page load signs the user in again. OAuth2
Proxy keeps a separate CSRF cookie for each sign-in in progress, so sign-ins
started at the same time, by a page reload and background requests or by
several tabs, do not invalidate each other. Starting a sign-in deletes the
oldest of these cookies beyond `oauth2Proxy.cookie.csrfPerRequestLimit`
(default 20). Requests below `/api/`, `/ui/<instance>/api/` and
`/ui/<instance>/assets/` answer `401` instead of redirecting to Entra: a
browser cannot follow that redirect from a script, and an Agent UI can request
dozens of assets at once, each of which would otherwise start a sign-in. The
Portal then asks the user to refresh the page.

## Authentication and ownership

```text
Browser → gateway → redirect header → OAuth2 Proxy / Entra login
        → ownership filter → portal or instance VirtualService
```

OAuth2 Proxy uses Entra's `preferred_username` as its email claim by default.
Local tenant users are not guaranteed to receive an `email` claim, while the
tenant-facing UPN remains available for Portal identity headers. Set
`oauth2Proxy.emailClaim: email` only when every allowed user in the tenant is
known to receive that claim. This selection is a login identifier; instance
ownership and Agent authorization continue to use the verified tenant and
object IDs from the signed ID token.

When the Portal ID token includes Entra's optional `login_hint` claim, the
Portal passes that opaque value to the separate Agent authorization client.
It falls back to `preferred_username` only for app registrations that have not
enabled the optional claim. The authorization request does not force an account
picker, allowing Entra to reuse the hinted Portal session. The hint selects a
sign-in session; it is never an authorization key. The callback must still
match the instance owner's verified tenant and object IDs, so enabling the
claim does not relax owner isolation.

The Agent authorization client requests both the OIDC `openid` and `profile`
scopes. Entra's `profile` scope supplies the immutable object ID used by the
callback owner check; `openid` alone does not guarantee that claim. Do not
replace the object-ID check with a username or email fallback.

For authenticated Portal routes, OAuth2 Proxy returns its validated ID token in
the internal `Authorization` header. The gateway filter first removes any
client-supplied `X-CetiRun-Portal-ID-Token`, then moves the validated token to
that header only for the Portal and removes `Authorization`. Agent routes
receive neither the Portal ID token nor its authorization header. The Portal
verifies the token signature, issuer, audience, tenant, object ID, and
timestamps before using it for Agent authorization ownership.

Native Agent UIs, such as the OpenClaw Control UI, authenticate some of their
own HTTP requests with an `Authorization` header that ext-auth replaces. The
security headers filter records the value the browser sent, before ext-auth,
in the request's dynamic metadata, which never leaves the gateway. After the
ownership check, the ownership filter restores it on owned
`/ui/<instance>/` routes, unless OAuth2 Proxy accepted that same credential as
the user's identity. The native Agent API routes below keep accepting only
their dedicated header. On other owned Agent routes, such as the Terminal and
Desktop, the filter removes `Authorization` unless one of the route-bound
credentials below applies.

The deployment helper also reconciles `authorization` in the canonical
oauth2-proxy ext-auth provider's `headersToUpstreamOnAllow`. This step is
required when upgrading an older shared Istio ConfigMap; Helm cannot modify
that AKS-managed ConfigMap.

The ownership filter extracts the instance from `/ui/<instance>/` or
the first path segment for other instance services, then calls the portal's
internal ownership endpoint. Before forwarding a normal or webhook request to
an instance workload, it removes the `_cetirun_oauth2` Portal session cookie
(including chunk suffixes) and OAuth2 Proxy's sign-in (CSRF) cookies while
retaining cookies owned by the Agent UI.
Portal routes retain the Portal cookie. The filter does not compare a subdomain
with an owner alias. The portal namespace requires actual sidecar injection, enforced
NetworkPolicy, and its chart's authorization policy.

Portal calls to an instance's `cetirun-auth` helper use the dedicated
`X-CetiRun-Auth-Key` browser header because OAuth2 Proxy owns `Authorization`.
Only after the ownership check succeeds, the filter converts that header to an
internal bearer credential for `/<instance>/cetirun-auth/*`. It removes the
dedicated header on Portal, webhook, and other Agent routes. Hermes native API
clients similarly carry `API_SERVER_KEY` in `X-CetiRun-Agent-API-Key`; the
filter converts it to a bearer credential only for the owned
`/ui/<instance>/v1/models` and `/ui/<instance>/v1/chat/completions` routes (and
their legacy instance-subdomain forms). The two transport headers are never
interchangeable and are removed from every unrelated route.
Portal port 8080 uses strict mTLS and gateway service-account identity. Port
8081 deliberately disables mTLS for the Lua ownership call and relies on the
gateway-namespace NetworkPolicy restriction; do not describe it as an
mTLS-authenticated endpoint.

Sources: [ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}),
[portal peer authentication]({{< repo "app/cetirun-portal/chart/templates/peer-authentication.yaml" >}}),
and [portal NetworkPolicy]({{< repo "app/cetirun-portal/chart/templates/network-policy-internal-auth.yaml" >}}).
The operator owns per-instance VirtualServices, not this shared chart.

Portal `admins.aliases` enables administrative APIs; it is not blanket
authorization to read another user's instance. The Istio helper passes
`authPolicy.adminAliases`, but no Istio template consumes it. Configure
the portal's reviewed admin list separately; running only the Istio helper
does not grant portal admin access.

### Agent UI content security policy

Applications served below `/ui/<instance>/` own their Content Security Policy
(CSP). The gateway preserves their `Content-Security-Policy` and
`Content-Security-Policy-Report-Only` response headers, including dynamic nonces
and hashes. It does not supply a fallback CSP when the application omits one.
Other routes retain the platform policy; the bare `/ui/<instance>` redirect
also retains it. This is a response-header rule, not an authentication exemption.

AAD authentication, instance ownership checks and platform credential stripping
remain in effect. The gateway also retains HSTS, `nosniff`, Referrer-Policy and
the configured X-Frame-Options header. Applications must set a CSP appropriate
for their scripts, API calls, workers and embedded content. Platform operators
do not maintain per-application resource allowlists.

Path prefixes do not isolate browser origins. An Agent UI on the Portal's origin
can make same-origin requests with the visitor's platform session, subject to
the visitor's server-side permissions. Cookie stripping before forwarding to
an Agent does not prevent those separate browser requests. Use separate origins
and corresponding cookie, CORS and CSRF controls when hosting mutually untrusted
frontends; a storage namespace adapter is not a security boundary.

### Portal ownership endpoint

Changing the portal service name or namespace also requires updating
`portalAuth.clusterHost`. The standalone helper defaults to
`cetirun-portal.cetirun-portal.svc.cluster.local:8081`.
The platform umbrella installs the portal in `cetirun-system`, so set
`PORTAL_AUTH_NAMESPACE=cetirun-system` before running the separate Istio
deployment helper.

Use `PORTAL_AUTH_SERVICE` for a custom service name or
`PORTAL_AUTH_CLUSTER_HOST` for a service FQDN override, without a port suffix.
The ownership endpoint uses port 8081. The umbrella
does not reconfigure the independent Istio release. See the
[deployment helper]({{< repo "infra/istio/deploy.sh" >}}) and
[platform installation](../../getting-started/platform/#platform-chart).

## Exemptions

| Setting | Scope |
| --- | --- |
| `authPolicy.authExemptPrefixes` | Host prefixes bypassing OAuth2 Proxy |
| `authPolicy.aliasExemptPrefixes` | Present in values, but unused by the ownership filter |

Both prefix lists contain `oauth2` and `llmapi`. `authExemptPrefixes` bypasses
OAuth2 Proxy for those subdomain hosts, while `aliasExemptPrefixes` prevents
the ownership filter from interpreting those shared hosts as instance names.
Review whether they are needed. These values do not install an LLM gateway or
provide model request authorization.

The [authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}})
also exempts apex `/oauth2/*`, `/healthz`, and `/readyz`. The ownership filter's
static portal-path exclusions are separate from OAuth exemptions; skipping
an ownership call does not by itself skip sign-in.

## Egress and logging

The chart adds ServiceEntries, access logging, and blacklist filters in the
Istio root namespace. Access logging is enabled by default and can include
destination names. A ServiceEntry list does not by itself create an egress
allowlist; the chart does not implement a complete outbound allowlist.
Review configured blacklist rules and their mesh-wide effect.

The agent NetworkPolicy permits external IPv4 egress except blocked ranges;
it does not restrict all external traffic to HTTPS. Its configured
`allowedInternalCIDRs` are additional allow rules, so private-range exclusions
are not absolute. Review or replace that environment-specific list. The
[blacklist filter]({{< repo "infra/istio/templates/egress/blacklist.yaml" >}})
matches configured TLS SNI/IP destinations; it is not a complete domain or
application-level egress boundary.

## Inspect, upgrade, or remove

**Connected, read-only:**

```bash
helm status cetirun-istio -n cetirun-istio
kubectl get gateway.networking.istio.io cetirun-gateway -n aks-istio-ingress
kubectl get envoyfilter -n aks-istio-ingress
kubectl get pods -n cetirun-istio
```

For diagnosis, inspect the specific shared ConfigMap revision and gateway
listeners with `istioctl`; review OAuth2 Proxy logs privately. Helm values can
contain certificate or cookie material, so do not dump them into reports.

An upgrade can affect every instance. Review changed values and the external
authorization patch before applying. Uninstalling Helm does not remove the
external authorization provider from the managed ConfigMap; the blacklist
filter also has a keep policy. Plan those residual resources and the shared
access impact explicitly. Do not blindly remove a shared provider.
