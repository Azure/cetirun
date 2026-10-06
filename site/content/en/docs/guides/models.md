---
title: "Choose a model"
description: "Use the live Copilot catalog or configure a provider-specific model."
weight: 10
---

Select a GitHub Copilot model in the portal, or configure an OpenAI or
Anthropic model through Helm values.

## Models through the AI Gateway

The platform AI Gateway is part of every platform installation: the operator's
`aiGateway.enabled` and the portal's `features.aiGatewayEnabled` are required.
OpenClaw instances take all their models from the Gateway, from two sources:

- **Shared models** are the models an administrator configures in the Gateway
  with the access group `cetirun-shared`. They keep the administrator's names.
- **GitHub Copilot** models come from the owner's own Copilot subscription,
  through a small proxy per instance that only the Gateway can reach. They
  appear as `copilot/<model>`.

The create form offers both sources, on by default. At least one stays on:
GitHub Copilot stays on while shared models are off or none is available. Every
new instance needs a Team.

To add Copilot models, open the instance and choose **Sign in with GitHub**. The
portal runs the GitHub device-code sign-in, stores the token for the instance's
proxy, and records the account's chat models. They appear once the proxy has
started, usually within a few minutes. A model the Gateway has no price for is
listed as not offered: it would escape the instance's budget.

Each instance has its own virtual key with a monthly budget. The instance page
lists its models and sets the default. When an administrator adds or removes a
shared model, or changes its limits, the operator updates every instance that
uses shared models, which restarts them.

The key's value is kept in the instance's `ai-gateway-virtual-key` Secret. If
that Secret is lost while the key still exists in the Gateway, the operator
does not replace the key, which would reset its spend, restore the default
budget and unblock a blocked key. The Agent instead reports `AIGatewayReady`
as false with the reason `KeySecretLost`, with an `AIGatewayKeySecretLost`
event. Restore the Secret, or delete the key in the Gateway to have a new one
created with the default budget.

An instance's configuration lists at most 256 models. Its GitHub Copilot
models, at most 128, all stay; shared models fill the rest in name order, and
the instance's default model and the deployment's default are kept. The
operator reports the shared models it leaves out in an `AIGatewayModelsLimited`
event on the Agent.

Sources: [operator reconcile]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[Copilot proxy template]({{< repo "app/cetirun-chart/templates/copilot-proxy.yaml" >}}),
and [portal sign-in]({{< repo "app/cetirun-portal/internal/handler/copilot_login.go" >}}).

## GitHub Copilot in the portal

Instances routed through the AI Gateway use the sign-in above instead of the
flow in this section.

An existing instance can discover models using the Copilot account signed in
on that instance. The frontend requires a `Ready` OpenClaw or Hermes instance,
an access URL, and its authentication-helper key. Complete Copilot login in
the portal, open the instance's settings, refresh the catalog if needed, and
save the selection.

New instances have no authenticated Copilot identity, so creation offers manual
entry or the deployment default. Manual entry remains available if the catalog
cannot be loaded. A missing model causes a warning, not a guarantee that saving
it will work: the provider decides whether a request is authorized.

Changing the model updates the custom resource, causes reconciliation, and can
restart the gateway. Expect a brief interruption. Clear the per-instance
selection to resume the deployment default. The UI decides whether to show the
picker from the selected team's provider, falling back to `github_copilot`.
It does not resolve deployment-wide or arbitrary instance Helm provider
overrides. If those differ, check the effective provider before editing a model.

Sources: [model picker]({{< repo "app/cetirun-portal/web/src/components/CopilotModelPicker.tsx" >}}),
[team-provider hook]({{< repo "app/cetirun-portal/web/src/hooks/useTeams.ts" >}}),
and [auth/catalog hooks]({{< repo "app/cetirun-portal/web/src/hooks/useCopilotAuth.ts" >}}).

## Persistence and precedence

`Agent.spec.model` is an optional Copilot model ID, at most 256
characters. An empty `model` string in a portal PATCH clears the override.
The API stores the string without checking it against a live catalog or
rejecting it for non-Copilot providers; the operator decides whether to apply it.

