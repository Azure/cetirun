---
title: "Choose a model"
description: "Choose shared models or GitHub Copilot through the AI Gateway."
weight: 10
---

Configure model access through the AI Gateway. The platform does not configure
direct provider credentials for Agents.

## Models through the AI Gateway

The platform AI Gateway is part of every platform installation: the operator's
`aiGateway.enabled` and the portal's `features.aiGatewayEnabled` are required.
OpenClaw, Hermes, and approved Custom instances take their platform-provided
models from the Gateway, from two sources:

- **Shared models** are the models an administrator configures in the Gateway
  chart's `models`, which gives each a deployment ID and an access group
  derived from its name. They keep the administrator's names. Instances without
  a policy are offered all of them, and policy-bound instances those their
  AgentPolicy grants. A model added in the Gateway's console is shared only
  with the access group `cetirun-shared`, and only with instances without a
  policy.
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
while it restarts after a sign-in, the requests sent to it fail. The Gateway's
health checks would take it out of rotation at their next probe, but they leave
Copilot models alone unless the deployment turns their probes on; see below.

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

The Gateway's health checks leave Copilot models alone by default. Each probe
is a real call on the owner's Copilot allowance: at the default interval of
five minutes, about 288 calls a day for each Copilot model of each instance.
The operator's `aiGateway.copilotProxy.healthChecks.enabled` turns the probes
on for every instance. An instance's Copilot route that fails its probe is then
taken out of rotation; that changes where requests go only for a model that
shared deployments serve too.

Each instance's key reaches only its own access groups: those of the shared
models it may use, with `cetirun-shared` for an instance without a policy, and
the instance's own Copilot group, so its Copilot models
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
shared model but need not be listed. An instance without a Policy gets every
router whose models are all shared models. A router the operator cannot read
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

A shared model can also be a router that the administrator configures in the
Gateway: for each request it picks one of several shared models, such as a
cheaper one for simple requests. An instance that may use the router reaches
every model the router can pick. See the Gateway chart's
[Smart routing]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#smart-routing)
section; its
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers)
part has Log Analytics queries that show how requests were routed.

The create form offers both sources, on by default, and every new Agent needs a
model to use: shared models while at least one is offered, or
GitHub Copilot while the deployment offers it. GitHub Copilot stays on while
shared models are off or none is offered, and shared models stay on without
GitHub Copilot. With neither source, the portal creates no Agent and asks for a
shared model. It reads the sources from the catalog the operator publishes, so
it also waits until the catalog exists. The catalog lists the agent types the
selected runtime chart routes through the Gateway. The portal creates an Agent
only after it has read a compatible chart. A Team remains optional; when
selected, its Agent Policy further restricts the model sources.

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

## Runtime configuration

OpenClaw and Hermes receive their model list and virtual key from the AI
Gateway on every start. Hermes writes the Gateway provider as
`cetirun-gateway` in `~/.hermes/config.yaml` and reads its key from
`CETIRUN_GATEWAY_API_KEY`; the key is in neither HOME file. A model removed from
the Gateway no longer appears in the Agent configuration. This routing does not
prevent arbitrary user code in a Custom image from making its own outbound
network connection; use network policy when that control is required.

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
Changing the operator's `aiGateway.copilotProxy.healthChecks.enabled`, or
upgrading with it off from a version that let the Gateway probe Copilot models,
registers every instance's Copilot models again at its next verification,
within about ten minutes, without restarting instances. Each route is briefly
unavailable while it is deleted and added again.

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
is sent to, and the calls of an `llm` or `jev` classifier are charged too; the
router's own price in the Gateway only satisfies the check for unpriced models.
The key's budget applies as usual. A per-model rate limit is matched against
the model name the instance requests, the router's internal name, so a
per-model limit on a tier model does not apply to requests that go through a
router.

An instance that cannot use one of its Team's routers shows why on its page:
for example, the Policy does not list the router, a tier uses a model the
Policy does not allow, or a GitHub Copilot tier waits for its owner to sign in.
Before the operator has registered a router, the instance shows it as not
registered yet.

## GitHub Copilot in the portal

Every harness uses the Portal sign-in described above. The Portal stores the
owner's Copilot credential through the Gateway management flow; it does not
call an in-agent authentication helper. Select a model from the Gateway
catalog after the owner completes sign-in and the operator publishes access.
If the Gateway or catalog is unavailable, resolve that condition before
creating an Agent or selecting a new model.

Changing the model updates the custom resource, causes reconciliation, and can
restart the Agent. Expect a brief interruption. Clear the per-instance
selection to resume the Gateway default. The Portal offers only models in the
Gateway catalog that the selected optional Team Policy permits.

## Persistence and precedence

`Agent.spec.model` is an optional Gateway model ID, at most 256 characters. An
empty `model` string in a Portal PATCH clears the override. The Portal validates
new selections against the published Gateway catalog; the operator enforces the
final key grant. Deployment defaults and per-Agent selections do not bypass
Gateway authorization.

Platform-managed Agent charts do not accept provider-specific Helm values or
provider API keys. Configure providers, credentials, permissions, budgets, and
rate limits in the AI Gateway instead.

## Catalog architecture

The operator publishes the Gateway model catalog and each Agent's allowed
model access. The Portal reads those catalogs through its authenticated API.
Shared models come from Gateway configuration; Copilot models become available
after the owner signs in through the Portal. Provider credentials are not
returned to the browser or the Agent workload.

## When discovery fails

Check the Agent's AI Gateway conditions and the selected
Team Policy, if any. Complete Portal sign-in again when authentication has
expired, or retry after a temporary Gateway failure. Do not print or paste
token files to diagnose these errors.
