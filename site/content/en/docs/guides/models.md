---
title: "Choose a model"
description: "Select shared models or GitHub Copilot through the AI Gateway."
weight: 10
---

Configure model access through the AI Gateway. The platform does not configure
direct provider credentials for Agents.

## Models through the AI Gateway

The platform AI Gateway is part of every platform installation. The operator's
`aiGateway.enabled` and the portal's `features.aiGatewayEnabled` are necessary.
OpenClaw, Hermes, and approved Custom instances take their platform-supplied
models from the Gateway. These models come from two sources:

- **Shared models** are the models that an administrator configures in the
  Gateway chart's `models`. The chart gives each model a deployment ID and an
  access group that is derived from its name. Shared models keep the names from
  the administrator. All shared models are offered to instances without a
  policy. Policy-bound instances are offered the shared models that their
  AgentPolicy grants. A model that is added in the Gateway's console is shared
  only with the access group `cetirun-shared` and only with instances without a
  policy.

  By default, the Gateway blocks requests for unpriced models
  (`blockUnpricedModels`). When the Gateway blocks these requests, the operator
  offers only shared models with a price. The operator reads this setting from
  the Gateway. As a result, a change that is made in the Gateway's console also
  counts. To offer a model that is free on purpose, set its zero price in
  `litellm_params`. A zero in `model_info` alone cannot be told apart from a
  price that is not set.
- **GitHub Copilot** models come from the owner's own Copilot subscription.
  They go through a small proxy for each instance, and only the Gateway can
  reach this proxy. The operator's `aiGateway.copilotProxy.enabled` disables
  the proxy for the whole deployment.

An instance sees one name for each model. If a Copilot model is the same model
as a shared model that the instance can use, it takes the name of that shared
model. The instance lists it one time.

The operator compares the Copilot model ID with each deployment's
`model_info.base_model`, or with its `litellm_params.model` when the Gateway's
price table prices that name. The comparison ignores the provider prefix,
letter case, and the difference between dots and dashes. A Copilot model with
a date version, such as `gpt-4.1-2025-04-14`, is never merged. If several
shared models match, the first name in sort order is used.

The shared deployments or the instance's own Copilot subscription then serve
requests for the name. The Gateway's routing strategy selects between them. The
instance's Copilot route copies the routing fields of the shared model
(`order`, `weight`, `rpm`, `tpm`). As a result, standby deployments stay in
reserve as configured. In both cases, a request counts against the instance's
budget at the list price of the model.

When Copilot fails, the Gateway takes the instance's Copilot route out of
rotation for a few seconds and sends its requests to the shared deployments.
This occurs immediately after a 429. For other errors, it occurs after most of
the recent requests of the route fail. As a result, a few requests can fail
first.

When the instance's proxy itself cannot be reached, the requests that are sent
to it fail. For example, this occurs when the proxy restarts after a sign-in.
The Gateway's health checks leave Copilot models alone unless the deployment
enables their probes. With these probes enabled, the health checks take the
proxy out of rotation at their next probe. See below.

Any other Copilot model keeps its own ID, such as `gpt-5.5`. Only the
instance's Copilot subscription serves this model. A 429 from Copilot reaches
the instance without change, and the next request goes to Copilot again.

If a shared model already has that name but is another model, the Copilot model
is left out. The instance page lists it as not offered. If that shared model is
the same model but the instance cannot use it, the instance keeps that name for
its own Copilot model. Only the instance's own key reaches this Copilot model.

For a policy-bound instance, the Agent Policy's shared models also apply to
the owner's Copilot models that are the same models. Such a Copilot model is
offered only when the Policy allows a shared model that is that model. The
comparison is the same as for the names above. A Copilot model with a date
version is the same model only as the same version. The instance page lists
such a model as not allowed by the Agent Policy.

For example, the shared model `enterprise-sol` has the `base_model`
`azure/gpt-6-sol`, and no other shared model is that model. If the Policy does
not allow `enterprise-sol`, the instance does not get the owner's Copilot
`gpt-6-sol`. For a Copilot model that is not the same model as a shared model,
only the Policy's **Allow GitHub Copilot** is necessary. An example is a Claude
model when the Gateway has no Claude deployment.

When the Policy no longer allows such a model, the operator removes the model's
route from the Gateway before it updates the instance's key. If the Gateway
cannot remove the route, the key cannot reach any of the owner's Copilot models
until the route is removed. The same occurs while the operator cannot read the
Gateway's models or prices. The operator tries again about every 15 seconds.
Shared models continue to work, and the instance page tells why Copilot is not
available.