For the effective Copilot provider, the operator applies a non-empty
`spec.model` after custom Helm values.
Otherwise it uses `cetirun.defaultModel`, configured through the operator chart,
or `--default-model` / `CETIRUN_DEFAULT_MODEL` at process startup. The default
is `gpt-6-astra`. Setting a default does not grant model access; platform
operators must choose a model authorized for their
users. Changes to the deployment default trigger reconciliation for dependent
instances.

The standalone agent chart requires `secrets.model` and has no baked-in default.
For an operator-managed instance, keep provider-specific settings under
`spec.helm.values.secrets`. Setting only `secrets.model` there does not override
a non-empty deployment Copilot default or `spec.model`.

Sources: [model precedence]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[default-change detection]({{< repo "app/cetirun-operator/internal/controller/teams.go" >}}),
[operator defaults]({{< repo "app/cetirun-operator/chart/values.yaml" >}}),
and [portal PATCH handler]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

## OpenAI and Anthropic

Set the provider and model through reviewed Helm values, and prepare the
namespace-local credential Secret separately. This **standalone chart values
fragment** assumes that Secret already exists; it does not provision credentials:

```yaml
secrets:
  provider: openai
  model: your-authorized-model-id
  existingSecret: provider-and-gateway
```

`github_copilot`, `openai`, and `anthropic` are the chart's provider choices.
For OpenClaw, `secrets.apiBase` renders a custom non-Copilot provider base URL.
Hermes does not use `secrets.apiBase`. Configuring a custom Hermes endpoint
requires runtime-specific configuration and verification.

`secrets.apiKey` populates a Secret only when the chart creates that Secret.
The operator normally sets `secrets.existingSecret`, skipping the entire Secret
template. Putting an API key into Helm values in that mode does not update the
existing Secret. OpenClaw reads `MODEL_API_KEY` from that Secret as its provider
key. For Hermes, the operator builds `hermes.env` from `OPENAI_API_KEY` or
`ANTHROPIC_API_KEY`, falling back to `MODEL_API_KEY`.

Avoid credentials in command-line arguments or checked-in values. Review
[the Secret template]({{< repo "app/cetirun-chart/templates/secret.yaml" >}})
and [operator Secret handling]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
for the runtime's required gateway, auth-helper, and API-server keys. A Copilot
`spec.model` does not override another provider's Helm model. The
[OpenClaw]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}})
templates are the source for provider rendering.

## Catalog architecture

```text
Portal browser → /<instance>/cetirun-auth/v1/models
               → GitHub token exchange
               → token-derived GitHub Copilot model endpoint
               → filtered model identifiers and metadata
```

The in-pod authentication service reads the instance's saved GitHub credential,
exchanges it for a Copilot API token, and requests `/models` from the
token-derived Copilot API origin. An invalid or absent proxy host falls back to
the fixed public Copilot API origin. The service caches successful results for
five minutes, keyed to the saved credential. Pressing **Refresh** refetches the
helper endpoint; it does not bypass that server cache.

Catalog filtering accepts advertised `chat/completions`, `responses`, and
`messages` endpoints, with or without `/v1`. It removes explicit non-chat
entries, disabled picker entries, account-router IDs, and duplicates. Entries
with missing endpoint metadata can remain. Returned `supportedEndpoints`
describe the provider catalog, **not** endpoints implemented by the instance
or proof that its runtime supports every selected model.

Only model IDs, display names, recognized endpoint metadata, and source/cache
information are returned; provider tokens and the full catalog are not.

The service uses the instance `CETIRUN_AUTH_KEY` for bearer authentication.
The browser still handles an instance credential; keep authenticated sessions
private. After provider login, the portal invalidates its auth and catalog caches.

## When discovery fails

| Response | Next step |
| --- | --- |
| `not_authenticated` | Sign in to Copilot on this instance |
| `catalog_forbidden` | Check entitlement and provider policy |
| `catalog_reauthenticate_required` | Complete the portal login flow again |
| `catalog_unavailable` | Retry later or enter an authorized model ID manually |

Do not print or paste token files to diagnose these errors. Verify model access
with the instance owner's authorized provider account. See the
[auth service source]({{< repo "app/cetirun-chart/configs/cetirun-auth-service.py" >}}).
