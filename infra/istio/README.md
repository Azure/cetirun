# CetiRun Istio authentication

Read [Istio authentication and egress](../../site/content/en/docs/operations/istio.md)
for identity, DNS/TLS, helper inputs, external authorization, ownership checks,
webhook exceptions, egress configuration, and warnings about removal.

`values.yaml` configures the shared chart. `templates/ingress/` contains the
gateway and authentication resources. `templates/egress/` contains the
destination and logging controls for all of the mesh. The operator manages the
VirtualServices for each instance.

`deploy.sh` changes both the Helm resources and the AKS Istio shared ConfigMap.
For a manual Helm install, a separate configuration for external authorization
is necessary. The chart and the helper do not issue or renew TLS certificates.

For a single public hostname backed by Azure Public IP DNS or a user-managed
DNS record, configure `publicTLS` in the normal environment file as
[Public DNS and TLS](../../site/content/en/docs/operations/public-ingress.md)
describes. `make deploy` can issue and renew the externally managed
`cetirun-tls` Secret with cert-manager HTTP-01. The umbrella chart owns the
port-80 ACME listener only. This chart continues to own the HTTPS Gateway and
must use `tls.enabled: false` for that ownership model.

Before you run the deployment helper, install its pinned Python dependency:

```bash
python3 -m pip install --requirement infra/istio/requirements.txt
```

## Portal ownership endpoint

For the platform umbrella, set `PORTAL_AUTH_NAMESPACE=cetirun-system` before
you run `deploy.sh`. The default value for the standalone helper is
`cetirun-portal`. `PORTAL_AUTH_SERVICE` and `PORTAL_AUTH_CLUSTER_HOST` let you
set custom service locations. Refer to the
[ownership endpoint configuration](../../site/content/en/docs/operations/istio.md#portal-ownership-endpoint).

## Ingress configuration

The Gateway serves HTTPS on port 443. Every authentication policy rule and all
three ingress HTTP filters match only that port. As a result, they do not
intercept an HTTP-01 challenge on a separate port-80 listener. The chart does
not create that listener or its challenge route.

| Value | Default | Behavior |
| --- | --- | --- |
| `gateway.includeWildcardHost` | `true` | Includes both `domain` and `*.domain` in the Gateway. `false` includes only the exact `domain`. |
| `oauth2Proxy.skipJwtBearerTokens` | `null` | Keeps the default of the mode: validated JWT authentication is on for `entra-id` and off for `local-oidc`. With `false`, a session cookie is necessary. `true` is rejected in `local-oidc` mode. |
| `oauth2Proxy.logging.request` | `true` | Enables OAuth2 Proxy request logs. |
| `oauth2Proxy.logging.auth` | `true` | Enables OAuth2 Proxy authentication logs. |
| `oauth2Proxy.logging.standard` | `true` | Enables OAuth2 Proxy standard logs. These logs also contain diagnostic errors. |
| `cookieSecretLegacyNamespace` | `istio-system` | Reads the legacy cookie Secret only if the release namespace does not have one. An empty string disables this fallback. |

The host and logging switches accept YAML booleans, not strings. The legacy
namespace must be a valid Kubernetes namespace or an empty string. If an older
release does not have an option, that option keeps the default above. This is
also true with `--reuse-values`.

For an apex-only deployment that makes session cookies necessary and disables
OAuth2 Proxy logs, the values file of the environment can contain:

```yaml
gateway:
  includeWildcardHost: false
cookieSecretLegacyNamespace: ""
oauth2Proxy:
  skipJwtBearerTokens: false
  logging:
    request: false
    auth: false
    standard: false
```

These values are deployment choices, not new global defaults. If you disable
standard logs, you also remove the diagnostic output of OAuth2 Proxy. The
removal of the wildcard host does not remove authentication rules for legacy
subdomains or add anonymous routes. The exempt paths, callback method guards,
and Portal ownership checks stay in place.

OAuth2 Proxy always uses PKCE with `S256`, verifies the OIDC nonce, and sets
Secure and HttpOnly session cookies. The CSRF cookies for each request, their
configured limit, and the 401 behavior of API and Agent asset requests stay
enabled. TLS verification for OIDC is not disabled. The ingress filters
continue to remove Portal identity tokens and session cookies before the
requests get to Agent workloads.

## Preserve sessions and TLS during upgrades

If `cookieSecret` is empty, Helm first reads `oauth2-proxy-credentials` in the
release namespace. Helm uses the `cookie-secret` bytes of that Secret again and
does not change them. Helm tries `cookieSecretLegacyNamespace` only if that
Secret does not exist. If you disable the legacy lookup, you do not disable the
reuse in the current namespace.

If a Secret exists but does not contain `cookie-secret`, the chart render
fails. If neither reusable Secret exists, an upgrade also fails and does not
generate a new session key. If neither Secret exists, a first install generates
a 32-character key. An explicit `cookieSecret` overrides the lookup and can
rotate the key. A rotated key makes the sessions invalid. Do not change it
during a normal upgrade.

A client-only `helm template --is-upgrade` cannot see cluster Secrets. Without
an explicit `cookieSecret`, a failure of this command does not show that the
live Secret does not exist. Offline tests supply a synthetic key only to the
render command. Never copy that key, or a manifest that contains it, into
deployment values or a real upgrade.

A real Helm upgrade must use server lookup or an explicitly supplied key that
already exists. Render-only deployment tools cannot keep a generated key
through lookup. These tools must have a stable, separately protected
`cookieSecret`.

The Gateway still refers to `cetirun-tls` in `gateway.namespace`. With
`tls.enabled: false`, the chart does not render or update that externally
managed Secret. With `tls.enabled: true`, both `tls.cert` and `tls.key` are
necessary, and the chart uses them as they are supplied. During an upgrade, keep
the same TLS ownership mode and inputs. If you disable `tls.enabled` after Helm
managed the Secret, the upgrade removes the Secret from the release and can
delete it. No ingress option above migrates TLS ownership, issues a
certificate, or rotates a key.

## Offline checks

```bash
bash scripts/cetirun-istio-helm-test.sh
python3 -B -m unittest discover -s scripts/tests -p 'test_istio_*.py' -v
```

The Python checks must have the pinned PyYAML dependency in `requirements.txt`.
The Secret-reuse tests run Helm against a loopback-only API fixture that has
synthetic objects and no credentials. That fixture supports discovery and GET
requests. It does not support Kubernetes writes or OpenAPI validation. These
checks cover the rendered chart and the lookup decisions. They do not cover a
live upgrade, OIDC sign-in, or Istio traffic.