The instance's key reaches a Copilot model only by that name. The
`copilot/<model>` names of earlier versions are removed from the key. An
instance whose default model is set as `copilot/<model>` uses the current name
of the model.

By default, the Gateway's health checks leave Copilot models alone. Each probe
is a real call on the owner's Copilot allowance. At the default interval of
five minutes, this is about 288 calls a day for each Copilot model of each
instance. The operator's `aiGateway.copilotProxy.healthChecks.enabled` enables
the probes for every instance. With the probes enabled, an instance's Copilot
route that fails its probe is taken out of rotation. This changes where
requests go only for a model that shared deployments also serve.

Each instance's key reaches only its own access groups. These are the groups of
the shared models that the instance can use and the instance's own Copilot
group. For an instance without a policy, these groups include `cetirun-shared`.
As a result, the Copilot models of the instance reach only its own proxy, also
under the name of a shared model.

The Gateway also examines router fallbacks against the key
(`operatorIntegration`). Its guard keeps every key that does not hold an
instance's Copilot group away from that instance's Copilot routes. Examples are
the master key and a key that is allowed every model. Keep teams and keys in
the Gateway console limited to access groups.

A shared model can also be a router that the administrator configures in the
Gateway. For each request, the router picks one of several shared models, such
as a cheaper model for simple requests. An instance gets such a router only
when it can use every model that the router sends requests to. This is the
same as for a Team's routers.

With an Agent Policy, the Policy must list the router, every model of its
tiers, its default model, and a context compaction model. A classifier model
must be a shared model, but it is not necessary to list it. An instance without
a Policy gets every router whose models are all shared models.

If the operator cannot read a router completely, instances with a Policy do not
get it. Examples are a router with a setting that the operator does not know
and a router with a classifier plugin. If an instance cannot use a router, the
router is left out of the key and the models of the instance. If the Policy of
that instance lists the router, the instance shows why on its page.

