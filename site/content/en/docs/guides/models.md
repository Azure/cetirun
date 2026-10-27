---
title: "Choose a model"
description: "Choose models authorized by your Team Policy through the AI Gateway."
weight: 10
---

Choose an authorized model in the portal. OpenClaw, Hermes, and Custom Agents
use the same path: `Agent → AI Gateway → Model`. Every Agent requires a Team
with an Agent Policy and Sandbox. Configure provider credentials on the Gateway.

## Models through the AI Gateway

The platform AI Gateway is part of every platform installation: the operator's
`aiGateway.enabled` and the portal's `features.aiGatewayEnabled` are required.
All supported harnesses take their platform-managed models from the Gateway.
The Team Policy determines which of these sources an Agent may request:

- **Shared models** are the models an administrator configures in the Gateway
  chart's `models`, which gives each a deployment ID and an access group
  derived from its name. They keep the administrator's names. Each Agent is
  offered only the models its Agent Policy grants. Unmanaged console models
  and the former `cetirun-shared` legacy grant do not bypass that Policy.
  While the Gateway blocks requests for unpriced models, as it does by default
  (`blockUnpricedModels`), the operator offers only shared models with a price.
  It reads the setting from the Gateway, so a change made in the Gateway's
  console counts too. To offer a model that is free on purpose, set its zero
  price in `litellm_params`; a zero in `model_info` alone cannot be told apart
  from a missing price.
- **GitHub Copilot** models come from the owner's own Copilot subscription,
  through a small proxy per instance that only the Gateway can reach. The
  operator's `aiGateway.copilotProxy.enabled` turns the proxy off for the whole
  deployment.

An instance sees one name per model. A Copilot model that is the same model as
a shared model the instance may use takes that shared model's name, and the
instance lists it once. The operator compares the Copilot model's ID with each
deployment's `model_info.base_model`, or its `litellm_params.model` when the
Gateway's price table prices that name, ignoring the provider prefix, letter
case, and dots versus dashes. A Copilot model with a date version, such as
`gpt-4.1-2025-04-14`, is never merged, and when several shared models match,
the first name in sort order is used. Requests for the name are then served by
the shared deployments or by the instance's own Copilot subscription, as the
Gateway's routing strategy chooses. The instance's Copilot route copies the
shared model's routing fields (`order`, `weight`, `rpm`, `tpm`), so standby
deployments stay in reserve as configured. Either way, a request counts against
the instance's budget at the model's list price. When Copilot fails, the Gateway
takes the instance's Copilot route out of rotation for a few seconds and sends
its requests to the shared deployments: at once after a 429, and for other
errors once most of the route's recent requests have failed, so a few requests
can fail first. While the instance's proxy itself cannot be reached, for example
while it restarts after a sign-in, the requests sent to it fail until the
Gateway's next health check takes it out of rotation.

Any other Copilot model keeps its own ID, such as `gpt-5.5`, and only the
instance's Copilot subscription serves it: a 429 from Copilot reaches the
instance as it is, and the next request goes to Copilot again. If a shared model
already has that name but is another model, the Copilot model is left out, and
the instance page lists it as not offered. If it is the same model but one the
instance may not use, the instance keeps that name for its own Copilot model,
which only its own key reaches.
The instance's key reaches a Copilot model only by that name; the
`copilot/<model>` names of earlier versions are removed from the key. An
instance whose default model is set as `copilot/<model>` uses the model's
current name.

The Gateway's health checks probe every Copilot model too, every five minutes by
default. Each probe is a real call, so it spends the owner's Copilot allowance.

Each instance's key reaches only its own access groups: those of the shared
models its Policy grants and the instance's own Copilot group, so its Copilot models
reach only its own proxy, also under a shared model's name. The Gateway checks
router fallbacks against the key as well (`operatorIntegration`), and its guard
keeps every key that does not hold an instance's Copilot group, such as the
master key or a key allowed every model, away from that instance's Copilot
routes. Keep teams and keys in the Gateway console limited to access groups.

