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
OpenClaw and Hermes instances take all their models from the Gateway, from two
sources:

- **Shared models** are the models an administrator configures in the Gateway
  chart's `models`, which gives each its own deployment ID and access group.
  They keep the administrator's names. Instances without a policy are offered
  all of them, and policy-bound instances those their AgentPolicy grants. A model
  added in the Gateway's console is shared only with the access group
  `cetirun-shared`, and only with instances without a policy.
  While the Gateway blocks requests for unpriced models, as it does by default
  (`blockUnpricedModels`), the operator offers only shared models with a price.
  It reads the setting from the Gateway, so a change made in the Gateway's
  console counts too. To offer a model that is free on purpose, set its zero
  price in `litellm_params`; a zero in `model_info` alone cannot be told apart
  from a missing price.
- **GitHub Copilot** models come from the owner's own Copilot subscription,
  through a small proxy per instance that only the Gateway can reach. They
  appear as `copilot/<model>`. The operator's `aiGateway.copilotProxy.enabled`
  turns the proxy off for the whole deployment.

Each instance's key reaches only its own access groups: those of its shared
models, with `cetirun-shared` for an instance without a policy, and the
instance's own Copilot group, so its `copilot/<model>` names reach its own proxy.
The Gateway checks router fallbacks against the key as well
(`operatorIntegration`). Keep teams and keys in the Gateway console limited to
access groups: a team or key allowed every model, or a `copilot/` name
directly, spreads requests for that name over every instance's proxy.

The create form offers both sources, on by default, and a new OpenClaw or Hermes
instance needs a model to use: shared models while at least one is offered, or
GitHub Copilot while the deployment offers it. GitHub Copilot stays on while
shared models are off or none is offered, and shared models stay on without
GitHub Copilot. With neither source, the portal creates no OpenClaw or Hermes
instance and asks for a shared model. It reads the sources from the catalog the operator
publishes, so it also waits until the catalog exists. The catalog also lists
the agent types the operator's default agent chart routes through the Gateway:
an earlier chart routes OpenClaw but not Hermes, and the portal then creates no
Hermes instance and asks for the agent chart to be upgraded. Until the
operator has read that chart, the portal creates no OpenClaw or Hermes
instance. Every new instance needs a Team.

To add Copilot models, open the instance and choose **Sign in with GitHub**. The
portal runs the GitHub device-code sign-in, stores the token for the instance's
proxy, and records the account's chat models. They appear once the proxy has
started, usually within a few minutes, and the instance restarts to pick them
up. A model the Gateway has no price for is listed as not offered: it would
escape the instance's budget. The token and the models carry the same sign-in
generation, and the operator uses models only with the token they came with,
once the portal has completed that sign-in: if a sign-in stops or is cancelled
after storing its token, the instance offers no Copilot models and reports
`SignInIncomplete` until you sign in again. Each new sign-in also
restarts the proxy: it otherwise keeps the Copilot token it exchanged until
that token expires, so another GitHub account would take effect only then.

After an upgrade from a version whose portal did not yet record completed
sign-ins, a sign-in that portal finished has a generation but no completion.
At the instance's next verification, within ten minutes of the upgrade, the
operator removes its Copilot routes and reports `SignInIncomplete`. Shared
models stay, but an instance with only GitHub Copilot has no models until its
owner chooses **Sign in with GitHub** again on the instance overview.

An instance that signed in to GitHub Copilot inside the agent, before it took
its models from the Gateway, still has that sign-in in its HOME, where the agent
can read it. On its first start through the Gateway, after `doctor`, the
`retire-copilot-sign-in` init container removes it and keeps every other
credential. Through the OpenClaw CLI, it removes the `github-copilot:github`
auth profile and the `GITHUB_COPILOT_TOKEN*` secret-store entries of OpenClaw's
device login, overwriting each entry first because a removed entry keeps its
value for 30 days, and then compacts OpenClaw's databases. It also removes the
`COPILOT_GITHUB_TOKEN` and `OPENCLAW_GITHUB_TOKEN` lines of `~/.openclaw/.env`,
and the profile from the `auth-profiles.json` files of earlier OpenClaw
versions and the copies `doctor` archived after importing them. A step that
fails is logged with OpenClaw's reason and tried again at the next start; it
does not keep the instance from starting. Once every step has succeeded, the
HOME is marked, and later starts leave it alone. With `doctor` turned off,
OpenClaw refuses its auth store while earlier profile files wait for import,
so the profile goes at a start after the import. The token is not carried
over: the owner signs in again with **Sign in with GitHub**.

