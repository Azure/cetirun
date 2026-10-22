# CetiRun AI Gateway

A LiteLLM-based gateway that gives AI agents a single endpoint for model access.

This chart installs on its own. It has no dependency on the CetiRun operator,
portal or agent chart, so an administrator who already has their own agents can
install it, configure provider keys, and point those agents at the result.

## Install

One command. With the defaults the chart brings its own PostgreSQL and Valkey,
so the only inputs are the models and their keys:

```bash
kubectl create secret generic gateway-provider-keys \
  --from-literal=AZURE_PROD_KEY='...' \
  --from-literal=ANTHROPIC_KEY='...'

helm install gateway oci://<registry>/helm/cetirun-ai-gateway --version <version> \
  --set global.registry=<registry> -f my-values.yaml
```

A minimal `my-values.yaml`:

```yaml
models:
  - model_name: gpt-4o
    litellm_params:
      model: azure/my-gpt-4o-deployment
      api_base: https://example.openai.azure.com/
      api_key: os.environ/AZURE_PROD_KEY
      api_version: "2025-04-01-preview"
    model_info:
      base_model: azure/gpt-4o

providerCredentials:
  existingSecret: gateway-provider-keys
```

`<registry>` is where the CetiRun images and charts are published. The master
key, salt key and console password are generated; see [Credentials](#credentials).

The model list can be empty. Standalone Gateways can add models through the
console later. CetiRun AgentPolicy shared-model grants require chart-configured
models with `operatorIntegration` enabled, as described below.

The last two lines of the model entry are not optional in practice. Without a
price the model is refused, and for newer Azure models an older `api_version`
serves plain chat but fails every tool call an agent makes. See [Budgets and
unpriced models](#budgets-and-unpriced-models) and [Azure
OpenAI](#azure-openai).

### CetiRun AgentPolicy model bindings

With `operatorIntegration: true`, each chart-configured model gets a
`cetirun-model-<name hash>` access group and a
`cetirun-shared-<name hash>-<configuration hash>` deployment ID. The name hash
is the first 16 hexadecimal characters of SHA-256 over `model_name`. The
configuration hash is the same over the complete model object's canonical
JSON, before those generated fields are added. Entries with the same
`model_name`, such as load-balanced deployments of one model, share the group
and keep distinct IDs. Existing non-reserved access groups and provider
parameters remain unchanged. The chart rejects a supplied `model_info.id` or
access group using the reserved `cetirun-model-` prefix.

Agent keys are granted the group, which follows the model name, the identity
an AgentPolicy grants. Changing a configured model's endpoint, provider
parameters, price or other metadata changes only its deployment ID: Agents
keep their grant, and the Operator neither updates their keys nor holds
policy-bound Agents for it. Changes that reach an Agent's own configuration
still update it as before: a model's token limits, or a model without a price
leaving the list while the Gateway blocks unpriced models. Rotating the value
of a referenced Secret changes neither ID nor group. Adding or removing a
model name adds or removes a group; when its catalog refresh sees that, the
Operator updates the keys of Agents with shared models, and re-projects only
the policy-bound Agents whose AgentPolicy allows that name. A name a policy
does not allow changes nothing for its Agents; before, any new name re-projected
every policy-bound Agent, which stops a running native Sandbox Agent for good
(measured on a Kubernetes 1.37 cluster).

The Operator trusts a group only while every deployment carrying it is
configured in this chart rather than added through the console, has a unique
deployment ID of the form above, and serves the model name the group is
derived from. A console deployment under the same name is not automatically
trusted or granted. A console deployment that copies the group makes the
Operator distrust it, and Agents lose that model until the copy is removed.
Two identical entries render the same deployment ID; if the Gateway lists it
twice, the Operator does not trust the group.

Earlier versions of this chart derived the group from the complete model
configuration, so editing a model changed its group, and Agents could not use
the model until the Operator granted the new one. Upgrading changes every
group once. Upgrade this chart together with the Operator, as the platform
chart does: the Operator trusts only groups of the current form. It updates
the keys of Agents with shared models when its catalog refresh sees the new
groups, and re-projects policy-bound Agents once. Native Sandbox Agents are
single-start, so they stay held after that re-projection until they are
recreated.

This behavior is opt-in. Standalone Gateways retain their existing model list
and custom deployment IDs unchanged when `operatorIntegration` is false. The
platform umbrella chart already enables this integration; no additional
platform value or image is needed.

## Requirements

**PostgreSQL** holds virtual keys, spend records, and any model added through the
admin console.

**Redis or Valkey** is required whenever `replicaCount` is above 1, and the chart
rejects that combination without it. This is not a performance option: each
replica otherwise keeps its own copy of every key budget and rate limit, so the
effective limit is multiplied by the replica count. Measured against LiteLLM, a
`$0.10` budget permitted `$0.20` of spend across two replicas, and adding Redis
restored the correct figure.

Missing Redis does not affect recording. Measured without it, spend totals,
spend logs and daily usage aggregates were complete across replicas; only
enforcement broke. With Redis configured, recording has gaps of its own; see
[Known gaps](#known-gaps).

Nothing durable lives in Redis. The record of spend is in PostgreSQL, and Redis
only holds in-flight coordination, so it needs no persistence, backup or
replication.

What the Gateway does when either one is unavailable is described under
[Availability](#availability).

### Bundled or your own

By default both are bundled: a single PostgreSQL on a persistent volume and a
single password-protected Valkey, from the Azure Linux images on MCR. That is
enough for evaluation, demos and small installations.

For anything that needs backups or high availability, use managed services
instead:

```yaml
postgresql:
  bundled:
    enabled: false
  urlSecret:
    existingSecret: gateway-database   # key database-url: postgresql://...

redis:
  bundled:
    enabled: false
  host: my-redis.example.com
  passwordSecret:
    existingSecret: gateway-redis      # key redis-password
```

The bundled PostgreSQL behaves like an external database across the release's
lifecycle. `helm uninstall` keeps its volume and its password Secret, so an
install under the same release name finds the data again: measured, a model
added through the console before the uninstall was served after it.

To discard the data, delete what `helm uninstall` left behind yourself: the
volume claim, and the `-auth`, `-postgresql` and, when the chart generated one,
`-valkey` Secrets. All of them carry the release's instance label:

```bash
kubectl get pvc,secret -l app.kubernetes.io/instance=<release>
```

For a release called `gateway` the claim is
`data-gateway-cetirun-ai-gateway-postgresql-0`. With a long release name the
bundled resources' names are shortened and carry a hash instead.

The volume's size and storage class are fixed at install. The default size is
100Gi: the database keeps the spend log of every agent using the Gateway, for
`spendLogs.retention`, so it grows with the number of agents and how much they
run. A StatefulSet's
volume claim template cannot be changed, so Kubernetes refuses an upgrade that
changes `postgresql.bundled.persistence.size` or `storageClass`: measured,
going from 8Gi to 16Gi failed with `spec.volumeClaimTemplates` and left the
release marked failed until an upgrade with the original values restored it. To
grow the database, expand the claim itself and keep `persistence.size` as it
was. That works when the claim's storage class allows expansion
(`allowVolumeExpansion: true`), as the AKS built-in classes do:

```bash
kubectl patch pvc data-gateway-cetirun-ai-gateway-postgresql-0 \
  -p '{"spec":{"resources":{"requests":{"storage":"200Gi"}}}}'
```

Local connections inside the PostgreSQL container are trusted, as in the
upstream image; connections from other pods need the password. Measured: a
wrong password from another pod is refused, and Valkey refuses a client without
one.

### Response caching is off

Budgets are only shared across replicas once LiteLLM's cache is backed by Redis,
so the chart configures that cache. Left at its default, the same cache also
answers identical requests itself. Observed on this chart before the fix:

- an identical request was served from Redis rather than the provider;
- the cache key is built from the request content, so two callers sending the
  same content got one shared answer, generated for whichever asked first;
- an agent retrying after a bad answer would get the same answer back.

A cache hit costs nothing at the provider and is recorded as zero spend, which
is correct accounting rather than a leak. What it does break is the expectation
that each request reaches the model, and the isolation between callers.

The chart sets `cache_params.mode: default_off`, which keeps the Redis-backed
coordination and makes response caching something a request has to ask for.
Verified both ways: identical requests reach the provider, and a budget still
holds exactly across two replicas.

Note that the `x-litellm-response-cost` header reports the full cost even on a
cache hit, so it cannot be used to tell whether one happened.

## Models

The `models` value is LiteLLM's own `model_list`, passed through unchanged, so
upstream provider documentation applies directly.

`model_name` is what a client asks for. `litellm_params.model` is what the
Gateway sends upstream, carrying a prefix that selects the provider handler.

Keep credentials out of the model list. Reference them as `os.environ/NAME` and
supply the value through `providerCredentials`, so keys never appear in rendered
templates or in `helm get values` output.

### Reserved names

Model names beginning with `cetirun-` are rejected: that prefix belongs to models
the CetiRun operator registers automatically. With `operatorIntegration` on, names
beginning with `copilot/` are rejected too: the operator keeps `copilot/<model>`
as an alias of each CetiRun Agent's GitHub Copilot model on that Agent's key,
and the alias takes precedence over a model of the same name. A standalone
Gateway may keep serving models under `copilot/`. Both prefixes are matched in any letter case. The console checks
neither, so do not add models under them there; the operator leaves such shared
models out of the model list it configures for Agents. It does the same for names
under `openai/`, which the agent chart refuses as a provider prefix, and names
with whitespace or more than 256 characters. Shared models under other provider
prefixes are listed like any other.

The reservation exists because adding a model under a name that already exists
does not replace the original. Both are served under the same name and traffic
is split between two different backends, with nothing visible to the client to
indicate it.

### Reasoning models

A request that sets a reasoning effort, `reasoning_effort` on Chat Completions
or `reasoning.effort` on the Responses API, is refused by LiteLLM with a 400
(`UnsupportedParamsError`), before anything reaches the provider, when LiteLLM
does not take the deployment for a reasoning model. It decides by name and by
its price table, so a reasoning model under a name of your own may need one
more setting. Measured on LiteLLM 1.105.1 with made-up deployment names in
`litellm_params.model` and a mock provider:

| Deployment | Chat Completions | Responses API |
| --- | --- | --- |
| `azure/<deployment name>` | 400 | passed on |
| the same with `model_info.base_model: azure/gpt-5.4-nano` | passed on | passed on |
| the same with `model_info.supports_reasoning: true` | 400 | passed on |
| `openai/<name>` | passed on | 400 |
| the same with `model_info.base_model: gpt-5.4-nano` | passed on | 400 |
| the same with `model_info.supports_reasoning: true` | passed on | passed on |

For Azure OpenAI, set `base_model`, which also prices the deployment: on Chat
Completions, LiteLLM accepts a reasoning effort for an `azure/` deployment only
when its name or `base_model` names an o-series, GPT-5 or GPT-6 model, and
`supports_reasoning` does not change that. `base_model` does not decide whether
a chat request with tools goes to the Responses API; see [Azure
OpenAI](#azure-openai). For a reasoning model under
`openai/`, such as one behind another OpenAI-compatible endpoint, whose name is
not in LiteLLM's price table, set `model_info.supports_reasoning: true`;
`base_model` does not stand in for it. A deployment named after its model, such
as `azure/gpt-5.4-nano` or `openai/gpt-5.4-nano`, needs neither. LiteLLM's
`drop_params`, which the chart does not set, would turn each 400 into a request
sent without the reasoning effort, with nothing to tell the client.

## Budgets and unpriced models

LiteLLM computes spend from a built-in price table, multiplying it by the tokens
a request used. A model missing from that table costs zero, and **budget checks
are skipped entirely for zero-cost models** ╬ô├ç├╢ so a budget set on a virtual key
silently does nothing.

This is not an edge case. An Azure OpenAI deployment name such as
`my-gpt4-prod` is not in the price table, and naming a deployment is the
ordinary way to configure Azure. Measured against a real deployment, a `$0.0001`
budget allowed every request.

The chart therefore sets `blockUnpricedModels: true`, which refuses such a
request with a message naming the model and telling you to declare pricing.
Failing at the first request is better than never enforcing a budget somebody
believes is in place.

The console can change the setting later. LiteLLM keeps that change in its
database, where it takes precedence over the chart's value (see [Editing
settings in the console](#editing-settings-in-the-console-takes-them-out-of-chart-control)).
With CetiRun, the operator reads the setting in effect and gives Agents only
the shared models the Gateway would serve.

### Pricing a deployment

Two ways, both verified to price requests made with a virtual key and to enforce
a budget on them.

**Name the underlying model**, when the deployment runs one LiteLLM knows. The
price comes from LiteLLM's own table, so there are no figures to maintain:

```yaml
models:
  - model_name: gpt-4o
    litellm_params:
      model: azure/my-gpt4-prod
      api_base: https://example.openai.azure.com/
      api_key: os.environ/AZURE_KEY
      api_version: "2025-04-01-preview"
    model_info:
      base_model: azure/gpt-4o
```

**Declare prices explicitly**, when it does not, or when your contract price
differs from list price. The figures are yours to keep current:

```yaml
    model_info:
      input_cost_per_token: 0.0000025
      output_cost_per_token: 0.00001
```

With neither, the model is refused.

### Check what LiteLLM actually holds

A model's price has more parts than the two you set: cached input is billed at a
discount, and cache writes and context size are tracked separately. Explicit
prices override only the fields you name. When the deployment name matches a
model family in LiteLLM's table, the rest are filled in from there; when it
matches nothing, LiteLLM logs a warning that the cache prices default to zero,
which would make cached input free and undercount spend.

So after adding a model, look at what LiteLLM is using rather than assuming:

```bash
curl -s http://localhost:4000/model/info -H "Authorization: Bearer $MASTER_KEY" \
  | jq '.data[] | {model_name, price: (.model_info | {input_cost_per_token,
         output_cost_per_token, cache_read_input_token_cost,
         cache_creation_input_token_cost, max_input_tokens})}'
```

If `cache_read_input_token_cost` is missing or zero, set it to the provider's
cached-input price. For Azure OpenAI and OpenAI, set
`cache_creation_input_token_cost` equal to the input price, since writing to the
cache is not charged separately.

Measured on a real Azure deployment with only input and output prices set: the
cache-read price and a 922k-token context size were inherited from LiteLLM's
table, cache writes were charged at the input price, and each request's charge
matched those rates exactly.

### When a budget runs out

Budgets are in US dollars; there is no token allowance. A request over budget is
refused with HTTP 402 and `Budget has been exceeded!` by default
(`budgetExceededStatusCode`). LiteLLM's own default is 422 since 1.105, and 429
before that. The status decides what an agent does next. Measured with
OpenClaw 2026.9.6 and Hermes v2026.9.24 against an exhausted key:

| Status | OpenClaw | Hermes |
| --- | --- | --- |
| 402 | stops after one request: a billing error, the API key has run out of credits | stops after one request: billing or credits exhausted |
| 422 | stops after one request: the provider rejected the request schema or tool payload | stops after one request: the request was rejected as malformed |
| 429 | 10 attempts over two to two and a half minutes, then "API rate limit reached" | 3 attempts, then asks the user to wait a minute and retry |

With 402 both agents stop at once and say that credit ran out, which is
closest to the cause; with 429 they keep retrying although the budget only
comes back at its reset. The wording for 402 still points at the provider's
billing or API key rather than the Gateway's budget, so a user may look for the
problem in the wrong place. To choose another status, set
`budgetExceededStatusCode` to 422 or 429; the chart refuses any other value.
Other agents and other versions were not measured.

A `budget_duration` of `30d` or `1mo` resets on the first of each month, UTC,
rather than thirty days after the key was created. Resets are applied by a
background job that runs about every ten minutes, so a key can stay refused for
up to that long past its reset time. Measured with a one-minute budget: spend
was cleared and the key usable again about twelve minutes after the period
ended, with the next reset time moved on.

## Providers

Verified against a live Azure OpenAI deployment, with OpenCode as the agent:
model list, streaming, multi-round tool calling, spend recording, and budget
enforcement through a virtual key.

Everything else in LiteLLM's provider catalogue is expected to work and has not
been tried here. The list will grow as providers are actually exercised rather
than assumed.

### Azure OpenAI

**`api_version` has to support the Responses API**, not only Chat Completions.

For newer reasoning models, Azure refuses function tools on
`/chat/completions`:

> Function tools with reasoning_effort are not supported for this model in
> /v1/chat/completions.

LiteLLM knows this for a deployment named after such a model, and sends its
chat requests that carry tools to `/responses` instead; for other names, see
below. An agent's requests almost always carry tools, so in practice every
agent turn goes through the Responses API, and `api_version` decides whether
that endpoint exists. Measured directly against one deployment:

| `api_version` | Chat | Chat with tools | Responses | Responses with tools |
| --- | --- | --- | --- | --- |
| `2024-10-21` | 200 | 400 | **404** | ╬ô├ç├╢ |
| `2025-04-01-preview` | 200 | 400 | 200 | 200 |

With `2024-10-21`, the failure is easy to miss. A plain chat request succeeds,
so a smoke test passes. An agent then gets a reply to requests without tools ╬ô├ç├╢
OpenCode's session title generation, for instance ╬ô├ç├╢ while every real turn fails
with:

```
litellm.NotFoundError: AzureException NotFoundError - {"error":{"code":"404","message": "Resource not found"}}
```

and the Gateway log shows the request it made:
`404 Not Found for url '.../openai/responses?api-version=2024-10-21'`.

Use `2025-04-01-preview` or later. The "Chat with tools" column stays at 400
whatever the version; that refusal is the reason for the bridge, not a problem
with it.

LiteLLM goes by the deployment's own name, which in 1.105.1 has to begin with
`gpt-5.4` or a later version, such as `gpt-5.4-nano`. Measured on LiteLLM
1.105.1 with a mock provider: a chat request with tools to `azure/gpt-5.4-nano`
went to `/responses`, while one to a deployment with a name of its own and
`base_model: azure/gpt-5.4-nano` stayed on `/chat/completions`, where such a
model refuses it. So name the deployment after the model it runs. Otherwise,
`model_info.mode: responses` has LiteLLM send every chat request of that
deployment to `/responses`: in the same test, plain and streamed chat requests
got ordinary Chat Completions answers at the same price, and requests with
tools were sent there too. Its health probes then call the Responses API as
well, without the 16-token limit, and a moved request carries `store` only
when the client sends it, so otherwise the provider's default applies.

### Upstream errors are not retried

LiteLLM retries a failed upstream call twice by default, and agents retry on
their own, so the two multiply. Measured: one failing request became three
upstream calls, and the client waited four seconds for the error. An agent that
also retries twice would turn one failure into nine. Retrying a 429 only makes
the rate limiting worse.

The chart sets `litellm_settings.num_retries: 0`. Verified on two replicas: a
429 or a 500 from the provider reached the client at once, after one upstream
call. The cluster test checks this against the running version.

Saving the console's Router Settings page stores the value the Gateway is
running with, so it stays at zero. Replaying that save stored
`num_retries: 0`, and failures were still not retried.

A caller cannot turn retries back on for its own request. LiteLLM accepts
`num_retries` in the request body or the `x-litellm-num-retries` header, and
`model_group_retry_policy`; measured, each made one failing request four
upstream calls. The image's guard callback, which the entrypoint always
registers, drops those fields, and the cluster test checks it; see
[The guard callback](#the-guard-callback).

Sending a failed request to another deployment of the same model is not a
retry; see [Routing](#routing).

## Routing

Several entries in `models` with the same `model_name` are deployments of one
model: two Azure OpenAI resources, for example, or Azure OpenAI and OpenAI.
`router.routingStrategy` chooses one of them for each request; see
[Choosing a deployment](#choosing-a-deployment). An entry with a higher
`litellm_params.order` is kept in reserve until those with a lower one are
unavailable. Which deployments serve a model, and which model falls back to
which, are the administrator's; the `router` values set the rest.

Measured with two Gateway replicas and the bundled Valkey, against mock
deployments standing in for separate Azure OpenAI resources, with an
OpenClaw-like client and with OpenClaw itself, and checked against Azure
OpenAI where noted. The figures in tables were measured on LiteLLM 1.101.0,
the image's previous release; what changed in 1.105.1 is said where it
applies.

### Choosing a deployment

`router.routingStrategy` sets LiteLLM's `routing_strategy`:

- `simple-shuffle`, the default, picks at random, or in proportion to the
  `weight`, `rpm` or `tpm` in each entry's `litellm_params`: the first of the
  three that any entry sets to a positive value. Only this strategy sends a
  failed request to another deployment of the same model (`router.failover`).
- `least-busy` picks the deployment with the fewest requests in flight. Each
  replica counts only its own requests.
- `latency-based-routing` picks the deployment with the fastest recent
  responses. Each replica measures its own.
- `usage-based-routing-v2` picks the deployment with the fewest tokens this
  minute, skipping those over their `rpm` or `tpm`. The counts are kept in
  Redis, so every replica sees them; each request adds Redis calls.
- `cost-based-routing` picks the cheapest deployment. A deployment's price is
  `input_cost_per_token` and `output_cost_per_token` in its `litellm_params`,
  or else LiteLLM's price table entry for its `litellm_params.model`; it does
  not read `base_model` or `model_info`. A deployment with neither counts as
  the most expensive, which is the usual case for Azure OpenAI deployment
  names, so set the prices in `litellm_params`. With health checks on,
  LiteLLM 1.105.1 logs an error with a traceback for every probe under this
  strategy, since its cost tracker looks for a deployment ID that probes do
  not carry; requests are not affected.

LiteLLM's other strategies are refused: `usage-based-routing` selects without
running LiteLLM's auto routers and pre-call checks, and the others need
settings the chart does not provide. Each of `weight`, `order`, `rpm` and `tpm`
has to be set on every entry of a model or on none, and the chart refuses a
model that sets one on only some: LiteLLM gives an entry without `weight`,
`rpm` or `tpm` no requests while another has it, and leaves an entry without
`order` out while another has one.

### Primary and standby deployments

`litellm_params.order` keeps deployments in reserve. Here a discounted provider
serves the model first, and Azure OpenAI stands by:

```yaml
models:
  - model_name: gpt-4o
    litellm_params:
      model: openai/gpt-4o
      api_base: https://discount.example.com/v1
      api_key: os.environ/DISCOUNT_KEY
      order: 1
      input_cost_per_token: 0.000002
      output_cost_per_token: 0.000008
  - model_name: gpt-4o
    litellm_params:
      model: azure/my-gpt-4o-deployment
      api_base: https://example.openai.azure.com/
      api_key: os.environ/AZURE_PROD_KEY
      api_version: "2025-04-01-preview"
      order: 2
    model_info:
      base_model: azure/gpt-4o
```

Requests go to order 1. With health checks on, a deployment that failed its
last probe is skipped before anything is sent. A request that fails on order 1
is sent to order 2 within the same request, with every routing strategy.
Requests return to order 1 once its probe passes again, or, without health
checks, once its cooldown ends. Deployment affinity is off for such a model;
see [A key stays on its deployment](#a-key-stays-on-its-deployment). Measured
on LiteLLM 1.105.1 with mock deployments: with order 1 failing its probes, a
key's requests went to order 2, and once order 1 passed again they went back
to it.

### A failed request goes to another deployment

`router.failover`, on by default, sends a request that failed on one
deployment once more, to another deployment of the same model (LiteLLM's
`enable_weighted_failover`), with the `simple-shuffle` strategy only. The
failed deployment is not tried again, and a model with a single deployment
gets no second attempt, so `num_retries` stays 0.

| One of two deployments failing | Requests failed, of 30 |
| --- | --- |
| With 500, `router.failover: false` | 13 |
| With 429, `router.failover: false` | 2 |
| With 500 or 429, `router.failover: true` | 0 |

Without it, LiteLLM stops sending requests to a failing deployment only after
several failures, and only for a few seconds. Each replica counts the failures
it sees on its own; the cooldown that follows is shared through Redis. The
cost: a request the provider refuses as invalid, with a 400, is sent to the
other deployment too before the error reaches the client.

The GitHub Copilot routes the CetiRun operator registers for each Agent share
names with the shared models and with each other's, and are told apart by
access group. With failover on, no Agent's request reached another Agent's
route.

With `router.deploymentAffinity` on, a key stays on the deployment that served
it last, and failover cannot move it while that deployment is still in
rotation: it moves once LiteLLM has cooled the deployment down. LiteLLM does
that at once after a 429 from a model with other deployments, and for other
errors only after repeated failures, weighed against the deployment's
successes in the current minute. Measured with a key kept on a deployment that
returned 500: the first 2 of 10 requests failed, the other 8 went to another
deployment; right after 30 successful requests in the same minute, 8 of 8
failed. With 429, none of 10 failed.

### A continuation stays on its deployment

A Responses API request that continues an earlier one with
`previous_response_id` works only on the deployment that stored the earlier
response. `router.continuationAffinity`, on by default, sends it there. Across
two deployments, 6 of 20 continuations failed without it, none with it. When
that deployment is down, the continuation fails either way.

### Encrypted reasoning stays with its resource

A reasoning model can return its reasoning encrypted, and only the Azure
OpenAI resource or OpenAI organization that produced it can read it. Agents
that keep no state on the provider, such as OpenClaw, ask for it and send it
back with every turn. Measured on Azure OpenAI: another model on the same
resource accepted it, and a made-up or altered item was refused with a 400,
`invalid_encrypted_content`. That another resource refuses it the same way was
measured against mock resources only. OpenClaw then retries the turn once
without the encrypted reasoning. So when one model is served by more than one
resource, a conversation that has been on both pays an extra call, and loses
the reasoning it had, on every later turn: 61 of 84 turns in the test.

`router.encryptedContentAffinity`, on by default, keeps each conversation on
the deployment that encrypted the earliest reasoning in it, or on one with the
same endpoint and key (LiteLLM's `encrypted_content_affinity`). It marks the
items it returns, so it needs no state in Redis, and turning it off later is
safe: the marks are removed from every request either way. A deployment that
authenticates without an API key is never treated as sharing an endpoint and
key with another. The check applies to every model: LiteLLM cannot turn it off
for one.

LiteLLM forwards an item without the mark as it is: an item from before the
check was turned on, such as in a conversation an agent began before an
upgrade, or from elsewhere. When it reaches a deployment of another resource
or account, that deployment refuses the whole request with a 400,
`invalid_encrypted_content`, and a router fallback configured for the model
fails the same way. So while the check is on, the guard callback removes the
encrypted content and ID of such an item before the request is routed, and
keeps its readable summary, as LiteLLM does when it moves a marked item; an
item with nothing readable is left out. Such a conversation loses the encrypted
reasoning of its earlier turns, once; what the agent sends from then on is
marked. Measured on a test installation: an Azure OpenAI item without its mark
sent to a GitHub Copilot route of the same model was refused, and a Copilot
item sent to Azure OpenAI was refused for its ID; with their marks, both were
answered. OpenClaw had retried such turns without the encrypted reasoning, at
the cost of the failed call and of its fallback. Measured with mock resources:
with the guard, every such replay was answered, streamed or not, and an item
with the mark was routed as before; with the check off, nothing is removed.

The mark is written so that clients keep it. LiteLLM writes it as
`litellm_enc:<base64>;<encrypted content>`. OpenClaw keeps encrypted reasoning
only when it looks like an opaque token (letters, digits, `+ / _ -` and
trailing `=`); anything else goes through its transcript's secret redaction,
which shortens a `gAAAA╬ô├ç┬¬` value to its first six and last four characters. On
a test installation every OpenClaw turn after one with reasoning sent such a
shortened item back, was refused with `invalid_encrypted_content`, went
through the model's fallbacks, which failed the same way, and was retried by
OpenClaw without its reasoning: new conversations too, on Azure OpenAI and
GitHub Copilot alike. The guard callback therefore makes LiteLLM write the mark
as `ctrnenc<deployment ID in hex>x<encrypted content>` and read both forms, so
items marked before an upgrade still route; encrypted content that a client
shortened (it contains `╬ô├ç┬¬`) is removed like an item without the mark. Measured
with mock resources on LiteLLM 1.105.1: every item a response carries, streamed
and not, is in the new form, and items in either form are routed back to their
deployment and reach it unmarked.

A request that a deployment refuses for its encrypted reasoning (400,
`invalid_encrypted_content`) does not go on to the model's `router.fallbacks`:
a fallback's deployments cannot read that reasoning either, so each one fails
the same way. The guard ends the fallback, and the client gets the
deployment's 400, which OpenClaw answers by sending the turn again without the
encrypted reasoning. Other errors fall back as before. Measured with mock
resources: the refused request reached no fallback deployment and no
deployment went into cooldown; a 500 still fell back.

When that deployment fails, LiteLLM 1.105.1 keeps the conversation on it while
it still counts as available, so the conversation's requests fail with the
deployment's error; `router.failover` does not rescue them, since the check
pins the second attempt to the same deployment, which that attempt has to
skip. The deployment stops counting as available while it is in cooldown or
marked unhealthy by the health checks, and only then does the conversation
move: its turns go to another deployment without any encrypted reasoning, with
the reasoning summaries, until the deployment counts as available again.
Measured with two replicas and mock deployments: no deployment received
another's encrypted reasoning, and none refused a turn with a 400. LiteLLM
1.101.0 failed every such turn until the deployment was back, which is why the
check used to be off by default.

A cooldown starts once more than half of at least five calls to the deployment
in a minute failed on one replica, or at once on a 429, 401 or 404, and lasts
five seconds; the request whose failure starts it is the only one
`router.failover` sends on. Without health checks, a conversation therefore
leaves a failing deployment only for moments. With health checks on, as by
default, the conversation's requests fail only until a probe marks the
deployment unhealthy: within one `router.healthChecks.intervalSeconds`, 300
seconds by default, on the replica that probes, and up to one interval later
on the others, which reuse its result. The conversation then stays moved while
the probes keep failing. Keep health checks on; a shorter interval narrows the
window, at the cost of more probes.

### A key stays on its deployment

`router.deploymentAffinity`, on by default, sends each virtual key's requests
for a model to the deployment that served its last one, for an hour (LiteLLM's
`deployment_affinity`). Providers cache the start of a prompt on the
deployment that saw it, so an agent that keeps its conversation on one
deployment pays less and waits less for each turn. With Redis, every replica
sees the same choice. While that deployment is unavailable, the key's requests
go to the others. Measured on LiteLLM 1.105.1 with two replicas and two
deployments: four keys sent 10 requests each, alternating between the
replicas, and each key's 10 reached one deployment.

A request that fails on the deployment its key is kept on fails over to
another deployment (`router.failover`), like any other. LiteLLM 1.105.1 applies
the key's choice before it leaves out the deployments a failover already
tried, so the failover found no deployment and was not attempted: measured on
the e2e cluster, a key kept on a deployment that answered 500 got three 500s
("No fallback was attempted") before that deployment went into cooldown. The
Gateway's guard gives LiteLLM's affinity check only the deployments the
failover may still use; after the change the same key's requests were answered
by another deployment from the first failure.

It is off for a model whose deployments set `litellm_params.order`: LiteLLM
applies the choice before it keeps only the lowest order, and keeps the first
choice for the hour, so a key that reached the standby while the primary was
down would stay on the standby after the primary is back. The chart turns it
off for such a model in `model_group_affinity_config`, which keeps the other
checks on for it. Measured on LiteLLM 1.105.1: a key whose first requests came
while the primary was failing its probes went to the standby; once the primary
passed again, with affinity on for that model all 6 of its requests stayed on
the standby, with the chart's setting all 6 went back to the primary.

### Fallbacks

`router.fallbacks` lists, for a model, the models to try when a request to it
still fails, in order:

```yaml
router:
  fallbacks:
    - gpt-4o: [gpt-4o-mini]
  contextWindowFallbacks:
    - gpt-4o-mini: [gpt-4o]
```

With `operatorIntegration`, a fallback target is used only if the caller's key
may use it. `contextWindowFallbacks` applies to requests too long for the
model's context window. Measured earlier on a model with two deployments, one
unreachable: a fallback group turned 6 failed requests of 30 into none.

### Health checks

`router.healthChecks.enabled`, on by default, probes every deployment every
`router.healthChecks.intervalSeconds`, 300 by default, and sends requests only
to those that answered their last probe. Measured with one of two deployments
failing: every request went to the other one, where 10 of 30 failed without
health checks. When every deployment of a model fails its probe, requests are
still sent.

Every deployment means the models in `models`, those added through the
console, and the GitHub Copilot routes the CetiRun operator registers for each
Agent. Each probe is a real call, with `max_tokens` 16, so a probe of an
Agent's Copilot route spends that Agent owner's own GitHub Copilot allowance:
about 288 small calls a day per route at the default interval. A router from
LiteLLM's auto routing is not probed itself; its models are. Set
`model_info.mode` on a model that is not a chat model, such as an embedding or
image model, so it is probed the right way, and
`model_info.disable_background_health_check: true` on a model that should not
be probed.

Probes start at most `router.healthChecks.concurrency` at a time, 8 by
default (`health_check_concurrency`). Without a limit LiteLLM starts every
probe of a cycle at once: measured on LiteLLM 1.105.1, every deployment was
probed within about 100 ms, and one GitHub account answered 11 to 43 of a
cycle's Copilot probes with 429. LiteLLM retries a failed probe at the SDK
level, which adds calls, unless the deployment sets
`model_info.health_check_params: {max_retries: 0}`. The CetiRun operator
registers every GitHub Copilot route that way: measured on LiteLLM 1.105.1
against an upstream answering 429, a route registered with it got one call per
probe, and one without it three.

A probe answered with 429 or 408 is ignored for routing: the deployment keeps
the state of its previous probe, for up to two intervals, and no cooldown
starts. `/health` and the stored results still list it as unhealthy. The
results are stored in PostgreSQL.

With Redis, which the chart requires above one replica, one replica probes at
a time, under a lock in Redis, and the others use its results
(`use_shared_health_check`); the chart sets `DEFAULT_SHARED_HEALTH_CHECK_TTL`
to the interval so a shared result is not reused past the next probe, and
`DEFAULT_SHARED_HEALTH_CHECK_LOCK_TTL` to the interval as well: the lock
otherwise expires after 60 seconds, and a longer cycle would let another
replica probe every deployment again. Without
Redis, the single replica probes on its own. Measured on LiteLLM 1.105.1 with
two replicas, a 30-second interval and six mock deployments, two of them added
through the management API with per-Agent Copilot access groups: each
deployment was probed twice in 65 seconds, and with one of a model's two
deployments failing its probes, 12 of 12 requests went to the other and none
reached it.

Each probe writes a line to the request log with `"call_origin":
"health_check"` and the key alias `litellm-internal-health-check`; leave those
lines out of usage figures. The results are kept in PostgreSQL, for
`spendLogs.retention` like the spend log, and `/health/latest` and the
console show the latest result of each deployment. `/health` returns the last
results too; with health checks off, it probes every deployment live when
called with the master key. Agents' keys are refused there.

### Smart routing

An entry in `models` whose `litellm_params.model` is
`auto_router/complexity_router` is a router (LiteLLM's auto routing): for each
request, it picks one of four tiers, and sends the request to the model
configured for that tier.

```yaml
models:
  - model_name: auto
    litellm_params:
      model: auto_router/complexity_router
      # Only for the unpriced-model check; see below.
      input_cost_per_token: 0.00001
      output_cost_per_token: 0.00004
      complexity_router_config:
        tiers:
          SIMPLE: gpt-4o-mini
          MEDIUM: gpt-4o
          COMPLEX: gpt-4o
          REASONING: o3
      complexity_router_default_model: gpt-4o
    model_info:
      # The smallest of its models', for agents that read the limits.
      max_input_tokens: 128000
      max_output_tokens: 16384
```

The chart ships the mechanism; the tiers, their models and any keyword rules are
the administrator's. Measured with LiteLLM's default classifier, which scores
the latest message without a model call: "What is 2+2?" went to the simple
tier, a request for code to the medium one. A classifier that asks a model
(`classifier_type: llm`) makes that call for every request, charged to the
caller's key; the request log records it as a line of its own, with
`"call_origin": "autorouter_classifier"`. Anyone can move a request one tier
up by writing LiteLLM's escalation keyword, `LITELLM ESCALATE`, into it.

**The router's name needs a price.** The unpriced-model block applies to it like
any model, so the chart requires `input_cost_per_token` and
`output_cost_per_token` in its `litellm_params`, above zero; a price in
`model_info` is not enough. Measured: each call was charged at the price of the
model it picked, not the router's, and a key's budget held: with a $0.0001
budget, the fourth call was refused.

**OpenClaw's own context is skipped.** The classifier reads the newest message
a person wrote. OpenClaw 2026.9.6 sends its own context as a last user message
between `<<<BEGIN_OPENCLAW_INTERNAL_CONTEXT>>>` and
`<<<END_OPENCLAW_INTERNAL_CONTEXT>>>`, and classified by that block every
OpenClaw request went to the simple tier, a request for code included. Unless a
router sets its own `reminder_markers`, the chart adds that pair and LiteLLM's
own, `<system-reminder>`, whose blocks the classifier leaves out; the request
for code then went to the medium tier. OpenClaw called the router through Chat
Completions, without reasoning settings. A router's own `reminder_markers`
replace the chart's and have to be a list of `{open, close}` pairs, two
different strings that are not blank, as LiteLLM 1.105.1 requires; the chart
rejects anything else rather than let the proxy refuse to start.

**The agent keeps its conversation and its limits.** Unless a router sets them,
the chart changes three of LiteLLM 1.105.1's router defaults:

- `context_compaction: false`. LiteLLM would otherwise rewrite the
  conversation history near the chosen model's input limit, with extra model
  calls. Agents such as OpenClaw and Hermes compact their own context.
- `max_tokens_from_tier_model: false`. LiteLLM would otherwise replace the
  caller's `max_tokens` with the output limit of the tier's model; the
  caller's value is forwarded instead.
- `enable_context_window_escalation: true`. A long conversation whose newest
  message is simple would otherwise land on a tier whose models cannot hold
  it, and the provider would refuse it. LiteLLM moves it to the lowest tier
  with a model whose declared window fits; set `max_input_tokens` in each
  model's `model_info` for that.

**A router is granted only with its models.** With `operatorIntegration`, the
CetiRun operator puts a router on an Agent's key only when the Agent may use
every model the router sends requests to. The models of its tiers, its default
model and a context compaction model have to be shared models the Agent may
use: with an AgentPolicy, models its shared-model list names, as it has to name
the router. A classifier model, `classifier_llm_config.model`, a
`jev_classifier_config.deployment_name` or the `embedding_model` of semantic
keyword matching, has to be a shared model but need not be listed: the router
calls it to classify the Agent's request, charged to the Agent's key. An Agent
without a policy may use every shared model, so it gets every router whose
models are all shared models. A router the operator cannot read completely is
not given to Agents with a policy: one with a `complexity_router_config` field
LiteLLM 1.105.1 does not have, a classifier or routing plugin, a jev classifier
without a `deployment_name`, or another kind of LiteLLM auto router. A router
an Agent may not use stays off its key and out of its model list; when the
Agent's policy lists the router, the Agent's page in the Portal says why. The
operator reads the routers from the Gateway's model list, so a changed router
reaches the keys at its next listing, within about two minutes; a router's
configuration is not part of an AgentPolicy's Gateway projection.

**Models that answer no conversation are not offered to Agents.** A shared
model whose `model_info.mode` is set to something other than `chat`,
`completion` or `responses`, such as `evaluation` for a decision model like
Microsoft-Decision-1 or `embedding`, is left out of the models Agents are
configured with, of their default model, and of the catalog the Portal reads.
It stays a shared model otherwise: a router may classify requests with it, and
the key of an Agent without a policy keeps its access group. A router whose
tier, default model or compaction model is such a model is misconfigured. Set
`mode` on the model's `model_info`; a model without one counts as a chat model.

For the keys the operator issues, the Gateway refuses with HTTP 403 a request a
router sends to a model outside the key's access groups; the router's own
classifier calls are exempt (see [The guard callback](#the-guard-callback)).
Other keys reach every model of the router's tiers, whether or not they may use
them directly. A key that may use the models but not the router cannot use the
router. The chart rejects a tier, default model or classifier model that is not
configured in `models`.

**The tiers decide what a router saves.** Each call is charged at the price of
the model it picked, and, with a classifier that asks a model, the classifier
call as well, which reads the conversation and so costs more as it grows. A
router saves where it sends turns to a model cheaper than the one the
conversation would otherwise use, so order the tiers by price and keep the top
tier near the model you would use without the router. Measured with the same 30
turns (three conversations with a 6,000-token context) on Azure OpenAI:

| Through | Cost |
| --- | --- |
| A router with tiers gpt-5.4-nano, gpt-5.6-terra, gpt-6-sol, gpt-6-astra and the Decision classifier | 0.250 |
| gpt-6-sol alone | 0.122 |
| A router with tiers gpt-5.4-nano, gpt-6-sol, gpt-6-sol, gpt-6-sol (estimated from the same turns) | 0.097 |

In the first router gpt-5.6-terra costs more than gpt-6-sol a tier above it,
and gpt-6-astra five times as much. Classifier calls were 3.4% of its cost.

Each model keeps its own prompt cache. A turn on a model the conversation has
not used yet pays the full input price for the whole conversation; a later
turn on a model it used before reads that model's cached history again, other
models' turns in between included. A 30,000-token conversation alternating
between gpt-5.4-nano and gpt-6-astra cost 18 to 23% less than the same turns on
gpt-6-astra alone, with gpt-6-astra reading its cache on each return; the
largest single charge, there and on gpt-6-astra alone, was gpt-6-astra's first
read of the conversation. Providers keep a cache for minutes of inactivity, so
a model used rarely may find it gone.

Check a classifier with prompts in the languages your users write. Of 24
prompts, eight each in Chinese, Chinese mixed with English, and English, the
default classifier sent all eight Chinese ones to the simple tier, proofs and
system designs included (6 of 24 on the expected tier); the Decision
classifier placed 8 of 8 English, 6 of 8 mixed and 3 of 8 Chinese prompts on
the expected tier, sending hard Chinese prompts to the simple one.

**A conversation can change model.** Each request is classified on its own, so
one conversation can move between tiers. On the same Azure OpenAI resource that
kept working, encrypted reasoning included. Across resources, with LiteLLM
1.101.0, an agent such as OpenClaw retried the turn without its encrypted
reasoning; LiteLLM 1.105.1, with `router.encryptedContentAffinity` on, leaves
out the reasoning the new resource cannot read before it sends the turn
(from LiteLLM's source; not measured here). Either way that reasoning is lost,
so keep a router's models on one resource where you can. LiteLLM can keep a
conversation on its first tier (`session_affinity`, or `classification_mode:
user_turn`) only for clients that send a session ID it recognizes; OpenClaw
2026.9.6 sends none it does.

**One router per licensed capability.** Without a LiteLLM licence, LiteLLM
1.105.1 lets at most one router claim each of: `classifier_type: heuristic_v2`,
`classifier_type: capability`, `classifier_type: llm_v2`, and its own
`tier_definitions` or classifier prompt (`classifier_llm_config.system_prompt`,
`classification_prompt` or `classification_examples`, with a classifier that
calls a model). With two in one class, the proxy refused to start; the chart
rejects the second. One router in each class is accepted.

The request log line of a routed call carries the router's name as `model`, the
model it picked as `provider_model`, and `routing_tier`, `routing_cause`,
`routing_score`, `routing_escalated` when an escalation keyword or the prompt's
length moved it up a tier, and, with a model classifier, `classifier_model` and
`classifier_cost`. [Measuring the routers](#measuring-the-routers) has queries
over them.

#### A router across providers

A router's tiers can mix providers: here GPT models on Azure OpenAI for the
simple and medium tiers, and a Claude model deployed in Azure AI Foundry for
the complex and reasoning tiers. Adjacent tiers on the same provider keep a
conversation that moves one tier up or down on its provider.

```yaml
models:
  - model_name: gpt-mini
    litellm_params:
      model: azure/my-gpt-mini-deployment
      api_base: https://example.openai.azure.com/
      api_key: os.environ/AZURE_OPENAI_KEY
      api_version: "2025-04-01-preview"
    model_info:
      base_model: azure/gpt-4o-mini
      max_input_tokens: 128000
  - model_name: gpt-main
    litellm_params:
      model: azure/my-gpt-deployment
      api_base: https://example.openai.azure.com/
      api_key: os.environ/AZURE_OPENAI_KEY
      api_version: "2025-04-01-preview"
    model_info:
      base_model: azure/gpt-4o
      max_input_tokens: 128000
  - model_name: claude
    litellm_params:
      model: azure_ai/my-claude-deployment
      api_base: https://example.services.ai.azure.com/anthropic
      api_key: os.environ/AZURE_AI_FOUNDRY_KEY
      # The Claude model rejects some OpenAI parameters, such as temperature
      # with top_p, or a forced tool_choice; LiteLLM drops those it rejects.
      drop_params: true
      # Prompt caching on the system message and the newest message.
      cache_control_injection_points:
        - location: message
          role: system
        - location: message
          index: -1
      input_cost_per_token: 0.000003
      output_cost_per_token: 0.000015
    model_info:
      max_input_tokens: 200000
  - model_name: auto
    litellm_params:
      model: auto_router/complexity_router
      input_cost_per_token: 0.000003
      output_cost_per_token: 0.000015
      complexity_router_config:
        classification_mode: user_turn
        tiers:
          SIMPLE: gpt-mini
          MEDIUM: gpt-main
          COMPLEX: claude
          REASONING: claude
      complexity_router_default_model: gpt-main
```

Offer such a router to clients that use the Chat Completions API. A client
using the Responses API or Anthropic's Messages API sends the provider's own
reasoning items back with each turn; at a switch between providers, the other
provider's reasoning is dropped or the request may be refused. The first turn
on each provider's model pays the full input price for the conversation; see
"The tiers decide what a router saves" above.

#### Measuring the routers

With the request log collected by Container Insights (see [One log line per
call](#one-log-line-per-call)), these queries show how the routers route. Run
one on the Logs page of the Log Analytics workspace and pick the time range
there. When more than one Gateway writes to the workspace, add
`| where PodNamespace == "<namespace>"` under the line with `ContainerLogV2`.
The first four count only the calls clients sent: a call LiteLLM makes itself,
such as a health check probe or a classifier call, carries a `call_origin`. A
routed call has a `routing_cause`; one without a `routing_tier` went to the
router's default model, for example because its classifier failed.

Per router, tier and the model that served the call: answered calls and their
share of the router's answered calls, failed attempts, and answered calls that
an escalation keyword or the prompt's length moved up a tier. A request that
failed on one deployment and went on to another writes a line for each
attempt, so `failed` counts attempts:

```kusto
let routed = ContainerLogV2
| where ContainerName == "gateway"
| where tostring(LogMessage.log_type) == "cetirun.ai_gateway.request"
| where isempty(tostring(LogMessage.call_origin)) and isnotempty(tostring(LogMessage.routing_cause))
| extend router = tostring(LogMessage.model), tier = tostring(LogMessage.routing_tier),
    model = tostring(LogMessage.provider_model), ok = tostring(LogMessage.status) == "success";
routed
| summarize answered = countif(ok), failed = countif(not(ok)),
    escalated = countif(ok and tostring(LogMessage.routing_escalated) == "true") by router, tier, model
| join kind=inner (routed | summarize router_answered = countif(ok) by router) on router
| project router, tier, model, answered, share_pct = round(100.0 * answered / router_answered, 1),
    failed, escalated
| order by router asc, answered desc
```

What each tier spent, in total and per answered call, and what classifying the
requests that went to it cost (`classifier_cost`, with a model classifier):

```kusto
ContainerLogV2
| where ContainerName == "gateway"
| where tostring(LogMessage.log_type) == "cetirun.ai_gateway.request"
| where isempty(tostring(LogMessage.call_origin)) and isnotempty(tostring(LogMessage.routing_cause))
| summarize answered = countif(tostring(LogMessage.status) == "success"),
    spend = sum(todouble(LogMessage.spend)), classifier_cost = sum(todouble(LogMessage.classifier_cost))
    by router = tostring(LogMessage.model), tier = tostring(LogMessage.routing_tier)
| extend spend_per_call = spend / answered
| order by router asc, spend desc
```

How much of each tier's input the provider read from its prompt cache: cached
input tokens as a share of all input tokens, which include them, over answered
calls:

```kusto
ContainerLogV2
| where ContainerName == "gateway"
| where tostring(LogMessage.log_type) == "cetirun.ai_gateway.request"
| where isempty(tostring(LogMessage.call_origin)) and isnotempty(tostring(LogMessage.routing_cause))
| where tostring(LogMessage.status) == "success"
| summarize prompt_tokens = sum(tolong(LogMessage.prompt_tokens)),
    cached_tokens = sum(tolong(LogMessage.cached_tokens))
    by router = tostring(LogMessage.model), tier = tostring(LogMessage.routing_tier)
| extend cached_pct = round(100.0 * cached_tokens / prompt_tokens, 1)
| order by router asc, tier asc
```

How long each tier's answered calls took, and, for streamed calls, how long
until the first token:

```kusto
ContainerLogV2
| where ContainerName == "gateway"
| where tostring(LogMessage.log_type) == "cetirun.ai_gateway.request"
| where isempty(tostring(LogMessage.call_origin)) and isnotempty(tostring(LogMessage.routing_cause))
| where tostring(LogMessage.status) == "success"
| summarize answered = count(), avg_ms = round(avg(todouble(LogMessage.duration_ms))),
    p95_ms = percentile(todouble(LogMessage.duration_ms), 95),
    first_token_p95_ms = percentile(todouble(LogMessage.time_to_first_token_ms), 95)
    by router = tostring(LogMessage.model), tier = tostring(LogMessage.routing_tier)
| order by router asc, tier asc
```

The model classifiers' own calls, by classifier model: how many there were, how
many failed, what they cost and how long they took. A classified request waits
for its classifier before its model is called. The routed calls'
`classifier_cost` repeats the spend of these calls, so add one or the other to
a router's spend, not both:

```kusto
ContainerLogV2
| where ContainerName == "gateway"
| where tostring(LogMessage.log_type) == "cetirun.ai_gateway.request"
| where tostring(LogMessage.call_origin) == "autorouter_classifier"
| summarize calls = count(), failed = countif(tostring(LogMessage.status) == "failure"),
    spend = sum(todouble(LogMessage.spend)), avg_ms = round(avg(todouble(LogMessage.duration_ms))),
    p95_ms = percentile(todouble(LogMessage.duration_ms), 95)
    by classifier = tostring(LogMessage.model)
```

**What the same requests would have cost on the most expensive tier is not in
the log.** It needs the prices of that tier's model, which no line carries, and
the tokens that model would have used, which nothing records: another model
counts the prompt in its own tokens, answers at its own length, and keeps a
prompt cache of its own. Spend per answered call compares tiers on different
requests, not on the same ones.

LiteLLM 1.105.1 makes that estimate itself. Its console's Cost Optimization
page, marked experimental, shows it in the Auto-Router tab, which only proxy
admins see and which reads `GET /auto_router/benchmarks`; no LiteLLM licence
is needed. It prices a routed request's own tokens at the prices of the most
expensive model in the router's highest tier, and subtracts what the request
and its classifier cost. Apart from some requests through the Anthropic
Messages API, it prices only requests that neither read from nor wrote to a
prompt cache, and counts every other request as having saved nothing; an agent
resends its conversation with every turn, which providers cache, so that can be
most of its requests. LiteLLM fills the tab's tables only while it writes its
spend log (`spendLogs.enabled`, on by default). From LiteLLM's source; not
tried here.

None of these figures says whether the cheaper tiers answered as well as the
most expensive one would have; that needs the requests themselves.

### Settings saved in the console

These settings are in LiteLLM's `router_settings` and `general_settings`.
Saving the console's Router Settings page writes the whole block to the
database, which then wins over the chart; see
[Editing settings in the console takes them out of chart control](#editing-settings-in-the-console-takes-them-out-of-chart-control).

## Administration

The admin console is enabled and served on the same port as the API. The chart
creates no Ingress; reach it through a port-forward. Resource names are prefixed
with the release name, so for a release called `gateway`:

```bash
kubectl port-forward svc/gateway-cetirun-ai-gateway 4000:4000
```

Then open <http://localhost:4000/ui>.

On Windows, the port-forward window prints lines like `error copying from local
connection to remote stream ... An existing connection was forcibly closed` while
an agent is in use. They come from the client on your own machine dropping idle
connections as it opens new ones; requests at those moments completed normally.
They only matter if the agent shows an error at the same time, or if the
port-forward itself exits.

The console password is separate from the master key on purpose. When LiteLLM
has no UI password configured it accepts the master key instead, which turns a
console sign-in into full management API access.

`auth.adminUI.enabled: false` sets LiteLLM's `DISABLE_ADMIN_UI`. In v1.101.0
that makes the console page say it is disabled and turns off SSO sign-in, but
the sign-in endpoint still answers: posting the master key to `/login` returned
a console session. That grants nothing the master key does not already grant,
so treat the setting as hiding the console rather than removing it.

### Revoking a key takes up to a minute to reach every replica

A deleted virtual key is refused at once by the replica that handled the
deletion, and keeps working on the others for up to 60 seconds. Each replica
caches key lookups in memory for that long, and **Redis does not change it**:
measured with Redis configured, the other replica accepted the deleted key for
55 to 60 seconds, the same as without.

The window is set by `general_settings.user_api_key_cache_ttl`, which defaults
to 60. Set to 10 in a test, it shrank to 5 to 10 seconds. The chart does not set
it. The environment variable `DEFAULT_MANAGEMENT_OBJECT_IN_MEMORY_CACHE_TTL`
looks like it should control this and does not; LiteLLM passes its own default
of 60 to the key cache, so the variable is never consulted.

### Give agent keys model routes only

A virtual key created through the management API with default settings cannot
create, change or list keys, add or delete models, or read spend logs. It can
call `/health`, though, and each call makes a real request to the backend,
charged to no budget. A leaked key could use that to generate upstream traffic.

Keys created in the console as the "AI APIs" type are limited to model routes
and are refused on `/health`. Create keys through the API the same way:

```json
{"models": ["gpt-4o"], "max_budget": 20, "allowed_routes": ["llm_api_routes"]}
```

LiteLLM files the lookup of one model, `GET /v1/models/<model>`, under its
internal-user routes rather than the model routes, so such a key gets a 403
there while `GET /v1/models` works. Clients that look a model up before using
it, Hermes among them, need it: add the two prefixes, which match only that
lookup and Google's `generateContent` routes (model routes already). The keys
the CetiRun operator issues carry them, and the operator adds them to keys it
issued before:

```json
{"allowed_routes": ["llm_api_routes", "/v1/models", "/models"]}
```

### Serving CetiRun Agents

The CetiRun operator gives each Agent a key restricted to access groups, and
registers the Agent's GitHub Copilot models in a group of the Agent's own,
`cetirun-copilot-<Agent UID>`, each pointing at that Agent's proxy. Set
`operatorIntegration: true` for such a Gateway; the `cetirun-platform` chart sets
it for the Gateway it installs. It reserves the `copilot/` prefix, see
[Reserved names](#reserved-names), and sets
`general_settings.enforce_fallback_model_access`.

An Agent sees one name per model. A Copilot model goes under the name of a
shared model the Agent is granted when they are the same model: the shared
model's real model is each of its deployments' `model_info.base_model` or,
without one, its `litellm_params.model` when LiteLLM's price table prices that
name, and all its deployments must agree. Both sides are compared without the
provider prefix, in lower case, with dots as dashes, so `claude-sonnet-4.5`
matches `anthropic/claude-sonnet-4-5`. A Copilot model with a date version, such
as `gpt-4.1-2025-04-14`, is never merged, and when several shared models match,
the first name in sort order is used. Such a route copies the shared model's
routing fields: the lowest `litellm_params.order` of its deployments and, of the
deployments at that order, the largest `weight`, `rpm` and `tpm` any of them
sets. `router.routingStrategy` then spreads the Agent's requests over the shared
deployments and its own route, and LiteLLM's default cooldown takes a failing
route out of rotation, see [Failover](#a-failed-request-goes-to-another-deployment).
LiteLLM neither cools down nor retries elsewhere a request that could not
connect, as when the Agent's proxy is down: until the health checks take the
route out of rotation at their next probe (`router.healthChecks`), the requests
that land on it fail. Measured with a route whose proxy refused connections:
4 of 20 requests failed. Spend is counted at the model's list price either way.

Any other Copilot model goes under its own ID, such as `gpt-5.5`, with
`litellm_params.cooldown_time: 0`: it is the only deployment the Agent's key
reaches under that name, so a 429 from Copilot reaches the Agent as it is,
instead of taking the route out of rotation for every request. If a model the
Gateway serves already has that name and is another real model, the Copilot
model is left out and reported on the Agent (`status.aiGateway.copilot.skipped`).
When it is the same real model, as a shared model the Agent is not granted,
the route takes that name in the Agent's own access group: the Agent's key
reaches only its own Copilot route there, never the shared deployments. The operator keeps `copilot/<model>`,
the names of earlier versions, as aliases on the Agent's key. When the shared
models change, it registers the routes again with the names and fields that
then apply.

LiteLLM checks a key against the requested model only. A fallback the Gateway
is configured with, in `router_settings` or the console, is chosen after that
check, and without the setting LiteLLM tries it unchecked; for a model name
several Agents share, it then picks among all of them when the caller's groups
match none. So a shared model's fallback to a Copilot model could reach another
Agent's proxy. With the setting, LiteLLM skips a fallback target the caller's key
could not request directly.

The groups only separate Agents while keys and teams are limited to them.
LiteLLM narrows a key granted a model only through access groups to the
deployments in those groups, but a key or team allowed every model, or a model
name directly, has no group to filter by, so it would spread that key's requests
for a name Copilot routes share over every Agent's proxy. The image's guard
callback prevents that: a key reaches a per-Agent GitHub Copilot route only if
it holds that route's access group; keys granted every model, such as the master
key, reach only the shared deployments of a shared model's name. The operator
creates teams and keys without such grants; do not add them in the console.

### The guard callback

The image's entrypoint always registers a second callback, the guard, before
LiteLLM loads its configuration, whatever `requestLog` says. Settings saved in
the console cannot remove it. It:

- drops request fields a caller could use to change the Gateway for its own
  call: `litellm_request_debug`, and the retry fields described under
  [Upstream errors are not retried](#upstream-errors-are-not-retried);
- leaves the per-Agent GitHub Copilot routes out of a request unless the
  calling key holds the route's access group, `cetirun-copilot-<Agent UID>`.
  A key granted every model, such as the master key, a key holding `*` or the
  model name of a route, and a request whose key LiteLLM does not pass on,
  reach none of them; asking for a name only Copilot routes serve, such a key
  gets LiteLLM's "No deployments available" 429. An auto router's choice of
  tier is held to the same rule;
- holds a key the CetiRun operator issued to the deployments in its access
  groups, also where LiteLLM does not: LiteLLM checks a key against the model
  a request names, and when that is an auto router, it does not check the
  model the router picks. The operator grants a router only to Agents that may
  use every model it sends requests to; the guard enforces that on each
  request, also before the operator withdraws a grant after a configuration
  change. A request the router sends to a model the key may not use gets a
  403, "This key may not use the model <name>.", with no retry, fallback or
  cooldown, and the Gateway logs a warning with the model and the key's alias.
  The request log line of such a call has the status `failure`, `error_code`
  403 and the router's name as `provider_model`, so the queries under
  [Measuring the routers](#measuring-the-routers) count it as a failed attempt
  of its tier under the router's name.
  When the key may use the model but none of its deployments is available,
  LiteLLM's own error applies. The call a router makes to its classifier model
  (`classifier_type: llm` and the other classifiers that ask a model) is made
  on the key's behalf and is not held to the key's groups. The guard
  recognizes the operator's keys by their model list: only `cetirun-` access
  groups, or `no-default-models`. The master key, and keys granted models by
  name, `*` or every model, are left to LiteLLM's own checks;
- keeps a key the CetiRun operator issued from listing what it cannot talk to:
  `GET /v1/models` (and `/model/info`) leaves out the key's access groups that
  LiteLLM lists as if they were models, such as an Agent's Copilot group before
  its owner signs in, and models whose every deployment sets a `mode` other
  than `chat`, `completion` or `responses`, such as a decision model a router
  classifies with; `GET /v1/models/<name>` answers 404 for them. The operator
  leaves them out of the Agent's configuration as well. Other callers see
  LiteLLM's listing;
- while `router.encryptedContentAffinity` is on, removes the encrypted content
  of Responses API reasoning items that carry no mark of the deployment that
  produced it, or that a client shortened, keeping their summaries; makes
  LiteLLM write that mark in a form clients keep; and ends a request's
  fallbacks once a deployment refused its encrypted reasoning; see
  [Encrypted reasoning stays with its resource](#encrypted-reasoning-stays-with-its-resource);
- removes sampling parameters from a request to a reasoning model of the
  GPT-5 or GPT-6 series, asked for by name or chosen by an auto router:
  `temperature` other than 1, `top_p`, `logprobs` and `top_logprobs`, which
  LiteLLM refuses with a 400 for such a model unless the reasoning effort
  resolves to `none`. Hermes, for one, sends side tasks such as conversation
  titles with `temperature: 0` to the model the user chose or to the router,
  and those failed whenever that was a reasoning model, on Azure and on
  Copilot routes alike, without the user seeing it. LiteLLM's own rule
  decides, and the Gateway logs a warning for each request it changes;
- lets a request fail over from the deployment its key is kept on; see
  [A key stays on its deployment](#a-key-stays-on-its-deployment);
- keeps error messages free of LiteLLM's internal deployment list: error
  messages returned to callers no longer include LiteLLM's internal list of
  deployments in cooldown. The response keeps its status, 429, the "Try again
  in N seconds" wording and, on the Chat Completions and Responses APIs, the
  `retry-after` header; LiteLLM sets none on its Anthropic Messages API. The
  Gateway's own log and the request log keep the full message.

With `operatorIntegration` off, the guard's request fields and error messages
apply all the same; a standalone Gateway has no `cetirun-copilot-` groups for
it to separate, and no keys the operator issued.

Measured on LiteLLM 1.105.1 with two replicas and two Copilot routes added
through the management API under one `copilot/` name: each of two Agent keys
reached only its own route, 6 of 6; a key granted every proxy model, a key
granted the `copilot/` name and the master key got the 429; and the 429 for a
model with every deployment in cooldown carried no deployment list on the
Chat Completions, Responses and Anthropic Messages APIs.

Measured again with two Agents' routes under a shared model's name next to its
two shared deployments, and under a name of their own: 12 keys of each Agent,
5 requests each, reached the shared deployments and their own route, never the
other Agent's; shared-only keys, 12 keys granted every proxy model and the master
key reached only the shared deployments; the master key, a key granted every
proxy model and a key granted the name directly got the 429 for the name only
Copilot routes serve. A route with the copied `order: 1` next to a primary and a
standby took requests with the primary and none went to the standby; with the
primary failing, a shared-only key's requests all went to the standby. A route
of its own name answering 429 passed the 429 on and served the next request
right after; the same route without `cooldown_time: 0` answered "No deployments
available" until its cooldown ended, a few seconds later. `copilot/<model>` aliases on the key reached the
Agent's own route on the Chat Completions and Responses APIs; a key without the
alias got 403.

Measured on LiteLLM 1.105.1 with one replica, mock models each in an access
group of its own, and routers over them configured as the chart renders them:
a key granted a router and the models of its simple and medium tiers was
served on those tiers and got the 403 for its complex tier, on the Chat
Completions API, streamed or not, and on the Responses and Anthropic Messages
APIs. That model received no call, and a key granted it was served by it right
after. A router registered through the management API, as the operator
registers a Team's routers, behaved the same, and its GitHub Copilot tier
reached only the caller's own route, 6 of 6. A router with `classifier_type:
llm` called its classifier model for a key not granted it, then served the
tiers the key holds. A key granted the router by name and the master key
reached every tier. The request log records a refused request with status
`failure` and error code 403: one line, or two for a streamed request.

On a test installation with two replicas and Azure OpenAI models, the same
held: through a router configured in the chart, a key granted the router and
its simple tier's model was answered on that tier and refused with the 403 on
its medium and reasoning tiers, on the Chat Completions and Responses APIs,
and a key granted every tier was answered on each; through a Team's router
with an LLM classifier, a key not granted the classifier model got the
classifier's decision and answers from the tiers it holds, and the 403 for the
other, also when the request claimed to be a classifier call.

## Credentials

Four distinct things:

| Credential | Held by | Used for |
| --- | --- | --- |
| Provider API key | Gateway | Calling OpenAI, Anthropic, Azure OpenAI and so on |
| Virtual key | Each agent | Calling this Gateway |
| Master key | Operators and scripts | LiteLLM's management API |
| Console password | A human administrator | Signing in to the admin console |

The chart generates the master key, salt key and console password when no
existing Secret is supplied, and reads each back from the live Secret on upgrade
rather than regenerating it.

Reading them back relies on Helm's `lookup`, which returns nothing when a chart
is rendered with `helm template`: two renders produced two different salt keys.
Argo CD renders charts that way, so under it every sync would generate new
values, salt key included. With Argo CD, or any tool that renders with
`helm template`, supply all three through existing Secrets, and use an external
PostgreSQL, whose bundled counterpart generates its password the same way.

The bundled Valkey's password is generated the same way too. Supply it through
`redis.passwordSecret`, or use your own Redis: a password generated anew at
each render updates the Secret without restarting Valkey or the Gateway, so the
next restart of only one of them leaves the two with different passwords, and
budgets stop being shared across replicas. With `redis.passwordSecret` set,
the chart generates no Valkey password, and Valkey and the Gateway both read
yours.

### Changing a credential takes a restart

The Gateway reads its credentials from environment variables when it starts.
After changing a provider key, the master key or the console password in a
Secret, restart it:

```bash
kubectl rollout restart deployment/gateway-cetirun-ai-gateway
```

Measured: a minute after a provider key changed in its Secret, both replicas
were still sending the old one, and a `helm upgrade` with unchanged values did
not restart them. After the restart both sent the new key. Keep the old provider
key valid until the rollout finishes; Azure OpenAI issues two keys per resource
for this purpose.

Inline credentials (`providerCredentials.inline`) are the exception: the chart
restarts the pods itself when an inline value changes.

A Redis password from `redis.passwordSecret` is read at startup as well, and
with the bundled Valkey the server reads it too. After changing it, restart
both; until both have restarted, budgets are not shared across replicas:

```bash
kubectl rollout restart deployment/gateway-cetirun-ai-gateway-valkey
kubectl rollout restart deployment/gateway-cetirun-ai-gateway
```

The bundled PostgreSQL's password takes one more step. The database applies
`POSTGRES_PASSWORD` only when it initializes an empty volume, so changing the
Secret alone leaves the `litellm` role on its old password, and the Gateway's
next Pods cannot connect. Change the role first, check the new password, then
change the Secret, which holds the password twice, then restart the Gateway.
Run this in bash, with `gateway` set to the Gateway's Deployment name
(`<release>-cetirun-ai-gateway`, or `cetirun-ai-gateway` when the platform
chart installs it):

```bash
(
  set -euo pipefail
  gateway=gateway-cetirun-ai-gateway
  # The database's StatefulSet and Secret share a name, which the chart
  # shortens for a long release name: read it from the Gateway.
  ref='{.spec.template.spec.containers[?(@.name=="gateway")].env[?(@.name=="DATABASE_URL")].valueFrom.secretKeyRef.name}'
  db="$(kubectl get deployment "$gateway" -o jsonpath="$ref")"
  [[ -n "$db" ]] || { echo "Deployment $gateway names no database Secret; nothing changed" >&2; exit 1; }
  kubectl get statefulset "$db" >/dev/null # only the bundled PostgreSQL has one
  # Read and check the database URL before changing anything.
  url="$(kubectl get secret "$db" -o jsonpath='{.data.database-url}' | base64 -d)"
  [[ "$url" =~ ^postgresql://litellm:[^@/]+@[^@/]+/litellm$ ]] ||
    { echo "unexpected database URL in Secret $db; nothing changed" >&2; exit 1; }
  password="$(openssl rand -hex 24)"
  url="postgresql://litellm:${password}@${url#*@}"
  # Encode the Secret's new values now, so that a failure changes nothing.
  password64="$(printf '%s' "$password" | base64 | tr -d '\n')"
  url64="$(printf '%s' "$url" | base64 | tr -d '\n')"
  [[ -n "$password64" && -n "$url64" ]] ||
    { echo "could not encode the new values; nothing changed" >&2; exit 1; }
  # Change the role over the Pod's local socket, which the database trusts.
  printf "ALTER ROLE litellm WITH PASSWORD '%s';\n" "$password" |
    kubectl exec -i "${db}-0" -c postgresql -- \
      psql -U litellm -d litellm -v ON_ERROR_STOP=1 -q -f -
  # Check the new password over the network, the way the Gateway connects.
  ip="$(kubectl get pod "${db}-0" -o jsonpath='{.status.podIP}')"
  [[ -n "$ip" ]] || { echo "Pod ${db}-0 has no IP" >&2; exit 1; }
  printf '%s\n' "$password" | kubectl exec -i "${db}-0" -c postgresql -- sh -c \
    'read -r PGPASSWORD && export PGPASSWORD && psql -h "$1" -U litellm -d litellm -tAc "select 1"' \
    sh "$ip" >/dev/null
  printf '{"data":{"password":"%s","database-url":"%s"}}' "$password64" "$url64" |
    kubectl patch secret "$db" --type merge --patch-file /dev/stdin
  kubectl rollout restart "deployment/${gateway}"
  kubectl rollout status "deployment/${gateway}" --timeout=10m
)
```

Each command runs only after the previous one succeeded. A failure before
`ALTER ROLE` changes nothing. A failure after it leaves the database and the
Secret with different passwords: the running Gateway keeps the connections it
holds, but a Gateway Pod that starts cannot connect. Fix the cause and run the
block again before the Gateway restarts; each run sets a new password and
brings the database, the Secret and the Gateway back in line.

The password reaches `psql` and `kubectl patch` on their standard input, not
as an argument, so it does not show in process listings.

Measured on AKS: requests kept working throughout, the restarted Gateway
connected with the new password, the database refused other passwords over the
network, a restart of the database Pod kept its data, and a later
`helm upgrade` kept the new password, because the chart reads the Secret back.

## Upgrade contract

### The salt key must never change

`LITELLM_SALT_KEY` encrypts provider credentials stored in the database, and
LiteLLM offers no rotation path.

When it changes, the proxy starts normally and `/health/readiness` reports
healthy, while every model stored in the database disappears from `/model/info`
and `/v1/models`. The only signal is a warning repeated in the log. Restoring the
original value recovers everything, so the danger is losing the value rather
than changing it.

The chart preserves its generated value across upgrades. What it cannot protect
against is the Secret being deleted, so back it up:

```bash
kubectl get secret gateway-cetirun-ai-gateway-auth -o jsonpath='{.data.salt-key}' | base64 -d
```

In PowerShell, where `base64` is not available:

```powershell
$v = kubectl get secret gateway-cetirun-ai-gateway-auth -o jsonpath='{.data.salt-key}'
[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($v))
```

If the Secret is deleted anyway, the next `helm upgrade` generates a new salt key
and master key without complaint, and the running pods carry on with the old
values, so nothing looks wrong. The damage arrives with the next restart,
whatever causes it. Measured: after that restart, models added through the
console were gone from the model list and refused, and the previous master key
was refused with 401.

To recover, write the original value back into the chart's Secret and restart:

```bash
kubectl patch secret gateway-cetirun-ai-gateway-auth --type merge -p \
  "{\"data\":{\"salt-key\":\"$(printf '%s' "$SALT_KEY" | base64)\"}}"
kubectl rollout restart deployment/gateway-cetirun-ai-gateway
```

with `$SALT_KEY` holding the value you backed up. Restore `master-key` the same
way to get the previous master key back. Verified with both restored: the models
and the previous master key returned, and the next upgrade kept the restored
salt key.

Readiness probes do not detect this failure. An upgrade check should compare the
model count before and after, or assert the Secret is unchanged.

### Uninstalling keeps the credentials Secret

`helm uninstall` leaves `<release>-cetirun-ai-gateway-auth` in place, and a later
`helm install` under the same release name and namespace picks it up again.

This is deliberate. The database outlives the release, whether it is external or
the bundled one's volume, so before the Secret was kept, an uninstall followed
by an install generated a new salt key against the old database. Measured:

| After uninstall and install | Secret deleted | Secret kept |
| --- | --- | --- |
| Salt key, master key, console password | all regenerated | unchanged |
| Models added through the console | gone from the model list, requests 400 | served |
| Models in `models` | served | served |
| Virtual keys | still valid | still valid |
| Previous master key | 401 | still valid |

The only sign of the first column was a decryption warning in the log. The model
rows stay in PostgreSQL, encrypted with a key that no longer exists.

To remove the Gateway completely, delete the Secret yourself after uninstalling
╬ô├ç├╢ and only if the database is being discarded too. Deleting it while keeping the
database leaves every console-added model permanently unreadable.

### Upgrading LiteLLM

Rebuild the image for the new release (see [Image](#image)) and upgrade. Each new
Gateway pod applies the new version's migrations in its init container before
the proxy starts.

Measured from `v1.101.0` to `v1.102.0` with data in place, on the upstream
image before the chart moved to its own: six migrations applied, virtual keys
and their spend preserved, models added through the console still decrypted and
served, and a budget partly spent before the upgrade was enforced exactly after
it. Measured again from 1.101.0 to 1.105.1, with the entrypoint's `migrate` on
the upstream 1.105.1 image: 30 migrations applied in six seconds, the key and
its spend kept, a model added through the console still decrypted and served,
and the key's budget enforced after it.

From 1.105, LiteLLM builds two indexes on `LiteLLM_SpendLogs` after the
migrations rather than in one, so that building them never blocks writes. Its
proxies build them only when they migrate themselves, which the chart's do not,
so the `migrate` step builds them after the migrations; without them, queries
on the spend log are slower. A build that could not finish does not fail the
step: the next pod's migration tries again.

### Rolling back

`helm rollback` to a revision with an older image works: the older version
started, passed readiness, served both configured and console-added models, and
kept key spend.

It does not undo migrations ╬ô├ç├╢ nothing can ╬ô├ç├╢ so the older version runs against a
newer schema. It notices, and logs this on every start:

```
LiteLLM:ERROR: check_migration.py - Γëí╞Æ├£┬┐Γëí╞Æ├£┬┐Γëí╞Æ├£┬┐ prisma schema out of sync with db.
Consider running these sql_commands to sync the two - ['ALTER TABLE ... DROP COLUMN ...', ...]
```

**Do not run those commands.** They drop columns the newer version added, taking
any data written to them with it. The newer migrations stay recorded as applied,
so upgrading again would not recreate the columns, and the newer version would
then fail on them. The error is harmless if left alone; the chart starts the
proxies with schema updates disabled, so nothing is applied automatically.

Rollback with `migrations.enabled: false`, where the proxies migrate themselves,
has not been tested.

### Editing settings in the console takes them out of chart control

Changing any field of a settings block ╬ô├ç├╢ through the admin console or through a
management API that touches one ╬ô├ç├╢ writes that whole block to the database, after
which the database wins on every key in it and the corresponding chart values
stop taking effect. There is no warning.

Observed twice. Changing a single routing dropdown in the console also froze
`num_retries` and `cooldown_time`, neither of which had been touched. Separately,
one API call to toggle the unpriced-model block persisted `router_settings`,
`litellm_settings` and `general_settings` together, including everything the
chart had configured.

Recovery is to delete the row from the `LiteLLM_Config` table and restart, which
returns those keys to chart control.

This is why the chart writes as little as possible into those blocks.

### Values the chart owns

`model_list`; `general_settings.store_model_in_db`, the spend log retention
and its deletion interval, and the health check settings when
`router.healthChecks` is on; in `litellm_settings`,
`num_retries`, the unpriced-model block, `global_disable_no_log_param`, the
metrics callback and its settings, and, when Redis is configured, the Redis
cache; and in `router_settings`, Redis when it is configured, the routing
strategy, and the failover, the affinity checks and the fallbacks of `router`.
Everything else in
LiteLLM's configuration is left alone. The request log and the spend log
setting are not in LiteLLM's configuration at all: the image's entrypoint
applies them (see [One log line per call](#one-log-line-per-call)), so saving
a settings block cannot remove them.

### Models added through the console survive upgrades

They live in the database rather than in the configuration file, and a chart
upgrade does not remove them. Note that a console-added model whose name matches
one in `models` becomes an additional backend rather than an override.

### Upgrading with `--reuse-values`

`helm upgrade --reuse-values` keeps the values of the chart version a release
was installed with, without the defaults of settings added since. The chart
reads each such setting with its default, key by key, so a release whose
`router` or `spendLogs` comes from an earlier version gets the defaults of the
keys it lacks. A release installed before these settings existed upgrades
with:

- `requestLog`, `spendLogs` and `metrics`: the request log on, LiteLLM's spend
  log kept, metrics off;
- `spendLogs.retention`: `7d`, so LiteLLM starts deleting spend log rows older
  than seven days;
- `router`: `simple-shuffle`, failover and the affinity checks on, no
  fallbacks, and health checks every 300 seconds, at most 8 probes at a time,
  which start probing every deployment;
- `budgetExceededStatusCode`: 402;
- `podDisruptionBudget`: a PodDisruptionBudget above one replica;
- `priceTable`: `remote`, so LiteLLM keeps downloading the price table.

## Availability

### Rollouts let requests in flight finish

A replica that is being replaced or evicted stops accepting requests and
finishes the ones it has. LiteLLM puts no limit on that, so the pod's
`terminationGracePeriodSeconds` decides how long a response can run and still
survive a rollout or a node drain. At the Kubernetes default of 30 seconds, a
rolling restart cut a 90-second streamed response off after 80 chunks; with a
longer period the same stream completed.

The chart sets 600 seconds, the same ten minutes after which the OpenAI SDKs
give up on a request by default. It costs nothing when there is nothing to wait
for: a replica with no request in flight exited four seconds after being told to
stop. Raise it if single model calls can run longer.

Deleting one of two replicas while requests flowed through the Service, both
normally and by force, failed none of 100 requests on a single-node test
cluster.

Above one replica the chart creates a PodDisruptionBudget for the Gateway pods
with `maxUnavailable: 1` (`podDisruptionBudget.enabled`, on by default), so a
node drain evicts one replica at a time and waits while another is
unavailable. It is not created for a single replica, and `maxUnavailable`
rather than `minAvailable` means it does not block a drain even when the
Deployment has been scaled down to one pod. The bundled PostgreSQL and Valkey
have none. The chart sets no anti-affinity, so both replicas can still land on
one node and be lost together when that node fails. On a multi-node cluster,
use `affinity` to spread them.

### Pods keep their configuration until they are replaced

The generated configuration is mounted as a single file (`subPath`), so
Kubernetes does not update it inside running pods; a configuration change
replaces the pods instead (`checksum/config`). With the ConfigMap mounted as a
directory, Kubernetes updated the file in the old pods about a minute after an
upgrade. LiteLLM re-reads its configuration file every 30 seconds while models
are stored in the database and removes each configured model whose deployment
ID is no longer in the file, without adding the new entry until it restarts.
An upgrade that edited a model therefore made the old pods stop serving it
until they were replaced. Now each pod serves the configuration it started
with, and the edited model stays available throughout the rollout.

During a rollout the Service sends requests to old and new pods, which can
list different models when a model is added, removed or renamed. The CetiRun
operator lists the models through the Service, so it waits while the ready
pods behind the Gateway Service belong to more than one pod template, checks
again every 15 seconds, and applies the new model list once when one template
serves the Gateway. It keeps its previous view for at most ten minutes of a
rollout, and does not wait when it cannot read the Gateway's Service or pods.
Agents are not updated or restarted while the rollout runs.

### When Redis is unavailable

Requests keep being served. Readiness does not depend on Redis, so the replicas
stay in the Service. Measured by stopping Redis for up to three and a half
minutes:

- LiteLLM stops calling Redis within seconds of the first failures and tries it
  again every 60 seconds. Each replica was back on Redis within about a minute
  of Redis returning, without a restart.
- Budgets fall back to the spend recorded in PostgreSQL, which trailed live
  spend by 10 to 15 seconds. Requests inside that window are not yet counted: a
  key with room for five requests was allowed 16 in a ten-second burst, and 7
  when requests arrived five seconds apart.
- No spend was lost. Every key's recorded spend matched the requests it was
  allowed.

Redis restarts empty, which is fine for the same reason. Rate limits were not
tested during an outage.

### When PostgreSQL is unavailable

The Gateway stops serving. Measured by stopping PostgreSQL for about three
minutes:

- Requests with virtual keys started failing with 503 within a minute, since
  keys are looked up in the database. Creating a key returned 500.
- About a minute in, readiness had failed on both replicas and the Service had
  no endpoints left, so every request, master key included, was refused.
- Nothing restarted. Service resumed about 20 seconds after PostgreSQL returned,
  with no action needed.
- **Requests answered while the database was going down were never charged.**
  LiteLLM retries a spend write three times and then drops it. On two test keys,
  the spend of 7 and 9 requests never reached the recorded total.

A highly available PostgreSQL keeps this to the length of a failover. The spend
lost is bounded by what the Gateway serves meanwhile, which the 503s keep small.

## Request log and metrics

LiteLLM keeps a record of each call in its database, which the console's request
view reads, and daily totals per key, user, Team and model. Neither leaves the
Gateway. For collection elsewhere, the chart adds a log line per call and can
serve LiteLLM's Prometheus metrics. Measured on LiteLLM 1.101.0, on AKS with
Container Insights and Azure Monitor managed Prometheus.

### One log line per call

With `requestLog.enabled`, the default, each model call writes one JSON line to
the Gateway's stdout, through a callback in the image. It records the call's
metadata: the virtual key's alias and hash, the user, the Team, and the Agent,
from the metadata the CetiRun operator puts on the key; the model, the provider
model and the provider's host; tokens, with cached and reasoning tokens where
the provider reports them; spend; duration and, when streamed, time to first
token; the client's address and user agent; and, for a failure, the error.
A call LiteLLM makes itself carries its origin in `call_origin`: a background
health check probe is `"health_check"`, so usage figures can leave it out.
Abridged:

```json
{"log_type":"cetirun.ai_gateway.request","schema_version":1,"time":"2026-10-08T13:13:27.780Z","status":"success","route":"/v1/chat/completions","stream":false,"model":"gpt-4o","provider_model":"azure/prod-gpt-4o","prompt_tokens":1000,"completion_tokens":1000,"cached_tokens":800,"spend":0.014,"key_alias":"cetirun-alice-agent-1234","user_id":"alice","team_alias":"research","key_metadata":{"cetirun_agent":"alice-agent","cetirun_team":"research"},"duration_ms":686}
```

Every call was recorded: across two replicas, 40 calls made 40 lines, covering
the Chat Completions and Responses APIs, streamed and not, and failures. The
spend in the lines added up to the key's spend.

With Container Insights, `ContainerLogV2` parses each line into the dynamic
`LogMessage` column:

```kusto
ContainerLogV2
| where ContainerName == "gateway"
| where tostring(LogMessage.log_type) == "cetirun.ai_gateway.request"
| summarize calls = count(), spend = sum(todouble(LogMessage.spend))
    by agent = tostring(LogMessage.key_metadata.cetirun_agent), model = tostring(LogMessage.model)
```

Lines reached the Log Analytics workspace about 80 seconds after the call, and
each was billed at 1.4 to 1.9 KB, the larger ones for the Responses API, whose
response IDs are long.

The cluster has to be collecting container logs. On AKS that takes the
monitoring add-on: a Container Insights data collection rule and its
association alone, without the add-on, collected nothing, and no `ama-logs`
pods ran.

**No content.** The line carries no request or response content, and has no
setting that adds it. Every string in it is cut short: 256 characters, or
1,024 for the request ID and the error text. Clients choose many of them: the
session ID, which LiteLLM records as `trace_id`, the call ID header, the model
name, the end user, the user agent and the tags. A line never exceeds 15 KiB:
container runtimes split longer lines, and Container Insights stores the
pieces as separate records, none of them valid JSON. Should escaping make a
line longer, which control characters can, the error text, the client's tags
and extra key metadata go first, then the longest strings are cut, and the line
carries `"truncated": true`. The image's guard callback also drops a client's
`litellm_request_debug` field: with it, LiteLLM printed that call's prompt and
the provider's raw response to the log.

**Callers cannot leave it out.** A client can ask LiteLLM to skip its logging
callbacks for a call by sending `"no-log": true`; measured without the chart's
setting, such a call wrote no line. The chart always sets
`litellm_settings.global_disable_no_log_param`, which makes LiteLLM ignore the
field.

**Registered by the image, not in LiteLLM's configuration.** The chart sets
`CETIRUN_REQUEST_LOG=true` on the Gateway container, and the image's entrypoint
registers the callback before LiteLLM loads its configuration. A callback
listed in `litellm_settings.callbacks` would depend on how LiteLLM combines its
configuration file with settings saved in its database: reading its
configuration with the database connected, it replaces a configured list with
a saved one. LiteLLM 1.101.0 reads the file before it connects and adds saved
callbacks to the configured ones later, so a saved list left a configured
callback in place when tested, but that rests on LiteLLM's startup order. A
configuration that names the callback also keeps an image without it from
starting: an image built before the request log exited at startup with an
`ImportError`. The entrypoint's registration depends on neither, and an image
built before the request log ignores the variable and starts with the chart's
configuration.

The proxy runs as one uvicorn process per pod, as LiteLLM starts by default.
The entrypoint refuses more workers, or another server, since they would load
LiteLLM again without the image's callbacks, the guard included, whatever
`requestLog` says; scale with `replicaCount` instead.

### LiteLLM's spend log

LiteLLM writes a row per call to `LiteLLM_SpendLogs`, which the console's
request log pages read. The row holds the call's metadata, not its prompt or
response: the chart does not set `store_prompts_in_spend_logs`. The table
grows by one row for every call, so `spendLogs.retention`, `7d` by default,
renders LiteLLM's `maximum_spend_logs_retention_period`, and
`maximum_health_check_retention_period` for the stored health check results.
Spend, budgets and the daily usage totals are kept beyond it. Set it empty to
keep every row. A retention period saved in the console's settings wins over
the chart's, as for any `general_settings` key.

Each replica deletes the older rows on its own schedule: the first time one
interval (`maximum_spend_logs_retention_interval`) after the pod starts, then
every interval. LiteLLM lengthens the interval by up to a minute, drawn when
the pod starts, and shifts each replica's runs by up to five minutes more, and
a replica skips a run while another replica's run holds a lock in Redis. At
LiteLLM's default interval of a day, a Gateway whose pods are replaced within
a day, as every configuration change replaces them, never deletes a row. So
whenever a retention is set, the chart sets the interval to an hour: rows
outlive the retention by about an hour at most, a replaced pod only postpones
the deletion, and a run with nothing to delete costs each replica two indexed
queries per table. A run deletes 1,000 rows at a time, at most 500 times per
table and for five minutes, and the next run continues from there.

Measured on LiteLLM 1.105.1 with the request log on: every call, probes
included, added a row, none with a prompt. With two replicas, and the rendered
configuration changed to a one-minute retention and a 30-second interval,
each replica ran the job on a period of its own, 49 and 83 seconds, the first
time 75 and 128 seconds after it started; every run took the lock, and the
runs deleted 88 spend log rows and 11 health check results, all past the
retention. With the chart's hourly interval, two proxies scheduled their
first run 63 and 66 minutes after starting.

`spendLogs.enabled: false` has LiteLLM write no row per call. It is turned off
only together with the request log, so no configuration leaves the Gateway
without a record of each call: `spendLogs.enabled: false` needs
`requestLog.enabled`, and the image's entrypoint turns the spend log off only
once it has registered the request log callback. It sets LiteLLM's
`disable_spend_logs` in the proxy process, not in `general_settings`, so the
setting is not saved with a settings block and does not outlive a rollback; a
`disable_spend_logs` written in `general_settings` still wins. An image built
before the request log keeps writing the table.

Measured with it off: 16 calls added no row, while the key's, Team's and user's
spend each grew by exactly the calls' cost, budgets held, and the daily totals
counted all 16. The console's usage pages read those totals and keep working.
Its request log pages, which read `LiteLLM_SpendLogs`, stay empty. LiteLLM
reads the setting when the proxy starts.

### Credentials and Container Insights

Container Insights copies the environment of every container, values taken
from Secrets included, into its `ContainerInventory` table once a minute,
unless environment collection is turned off for the cluster or for the
container. The Gateway holds its master key, salt key, database connection
string and provider keys in its environment. Every container in this chart
that holds a credential sets `AZMON_COLLECT_ENV=False`, which turns that
collection off for itself; the CetiRun cluster template also turns it off for
the whole cluster.

### Metrics

`metrics.enabled` registers LiteLLM's Prometheus callback and serves the
metrics at `/metrics` on `metrics.port`. Left unset, that is 9090, or 9091 when
`service.port`, where the proxy listens, is 9090, so turning metrics on never
needs a port chosen by hand. LiteLLM runs a
separate process for that port, which reads the samples the proxy leaves in a
directory they share, so a scrape never waits behind model traffic. Each
replica serves its own samples; sum them across pods.

That port has no authentication. LiteLLM also serves `/metrics` on the Service
port, where only the master key is accepted: an Agent's key and a key without
route limits were both refused with 401. The metrics port is not on the
Service. Anything that can reach a Gateway pod can read every key alias, user,
Team and spend figure the metrics carry, so restrict the port with a
NetworkPolicy where other workloads should not read it. A CetiRun Agent's
egress reaches only the Service port.

The metrics carry the key alias and hash, user, Team, model and route as labels:
for example `litellm_spend_metric_total`, `litellm_proxy_total_requests_metric_total`,
`litellm_input_cached_tokens_metric_total` and `litellm_remaining_api_key_budget_metric`.
`metrics.excludeLabels` leaves out the client's address and user agent, which
start a new series with every Agent restart and client version, and the user's
email address.

For Azure Monitor managed Prometheus, set `metrics.podMonitor.enabled`. The
chart creates a PodMonitor of `azmonitoring.coreos.com/v1`, the API the managed
collector reads, when the cluster serves that API, and none otherwise, so an
install does not fail on a cluster without managed Prometheus. Set
`metrics.podMonitor.apiVersion` to `monitoring.coreos.com/v1` for the
Prometheus Operator. The cetirun-platform chart turns on both the metrics and
the PodMonitor. Measured: both replicas were scraped, and in the Azure Monitor
workspace

```promql
sum by (api_key_alias, team_alias, user, requested_model) (litellm_spend_metric_total)
```

returned each key's spend. Querying the workspace needs the Monitoring Data
Reader role on it.

Counters lose the increments a label set makes before its first scrape and
start again from zero when a pod restarts, so use the metrics for rates,
trends and alerts, and the request log or the daily totals for exact spend.

### Reading the totals

LiteLLM's token counts include cached tokens. `prompt_tokens` counts every
input token, and the daily totals' `cache_read_input_tokens` is the part of it
read from the provider's prompt cache, which is charged at the lower cache
rate. So the token counts priced at the full input rate come to more than the
recorded spend, by the cache discount. Measured with 4,000 of 20,000 input
tokens cached at a quarter of the input rate: the full-rate figure was 0.40,
the recorded spend 0.37, and pricing the cached part at its own rate gave 0.37.

`LiteLLM_SpendLogs`, the console's per-request record, is keyed on the
provider's response ID, and LiteLLM silently skips a row whose ID it already
holds. Real providers return a new ID with every response. A test backend that
repeats one ID keeps one row for all its answers: five calls left one row while
the key was charged for five.

## Migrations

LiteLLM applies migrations on startup by default. The chart instead runs them in
an init container of each Gateway pod, with the image's `migrate` command, and
starts the proxies with `DISABLE_SCHEMA_UPDATE` set.

Pods take turns. Two migrations running at once were measured to deadlock:
prisma waits for its own lock inside an open transaction, and one of LiteLLM's
migrations builds an index concurrently, which waits for every open
transaction. PostgreSQL resolves that by killing one of them, which can leave a
failed migration that blocks every later deploy. So `migrate` first takes a
PostgreSQL advisory lock of its own, polling without holding a transaction open.
Measured with two pods started together: the second waited, then found nothing
left to apply.

An init container, unlike the pre-install hook this chart used before, can wait
for a database that is still starting, which the bundled PostgreSQL is on first
install. `migrate` waits up to ten minutes for it.

Set `migrations.enabled: false` to return to startup migrations, which is only
appropriate at a single replica.

## Image

The Gateway runs `cetirun-ai-gateway`, built from `app/cetirun-ai-gateway` and
published with the other CetiRun images. It is open-source LiteLLM, 1.105.1,
installed as pinned packages on the Azure Linux Python base from MCR, running as
the non-root user 65532.

- `requirements.txt` is the exact package set of the upstream LiteLLM image for
  the same release, installed with `--no-deps`, so nothing else is pulled in.
  The differences are deliberate: `litellm-enterprise`, which carries the
  BerriAI Enterprise License, is left out; two backports that Python 3.12
  needs are added; and a package with a known security advisory is moved to a
  patched version, with the advisory marked in the file. The 1.105.1 set needs
  no such move. The build fails if the enterprise package is present, or if
  `pip check` finds a requirement the set does not meet.
- The prisma CLI and engines are installed at build time, so migrations need
  neither npm nor network access. Measured on a network with no route out:
  migrations applied and the proxy became ready.
- `cetirun-ai-gateway migrate` applies migrations; any other arguments start
  the proxy. Starting never touches a database, because the same image serves
  the per-agent Copilot proxy, which has none. The Gateway proxy starts in the
  entrypoint's own process with the guard callback registered, and, with
  `CETIRUN_REQUEST_LOG=true`, which the chart sets, the request log callback.

To move to a new LiteLLM release, regenerate `requirements.txt` from that
release's upstream image, rebuild, and let the build's `pip check` name any
backport the new set needs. Pull-request dependency review then flags any
package with a high or critical advisory.

With `global.registry` set, the chart uses `<registry>/cetirun-ai-gateway`, the
same convention as the other CetiRun charts; `image.digest` pins an exact build.

## Testing

```bash
bash scripts/cetirun-ai-gateway-helm-test.sh       # offline render assertions
bash scripts/cetirun-ai-gateway-cluster-test.sh    # install and upgrade on a real cluster
```

The render assertions need no cluster and run in `make chart-check`.

The cluster test needs kubectl pointed at a disposable cluster that already has
the image, plus helm, curl and jq. It creates its own namespace (`NAMESPACE`,
default `gateway-cluster-test`) and deletes it afterwards; it refuses a
namespace that already exists and leaves it as it was. It deploys a mock
provider, installs the
chart with its defaults, including the bundled PostgreSQL and Valkey, exercises
it, upgrades it, uninstalls and reinstalls it, and checks what survived. It runs
in CI on kind for any change to the image or the chart, with the image built
from the same commit.

It exists because the render assertions stayed green through real bugs: two
hook-ordering failures, a migration that succeeded without creating any of
LiteLLM's tables, a Redis configuration that answered identical requests from
cache across callers, and an uninstall that deleted the salt key while the
database kept everything encrypted with it. It also checks behaviours only a
running Gateway can show: that an upstream failure is not retried, that a
replica shutting down finishes the request it is serving, that replicas
migrate in turn, that the bundled services refuse clients without their
passwords, that every answered call reaches both the spend log and the request
log, that with the spend log turned off calls are still charged and logged but
add no spend log row, that a call sent with `"no-log": true` is still logged,
that the metrics port serves a pod's requests while the Service port still
requires a key, and that a `callbacks` list saved in LiteLLM's database leaves
the request log, and the spend log setting, in place. With
`PREVIOUS_IMAGE_TAG` set to an image built before the request log, it also
checks that the chart still starts that image and that LiteLLM's spend log
stays on with it.

Its checks were confirmed by putting each original problem back and watching
the test fail at the check meant to catch it. Doing that also exposed
weaknesses in the test itself, since fixed: counting every table passed on an
empty database, because migration creates its own bookkeeping table even when
it applies nothing; and its mock provider answered every call with the same
response ID, so LiteLLM's spend log kept one success however many there were,
which this README once recorded as a gap in LiteLLM.

## Known gaps

Recorded and deferred: none of these stops the Gateway from serving agents.

**Not in the chart yet.** Redis TLS, which Azure Cache for Redis requires; and
anti-affinity for the Gateway replicas (set `affinity` by hand).

**Prices are not pinned to the image by default.** LiteLLM downloads its price
table from GitHub each time a Gateway pod starts, and falls back to the copy it
ships with when that fails, so a restart can change what `base_model` pricing
resolves to. `priceTable: bundled` uses only the copy in the image, for a
cluster without internet access or prices that change only with the image: the
chart then sets LiteLLM's `LITELLM_LOCAL_MODEL_COST_MAP` on the proxy and on the
migrate step, which loads LiteLLM too, and a reload of the price table from the
console, at once or on a schedule, loads the image's copy again. In exchange,
new models and price changes arrive only with a new image, and a model missing
from the image's table is unpriced: refused while `blockUnpricedModels` is on,
unless its entry declares a price. With CetiRun, the operator prices the Agents'
GitHub Copilot models from the table the Gateway loaded, so a Copilot model
newer than the image is not offered. Measured on LiteLLM 1.105.1 with the
table's address pointed at a mock: with the variable, neither the proxy's
start, the migrate step's import of LiteLLM nor a reload requested the table;
without it, each did.

**GitOps.** Generated credentials rely on Helm's `lookup`, which returns nothing
under `helm template`, so tools that render that way, such as Argo CD, must
supply the master key, salt key and console password through existing Secrets,
supply the bundled Valkey's password through `redis.passwordSecret` or use
their own Redis, and use an external PostgreSQL: the bundled one's password is
generated the same way, with no option to supply it, and a changed password
locks the Gateway out of data initialised with the old one.