A shared model can also be a router that the administrator configures in the
Gateway: for each request it picks one of several shared models, such as a
cheaper one for simple requests. An instance gets such a router only when it
may use every model the router sends requests to, as for a Team's routers. With
an Agent Policy, the Policy must list the router and every model of its tiers,
its default model and a context compaction model; a classifier model must be a
shared model but need not be listed. Every Agent requires a Policy; its grants
determine the available routers. A router the operator cannot read
completely, such as one with a setting it does not know or a classifier plugin,
is not given to instances with a Policy. A router an instance cannot use is
left out of its key and its models, and an instance whose Policy lists the
router shows why on its page. A changed router reaches the instances' keys
within about two minutes; it does not change the Policy's Gateway projection.
See the Gateway chart's
[Smart routing]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#smart-routing)
section; its
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers)
part has Log Analytics queries that show how requests were routed. A Team's own
routers are described under
[Smart-routing models](#smart-routing-models).

The create form offers the model sources allowed by the selected Team Policy
and deployment. A new Agent needs at least one authorized source. The form
waits for the Gateway catalog and a supported runtime profile; an older chart
without the Gateway contract must be upgraded before the Agent can execute.
Every new instance needs a Team and an allowed Sandbox. Custom images must
consume the injected Gateway URL, model, and virtual key through their runtime
contract; CetiRun no longer offers an agent-managed model mode.

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
An Agent pinned to a chart without the Gateway contract remains held until
its chart is upgraded. There is no direct-provider fallback.

Each instance has its own virtual key with a monthly budget. The instance page
lists its models and sets the default; saving a new default restarts the
instance. When an administrator adds or removes a shared model, changes its
limits, or changes whether it is offered through its price or the Gateway's
setting, the operator updates every instance that uses shared models, which
restarts them, and registers their Copilot models again under the names that
now apply. A change to a shared model's base model or routing fields alone
reaches the Copilot routes at each instance's next verification, within ten
minutes. A restarting instance shows as provisioning for a short while, and its
UI is unavailable until it is ready again. Other changes to a shared model,
such as its endpoint, provider parameters or price, keep the access groups
instances are granted: the Gateway chart derives a model's group from its name
alone. Instances keep using the model, and none of them restarts. After an
upgrade from a version that named Copilot models `copilot/<model>`, the
operator registers each instance's Copilot models under their new names at its
next verification, and the instance restarts with its new model list. A client
outside the instance that still requests `copilot/<model>` is refused and must
use the new name.
A Gateway upgrade does not interrupt instances either. Each Gateway pod keeps
the configuration it started with until it is replaced, and the operator waits
for the rollout to finish before it applies a changed model list, so instances
are updated once rather than once for each replaced pod.

OpenClaw also sends short internal tasks, such as generated session titles and
activity recaps, to a utility model. On the Gateway, an instance's utility
model is its default model: OpenClaw would otherwise pick OpenAI's small model,
which the Gateway does not offer. These calls count against the instance's
key like any other.

An instance governed by an Agent Policy can also carry request limits on its
key: parallel requests, requests per minute, tokens per minute, and per-model
limits. The Policy sets them for every Agent of its Teams, each on its own key.
The operator applies limit-only changes to the existing key without stopping or
restarting running Agents. Budget and model access changes still use the normal
policy reprojection fence. A request
over a limit receives HTTP 429 with a `retry-after` header. A tokens per minute
limit counts the tokens of finished requests; see the
[AgentPolicy rate limits](../../reference/crds/#agentpolicy-rate-limits)
reference.

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
the instance's default model, the deployment's default and the shared models
that are Copilot models too are kept. The
operator reports the shared models it leaves out in an `AIGatewayModelsLimited`
event on the Agent.

Sources: [operator reconcile]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[Copilot model names]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_copilot_merge.go" >}}),
[shared-model catalog]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_catalog.go" >}}),
[Gateway routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_shared_routers.go" >}}),
[Copilot proxy template]({{< repo "app/cetirun-chart/templates/copilot-proxy.yaml" >}}),
and [portal sign-in]({{< repo "app/cetirun-portal/internal/handler/copilot_login.go" >}}).

### Smart-routing models

A Team can also define its own smart-routing models in the Portal; see
[Configure a team](../teams/#smart-routing). An instance that may use one
lists it under its name, such as `auto`, and the create form offers the names
the Team's Policy allows as the default model. The instance's configuration
carries the router's internal Gateway name as the model ID: OpenClaw shows
the router's name, while Hermes lists the internal name. Choosing the router's
name as the default model selects it. The instance is configured with the
smallest context window and output limit among the router's tier models that
the Gateway reports, so a request sized for the router fits every tier. When
the owner's GitHub Copilot model is merged into a tier's shared model, the
instance's requests for that tier may reach its Copilot route too, so the
router's limits include the route's, as the instance's entry for that shared
model does; the same holds for a router configured in the Gateway.

Each request is charged to the instance's key at the price of the tier model it
is sent to, and a model classifier's calls are charged too; the router's own
price in the Gateway only satisfies the check for unpriced models. The key's
budget applies as usual. A per-model rate limit is matched against the model
name the instance requests, the router's internal name, so a per-model limit on
a tier model does not apply to requests that go through a router.

An instance that cannot use one of its Team's routers shows why on its page:
for example, the Policy does not list the router, a tier uses a model the
Policy does not allow, or a GitHub Copilot tier waits for its owner to sign in.
Before the operator has registered a router, the instance shows it as not
registered yet.

## Persistence and precedence

`Agent.spec.model` selects an authorized Gateway model or Team router name,
up to 256 characters. An empty `model` string in a portal PATCH clears the
selection. The operator resolves the selected model against the effective
Policy and Gateway catalog. When no instance model is selected, it considers
`cetirun.defaultModel` and the authorized catalog; setting a deployment default
does not grant access to that model.

Changing the model reconciles the Agent and can restart it. A Team change
preflights the target Policy and model before stopping the source execution.
An omitted model in a Team-change request keeps the current selection; choose
a different model when the target Policy does not permit it. Team changes keep
an existing per-Agent monthly budget override and reject a target Policy whose
maximum cannot honor it; administrators change that override only while the
Agent is paused.

Provider fields and API keys in Agent or Team Helm values are no longer model
configuration inputs. The operator projects the Gateway endpoint and the
Agent's separate `ai-gateway-virtual-key` Secret. The runtime authentication
Secret remains separate: it authenticates the Agent server or UI, not model
requests.

Sources: [model precedence]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[Gateway grants]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
and [portal PATCH handler]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

## Provider configuration

Administrators configure OpenAI, Anthropic, and other providers on the AI
Gateway and grant their published model names through Agent Policies. Keep
provider credentials in Gateway-managed Secrets. Agents receive their own
budgeted virtual key; they receive no platform-provided direct provider key.

The standalone runtime chart also requires a Gateway URL and virtual-key
Secret. This fragment assumes both referenced Secrets already exist:

```yaml
secrets:
  provider: openai
  model: your-authorized-model-id
  apiBase: http://gateway.example.svc.cluster.local:4000/v1
  existingSecret: agent-runtime-auth
aiGateway:
  enabled: true
  keySecretName: agent-model-key
  keySecretKey: api-key
```

Here `openai` denotes the Gateway-compatible API transport. It does not select
a direct provider. `secrets.apiKey` must remain empty. Custom executable code
can perform its own network requests; this runtime contract is not a network
firewall against every possible external model call.

## When discovery fails

Check the selected Team's Policy, the published Gateway catalog, and the
Agent's `AIGatewayReady` condition. Missing Team/Policy/Sandbox bindings are
rejected or held; they do not enable a legacy execution path.

For GitHub Copilot, complete **Sign in with GitHub** on the instance overview.
The Portal stores the token for the Gateway's per-instance proxy and publishes
its catalog. `SignInIncomplete` requires another completed sign-in. A model
without pricing or a Policy grant remains unavailable. Do not copy token files
or credentials into diagnostics.
