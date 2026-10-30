# CetiRun Istio authentication

Read [Istio authentication and egress](../../site/content/en/docs/operations/istio.md)
for identity, DNS/TLS, helper inputs, external authorization, ownership checks,
webhook exceptions, egress configuration, and removal caveats.

`values.yaml` configures the shared chart. `templates/ingress/` holds gateway
and authentication resources; `templates/egress/` holds mesh-wide destination
and logging controls. The operator manages per-instance VirtualServices.

`deploy.sh` mutates both Helm resources and the AKS Istio shared ConfigMap.
A manual Helm install requires separate external authorization configuration.
Neither the chart nor helper issues or renews TLS certificates.

Install the deployment helper's pinned Python dependency before running it:

```bash
python3 -m pip install --requirement infra/istio/requirements.txt
```

## Portal ownership endpoint

For the platform umbrella, set `PORTAL_AUTH_NAMESPACE=cetirun-system` before
running `deploy.sh`. The standalone helper default is `cetirun-portal`.
`PORTAL_AUTH_SERVICE` and `PORTAL_AUTH_CLUSTER_HOST` support custom service
locations. See the
[ownership endpoint configuration](../../site/content/en/docs/operations/istio.md#portal-ownership-endpoint).

## Ingress configuration

The Gateway serves HTTPS on port 443. Every authentication policy rule and all
three ingress HTTP filters match that port only, so they do not intercept an
HTTP-01 challenge on a separate port-80 listener. The chart does not create
that listener or its challenge route.

| Value | Default | Behavior |
| --- | --- | --- |
| `gateway.includeWildcardHost` | `true` | Includes both `domain` and `*.domain` in the Gateway. `false` includes only the exact `domain`. |
| `oauth2Proxy.skipJwtBearerTokens` | `null` | Keeps the mode default: validated JWT authentication is on for `entra-id` and off for `local-oidc`. `false` requires a session cookie. `true` is rejected in `local-oidc` mode. |
| `oauth2Proxy.logging.request` | `true` | Enables OAuth2 Proxy request logs. |
| `oauth2Proxy.logging.auth` | `true` | Enables OAuth2 Proxy authentication logs. |
| `oauth2Proxy.logging.standard` | `true` | Enables OAuth2 Proxy standard logs, including diagnostic errors. |
| `cookieSecretLegacyNamespace` | `istio-system` | Reads the legacy cookie Secret only when the release namespace has none. An empty string disables this fallback. |

Host and logging switches accept YAML booleans, not strings. The legacy namespace
must be a valid Kubernetes namespace or an empty string. Missing options in an
older release retain the defaults above, including with `--reuse-values`.

For an apex-only deployment that requires session cookies and disables OAuth2
Proxy logs, the environment's values file can contain:

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

These are deployment choices, not new global defaults. Disabling standard logs
also removes OAuth2 Proxy's diagnostic output. Removing the wildcard host does
not remove authentication rules for legacy subdomains or add anonymous routes.
Existing exempt paths, callback method guards, and Portal ownership checks stay
in place.

OAuth2 Proxy always uses PKCE with `S256`, verifies the OIDC nonce, and sets
Secure and HttpOnly session cookies. Per-request CSRF cookies, their configured
limit, and the 401 behavior of API and Agent asset requests remain enabled.
OIDC TLS verification is not disabled. The ingress filters continue to strip
Portal identity tokens and session cookies before requests reach Agent workloads.

## Preserve sessions and TLS during upgrades

With `cookieSecret` empty, Helm first reads `oauth2-proxy-credentials` in the
release namespace. It reuses that Secret's `cookie-secret` bytes unchanged.
Only if that Secret is absent does Helm try `cookieSecretLegacyNamespace`.
Disabling the legacy lookup does not disable current-namespace reuse.

An existing Secret without `cookie-secret` fails rendering. An upgrade without
either reusable Secret also fails, rather than generating a new session key.
A first install with neither Secret generates a 32-character key. An explicit
`cookieSecret` takes precedence over lookup and can rotate the key, invalidating
sessions. Do not change it during an ordinary upgrade.

Client-only `helm template --is-upgrade` cannot see cluster Secrets. With no
explicit `cookieSecret`, its failure does not show that the live Secret is
missing. Offline tests supply a synthetic key only to the rendering command.
Never copy that key or a manifest containing it into deployment values or an
actual upgrade. A real Helm upgrade must use server lookup or an explicitly
supplied existing key. Render-only deployment tools cannot preserve a generated
key through lookup; they need a stable, separately protected `cookieSecret`.

The Gateway still references `cetirun-tls` in `gateway.namespace`. With
`tls.enabled: false`, the chart does not render or update that externally managed
Secret. With `tls.enabled: true`, both `tls.cert` and `tls.key` are required and
are used as supplied. Keep the existing TLS ownership mode and inputs on an
upgrade. Turning `tls.enabled` off after Helm managed the Secret removes it from
the release and can delete it. None of the ingress options above performs a TLS
ownership migration, issues a certificate, or rotates a key.

## Offline checks

```bash
bash scripts/cetirun-istio-helm-test.sh
python3 -B -m unittest discover -s scripts/tests -p 'test_istio_*.py' -v
```

The Python checks need the pinned PyYAML dependency in `requirements.txt`.
Secret-reuse tests run Helm against a loopback-only API fixture with synthetic
objects and no credentials. That fixture supports discovery and GET requests,
not Kubernetes writes or OpenAPI validation. These checks cover chart rendering
and lookup decisions, not a live upgrade, OIDC sign-in, or Istio traffic.
