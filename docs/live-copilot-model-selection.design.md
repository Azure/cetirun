# Live GitHub Copilot Model Selection

## Status

Implemented for the CetiRun Portal. This design deliberately does not retain
backward compatibility with the former image-embedded model list.

## Goals

- Let an instance owner select a GitHub Copilot model in the Portal.
- Discover the choices from the instance owner's current GitHub Copilot
  entitlement instead of shipping a stale, global list in the agent image.
- Persist the selection declaratively in the `OpenClawInstance` custom
  resource, so it survives pod recreation and is reconciled by the operator.
- Permit a model ID to be entered manually when discovery is unavailable.
- Provide a deployment-wide default for instances without an explicit choice.

## Non-goals

- Validate a model ID against a global or static catalog.
- Expose GitHub OAuth tokens or the unfiltered upstream model response to the
  Portal or browser.
- Guarantee that a model shown by GitHub will remain usable after it is saved.
  GitHub Copilot remains the entitlement authority.
- Support the retired Team-level model configuration surface. GitHub Copilot
  instances use only per-instance selection and the operator deployment default;
  non-Copilot providers remain configured through their Helm values.

## Architecture

```text
Portal browser
  | authenticated request using the per-instance CetiRun auth key
  v
instance gateway: /cetirun-auth/v1/models
  | reads the local GitHub OAuth credential
  v
GitHub: https://api.github.com/copilot_internal/v2/token
  | exchanges it for a short-lived Copilot API token
  v
GitHub Copilot: token-derived https://api.<tenant>.githubcopilot.com/models
```

The in-pod CetiRun authentication service is the trust boundary. It obtains
the active GitHub OAuth token from the Hermes environment file or OpenClaw auth
profile, exchanges it at GitHub for a short-lived Copilot API token, then calls
the Copilot catalog endpoint. The catalog host is derived only from GitHub's
`proxy-ep` token claim and is restricted to `githubcopilot.com`; malformed or
unrecognized values fall back to the official individual endpoint. Neither URL
can be overridden by instance environment variables. The service filters the
response to selectable chat-model identifiers, display names, and chat endpoint
metadata, then returns only that reduced payload. It caches successful responses
for five minutes. The Portal never receives either token, and the service never
returns an image-defined fallback list.

Both Hermes and OpenClaw use GitHub Copilot's official VS Code OAuth client for
the Portal device flow. Its tokens are accepted by the token-exchange endpoint.
Hermes tokens created by older images used a Hermes-specific client and cannot
be exchanged or migrated; the catalog endpoint returns an explicit
re-authentication response for those tokens, after which one Portal
re-authentication replaces the credential.

The Portal only queries the live catalog for an existing instance. A newly
created instance has no signed-in Copilot identity, so its creation form offers
manual entry or the deployment default. After successful Copilot login, the
Portal invalidates both authentication-status and model-catalog caches.

## API and persistence

`OpenClawInstance.spec.model` is an optional GitHub Copilot model ID with a
maximum length of 256 characters. Portal create and update APIs expose it as
`model` only when the effective provider is GitHub Copilot.

- A non-empty value is a per-instance override.
- An empty value on PATCH clears that override and resumes the deployment
  default.
- For GitHub Copilot, the operator renders `spec.model` when present; otherwise
  it renders the deployment default, `gpt-6-astra`, which can be overridden
  with `--default-model` / `CETIRUN_DEFAULT_MODEL`.
- An explicit `spec.model` is applied after user-provided Helm values only for
  GitHub Copilot. This prevents a legacy `secrets.model` override from
  silently changing a Portal selection. OpenAI and Anthropic retain the
  provider-specific model supplied in Helm values, including for older
  instances that retain a stale `spec.model` value.

The agent chart has no baked-in model default and requires `secrets.model`.
This makes an omitted deployment default visible at Helm rendering time rather
than silently selecting a model the user might not be entitled to use.

## Reconciliation and user experience

Changing a model updates the custom resource. The operator sees the change,
upgrades the instance Helm release, and restarts the gateway; the access link
can be briefly unavailable. A change to the operator deployment default is
included in its configuration hash, causing instances that rely on it to
reconcile.

For an existing instance, the picker shows the live catalog, supports refresh,
and keeps manual input available. If a manually entered or previously selected
value is absent from the current catalog, the Portal warns but does not block
the save. This accommodates temporarily unavailable catalogs and changes in
GitHub Copilot entitlements while leaving final validation to the provider.

## Failure handling and security

- No local token: return `401 not_authenticated` and invite the user to sign
  in to GitHub Copilot.
- GitHub rejects catalog access: return a structured `catalog_forbidden` error.
- A pre-upgrade Hermes token that cannot be exchanged returns
  `catalog_reauthenticate_required`; re-authenticate in the Portal to replace
  it with a catalog-compatible credential.
- Network, timeout, malformed response, or upstream failure: return a
  structured unavailable error; manual entry remains possible.
- The catalog response is sanitized and successful results alone are cached.
- Bearer authentication to the in-pod service uses the existing
  `CETIRUN_AUTH_KEY` instance secret.

## Validation

The implementation is covered by operator and Portal Go tests, Portal frontend
tests and production build, Python syntax validation for the auth service, and
Helm lint/template rendering with an explicit model. Deployment validation
confirmed that the Portal can load an authenticated user's catalog and update
an instance model.
