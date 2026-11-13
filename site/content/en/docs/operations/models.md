---
title: "Model access"
description: "Find which models an Agent can use, what decides its default model, and how to give a team its own provider account."
weight: 21
---

Every Agent takes its models from the [AI Gateway](../ai-gateway/). The
operator decides which models the virtual key of each Agent can use. Users
select from these models in the Portal, as
[Choose a model](../../guides/models/) shows. This page is for platform
administrators.

## Model sources

| Source | Configured by | Conditions | Agents that get it | Spend |
| --- | --- | --- | --- | --- |
| Shared models in the AI Gateway chart's `models` | The platform administrator, in the environment file of `make deploy` | A price, and a conversation model. Refer to [What decides the models of an Agent](#what-decides-the-models-of-an-agent) | Standard Agents get all of them. Agents of a policy-bound Team get the models that their Agent Policy allows | The price of the model |
| Models added in the AI Gateway console | The platform administrator, in the console | The access group `cetirun-shared` and a price | Only standard Agents | The price of the model |
| AI Gateway routers, for example `auto` | The platform administrator, in `models` | Every model that the router sends requests to | Refer to [Routers](#routers) | The price of the tier model, and the classifier calls |
| Team routers | A Portal administrator, on the Team | Every model that the router sends requests to | The Agents of that Team. Refer to [Routers](#routers) | The price of the tier model, and the classifier calls |
| The GitHub Copilot models of the owner | The owner of the Agent, with a sign-in on the Agent page | The Copilot proxy is enabled. For a policy-bound Team, the Policy allows Copilot | Each Agent gets the models of its own sign-in | The list price of the model, from the budget of the Agent |
| Outbound calls of a Custom image | The image | None. The platform does not control them | Custom Agents only | Not counted by the AI Gateway |

CetiAdmin uses the shared models of the AI Gateway. Its default model is
`cetiadmin.model.default`, `auto` by default. Refer to the platform chart's
[Configure CetiAdmin]({{< repo "app/cetirun-platform/README.md" >}}#configure-cetiadmin).

An Agent sees each model under one name. The Agent page in the Portal lists
the models of the Agent. It also tells why the Agent cannot use a model or a
router. With `kubectl`, read `status.aiGateway` of the Agent.

## What decides the models of an Agent

These settings decide which models the key of an Agent can use:

- **The Team and its Agent Policy.** A
  [standard Agent](../../concepts/glossary/#standard-agent) has no Team, or a
  Team without an Agent Policy. It can use every shared model. The Agent
  Policy of a policy-bound Team limits its Agents to
  `modelAccess.sharedModels.allowed`.
  It allows GitHub Copilot only with `modelAccess.copilot.allowed`.
- **The model sources of the Agent.** `spec.modelAccess.sharedModels` and
  `spec.modelAccess.githubCopilot` enable or disable the two sources. Without
  a value, both sources are enabled for an Agent without a Sandbox. A Sandbox
  Agent must request each source.
- **The price of the model.** While `blockUnpricedModels` is on, the operator
  gives Agents only the shared models with a price. Refer to
  [Set the price of a model](../ai-gateway/#set-the-price-of-a-model).
- **The mode of the model.** Agents get only conversation models: the
  `model_info.mode` is empty, `chat`, `completion`, or `responses`. Decision
  models (`model_info.mode: evaluation`) only classify requests for routers.
- **Reserved names.** Refer to [Reserved names](../ai-gateway/#reserved-names).
- **The limits.** Refer to [Limits](#limits).

## Default model

The default model of an Agent is the first model of this list that the Agent
can use:

1. The model that `spec.model` of the Agent names. The owner selects it in the
   Portal.
2. A Team router that `spec.model` names.
3. The operator's `aiGateway.defaultModel`. It is empty by default.
4. The first shared model in name order that is not an AI Gateway router.
5. The first GitHub Copilot model of the owner.
6. The first AI Gateway router.
7. The first Team router of the Agent.

If the Agent can use no model, its configuration names a placeholder model
that answers no request.

Without `aiGateway.defaultModel`, the default can be an expensive model that
comes first in name order. Set `aiGateway.defaultModel` to a model that most
Agents can use. An AI Gateway router becomes the default model only when the Agent
or `aiGateway.defaultModel` names it, or when the Agent can use nothing else.

The operator chart value `cetirun.defaultModel` does not select the default
model of an Agent. The AI Gateway configuration of the Agent replaces it.

`spec.model` is an AI Gateway model name of at most 256 characters. When the
Portal creates an Agent, it validates the selected model against the AI Gateway
catalog. A later change stores the requested name. The operator then resolves
the model that the key can use. A default model does not give an Agent access
to a model.

## Names of GitHub Copilot models

A Copilot model can be the same model as a shared model that the Agent can
use. Then the Copilot model uses the name of the shared model, and the Agent
lists the model one time. The shared deployments and the Copilot route of the
Agent then answer the requests for that name. Refer to
[GitHub Copilot proxies](../ai-gateway/#github-copilot-proxies).

The operator compares the Copilot model ID with the `model_info.base_model` of
each deployment of the shared model. It uses `litellm_params.model` instead
when the LiteLLM price table prices that name. The comparison ignores the
provider prefix, letter case, and the difference between dots and dashes:

- `claude-sonnet-4.5` is the same model as `anthropic/claude-sonnet-4-5`.
- A Copilot model with a date version, such as `gpt-4.1-2025-04-14`, is never
  merged.
- If several shared models match, the first name in sort order is used.

Any other Copilot model keeps its own ID, for example `gpt-5.5`. These cases
also apply:

- A shared model has the same name, but it is another model. Then the Agent
  does not get the Copilot model, and the Agent page lists it as not offered.
- A shared model is the same model, but the Agent cannot use that shared
  model. Then the Copilot model keeps that name in the access group of the
  Agent. Only the key of the Agent reaches it.
- The AI Gateway has no price for the model. Then the Agent page lists it as not
  offered, because the budget cannot limit the model.

The key of an Agent reaches a Copilot model only by that name. The operator
removed the `copilot/<model>` names of earlier versions from the keys. An Agent
whose default model is `copilot/<model>` uses the current name of the model.

### Copilot models under an Agent Policy

For a policy-bound Agent, the allowed shared models of its Policy also apply to
the Copilot models that are the same models. The Agent gets such a Copilot
model only when the Policy allows a shared model that is that model. A Copilot
model with a date version is the same model only as the same version. The
Agent page lists other such models as not allowed by the Agent Policy.

For example, the shared model `enterprise-sol` has the `base_model`
`azure/gpt-6-sol`, and no other shared model is that model. If the Policy does
not allow `enterprise-sol`, the Agent does not get the Copilot model
`gpt-6-sol` of its owner. A Copilot model that is not the same model as a
shared model needs only **Allow GitHub Copilot** in the Policy. An example is a
Claude model when the AI Gateway has no Claude deployment.

When the Policy stops the access to such a model, the operator removes the
route of the model from the AI Gateway. Then it updates the key of the Agent. If
the AI Gateway cannot remove the route, the key cannot reach any Copilot model of
the owner until the route is removed. The same occurs while the operator
cannot read the models or the prices of the AI Gateway. The operator tries again
about every 15 seconds. Shared models continue to work, and the Agent page
tells why Copilot is not available.

## Routers

A router is a model name that sends each request to one of several models.
Refer to [Smart routing](../smart-routing/). An Agent gets a router only when it
can use every model that the router sends requests to.

For an Agent with an Agent Policy, the Policy must list these models:

- the router
- every model of its tiers
- its default model
- its context compaction model, if it has one

The classifier model must be a shared model, but the Policy does not have to
list it. For a GitHub Copilot tier, the Policy must allow Copilot.

A standard Agent gets every AI Gateway router whose models are all shared
models.

If the operator cannot read a router completely, Agents with a Policy do not
get it. Examples are a router with a setting that the operator does not know,
and a router with a classifier plugin. A router that an Agent cannot use is not
in its key or in its models. If the Policy of the Agent lists the router, the
Agent page tells why.

A changed router reaches the keys of the Agents in about two minutes. It does
not change the AI Gateway projection of the Policy. If an administrator
removes or renames a Team router that an Agent Policy allows, the operator
stops the Agents of that Policy. Refer to
[When model changes reach Agents](../ai-gateway/#when-model-changes-reach-agents).

## Limits

The configuration of an Agent lists at most 256 models. All its GitHub Copilot
models stay, at most 128. Shared models fill the rest in name order. The
operator keeps these shared models first:

- the default model of the Agent
- the `aiGateway.defaultModel` of the deployment
- the shared models that are also Copilot models

The operator reports the shared models that it leaves out in an
`AIGatewayModelsLimited` event on the Agent.

## Use the provider account of a team

Agents cannot use their own provider credentials. The operator does not apply
a provider override of an Agent, and each workload gets only an AI Gateway
virtual key. To give a team its own provider account, do these steps:

1. Add the key of the account to the provider-key Secret of the AI Gateway.
   Use a name of its own, for example `RESEARCH_OPENAI_KEY`.
2. Add a shared model for the account to `models`, with
   `api_key: os.environ/RESEARCH_OPENAI_KEY` and a price.
3. Run `make deploy`.
4. Allow the model only in the Agent Policy of the Team that uses the account.

> **Warning:** Standard Agents get every shared model, also the model of
> step 2. Only Agent Policies limit the shared models of their own Agents.

## How Agents receive their models

The operator publishes the AI Gateway model catalog and the models of each
Agent. The Portal reads these catalogs through its authenticated API. Provider
credentials do not go to the browser or to the Agent workload.

OpenClaw and Hermes receive their model list and their virtual key from the
AI Gateway configuration at each start:

- OpenClaw writes the AI Gateway provider as `cetirun` in
  `~/.openclaw/openclaw.json`, so its model references are `cetirun/<model>`.
- Hermes writes the AI Gateway provider as `cetirun-gateway` in
  `~/.hermes/config.yaml`. It reads the key from `CETIRUN_GATEWAY_API_KEY`.
- Neither file holds the key.
- OpenClaw also sends short internal tasks to a utility model, for example
  session titles and activity summaries. With the AI Gateway, the utility model of
  an Agent is its default model. These calls count against the key like other
  calls.
- A model that the AI Gateway no longer serves disappears from the configuration
  of the Agent.

Earlier agent charts wrote the AI Gateway provider of OpenClaw as `openai`. At
each start, OpenClaw renames the models under `openai` that it counts as
retired, for example `gpt-4.1-mini`. The Agent then requested a model that the
AI Gateway does not have. An OpenClaw Agent restarts one time when you upgrade
it to a chart that writes `cetirun`. A session that names a model under
`openai/` then returns to the default model of the Agent.

A Custom image selects a model from the models of the AI Gateway, through its own
configuration or chat UI. Code in a Custom image can make its own outbound
connections, and the AI Gateway does not control them. If you must prevent these
connections, use a network policy.

Sources: [model access]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[default model]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_copilot.go" >}}),
[Copilot model names]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_copilot_merge.go" >}}),
[shared-model catalog]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_catalog.go" >}}),
[AI Gateway routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_shared_routers.go" >}}),
and [OpenClaw configuration]({{< repo "app/cetirun-chart/agents/openclaw/config/native.tpl" >}}).
