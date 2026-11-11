---
title: "AI Gateway"
description: "Configure the shared models, GitHub Copilot proxies, credentials, console, and logs of the AI Gateway, and troubleshoot model requests."
weight: 20
---

The AI Gateway is the model endpoint of every Agent. It is based on LiteLLM.
Each Agent sends its model requests to the AI Gateway with its own virtual key.
The AI Gateway sends each request to a provider deployment, or to the GitHub
Copilot proxy of that Agent. It also counts the spend of each key against the
budget of the key.

`make deploy` installs the AI Gateway as the Deployment `cetirun-ai-gateway` in
the platform namespace, `cetirun-system` by default. The AI Gateway is
necessary. Keep the operator's `aiGateway.enabled` and the Portal's
`features.aiGatewayEnabled` at `true`. The platform does not give provider
credentials to Agents.

This page is for platform administrators. Related pages:

- [Model access](../models/) tells which models each Agent gets, and which
  default model it uses.
- [Budgets and spend](../budgets/) tells how budgets and request limits apply,
  and how to read the spend.
- [Smart routing](../smart-routing/) configures routers.
- The AI Gateway chart
  [README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}) is the
  complete reference of the chart.

## How a request goes through the AI Gateway

1. The operator creates one virtual key for each Agent. It writes the value to
   the Secret `ai-gateway-virtual-key` in the namespace of the Agent.
2. The Agent sends a request to the AI Gateway Service with this key.
3. The AI Gateway accepts the request only for a model that the key can use.
4. For a shared model, the AI Gateway sends the request to a provider deployment
   in `models`.
5. For a GitHub Copilot model, the AI Gateway sends the request to the Copilot
   proxy of the Agent. Only the AI Gateway can reach this proxy.
6. The AI Gateway records the spend of the request against the key.

Each key reaches only its own access groups. These groups are the groups of
the shared models that the Agent can use, and the Copilot group of the Agent
itself. For a standard Agent, which has no Agent Policy, these groups also
include `cetirun-shared`. As a result, the Copilot models of an Agent reach
only its own proxy, also under the name of a shared model.

The AI Gateway also examines router fallbacks against the key
(`operatorIntegration`). A key that does not hold the Copilot group of an
Agent never reaches the Copilot routes of that Agent. Examples of such keys are
the master key and a key that can use every model. Keep the teams and keys
that you create in the AI Gateway console limited to access groups.

## Configure the shared models

Shared models are the provider deployments that the AI Gateway serves to Agents.
With `make deploy`, put them in the environment file under
`platform.values.cetirun-ai-gateway.models`. This value is the `model_list` of
LiteLLM:

```yaml
platform:
  values:
    cetirun-ai-gateway:
      providerCredentials:
        existingSecret: gateway-provider-keys  # holds AZURE_OPENAI_KEY
      models:
        - model_name: gpt-main
          litellm_params:
            model: azure/my-gpt-deployment
            api_base: https://example.openai.azure.com/
            api_key: os.environ/AZURE_OPENAI_KEY
            api_version: "2025-04-01-preview"
          model_info:
            base_model: azure/gpt-4o
```

- `model_name` is the name that Agents use.
- `litellm_params.model` is the value that the AI Gateway sends to the provider.
  Its prefix selects the provider handler.
- `api_key: os.environ/<NAME>` reads the key `<NAME>` from the provider-key
  Secret. `make deploy` accepts only this form. Do not put credentials in the
  model list.