A Hermes instance on the Gateway takes its models through a provider named
`cetirun-gateway` in `~/.hermes/config.yaml`. The chart replaces that provider
and the model choice at every start, so a model the Gateway stops offering
leaves the list. Hermes reads the instance's key from `CETIRUN_GATEWAY_API_KEY`,
which the chart sets from the key Secret; the key is in neither file. Because
Hermes loads `~/.hermes/.env` over its environment, a value of that variable
saved there is removed. The instance runs no in-agent sign-in helper. Once per
HOME, on its first start through the Gateway, init removes the GitHub Copilot
sign-in made inside the agent before: the `COPILOT_GITHUB_TOKEN` line of
`~/.hermes/.env` and Hermes' cache of the Copilot token it exchanged. A
`COPILOT_GITHUB_TOKEN` in the instance Secret is not copied into
`~/.hermes/.env` on the Gateway either. Every other credential, such as
`GITHUB_TOKEN`, stays. Hermes' auxiliary tasks, such as context compression,
use the instance's model unless configured otherwise.
A Hermes instance pinned to a chart version without this provider keeps its
own provider and sign-in until its chart is upgraded; its owner then signs in
again with **Sign in with GitHub**.

Each instance has its own virtual key with a monthly budget. The instance page
lists its models and sets the default; saving a new default restarts the
instance. When an administrator adds or removes a shared model, changes its
limits, or changes whether it is offered through its price or the Gateway's
setting, the operator updates every instance that uses shared models, which
restarts them. A restarting instance shows as provisioning for a short while,
and its UI is unavailable until it is ready again.

OpenClaw also sends short internal tasks, such as generated session titles and
activity recaps, to a utility model. On the Gateway, an instance's utility
model is its default model: OpenClaw would otherwise pick OpenAI's small model,
which the Gateway does not offer. These calls count against the instance's
key like any other.

The key's value is kept in the instance's `ai-gateway-virtual-key` Secret. If
that Secret is lost while the key still exists in the Gateway, the operator
does not replace the key, which would reset its spend, restore the default
budget and unblock a blocked key. The Agent instead reports `AIGatewayReady`
as false with the reason `KeySecretLost`, with an `AIGatewayKeySecretLost`
event. Restore the Secret, or delete the key in the Gateway to have a new one
created with the default budget.

With `operatorIntegration` enabled, the Gateway sets
`general_settings.user_api_key_cache_ttl` to zero. Each new virtual-key
request checks current database authorization instead of reusing a previous
worker's cached grant. Redis coordination for budgets and rate limits stays
enabled, and response caching remains off unless requested. Standalone
Gateways retain their existing authentication-cache defaults.

This trades extra database reads and dependence on database availability for
fresh authorization. Requests can return service-unavailable responses while
database connections recover. Allow the Gateway rollout to finish on every replica
before relying on the new setting. Requests already authorized or in flight
are not cancelled by a subsequent key change. This is not a guarantee about
stopping Agent processes or revoking credentials held by other services.

An instance's configuration lists at most 256 models. Its GitHub Copilot
models, at most 128, all stay; shared models fill the rest in name order, and
the instance's default model and the deployment's default are kept. The
operator reports the shared models it leaves out in an `AIGatewayModelsLimited`
event on the Agent.

Sources: [operator reconcile]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[shared-model catalog]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_catalog.go" >}}),
[Copilot proxy template]({{< repo "app/cetirun-chart/templates/copilot-proxy.yaml" >}}),
and [portal sign-in]({{< repo "app/cetirun-portal/internal/handler/copilot_login.go" >}}).

## GitHub Copilot in the portal

Instances routed through the AI Gateway use the sign-in above instead of the
flow in this section.

This flow reaches an in-agent sign-in helper. Hermes instances off the Gateway
keep it: in a deployment that has not enabled it, or pinned to a chart without
Hermes' Gateway provider. So do OpenClaw instances pinned to a chart without the
per-instance Copilot proxy. OpenClaw charts with the proxy carry no helper:
their models come only through the AI Gateway. In a deployment that has not
enabled the Gateway, such an instance has no model sign-in; the operator
reports `AIGatewayReady` as false with the reason `AIGatewayDisabled`, and the
portal does not offer this flow. The operator records whether an instance's workload serves the helper in
`status.signInHelper`, and routes `/<instance>/cetirun-auth/` only where it
does.

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
