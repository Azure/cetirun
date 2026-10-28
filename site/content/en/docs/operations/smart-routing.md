---
title: "Smart routing"
description: "Give a Team's Agents a model name that sends each request to a cheaper or a stronger model, and check where requests go."
weight: 25
---

A smart-routing model, or router, is a model name such as `auto`. For each
request, the AI Gateway classifies how demanding it is, as one of four tiers,
`simple`, `medium`, `complex` and `reasoning`, and sends it to the model of that
tier. An Agent that uses the router as its model gets a cheap model for simple
turns and a strong one for hard turns, in the same conversation.

This page sets up a Team whose Agents use a router, from the Gateway's models
to a checked request. It assumes an installation that runs
[policy-bound Teams](../policy-teams/), and an administrator of the Portal.

A router is defined in one of two places:

- **On a Team**, in the Portal: the Team's own routers, used by its Agents. The
  Gateway needs no change, and the classifier is `heuristic` or `llm`.
- **In the Gateway's values**: routers every Team's Policy can grant, with any
  classifier LiteLLM offers, such as a decision model; see
  [Routers in the Gateway](#routers-in-the-gateway).

## 1. Offer the tier models

Each tier names a shared model: a model in the Gateway's values, with a price.
Order the tiers by price, with the model you would otherwise use on top; the
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#smart-routing)
has measured costs. For example, in your platform values file:

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

Create the Secret in the release namespace before the upgrade, and run the
deployment helper with the values file, as in
[Install CetiRun](../../getting-started/platform/#platform-chart).
`base_model` gives each model LiteLLM's price for that model; the
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#pricing-a-deployment)
shows how to set a price yourself.

## 2. Allow the router in an Agent Policy

In the Portal, choose **Admin → Agent policies → New policy**. Allow the
harnesses, OpenClaw and Hermes, and under **Allowed shared models** list the
router's name and each model of its tiers, one per line:

```text
auto
gpt-mini
gpt-main
o3
```

An Agent gets a router only when its Policy allows the router and every tier
model; a classifier model need not be listed. Choose **Set monthly Gateway
budget**, which a Policy that allows models requires: each request is charged
at the price of the model it went to, and the calls of a model classifier are
charged too.

## 3. Create the Team and its router

Choose **Admin → Teams → New Team**: select the Agent Policy, and the Sandbox
as allowed and default Sandbox. Under **Smart routing**, choose
**Add smart-routing model**:

- **Model name**: `auto`;
- **Classifier**: the heuristic, or a model classifier with a shared chat model
  as **Classifier model**, see [Choose a classifier](#choose-a-classifier);
- **Simple tier** `gpt-mini`, **Medium tier** `gpt-main`, **Complex tier**
  `gpt-main`, **Reasoning tier** `o3`. The reasoning tier may stay empty to use
  the complex one.

The same configuration as resources, for `kubectl apply`:

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

The operator registers the router in the Gateway under an internal name,
`cetirun-router-<hash>`, within a minute or two. The Team page shows it as
ready, or why it is not, for example a tier model the Gateway does not offer:

```bash
kubectl get team routed -o jsonpath='{.status.modelRouters}'
```

## 4. Create an Agent on the router

A member of the Team chooses **New Agent** in the Portal, selects the Team,
keeps **Use shared models** on, and chooses `auto` as the **Default model**.
The Team's running Agents get the router too: adding it restarts them once.
Their owners can make it the default model in the **Models** section of the
Agent's page, which restarts the Agent again.

The Agent's page lists the router among its models, or says why the Agent
cannot use it. OpenClaw shows the router's name; Hermes shows its internal
name. Each Agent is configured with the smallest context window and output
limit among the tier models, so a request fits every tier.

## 5. Check where requests go

Talk to the Agent: a short question, then a request for a design or a proof.
Each request the Gateway answers writes a line to its log, with the router as
`model`, its internal name for a Team's router, and the tier and model it chose:

```bash
kubectl logs -n cetirun-system -c gateway --since=15m --tail=-1 \
  -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway \
  | grep '"routing_tier"'
```

`--tail=-1` matters: with a label selector, `kubectl logs` otherwise shows only
the last ten lines of each Pod.

| Field | Meaning |
| --- | --- |
| `model` | The router, `cetirun-router-<hash>` for a Team's router; the Team's `status.modelRouters[].gatewayModel` names it |
| `routing_tier` | The tier the router chose |
| `provider_model` | The deployment that answered, such as `azure/my-gpt-deployment` |
| `routing_cause` | What decided: `llm_classifier` or `jev_classifier` for a model classifier; `heuristic_scorer` or `reasoning_override` for the local scorer, which on a router with a model classifier means that the classifier failed |
| `spend` | What the request cost |

With Container Insights, the Gateway chart README's
[Measuring the routers]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#measuring-the-routers)
has Log Analytics queries over these lines: the share of each tier, its spend
and its latency. A client that calls the Gateway with a key of its own also
gets the tier in response headers; see
[Calling the Gateway]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#calling-the-gateway).

## Choose a classifier

| Classifier | How it decides | Cost | Where |
| --- | --- | --- | --- |
| `heuristic` | Scores English keywords and the request's length, without a model call | None | Team and Gateway routers |
| `llm` | A shared chat model reads the request and picks the tier | One model call per request, charged to the Agent | Team and Gateway routers |
| `jev` | A decision model, such as Microsoft-Decision-1, picks the tier | One call per request, charged to the Agent | Gateway routers |

The default, `heuristic`, is free and adds no latency, but places hard requests
poorly: measured on 40 prompts, ten for each tier, it chose the expected tier
for 15 of the English ones and 11 of their Chinese translations, and never the
`complex` or `reasoning` tier. Microsoft-Decision-1 chose it for 39 of 40 in
each language. Check a classifier with requests like your users'.

For `llm`, choose a small, fast chat model that the Gateway offers as a shared
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

When a model classifier fails or does not answer within three seconds, the
router uses `heuristic` for that request instead, without an error, and after
a timeout skips the classifier for 30 seconds. `routing_cause` shows it.

## Routers in the Gateway

A router can also be an entry in the Gateway's `models`, next to the tier
models. Every Agent Policy that lists the router's name, its tier models and
its default model grants it. A change needs a Gateway upgrade, and reaches
Agents within about two minutes.

Such a router can use a decision model. Microsoft-Decision-1 is deployed in an
Azure AI Foundry resource, not an Azure OpenAI one, and is reached with
LiteLLM's `azure_ai` provider. Its entry sets `model_info.mode: evaluation`:
LiteLLM then calls it only to classify, Agents are not offered it, and the
Gateway refuses requests that ask it for a conversation.

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

The Policy then lists `auto-decision`, `gpt-mini`, `gpt-main` and `o3`; the
decision model need not be listed. Microsoft-Decision-1 reads the Agent's whole
system prompt and its latest messages, and refuses a request it counts at more
than 64,000 tokens; the router then uses `heuristic` for it, without an error.
The [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#choosing-a-classifier)
describes the classifiers' settings.

Sources: [Team model routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_routers.go" >}}),
[Gateway routers]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_shared_routers.go" >}}),
[Gateway request log]({{< repo "app/cetirun-ai-gateway/cetirun_ai_gateway/request_log.py" >}}),
and [Configure a team](../../guides/teams/#smart-routing).