- `model_info.base_model` names the model in the LiteLLM price table. It gives
  the deployment a price. Refer to [Set the price of a model](#set-the-price-of-a-model).

Add each provider key to the Secret before the run. Then run the offline plan
and the deployment:

```bash
make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
```

The models that `make deploy` requires depend on `modelRouting.mode` in the
environment file:

| `modelRouting.mode` | Models that `make deploy` requires or refuses |
| --- | --- |
| `auto` (the default) | Requires the router `auto` with a `jev` classifier, `deployment_name: decision-1`, and all four tiers. Also requires the decision model `decision-1`. Other routers are allowed. Refer to [Smart routing](../smart-routing/#1-offer-the-tier-models). |
| `direct` | Requires at least one model. Refuses `auto`, `decision-1`, entries whose `litellm_params.model` starts with `auto_router/`, and entries with a `model_info.mode` other than `chat`, `completion`, or `responses`. An enabled CetiAdmin must name one of the models as `model.default`. |

In `direct` mode, the AI Gateway has no router and no decision model. Team
routers with the `heuristic` or `llm` classifier still work, because they use
only shared chat models. A `jev` classifier needs a decision model, so it does
not work in this mode.

### Reserved names

The chart refuses model names that start with `cetirun-`. The operator
registers its own models under this prefix. With `operatorIntegration`, which
the platform chart sets, the chart also refuses names that start with
`copilot/`. Team routers use `copilot/<model>` for GitHub Copilot tiers. Both
prefixes are compared in any letter case.

The console does not examine these prefixes. Do not add models with these
prefixes in the console. The operator does not give Agents a shared model
with one of these names. It also does not give them a name that starts with
`openai/`, a name with whitespace, or a name with more than 256 characters.

### Models that you add in the console

A model that you add in the AI Gateway console is shared only through the access
group `cetirun-shared`. Only standard Agents get it. An Agent Policy
can grant only the models of the chart's `models`. The operator does not trust
a console deployment that copies the access group of a chart model. While such
a copy exists, Agents do not get that model.

The database of the AI Gateway stores the console models. A chart upgrade does
not remove them. They stay readable only while the salt key stays the same.
Refer to [Credentials and the console](#credentials-and-the-console).

### Set the price of a model

By default, the AI Gateway refuses requests for a model without a price
(`blockUnpricedModels: true`). The reason is that LiteLLM does not apply a
budget to a model that costs zero. While the AI Gateway refuses these requests,
the operator gives Agents only the shared models with a price. The operator
reads the setting that is in effect. A change in the AI Gateway console also
counts.

You can give a model a price in two ways:

- **Name the underlying model.** Set `model_info.base_model` to a model in the
  LiteLLM price table, as in the example above.
- **Set the prices.** Set `input_cost_per_token` and `output_cost_per_token`
  in `litellm_params`, not in `model_info`. Use this when LiteLLM does not know
  the model, or when your contract price is different.

For example:

```yaml
        - model_name: local-model
          litellm_params:
            model: openai/local-model
            api_base: https://models.example.com/v1
            api_key: os.environ/LOCAL_MODEL_KEY
            input_cost_per_token: 0.0000025
            output_cost_per_token: 0.00001
```

A price in `litellm_params` works for ordinary models, for routers, for a model
that is free on purpose (price `0`), and for `cost-based-routing`. A price in
`model_info` works only as a positive price of an ordinary model. Refer to the
chart README's
[Pricing a deployment]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#pricing-a-deployment).

After you add a model, examine the prices that LiteLLM uses, for example the
cached-input price. Refer to the chart README's
[Check what LiteLLM actually holds]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#check-what-litellm-actually-holds).

### Images

The operator reads from the AI Gateway if a model accepts images
(`supports_vision`). For a model that accepts images under a name that is not
in the LiteLLM price table, set `model_info.supports_vision: true`.

A model accepts images only when every deployment of its name accepts them.
When the Copilot route of an Agent has the same name, that route must also
accept them. A router accepts images only when every model that it sends
requests to accepts them. Refer to the chart README's
[Images]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#images).

The configuration of an Agent lists the models that accept images
(`aiGateway.visionModels` of the agent chart). An agent chart without this
value sends images as before. An Agent gets the value when you upgrade it to a
chart that has the value.

### When model changes reach Agents

A change to the models replaces the Pods of the AI Gateway. Each AI Gateway Pod keeps
the configuration that it started with until it is replaced. Before the
operator applies a changed model list to Agents, it waits until the rollout
finishes. As a result, the operator updates each Agent one time, not one time
for each replaced Pod. The rollout does not interrupt running Agents.

When a platform administrator does one of these actions, the operator updates
the affected Agents:

- adds or removes a shared model
- changes the limits of a shared model
- changes if a shared model accepts images
- changes if a shared model is offered, through its price or the AI Gateway's
  setting

The affected Agents are the standard Agents that use shared models, and the
Sandbox Agents whose Agent Policy allows the model. This update restarts these
Agents. The operator also registers their Copilot models again under the names
that now apply. When an Agent restarts, it shows as provisioning for a short
time. Its UI is not available until it is ready again.

> **Warning:** Before you remove a shared model, remove its name from
> `modelAccess.sharedModels.allowed` in each Agent Policy. If an Agent Policy
> allows a name that the AI Gateway catalog does not offer, the operator stops
> its Agents that use shared models. It also blocks the AI Gateway key of each
> of these Agents. Their `PolicyResolved` condition is `False` with the reason
> `DependencyUnavailable` and the message
> `the live AI Gateway catalog cannot resolve every authorized shared model`.

The operator also blocks and stops these Agents in these cases:

- The AI Gateway stops offering the model because the model has no price.
- A console deployment copies the access group of the model.
- An administrator removes or renames a Team router that an Agent Policy
  allows.

To recover, make the AI Gateway offer the model again, or remove its name from
the Agent Policy.

A change to only the base model or the routing fields of a shared model
updates the Copilot routes. The operator applies it at the next check of each
Agent, within ten minutes.

Other changes to a shared model keep the access groups that Agents have.
Examples are a change to its endpoint, its provider parameters, or its price.
The chart derives the access group of a model only from its name. Agents
continue to use the model, and none of them restarts.

## GitHub Copilot proxies

Each Agent that uses GitHub Copilot has its own proxy. The proxy is a separate
Deployment in the namespace of the Agent. It runs the AI Gateway image. Only
the AI Gateway can reach it. The operator's `aiGateway.copilotProxy.enabled`
disables the proxy for the whole deployment. Then Agents use only shared
models, and the Portal does not create an Agent while no shared model is
offered.

A Copilot route can use the name of a shared model, as
[Names of GitHub Copilot models](../models/#names-of-github-copilot-models)
shows. Then the AI Gateway spreads the requests for that name over the shared
deployments and the Copilot route.

When the Copilot route fails, the AI Gateway takes it out of rotation for a few
seconds. It then sends its requests to the shared deployments:

- After a 429 from Copilot, this occurs immediately.
- For other errors, this occurs after most of the recent requests of the route
  fail. As a result, a few requests can fail first.

When the proxy itself cannot be reached, the requests that go to it fail. For
example, this occurs when the proxy restarts after a sign-in. A Copilot model
that has no shared model with the same name has only the Copilot route. A 429
from Copilot then reaches the Agent without change. The next request goes to
Copilot again.

### Health checks of the Copilot routes

By default, the health checks of the AI Gateway do not probe Copilot routes. Each
probe is a real call on the Copilot allowance of the owner. At the default
interval of five minutes, this is about 288 calls a day for each Copilot model
of each Agent.

The operator's `aiGateway.copilotProxy.healthChecks.enabled` enables the probes
for every Agent. With the probes enabled, the AI Gateway takes a Copilot route
that fails its probe out of rotation. This changes where requests go only for
a model that shared deployments also serve.

A change to this setting registers the Copilot models of every Agent again.
An upgrade with the setting off, from a version that let the AI Gateway probe
Copilot models, does the same. This occurs at the next check of each Agent,
within about ten minutes. Agents do not restart. Each route is not available
for a short time while the operator deletes it and adds it again.

## Virtual keys

The operator owns the virtual key of each Agent. The value of the key is in
the Secret `ai-gateway-virtual-key` of the Agent. A replacement key resets the
spend and unblocks a blocked key. The replacement key also gets its budget
again from the source in
[Where the budget of an Agent comes from](../budgets/#where-the-budget-of-an-agent-comes-from).
For these reasons, the operator does not replace a key that still exists in
the AI Gateway.

If the Secret is lost, the Agent reports `AIGatewayReady` as `False` with the
reason `KeySecretLost`. The Agent also gets an `AIGatewayKeySecretLost` event.
To recover, restore the Secret.

If `status.aiGateway.keyId` of the Agent is empty, you can delete the key in
the AI Gateway instead. The operator then creates a new key. If the field has a
value, the operator does not create a new key, even after you delete the old
one.

With `operatorIntegration`, the AI Gateway sets
`general_settings.user_api_key_cache_ttl` to zero. Each new request examines
the current authorization in the database. It does not use the cached grant of
an earlier worker. The cost is more database reads and a dependency on the
database. While database connections recover, requests can get
service-unavailable responses.

Before you rely on this setting, let the AI Gateway rollout finish on every
replica. A later change to a key does not cancel requests that are already
authorized. It also does not stop Agent processes or revoke credentials that
other services hold.

## Credentials and the console

The AI Gateway uses four credentials:

| Credential | Held by | Used for |
| --- | --- | --- |
| Provider API key | The AI Gateway | Calls to the providers |
| Virtual key | Each Agent | Calls to the AI Gateway |
| Master key | The operator and platform administrators | The management API of LiteLLM |
| Console password | A platform administrator | Sign-in to the AI Gateway console |

`make deploy` requires existing Secrets for the provider keys, the master key,
the salt key, the console password, and the Valkey password. It refuses
credentials in the environment file. Create these Secrets before the first
run, as [Install CetiRun](../../getting-started/platform/) shows. The examples
on this page use the names of that page, for example
`cetirun-ai-gateway-auth`.

> **Warning:** Do not change or lose the salt key. It encrypts the provider
> credentials of the models that you add in the console. If the salt key
> changes, these models disappear from the AI Gateway, and the readiness probes
> do not show a problem. Keep a backup copy of the salt key outside the
> cluster.

To write the salt key to a private file for a backup copy, use these
commands. Keep the file outside the repository, and do not paste the value
into a ticket:

```bash
umask 077
kubectl get secret cetirun-ai-gateway-auth -n cetirun-system \
  -o jsonpath='{.data.salt-key}' | base64 -d > /absolute/private/gateway-salt-key
```

The AI Gateway reads its credentials when it starts. After you change a provider
key, the master key, or the console password, restart the AI Gateway:

```bash
kubectl rollout restart deployment/cetirun-ai-gateway -n cetirun-system
kubectl rollout status deployment/cetirun-ai-gateway -n cetirun-system
```

Keep the old provider key valid until the rollout is complete. For the Valkey
password, the database password, and the recovery of a lost salt key, refer to
the chart README's
[Credentials]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#credentials)
and
[Upgrade contract]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#upgrade-contract).

### Open the console

The chart creates no Ingress for the console. To open it, forward a local
port. Then open `http://localhost:4000/ui`, and sign in as `admin`:

```bash
kubectl port-forward -n cetirun-system svc/cetirun-ai-gateway 4000:4000
```

The console password is the `ui-password` key of the credentials Secret, for
example `cetirun-ai-gateway-auth`.

> **Warning:** When you save a settings page in the console, LiteLLM writes
> the whole settings block to its database. After that, the database value
> wins over the chart for every key in that block. There is no warning. To
> return the block to chart control, refer to the chart README's
> [Editing settings in the console]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#editing-settings-in-the-console-takes-them-out-of-chart-control).

Do not change the keys and teams of Agents in the console. The operator owns
them. Refer to [Budgets and spend](../budgets/) for the budget settings that
you can change.

## Logs and metrics

The AI Gateway keeps these records of model calls:

| Record | Where | Content |
| --- | --- | --- |
| Request log | One JSON line for each call on the standard output of the AI Gateway | Key alias, user, Team, Agent, model, tokens, spend, duration, and errors. No prompts or responses |
| Spend log | The `LiteLLM_SpendLogs` table in the database of the AI Gateway. The console reads it | Call metadata. By default, also the prompt and the response of each call |
| Content records | An Azure Data Explorer database, only with `content.enabled`. The console reads the content of a call from there | The prompt and the response of each call. Notes replace inline images and files, and encrypted reasoning |
| Metrics | LiteLLM's Prometheus metrics on a port of each AI Gateway Pod | Spend, requests, tokens, and the remaining budget of each key |

`make deploy` installs the [ADX-Mon collection](../observability/), which keeps
the request log outside the AI Gateway, in Azure Data Explorer. The spend log
keeps each row for seven days by default (`spendLogs.retention`).

> **Warning:** By default, the spend log stores the prompt and the response of
> each call. They can contain sensitive data. To keep only the metadata, set
> `platform.values.cetirun-ai-gateway.spendLogs.storePrompts: false`.

With `content.enabled`, the spend log keeps no prompts or responses. The
content database keeps them instead. Give access to it only to the users who
must read them. Refer to the chart README's
[Content in Azure Data Explorer]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#content-in-azure-data-explorer).

The platform chart enables the metrics, and the ADX-Mon collection scrapes them
from each AI Gateway Pod. The metrics port has no authentication. Each workload
that can reach an AI Gateway Pod can read the key aliases, users, Teams, and
spend of the metrics. If other workloads must not read them, restrict the port
with a NetworkPolicy. The egress of an Agent reaches only the Service port of
the AI Gateway.

For the metric names and query examples, refer to the chart README's
[Request log and metrics]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#request-log-and-metrics).
For the spend of each Agent, refer to [Budgets and spend](../budgets/#read-the-spend).

## Upgrades and rollbacks

`make deploy` upgrades the AI Gateway together with the operator. Each new
AI Gateway Pod applies the database migrations of its LiteLLM version before the
proxy starts. An AI Gateway upgrade does not interrupt Agents.

A rollback to an earlier AI Gateway image does not undo the database migrations.
The earlier version then logs a message with SQL commands that make the
schema older.

> **Warning:** Do not run the SQL commands that an earlier AI Gateway version logs
> after a rollback. They delete the columns that the newer version added, and
> the data in these columns. A later upgrade then fails. Refer to the
> [rollback section]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#rolling-back)
> of the chart README.

After an upgrade from a version that named Copilot models `copilot/<model>`,
the operator registers the Copilot models of each Agent under their new names.
This occurs at the next check of each Agent. Then the Agent restarts with its
new model list. A client outside the Agent that still requests
`copilot/<model>` gets a refusal. The client must use the new name.

For availability during rollouts and database outages, refer to the chart
README's [Availability]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#availability).

## Troubleshoot model requests

Start with the AI Gateway status of the Agent:

```bash
kubectl get agent <agent> -o jsonpath='{.status.aiGateway}{"\n"}{range .status.conditions[*]}{.type}={.status} {.reason}{"\n"}{end}'
```

To find a recent failed call in the request log, search the AI Gateway log for
the key alias of the Agent, `cetirun-<agent>-<agent UID>`. For older calls,
filter `GatewayRequests()` with `Agent == "<agent>"` and `Status == "failure"`.
Refer to the chart README's
[Request log and metrics]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#request-log-and-metrics).

```bash
kubectl logs -n cetirun-system -c gateway --since=15m --tail=-1 \
  -l app.kubernetes.io/name=cetirun-ai-gateway,app.kubernetes.io/component=gateway \
  | grep '"key_alias":"cetirun-<agent>-' | grep '"status":"failure"'
```

| Symptom or message | Cause | Action |
| --- | --- | --- |
| HTTP 402, `Budget has been exceeded!`. OpenClaw reports a billing error of its provider. Hermes says to add credits | The key spent its monthly budget | Raise the budget, or wait for the reset. Refer to [Budgets and spend](../budgets/) |
| HTTP 429 with a `retry-after` header | A request limit of the Agent Policy | Wait, or raise the limit in the Policy. Refer to [Request limits](../budgets/#request-limits) |
| HTTP 429, `No deployments available` | No deployment of the model is available to the key. For example, all deployments are in cooldown after errors, or only Copilot routes serve the name and the key cannot use them | Examine the provider errors in the request log. Wait for the cooldown to end |
| HTTP 403, `This key may not use the model <name>.` | A router sent the request to a tier model that the key cannot use, or the key asked for a decision model | Allow the tier model in the Policy of the Agent. Refer to [Smart routing](../smart-routing/) |
| HTTP 400, `The model <name> is a decision model` | A request asked a decision model for a conversation | Use a chat model. Decision models only classify requests for routers |
| The AI Gateway refuses a model and says to declare pricing | The model has no price, and `blockUnpricedModels` is on | Set a price. Refer to [Set the price of a model](#set-the-price-of-a-model) |
| The create form says `The AI Gateway has not published its models yet` | The operator has not published the model catalog | Wait a minute. If the message stays, examine the operator log and the AI Gateway rollout |
| The Agent page lists a GitHub Copilot model as not offered | The AI Gateway has no price for the model, or a shared model with the same name is another model | Refer to [Model access](../models/#names-of-github-copilot-models) |
| A router shows `the router is not registered in the AI Gateway yet` | The operator has not registered the Team router | Wait about two minutes. Then examine `status.modelRouters` of the Team |
| Copilot phase `SignInRequired`, or Copilot reason `SignInIncomplete` | The owner has not completed the GitHub Copilot sign-in | The owner signs in again on the Agent page |
| Copilot reason `ProxyStarting` | The Copilot proxy of the Agent is not ready yet | Wait a few minutes |
| Copilot reason `NotAllowedRouteRemovalFailed` or `PolicyCheckUnavailable` | The operator cannot remove a Copilot route that the Policy no longer allows, or it cannot read the models or prices of the AI Gateway | Shared models continue to work. The operator tries again about every 15 seconds. Examine the AI Gateway |
| `AIGatewayReady` is `False` with the reason `KeySecretLost` | The Secret of the key is lost | Refer to [Virtual keys](#virtual-keys) |
| `AIGatewayModelsLimited` event | The Agent can use more models than its configuration can list | Refer to [Model access](../models/#limits) |
| The models that you added in the console disappeared after a restart | The salt key changed | Restore the salt key. Refer to the chart README's [Upgrade contract]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#upgrade-contract) |

Do not print or paste key values or token files to diagnose these errors.

Sources: [operator reconcile]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[Copilot model names]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_copilot_merge.go" >}}),
[shared-model catalog]({{< repo "app/cetirun-operator/internal/controller/ai_gateway_catalog.go" >}}),
[AI Gateway guard]({{< repo "app/cetirun-ai-gateway/cetirun_ai_gateway/guard.py" >}}),
[Copilot proxy template]({{< repo "app/cetirun-chart/templates/copilot-proxy.yaml" >}}),
and the [deployment helper]({{< repo "scripts/cetirun_platform_deploy.py" >}}).
