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
   `https://example.com/oauth2/callback`).
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
uses `/oauth2/sign_out` on the current origin, matching the apex route above.
It passes the current page as the URL-encoded `rd` return address. After
deployment, verify that **Sign out** clears the application session and that
subsequent protected requests go through authentication again.

Deployment summaries and Helm notes may still list `oauth2.<domain>` without
creating a route for that host. Adding DNS alone does not create an HTTP route.

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
requires Bash, Helm, kubectl, jq, OpenSSL, and these inputs for its normal path:

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

The patch chooses the revision on the first matching Istiod pod and skips its
write if the existing mesh text contains `oauth2-proxy`. This is not validation
of an existing provider's configuration or every active revision. Check both
during Istio upgrades.

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

## Authentication and ownership

```text
Browser → gateway → redirect header → OAuth2 Proxy / Entra login
        → ownership filter → portal or instance VirtualService
```

The ownership filter extracts the instance from `/ui/<instance>/` or
the first path segment for other instance services, then calls the portal's
internal ownership endpoint. It does not compare a subdomain with an owner
alias. The portal namespace requires actual sidecar injection, enforced
NetworkPolicy, and its chart's authorization policy.
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
| `authPolicy.webhookBypassPaths` | POST-only webhook suffix exceptions |

Both prefix lists contain `oauth2` and `llmapi`, but only `authExemptPrefixes`
is consumed by the templates. It bypasses OAuth2 Proxy for those
subdomain hosts. Review whether they are needed; `aliasExemptPrefixes` is not
a working ownership-policy control. These values do not install an LLM gateway
or provide model request authorization.

The [authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}})
also exempts apex `/oauth2/*`, `/healthz`, and `/readyz`. The ownership filter's
static portal-path exclusions are separate from OAuth exemptions; skipping
an ownership call does not by itself skip sign-in.

The webhook default is `["*/api/messages"]`, **not** an unrestricted path
exemption. For a live per-instance VirtualService, the exact
`POST /<instance>/api/messages` route goes to the Teams webhook if enabled,
or returns 404 if disabled. Non-POST requests to the suffix go through
OAuth2 Proxy.

The shared suffix exception is broader than those exact per-instance routes:
suspended or nonexistent instances have no such VirtualService. Do not promise
a fixed 404 or a Bot adapter for every matching URL. See the
[route implementation]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}).
Set `authPolicy.webhookBypassPaths: []` to remove the suffix exception entirely.
Applications behind exceptions must perform their own request authentication.

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
