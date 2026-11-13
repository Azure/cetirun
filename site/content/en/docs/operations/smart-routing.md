---
title: "Smart routing"
description: "Give a Team's Agents a model name that sends each request to a cheaper or a stronger model, and examine where requests go."
weight: 25
---

A smart-routing model, or router, is a model name such as `auto`. For each
request, the AI Gateway classifies how difficult the request is. The result is
one of four tiers: `simple`, `medium`, `complex`, and `reasoning`. The
AI Gateway sends the request to the model of that tier. If an Agent uses the
router as its model, the Agent gets a cheap model for simple turns. In the
same conversation, it gets a strong model for hard turns.

The steps on this page start with the models of the AI Gateway and end with a
request that you examine. They are for a Portal administrator, on an
installation that `make deploy` made, as in
[Install CetiRun](../../getting-started/platform/). Steps 2 and 3 are for a
Team with an Agent Policy. For these steps, the installation must run
[policy-bound Teams](../policy-teams/).

A router is defined in one of two places:

- **In the models of the AI Gateway**: routers that an Agent Policy can grant,
  and that Agents without a Team get. See
  [Routers in the AI Gateway](#routers-in-the-ai-gateway).
- **On a Team**, in the Portal: the Team's own routers, which its Agents use.
  No change to the AI Gateway is necessary. The classifier is `heuristic`,
  `llm`, or `jev`.

`make deploy` checks the AI Gateway routers by `modelRouting.mode` in the
environment file. The default mode, `auto`, requires the router `auto` of
step 1. `direct` mode refuses every AI Gateway router and the decision model.
Then Team routers can use only the `heuristic` and `llm` classifiers. Refer to
[Configure the shared models](../ai-gateway/#configure-the-shared-models).

## 1. Offer the tier models

Each tier names a shared model. A shared model is a model in the AI Gateway's
`models`, with a price. Order the tiers by price. At the top, put the model
that you use without a router. The
[AI Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#smart-routing)
contains measured costs. For the other fields of a model, refer to
[Configure the shared models](../ai-gateway/#configure-the-shared-models).

In the environment file of `make deploy`, the models go under
`platform.values.cetirun-ai-gateway.models`. This example has three tier
models, the decision model, and the router `auto`:

```yaml
platform:
  values:
    cetirun-ai-gateway:
      providerCredentials:
        existingSecret: gateway-provider-keys  # AZURE_OPENAI_KEY, AZURE_DECISION_KEY
      models:
        - model_name: gpt-mini
          litellm_params:
            model: azure/my-gpt-mini-deployment
            api_base: https://example.openai.azure.com/
            api_key: os.environ/AZURE_OPENAI_KEY
            api_version: "2025-04-01-preview"
          model_info:
            base_model: azure/gpt-4o-mini
        - model_name: gpt-main
          litellm_params:
            model: azure/my-gpt-deployment
            api_base: https://example.openai.azure.com/
            api_key: os.environ/AZURE_OPENAI_KEY
            api_version: "2025-04-01-preview"
          model_info:
            base_model: azure/gpt-4o
        - model_name: o3
          litellm_params:
            model: azure/my-o3-deployment
            api_base: https://example.openai.azure.com/
            api_key: os.environ/AZURE_OPENAI_KEY
            api_version: "2025-04-01-preview"
          model_info:
            base_model: azure/o3
        - model_name: decision-1
          litellm_params:
            model: azure_ai/my-decision-deployment
            api_base: https://example.services.ai.azure.com
            api_key: os.environ/AZURE_DECISION_KEY
          model_info:
            mode: evaluation
            base_model: azure_ai/Microsoft-Decision-1
        - model_name: auto
          litellm_params:
            model: auto_router/complexity_router
            # The router's own price only passes the unpriced-model check.
            # Each request is charged at the price of the model it goes to.
            input_cost_per_token: 0.00001
            output_cost_per_token: 0.00004
            complexity_router_config:
              classifier_type: jev
              jev_classifier_config:
                deployment_name: decision-1
              tiers:
                SIMPLE: gpt-mini
                MEDIUM: gpt-main
                COMPLEX: o3
                REASONING: o3
            complexity_router_default_model: gpt-main
          model_info:
            # The smallest limits of its tier models, for Agents that read them.
            max_input_tokens: 128000
            max_output_tokens: 16384
```

In the default `modelRouting.mode: auto`, `make deploy` requires that the
entries `decision-1` and `auto` have this form. `decision-1` uses LiteLLM's
`azure_ai` provider, `azure_ai/Microsoft-Decision-1` as its `base_model`, and
`mode: evaluation`. `base_model` gives each model LiteLLM's price for that
model. Without the `azure_ai/` prefix, LiteLLM finds no price for
Microsoft-Decision-1, the operator does not offer the classifier, and `auto`
does not work. The
[AI Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#pricing-a-deployment)
shows how to set a price yourself.

Each `api_key: os.environ/<NAME>` reads the key `<NAME>` of the provider-key
Secret. Add each key to the Secret before the run. Then run `make deploy`, as
in [Install CetiRun](../../getting-started/platform/#platform-chart). A change
to the models replaces the AI Gateway Pods. For Sandbox Agents, refer to
[Protect the selected peers](../policy-teams/#peer-labels).

## 2. Allow the routers in an Agent Policy

> **Warning:** Use a new Agent Policy for this step. For an Agent Policy that
> already has Agents, create the router first, as step 3 shows. Then add
> `auto-team` to the Agent Policy. Until the router exists, the operator cannot
> resolve the name, and it stops the Agents of that Agent Policy. Refer to
> [When model changes reach Agents](../ai-gateway/#when-model-changes-reach-agents).

In the Portal, select **Admin → Agent policies → New policy**. Under
**Allowed harnesses**, enable **OpenClaw** and **Hermes**. Under
**Allowed shared models**, select each router and the models of its tiers.
For `auto`, select `auto`, `gpt-mini`, `gpt-main`, and `o3`.

The picker offers the models of the AI Gateway catalog. It offers the routers
of a Team only after the Team uses the Agent Policy. For this reason, you add
the Team router `auto-team` in step 3.

An Agent gets a router only when its Agent Policy allows the router and the
models that [Routers](../models/#routers) lists. It is not necessary to list a
classifier model, such as `decision-1`.
Select **Set monthly Gateway budget**. An Agent Policy that allows models must
have this budget. Each request is charged at the price of the model that it went
to. The calls of a model classifier are also charged.

## 3. Create the Team and its router

Select **Admin → Teams → New Team**. Select the Agent Policy. Select the Sandbox
as the allowed and default Sandbox. Under **Smart routing**, select
**Add smart-routing model**:

- **Model name**: `auto-team`. A Team router cannot have the name of a shared
  model. `auto` is a shared model, because it is an AI Gateway router.
- **Classifier**: **Heuristic**, **Shared model**, or **Decision model**. For a
  model classifier, select a shared chat model or a decision model, such as
  `decision-1`, as **Classifier model**. See
  [Choose a classifier](#choose-a-classifier).
- **Simple tier** `gpt-mini`, **Medium tier** `gpt-main`, **Complex tier**
  `gpt-main`, **Reasoning tier** `o3`. The reasoning tier can stay empty. In
  that case, it uses the complex tier.

Save the Team. Then allow the Team router in the Agent Policy:

1. Select **Admin → Agent policies**, and open the Agent Policy of step 2.
2. Under **Allowed shared models**, select `auto-team`. The picker shows the
   Team of the router, and if the router is registered.
3. Save the Agent Policy.

This is the same configuration as resources, for `kubectl apply`:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: AgentPolicy
metadata:
  name: routed
spec:
  harnesses:
    allowed: [openclaw, hermes]
  skills:
    allowed: []
  modelAccess:
    sharedModels:
      allowed: [auto, auto-team, gpt-mini, gpt-main, o3]
    gatewayBudget:
      defaultMonthlyUSD: "10"
      maxMonthlyUSD: "20"
  security:
    readOnlyRootFilesystem: true
  network:
    ingress:
      mode: authenticated
---
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: routed
spec:
  agentPolicyRef: routed
  sandboxRefs: [standard]
  defaultSandboxRef: standard
  modelRouters:
    - name: auto-team
      classifier:
        type: heuristic
      tiers:
        simple: gpt-mini
        medium: gpt-main
        complex: gpt-main
        reasoning: o3
```

Within a minute or two, the operator registers the router in the AI Gateway
under an internal name, `cetirun-router-<hash>`. The Team page shows that the
router is ready, or why it is not. An example is a tier model that the
AI Gateway does not offer. Another example is a router name that is also the
name of a shared model:

```bash
kubectl get team routed -o jsonpath='{.status.modelRouters}'
```

## 4. Create an Agent on a router

In the Portal, the user who creates the Agent selects **New Agent** and then
selects the Team. The user keeps **Use shared models** enabled. As the
**Default model (optional)**, the user selects `auto-team (smart routing)`,
or `auto` for the AI Gateway router.

The Agents of the Team that already run also get a new router if their
Agent Policy allows it. A router change restarts an Agent only when the model
configuration of the Agent changes. Router status updates do not interrupt
Sandbox bootstrap. A change to the shared context of the Team restarts its
running Sandbox Agents one time. For the Agent Policy changes that restart
Agents, refer to
[What changes for Agents](../policy-teams/#what-changes-for-agents).

Owners can make a router the default model in the **Models** section of the
Agent's page. This restarts the Agent.

An Agent without a Team gets each AI Gateway router whose models are all
shared models, `auto` included. To examine `auto` without a policy-bound Team,
keep **(none)** as the Team, and select `auto` as the default model.

The Agent's page lists the routers among its models, or it shows why the Agent
cannot use a router. OpenClaw and Hermes show the router's name. Each Agent is
configured with the smallest context window and output limit among the
router's tier models. For an AI Gateway router, this includes its default
model, unless the router sets its own limits in `model_info`. With these
limits, a request fits every tier.

The Agent sends images to a router only when every model that the router sends
requests to accepts images. Refer to [Images](../ai-gateway/#images).

In the same way, OpenClaw offers reasoning levels for a router only when every
model that the router sends requests to reasons. Refer to
[Reasoning](../ai-gateway/#reasoning). For a Team's router, the Team's
`status.modelRouters[].nonReasoningModels` lists the tier models that do not
reason. The Team page in the Portal shows them with the router.

## 5. Check where requests go

Send a short question to the Agent. Then send a request for a design or a proof.
For each request that it answers, the AI Gateway writes a line to its log. In
this line, `model` is the router. For a Team's router, it is the internal name.
The line also shows what made the decision, the tier, and the model that
answered:

```bash
kubectl logs -n cetirun-system -c gateway --since=15m --tail=-1 \
  -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway \
  | grep '"routing_cause"'
```

`--tail=-1` is important. With a label selector and without this option,
`kubectl logs` shows only the last ten lines of each Pod.

| Field | Meaning |
| --- | --- |
| `model` | The name of the router, or `cetirun-router-<hash>` for a Team's router. The Team's `status.modelRouters[].gatewayModel` shows this name |
| `routing_cause` | What made the decision. `llm_classifier` or `jev_classifier` is for a model classifier. `heuristic_scorer` or `reasoning_override` is for the local scorer, including an explicit heuristic fallback. With the default fallback for `llm` and `jev`, `default_model_fallback` means that classification failed or was skipped. The request then went to the router's default model, and the line has no `routing_tier`. `health_failover` and `health_default_fallback` mean that the model of the tier had no healthy deployment. The request then went to another model of the router's tiers, or to the router's default model |
| `routing_tier` | The tier that the router selected |
| `provider_model` | The deployment that answered, such as `azure/my-gpt-deployment` |
| `spend` | What the request cost |

The AI Gateway chart README has queries of the ADX-Mon collection for these
lines in
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers).
The queries show the share of each tier, its spend, and its latency. A client
that calls the AI Gateway with its own key also gets the tier in response
headers. See
[Calling the AI Gateway]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#calling-the-ai-gateway).

### When a tier fails

The model of a tier can fail, for example when the GitHub Copilot proxy of an
Agent is down. The AI Gateway then sends the request to the router's other
models. First, it uses the other models of the tier. Then it uses the higher
tiers, the router's default model, and the lower tiers, in this order. It uses
only the models that the Agent's key can use.

In the log, the tier's line has the status `failure`. The line of the model that
answered has `fallback_from`, which names the router. The error that the Agent
gets when every model fails does not contain the router's internal name.

## Choose a classifier

| Classifier | How it decides | Cost | Where |
| --- | --- | --- | --- |
| `heuristic` | Scores English keywords and the length of the request, without a model call | None | Team and AI Gateway routers |
| `llm` | A shared chat model reads the request and selects the tier | One model call per request, charged to the Agent | Team and AI Gateway routers |
| `jev` | A decision model, such as Microsoft-Decision-1, selects the tier | One call per request, charged to the Agent | Team and AI Gateway routers |

The default, `heuristic`, is free and adds no latency, but it does not place
hard requests well. On 40 prompts, ten for each tier, it selects the expected
tier for 15 of the English prompts. For their Chinese translations, it selects
the expected tier for 11. It never selects the `complex` or `reasoning` tier.
Microsoft-Decision-1 selects the expected tier for 39 of 40 in each language.
Examine a classifier with requests that are similar to your users' requests.

For `llm`, select a small, fast chat model that the AI Gateway offers as a
shared model. For `jev`, select a decision model that the AI Gateway offers,
such as `decision-1`:

```yaml
  modelRouters:
    - name: auto-team
      classifier:
        type: jev
        model: decision-1
      tiers:
        simple: gpt-mini
        medium: gpt-main
        complex: o3
    - name: auto-llm
      classifier:
        type: llm
        model: gpt-mini
      tiers:
        simple: gpt-mini
        medium: gpt-main
        complex: o3
```

An `llm` classifier has ten seconds to answer. A `jev` classifier keeps its
three-second default. If classification fails, the router sends the request
to its default model. For a Team router, this is the model of the medium tier.

After a timeout, the router skips the classifier for 30 seconds. Requests
during this period also go to the default model. `routing_cause` shows
`default_model_fallback` in both cases.

An AI Gateway router can set `classifier_llm_config.timeout_ms` and
`classifier_fallback` to use different values. A router with its own
`tier_definitions` uses its `fallback_tier`. These settings are described in
[Choosing a classifier]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#choosing-a-classifier).

A `jev` classifier sends the Agent's whole system prompt and its latest
messages to the decision model. Microsoft-Decision-1 takes at most 32,768
tokens. The AI Gateway shortens a longer classifier request so that it fits. If
the newest user message fits, the AI Gateway keeps it whole. Refer to
[Configure a team](../../guides/teams/#smart-routing).

## Routers in the AI Gateway

A router can also be an entry in the AI Gateway's `models`, next to the tier
models, as `auto` in step 1. For the Agents that get such a router, refer to
[Routers](../models/#routers). Changes apply only after a `make deploy` run.
The limits that each Agent gets are in [step 4](#4-create-an-agent-on-a-router).

Microsoft-Decision-1 is deployed in an Azure AI Foundry resource, not in an
Azure OpenAI resource. It is reached with LiteLLM's `azure_ai` provider. Its
entry sets `model_info.mode: evaluation`. As a result, LiteLLM calls it only to
classify, and it is not offered to Agents. The AI Gateway also refuses requests
that ask it for a conversation.

Without a LiteLLM license, LiteLLM limits some classifier features to one
router per AI Gateway, for example a classifier prompt of the router's own.
Only AI Gateway routers can use these features. The
[AI Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#choosing-a-classifier)
describes the settings of the classifiers.

Sources: [Team model routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_routers.go" >}}),
[AI Gateway routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_shared_routers.go" >}}),
[AI Gateway request log]({{< repo "app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py" >}}),
[deployment helper]({{< repo "scripts/cetirun_platform_deploy.py" >}}),
and [Configure a team](../../guides/teams/#smart-routing).