A changed router reaches the keys of the instances within about two minutes. It
does not change the Gateway projection of the Policy. See the Gateway chart's
[Smart routing]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#smart-routing)
section. Its
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers)
part has queries of the ADX-Mon collection that show how requests were routed.
A Team's own routers are described under
[Smart-routing models](#smart-routing-models).

The create form offers both sources, and both are on by default. Every new
Agent must have a model to use: shared models when at least one is offered, or
GitHub Copilot when the deployment offers it. GitHub Copilot stays on when
shared models are off or when no shared model is offered. Shared models stay on
without GitHub Copilot. With neither source, the portal creates no Agent and
asks for a shared model.

The portal reads the sources from the catalog that the operator publishes. As a
result, it also waits until the catalog exists. The catalog lists the agent
types that the selected runtime chart routes through the Gateway. The portal
creates an Agent only after it reads a compatible chart. A Team stays optional.
When a Team is selected, its Agent Policy further restricts the model sources.

To add Copilot models, open the instance. Then select **Sign in with GitHub**.
The portal runs the GitHub device-code sign-in, stores the token for the
instance's proxy, and records the account's chat models. The models appear
after the proxy starts, usually within a few minutes. Then the instance
restarts to get them.

GitHub keeps at most ten of these tokens for each GitHub account. Each sign-in
makes a new token. When an account has ten, the next sign-in revokes the
oldest. Sign-ins with other tools that use the same GitHub app can also count.

As a result, many sign-ins with one account can sign an older instance out of
Copilot. That instance's Copilot models stop within a day, but the instance
still shows Copilot as signed in. To restore it, sign in on that instance
again.

A model that the Gateway has no price for is listed as not offered, because the
instance's budget cannot limit that model.

The token and the models carry the same sign-in generation. The operator uses
models only with the token that they came with, after the portal completes that
sign-in. If a sign-in stops or is cancelled after it stores its token, the
instance offers no Copilot models. The instance reports `SignInIncomplete`
until you sign in again.

Each new sign-in also restarts the proxy. Without this restart, the proxy keeps
the Copilot token that it exchanged until that token expires. As a result,
another GitHub account takes effect only when that token expires.

## Runtime configuration

OpenClaw and Hermes receive their model list and virtual key from the AI
Gateway on every start. Hermes writes the Gateway provider as
`cetirun-gateway` in `~/.hermes/config.yaml` and reads its key from
`CETIRUN_GATEWAY_API_KEY`. OpenClaw writes the Gateway provider as `cetirun`
in `~/.openclaw/openclaw.json`, so its model references are
`cetirun/<model>`. The key is in neither HOME file. If a model is
removed from the Gateway, it no longer appears in the Agent configuration.
Arbitrary user code in a Custom image can make its own outbound network
connection, and this routing does not prevent it. If that control is
necessary, use network policy.

Earlier runtime charts wrote the Gateway provider for OpenClaw as `openai`. On
each start, OpenClaw changes the names of models under `openai` that it thinks
are retired, for example `gpt-4.1-mini`. The instance then asked the Gateway
for a model that the Gateway does not have. When you upgrade an OpenClaw
instance to a chart that writes `cetirun`, the instance restarts once. A
session that has a model under `openai/` uses the default model of the
instance again.

Each instance has its own virtual key with a monthly budget. The instance page
lists its models. For OpenClaw and Hermes, the page also sets the default. When
you save a new default, the instance restarts. Custom images select a model
from the models that are available through the Gateway. They make this
selection through their own configuration or Chat UI.

When an administrator does one of these actions, the operator updates the
affected Agents:

- adds or removes a shared model
- changes the limits of a shared model
- changes if a shared model accepts images
- changes if a shared model reasons
- changes if a shared model is offered, through its price or the Gateway's
  setting

The affected Agents are the standard Agents that use shared models, and the
Sandbox Agents whose Agent Policy allows the model. This update restarts their
instances. The operator also registers their Copilot models again under the
names that now apply.

> **Warning:** Before you remove a shared model, remove its name from
> `modelAccess.sharedModels.allowed` in each Agent Policy. If an Agent Policy
> allows a name that the Gateway catalog does not offer, the operator stops
> its Agents that use shared models. It also blocks the Gateway key of each of
> these Agents. Their `PolicyResolved` condition is `False` with the reason
> `DependencyUnavailable` and the message
> `the live AI Gateway catalog cannot resolve every authorized shared model`.

The operator also blocks and stops these Agents in these cases:

- The Gateway stops offering the model because the model has no price.
- A console deployment copies the access group of the model.
- An administrator removes or renames a Team router that an Agent Policy
  allows.

To recover, make the Gateway offer the model again, or remove its name from
the Agent Policy.

If only the base model or routing fields of a shared model change, the change
reaches the Copilot routes at each instance's next check. This occurs within
ten minutes. When an instance restarts, it shows as provisioning for a short
time. Its UI is unavailable until it is ready again.

Other changes to a shared model, for example to its endpoint, provider
parameters, or price, keep the access groups that instances are granted. The
Gateway chart derives the group of a model only from its name. Instances
continue to use the model, and none of them restarts.

After an upgrade from a version that named Copilot models `copilot/<model>`,
the operator registers each instance's Copilot models under their new names.
This occurs at the next check of the instance. Then the instance restarts with
its new model list. A client outside the instance that still requests
`copilot/<model>` is refused. The client must use the new name.

A Gateway upgrade also does not interrupt instances. Each Gateway pod keeps the
configuration that it started with until it is replaced. Before the operator
applies a changed model list, it waits for the rollout to finish. As a result,
instances are updated one time, not one time for each replaced pod.

A change to the operator's `aiGateway.copilotProxy.healthChecks.enabled`
registers every instance's Copilot models again. An upgrade with this setting
off, from a version that let the Gateway probe Copilot models, does the same.
This occurs at the next check of each instance, within about ten minutes.
Instances do not restart. Each route is briefly unavailable while it is
deleted and added again.

OpenClaw also sends short internal tasks to a utility model, for example
generated session titles and activity recaps. On the Gateway, the utility model
of an instance is its default model. Otherwise, OpenClaw picks the small model
of the provider, which the Gateway does not offer. These calls count against the
instance's key like any other call.

An instance governed by an Agent Policy can also carry these request limits on
its key:

- parallel requests
- requests per minute
- tokens per minute
- per-model limits

The Policy sets these limits for every Agent of its Teams, each on its own key.
The operator applies limit-only changes to the key that exists, and it does
not stop or restart running Agents. Budget and model access changes still use
the normal policy reprojection fence.

A request over a limit receives HTTP 429 with a `retry-after` header. A tokens
per minute limit counts the tokens of finished requests. See the
[AgentPolicy rate limits](../../reference/crds/#agentpolicy-rate-limits)
reference.

The value of the key is kept in the instance's `ai-gateway-virtual-key` Secret.
If that Secret is lost and the key still exists in the Gateway, the operator
does not replace the key. A replacement resets the spend of the key, restores
the default budget, and unblocks a blocked key. Instead, the Agent reports
`AIGatewayReady` as false with the reason `KeySecretLost`, with an
`AIGatewayKeySecretLost` event. Restore the Secret, or delete the key in the
Gateway. If you delete the key, a new key is created with the default budget.

When `operatorIntegration` is enabled, the Gateway sets
`general_settings.user_api_key_cache_ttl` to zero. Each new virtual-key request
examines the current database authorization. It does not reuse the cached grant
of a previous worker. Redis coordination for budgets and rate limits stays
enabled. Response caching stays off unless it is requested. Standalone Gateways
keep their authentication-cache defaults.

This trades more database reads and a dependence on database availability for
fresh authorization. While database connections recover, requests can return
service-unavailable responses. Before you rely on the new setting, let the
Gateway rollout finish on every replica. A later key change does not cancel
requests that are already authorized or in flight. This is not a guarantee
that Agent processes stop or that credentials held by other services are
revoked.

An instance's configuration lists at most 256 models. Its GitHub Copilot
models, at most 128, all stay. Shared models fill the rest in name order. The
instance's default model, the deployment's default, and the shared models that
are also Copilot models are kept. The operator reports the shared models that
it leaves out in an `AIGatewayModelsLimited` event on the Agent.

Sources: [operator reconcile]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[Copilot model names]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_copilot_merge.go" >}}),
[shared-model catalog]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_catalog.go" >}}),
[Gateway routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_shared_routers.go" >}}),
[Copilot proxy template]({{< repo "app/cetirun-chart/templates/copilot-proxy.yaml" >}}),
and [portal sign-in]({{< repo "app/cetirun-portal/internal/handler/copilot_login.go" >}}).

### Images

OpenClaw and Hermes send images with a request to the models that accept them.
The operator reads this from the Gateway (`supports_vision`). Refer to the
Gateway chart's
[Images]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#images)
section. A model accepts images only when every deployment of its name accepts
them. When the instance's own Copilot route has the same name, that route must
also accept them. A smart-routing model accepts images only when every model
that it sends requests to accepts them.

For the other models:

- Hermes first sends the image to the model in a separate call, and the model
  describes it.
- OpenClaw sends only text to these models. Another model that accepts images
  describes the image. The `computer` tool is not available with these models.

The instance's configuration lists the models that accept images
(`aiGateway.visionModels` of the runtime chart). A runtime chart without this
value sends images as before. The instance takes the value when you upgrade it
to a chart that has it.

### Reasoning

OpenClaw offers its reasoning levels for the models that reason. It sends the
level that the owner selects as the reasoning effort of each request. The
default level is `medium`. The operator reads from the Gateway which models
reason (`supports_reasoning`). Refer to the Gateway chart's
[Reasoning models]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#reasoning-models)
section.

The operator uses the same rules as for images:

- A model reasons only when every deployment of its name reasons.
- When the instance's own Copilot route has the same name, that route must
  also reason.
- A smart-routing model reasons only when every model that it sends requests
  to reasons. For a Team's router, the Team page shows the tier models that do
  not reason.

A GitHub Copilot model reasons only when the Gateway's price table says so and
it is a Claude or GPT model. The Copilot proxy refuses a reasoning effort for
the other models, such as Gemini and Grok.

For the other models, OpenClaw offers only the level `off`, and it sends no
reasoning effort. A model that reasons then uses its default effort.

Hermes does not use this information. It sends its own reasoning effort to each
model. The Gateway removes the effort from a request when no deployment that
can answer the request reasons.

The instance's configuration lists the models that reason
(`aiGateway.reasoningModels` of the runtime chart). A runtime chart without
this value configures OpenClaw as before. The instance takes the value when you
upgrade it to a chart that has it.

### When the budget runs out

Each instance's key has a monthly budget in US dollars. For an instance of a
policy-bound Team, the Policy sets the budget. For other instances, the key
gets the operator's `aiGateway.defaultBudget` when it is created. An
administrator can change this budget in the Gateway's console.

When the spend of the key reaches the budget, the Gateway refuses every
request of the key with HTTP 402. OpenClaw then reports a billing error of its
model provider. Hermes says to add credits with the provider. Neither message
says that the CetiRun budget is used up. The requests to the instance's GitHub
Copilot models count against the same budget, so they stop too.

The budget resets on the first day of each month, UTC, up to about ten minutes
late. To restore the instance before that, an administrator raises the
budget. For a policy-bound instance, the administrator raises the budget of the
Policy, which restarts its running Agents one time. As an alternative, the
administrator suspends the instance and sets its `modelAccess.monthlyBudgetUSD`,
up to the maximum of the Policy. Refer to
[AgentPolicy model access](../../reference/crds/#agentpolicy-model-access).

### Smart-routing models

In the linked Policy's **Allowed shared models** picker, administrators can
select router aliases from its Teams alongside Gateway catalog models. Each
alias shows its Team and registration state. Grant the shared tier models
separately, and allow Copilot when a tier requires it; selecting an alias does
not grant its underlying models automatically.

A Team can also define its own smart-routing models in the Portal. See
[Configure a team](../teams/#smart-routing). An instance that can use one of
these models lists it under its name, such as `auto-team`. The create form
offers the names that the Team's Policy allows as the default model, with
`(smart routing)` after each name.

The instance's key reaches the router under its name and under its internal
Gateway name. Hermes is configured with the router's name. OpenClaw is
configured with the internal name as the model ID, and it shows the router's
name.

If you select the router's name as the default model, you select the router.
The instance is configured with the smallest context window and output limit
among the router's tier models that the Gateway reports. As a result, a request
sized for the router fits every tier. When the owner's GitHub Copilot model is
merged into a tier's shared model, the instance's requests for that tier can
also reach its Copilot route. As a result, the router's limits include the
limits of the route, as the instance's entry for that shared model does.

A router configured in the Gateway gets its limits in the same way, from the
models of its tiers and from its default model. If the router sets its own
`max_input_tokens` and `max_output_tokens` in `model_info`, these values are
used instead.

Each request is charged to the instance's key at the price of the tier model
that it is sent to. The calls of an `llm` or `jev` classifier are also charged.
The router's own price in the Gateway only satisfies the check for unpriced
models. The key's budget applies as usual.

A per-model rate limit is matched against the model name that the Gateway
counts, which is the router's internal name. A limit that a Policy sets for the
router's name is also set under that internal name. A per-model limit on a tier
model does not apply to requests that go through a router.

An instance that cannot use one of its Team's routers shows why on its page.
For example:

- The Policy does not list the router.
- A tier uses a model that the Policy does not allow.
- A GitHub Copilot tier waits for its owner to sign in.

Before the operator registers a router, the instance shows it as not
registered yet.

## GitHub Copilot in the portal

Every harness uses the Portal sign-in described above. The Portal stores the
owner's Copilot credential through the Gateway management flow. It does not
call an in-agent authentication helper. After the owner completes sign-in and
the operator publishes access, select a model from the Gateway catalog. If the
Gateway or catalog is unavailable, resolve that condition before you create an
Agent or select a new model.

A change to an OpenClaw or Hermes default model updates the custom resource,
causes reconciliation, and can restart the Agent. Expect a brief interruption.
To use the Gateway default again, clear the per-instance selection. The Portal
offers only models in the Gateway catalog that the selected optional Team
Policy allows.

## Persistence and precedence

`Agent.spec.model` is an optional Gateway model ID, at most 256 characters. An
empty `model` string in a Portal PATCH clears the override. When the Portal
creates an Agent, it validates the initial selection against the published
Gateway catalog. PATCH stores the requested model ID. The operator resolves the
effective model and enforces the final key grant. Deployment defaults and
per-Agent selections do not bypass Gateway authorization.

The Operator owns the model routing of platform-managed Agents. Per-Agent
provider overrides do not select an upstream provider, and workloads receive a
Gateway virtual key. Configure providers, credentials, permissions, budgets,
and rate limits in the AI Gateway instead.

## Catalog architecture

The operator publishes the Gateway model catalog and the allowed model access
of each Agent. The Portal reads those catalogs through its authenticated API.
Shared models come from Gateway configuration. Copilot models become available
after the owner signs in through the Portal. Provider credentials are not
returned to the browser or the Agent workload.

## When discovery fails

Examine the Agent's AI Gateway conditions and the selected
Team Policy, if any. If authentication expired, complete Portal sign-in again.
After a temporary Gateway failure, retry. Do not print or paste token files to
diagnose these errors.
