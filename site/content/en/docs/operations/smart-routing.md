---
title: "Smart routing"
description: "Give a Team's Agents a model name that sends each request to a cheaper or a stronger model, and examine where requests go."
weight: 25
---

A smart-routing model, or router, is a model name such as `auto`. For each
request, the AI Gateway classifies how difficult the request is. The result is
one of four tiers: `simple`, `medium`, `complex`, and `reasoning`. The Gateway
sends the request to the model of that tier. If an Agent uses the router as its
model, the Agent gets a cheap model for simple turns. In the same conversation,
it gets a strong model for hard turns.

The steps on this page configure a Team whose Agents use a router. They start
with the Gateway's models and end with a request that you examine. The steps
are for an administrator of the Portal, on an installation that runs
[policy-bound Teams](../policy-teams/).

A router is defined in one of two places:

- **On a Team**, in the Portal: the Team's own routers, which its Agents use. No
  change to the Gateway is necessary, and the classifier is `heuristic` or
  `llm`.
- **In the Gateway's values**: routers that every Team's Policy can grant. They
  can use any classifier that LiteLLM offers, such as a decision model. See
  [Routers in the Gateway](#routers-in-the-gateway).

## 1. Offer the tier models

Each tier names a shared model. A shared model is a model in the Gateway's
values, with a price. Order the tiers by price. At the top, put the model that
you use without a router. The
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#smart-routing)
contains measured costs. For example, in your platform values file:

```yaml
cetirun-ai-gateway:
  providerCredentials:
    existingSecret: gateway-provider-keys  # holds AZURE_OPENAI_KEY
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
```

Before the upgrade, create the Secret in the release namespace. Then run the
deployment helper with the values file, as in
[Install CetiRun](../../getting-started/platform/#platform-chart).
`base_model` gives each model LiteLLM's price for that model. The
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#pricing-a-deployment)
shows how to set a price yourself.

## 2. Allow the router in an Agent Policy

In the Portal, select **Admin → Agent policies → New policy**. Allow the
harnesses, OpenClaw and Hermes. Under **Allowed shared models**, list the
router's name and each model of its tiers, one per line:

```text
auto
gpt-mini
gpt-main
o3
```

An Agent gets a router only when its Policy allows the router and every tier
model. It is not necessary to list a classifier model. Select
**Set monthly Gateway budget**. A Policy that allows models must have this
budget. Each request is charged at the price of the model that it went to. The
calls of a model classifier are also charged.

## 3. Create the Team and its router

Select **Admin → Teams → New Team**. Select the Agent Policy. Select the Sandbox
as the allowed and default Sandbox. Under **Smart routing**, select
**Add smart-routing model**:

- **Model name**: `auto`.
- **Classifier**: the heuristic, or a model classifier with a shared chat model
  as **Classifier model**. See [Choose a classifier](#choose-a-classifier).
- **Simple tier** `gpt-mini`, **Medium tier** `gpt-main`, **Complex tier**
  `gpt-main`, **Reasoning tier** `o3`. The reasoning tier can stay empty. In
  that case, it uses the complex tier.

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
      allowed: [auto, gpt-mini, gpt-main, o3]
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
    - name: auto
      classifier:
        type: heuristic
      tiers:
        simple: gpt-mini
        medium: gpt-main
        complex: gpt-main
        reasoning: o3
```

Within a minute or two, the operator registers the router in the Gateway under
an internal name, `cetirun-router-<hash>`. The Team page shows that the router
is ready, or why it is not, for example a tier model that the Gateway does not
offer:

```bash
kubectl get team routed -o jsonpath='{.status.modelRouters}'
```

## 4. Create an Agent on the router

In the Portal, a member of the Team selects **New Agent** and then selects the
Team. The member keeps **Use shared models** enabled and selects `auto` as the
**Default model**. The Agents of the Team that already run also get the router.
The addition of the router restarts them one time. Their owners can make the
router the default model in the **Models** section of the Agent's page. This
restarts the Agent again.

The Agent's page lists the router among its models, or it shows why the Agent
cannot use the router. OpenClaw shows the router's name. Hermes shows the
internal name of the router. Each Agent is configured with the smallest context
window and output limit among the tier models. As a result, a request fits
every tier.

## 5. Check where requests go

Send a short question to the Agent. Then send a request for a design or a proof.
For each request that it answers, the Gateway writes a line to its log. In this
line, `model` is the router. For a Team's router, it is the internal name. The
line also contains the tier and the model that the router selected:

```bash
kubectl logs -n cetirun-system -c gateway --since=15m --tail=-1 \
  -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway \
  | grep '"routing_tier"'
```

`--tail=-1` is important. With a label selector and without this option,
`kubectl logs` shows only the last ten lines of each Pod.

| Field | Meaning |
| --- | --- |
| `model` | The name of the router, or `cetirun-router-<hash>` for a Team's router. The Team's `status.modelRouters[].gatewayModel` shows this name |
| `routing_tier` | The tier that the router selected |
| `provider_model` | The deployment that answered, such as `azure/my-gpt-deployment` |
| `routing_cause` | What made the decision. `llm_classifier` or `jev_classifier` is for a model classifier. `heuristic_scorer` or `reasoning_override` is for the local scorer. On a router with a model classifier, these values mean that the classifier failed |
| `spend` | What the request cost |

For Container Insights, the Gateway chart README has Log Analytics queries for
these lines in
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers).
The queries show the share of each tier, its spend, and its latency. A client
that calls the Gateway with its own key also gets the tier in response headers.
See
[Calling the Gateway]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#calling-the-gateway).

### When a tier fails

The model of a tier can fail, for example when the GitHub Copilot proxy of an
Agent is down. The Gateway then sends the request to the router's other models:
first the other models of the tier, then the higher tiers, then the router's
default model, and then the lower tiers. It uses only the models that the
Agent's key can use. In the log, the tier's line has the status `failure`. The
line of the model that answered has `fallback_from`, which names the router.
The error that the Agent gets when every model fails does not contain the
router's internal name.

## Choose a classifier

| Classifier | How it decides | Cost | Where |
| --- | --- | --- | --- |
| `heuristic` | Scores English keywords and the length of the request, without a model call | None | Team and Gateway routers |
| `llm` | A shared chat model reads the request and selects the tier | One model call per request, charged to the Agent | Team and Gateway routers |
| `jev` | A decision model, such as Microsoft-Decision-1, selects the tier | One call per request, charged to the Agent | Gateway routers |

The default, `heuristic`, is free and adds no latency, but it does not place
hard requests well. In a measurement on 40 prompts, ten for each tier, it
selected the expected tier for 15 of the English prompts. For their Chinese
translations, it selected the expected tier for 11. It never selected the
`complex` or `reasoning` tier. Microsoft-Decision-1 selected the expected tier
for 39 of 40 in each language. Examine a classifier with requests that are
similar to your users' requests.

For `llm`, select a small, fast chat model that the Gateway offers as a shared
model, such as `gpt-mini` here:

```yaml
  modelRouters:
    - name: auto
      classifier:
        type: llm
        model: gpt-mini
      tiers:
        simple: gpt-mini
        medium: gpt-main
        complex: o3
```

If a model classifier fails or does not answer within three seconds, the router
uses `heuristic` for that request instead, without an error. After a timeout,
the router skips the classifier for 30 seconds. `routing_cause` shows this.

## Routers in the Gateway

A router can also be an entry in the Gateway's `models`, next to the tier
models. Every Agent Policy that lists the router's name, its tier models, and
its default model grants the router. A Gateway upgrade is necessary for a
change. The change reaches Agents within about two minutes. Each Agent that
gets the router is configured with the smallest context window and output
limit among its tier models and its default model, as for a Team's router.

Such a router can use a decision model. Microsoft-Decision-1 is deployed in an
Azure AI Foundry resource, not in an Azure OpenAI resource. It is reached with
LiteLLM's `azure_ai` provider. Its entry sets `model_info.mode: evaluation`. As
a result, LiteLLM calls it only to classify, and it is not offered to Agents.
The Gateway also refuses requests that ask it for a conversation.

```yaml
cetirun-ai-gateway:
  models:
    # gpt-mini, gpt-main and o3 as above
    - model_name: decision-1
      litellm_params:
        model: azure_ai/my-decision-deployment
        api_base: https://example.services.ai.azure.com
        api_key: os.environ/AZURE_AI_FOUNDRY_KEY
      model_info:
        mode: evaluation
        base_model: azure_ai/Microsoft-Decision-1
    - model_name: auto-decision
      litellm_params:
        model: auto_router/complexity_router
        # The router's own price only passes the unpriced-model check;
        # each request is charged at the price of the model it goes to.
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
```

The Policy then lists `auto-decision`, `gpt-mini`, `gpt-main`, and `o3`. It is
not necessary to list the decision model. Microsoft-Decision-1 reads the Agent's
whole system prompt and its latest messages. It refuses a request that does not
fit its 32,768-token context. For such a request, the router uses `heuristic`,
without an error.
The [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#choosing-a-classifier)
describes the settings of the classifiers.

Sources: [Team model routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_routers.go" >}}),
[Gateway routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_shared_routers.go" >}}),
[Gateway request log]({{< repo "app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py" >}}),
and [Configure a team](../../guides/teams/#smart-routing).
