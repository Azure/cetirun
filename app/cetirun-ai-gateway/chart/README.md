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
`cetirun-model-<hash>` access group and matching `cetirun-shared-<hash>`
deployment ID. The suffix is the first 16 hexadecimal characters of SHA-256
over the complete model object's canonical JSON, before those generated fields
are added. Existing non-reserved access groups and provider parameters remain
unchanged. The chart rejects a supplied `model_info.id` or access group using
the reserved `cetirun-model-` prefix.

The Operator requires an exact, unique ID/group pair before granting a shared
model through AgentPolicy. A same-name console deployment is not automatically
trusted or granted. Changing the configured endpoint, provider parameters,
pricing or other model metadata changes the binding; the Operator must
reconcile the new grant before the Agent can use it. Rotating the value of a
referenced Secret does not change this configuration hash.

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
100Gi: the database keeps the spend log of every agent using the Gateway, so it
grows with the number of agents and how long they run. A StatefulSet's
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
beginning with `copilot/` are rejected too: `copilot/<model>` names each CetiRun
Agent's GitHub Copilot models. A standalone Gateway may keep serving models under
`copilot/`. Both prefixes are matched in any letter case. The console checks
neither, so do not add models under them there; the operator leaves such shared
models out of the model list it configures for Agents. It does the same for names
under `openai/`, which the agent chart refuses as a provider prefix, and names
with whitespace or more than 256 characters. Shared models under other provider
prefixes are listed like any other.

The reservation exists because adding a model under a name that already exists
does not replace the original. Both are served under the same name and traffic
is split between two different backends, with nothing visible to the client to
indicate it.

## Budgets and unpriced models

LiteLLM computes spend from a built-in price table, multiplying it by the tokens
a request used. A model missing from that table costs zero, and **budget checks
are skipped entirely for zero-cost models** — so a budget set on a virtual key
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
refused with HTTP 429 and `Budget has been exceeded!`. Clients treat 429 as rate
limiting, so an agent usually retries a few times before it reports the error.

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

LiteLLM knows this, and sends any chat request that carries tools to
`/responses` instead. An agent's requests almost always carry tools, so in
practice every agent turn goes through the Responses API, and `api_version`
decides whether that endpoint exists. Measured directly against one deployment:

| `api_version` | Chat | Chat with tools | Responses | Responses with tools |
| --- | --- | --- | --- | --- |
| `2024-10-21` | 200 | 400 | **404** | — |
| `2025-04-01-preview` | 200 | 400 | 200 | 200 |

With `2024-10-21`, the failure is easy to miss. A plain chat request succeeds,
so a smoke test passes. An agent then gets a reply to requests without tools —
OpenCode's session title generation, for instance — while every real turn fails
with:

```
litellm.NotFoundError: AzureException NotFoundError - {"error":{"code":"404","message": "Resource not found"}}
```

and the Gateway log shows the request it made:
`404 Not Found for url '.../openai/responses?api-version=2024-10-21'`.

Use `2025-04-01-preview` or later. The "Chat with tools" column stays at 400
whatever the version; that refusal is the reason for the bridge, not a problem
with it.

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
upstream calls. The image's request log callback drops those fields, and the
cluster test checks it. With `requestLog.enabled` off the callback is not
registered, and they are not dropped.

Sending a failed request to another deployment of the same model is not a
retry; see [Routing](#routing).

## Routing

Several entries in `models` with the same `model_name` are deployments of one
model: two Azure OpenAI resources, for example, or Azure OpenAI and OpenAI.
LiteLLM picks one at random for each request, or by the `weight`, `rpm` or
`tpm` in each entry's `litellm_params`; set the same field on every entry, since
LiteLLM looks at the first entry to choose which one to use. An entry with a
higher `litellm_params.order` is kept in reserve until those with a lower one
are unavailable. Which deployments serve a model, and which model falls back to
which, are the administrator's; the `router` values set the rest.

Measured with two Gateway replicas and the bundled Valkey, against mock
deployments standing in for separate Azure OpenAI resources, with an
OpenClaw-like client and with OpenClaw itself, and checked against Azure
OpenAI where noted.

### A failed request goes to another deployment

`router.failover`, on by default, sends a request that failed on one
deployment once more, to another deployment of the same model (LiteLLM's
`enable_weighted_failover`). The failed deployment is not tried again, and a
model with a single deployment gets no second attempt, so `num_retries` stays 0.

| One of two deployments failing | Requests failed, of 30 |
| --- | --- |
| With 500, `router.failover: false` | 13 |
| With 429, `router.failover: false` | 2 |
| With 500 or 429, `router.failover: true` | 0 |

Without it, LiteLLM stops sending requests to a failing deployment only after
several failures, and only for a few seconds, separately on each replica. The
cost: a request the provider refuses as invalid, with a 400, is sent to the
other deployment too before the error reaches the client.

The GitHub Copilot routes the CetiRun operator registers for each Agent share
one name per model, `copilot/<model>`, and are told apart by access group. With
failover on, no Agent's request reached another Agent's route.

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

`router.encryptedContentAffinity` keeps each conversation on the deployment
that encrypted its reasoning, or on one with the same endpoint and key
(LiteLLM's `encrypted_content_affinity`). It marks the items it returns, so it
needs no state in Redis, and turning it off later is safe: the marks are removed
from every request either way. It is off by default because the LiteLLM in the
image fails a pinned conversation rather than moving it: while its deployment
is down, every turn fails, and for the few seconds it stays in cooldown
afterwards turns get a 503 or 429; then it continues there. A deployment that
authenticates without an API key is never treated as sharing an endpoint and key
with another. Turn it on when one model name is served by more than one
resource or organization.

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

`router.healthChecks.enabled`, off by default, probes the deployments of the
models in `models` every `router.healthChecks.intervalSeconds`, 300 by default,
and sends requests only to those that answered their last probe. Measured with
one of two deployments failing: every request went to the other one, where 10
of 30 failed without health checks. When every deployment of a model fails its
probe, requests are still sent.

Each probe is a real call, with `max_tokens` 16, from each replica, and writes
a line to the request log under the key alias `litellm-internal-health-check`.
Only the models configured in `models` are probed: models added through the
console and the GitHub Copilot models the CetiRun operator registers for each
Agent are not, since probing those would spend the users' own quota. A router
from LiteLLM's auto routing is not probed itself; its models are. Set
`model_info.mode` on a model that is not a chat model, such as an embedding or
image model, so it is probed the right way. A probe answered with 429 or 408
counts as healthy: cooldowns handle those on live requests.

With health checks off, LiteLLM's `/health` endpoint probes every deployment
live when called with the master key, including the Agents' GitHub Copilot
routes; with them on, it returns the last results. Agents' keys are refused
there.

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

**Using the router uses its models.** A key that may use the router reaches every
model of its tiers, whether or not it may use them directly; a key that may use
the models but not the router cannot use the router. With
`operatorIntegration`, an AgentPolicy that grants the router grants its models.
The chart rejects a tier, default model or classifier model that is not
configured in `models`.

**A conversation can change model.** Each request is classified on its own, so
one conversation can move between tiers. On the same Azure OpenAI resource that
kept working, encrypted reasoning included; across resources, an agent such as
OpenClaw then retries the turn without its encrypted reasoning. Keep a router's
models on one resource. LiteLLM can keep a conversation on its first tier
(`session_affinity`, or `classification_mode: user_turn`) only for clients that
send a session ID it recognizes; OpenClaw 2026.9.6 sends none it does.

**One custom router.** Without a LiteLLM licence, at most one router may define
its own tiers or classifier prompt: with two, the proxy refused to start. The
chart rejects the second.

The request log line of a routed call carries the router's name as `model`, the
model it picked as `provider_model`, and `routing_tier`, `routing_cause`,
`routing_score` and, with a model classifier, `classifier_cost`. Whether a
configuration saves money without lowering the quality of the answers is not
measured here; that needs the requests themselves.

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

### Serving CetiRun Agents

The CetiRun operator gives each Agent a key restricted to access groups, and
registers the Agent's GitHub Copilot models under `copilot/<model>` in a group of
the Agent's own, each pointing at that Agent's proxy. Set `operatorIntegration:
true` for such a Gateway; the `cetirun-platform` chart sets it for the Gateway it
installs. It reserves the `copilot/` prefix, see [Reserved names](#reserved-names),
and sets `general_settings.enforce_fallback_model_access`.

LiteLLM checks a key against the requested model only. A fallback the Gateway
is configured with, in `router_settings` or the console, is chosen after that
check, and without the setting LiteLLM tries it unchecked; for a model name
several Agents share, it then picks among all of them when the caller's groups
match none. So a shared model's fallback to a Copilot model could reach another
Agent's proxy. With the setting, LiteLLM skips a fallback target the caller's key
could not request directly.

The groups only separate Agents while keys and teams are limited to them. A key
or team allowed every model, or a `copilot/` name directly, has no group to
filter by, so LiteLLM spreads its requests for a `copilot/` name over every
Agent's proxy. The operator creates teams and keys without such grants; do not
add them in the console.

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
— and only if the database is being discarded too. Deleting it while keeping the
database leaves every console-added model permanently unreadable.

### Upgrading LiteLLM

Rebuild the image for the new release (see [Image](#image)) and upgrade. Each new
Gateway pod applies the new version's migrations in its init container before
the proxy starts.

Measured from `v1.101.0` to `v1.102.0` with data in place, on the upstream
image before the chart moved to its own: six migrations applied, virtual keys
and their spend preserved, models added through the console still decrypted and
served, and a budget partly spent before the upgrade was enforced exactly after
it.

### Rolling back

`helm rollback` to a revision with an older image works: the older version
started, passed readiness, served both configured and console-added models, and
kept key spend.

It does not undo migrations — nothing can — so the older version runs against a
newer schema. It notices, and logs this on every start:

```
LiteLLM:ERROR: check_migration.py - 🚨🚨🚨 prisma schema out of sync with db.
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

Changing any field of a settings block — through the admin console or through a
management API that touches one — writes that whole block to the database, after
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

`model_list`; `general_settings.store_model_in_db`, and the health check
settings when `router.healthChecks` is on; in `litellm_settings`,
`num_retries`, the unpriced-model block, `global_disable_no_log_param`, the
metrics callback and its settings, and, when Redis is configured, the Redis
cache; and in `router_settings`, Redis when it is configured, and the failover,
the two affinity checks and the fallbacks of `router`. Everything else in
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
was installed with, without the defaults of settings added since. A release
installed before `requestLog`, `spendLogs` and `metrics` existed upgrades with
their defaults: the request log on, LiteLLM's spend log kept, metrics off. One
installed before `router` gets its defaults too: failover and continuation
affinity on, the rest off.

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

The chart creates no PodDisruptionBudget and sets no anti-affinity, so both
replicas can land on one node and be evicted together when it is drained. On a
multi-node cluster, use `affinity` to spread them.

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
carries `"truncated": true`. The callback also drops a client's
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
LiteLLM again without the callback; scale with `replicaCount` instead.

### LiteLLM's spend log

`spendLogs.enabled: false` has LiteLLM write no row per call to
`LiteLLM_SpendLogs`. The table grows by one row for every call and is never
trimmed unless a retention period is set in the console. The cetirun-platform
chart turns it off, since the request log keeps the per-call record outside the
database.

It is turned off only together with the request log, so no configuration leaves
the Gateway without a record of each call: `spendLogs.enabled: false` needs
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
published with the other CetiRun images. It is open-source LiteLLM, installed as
pinned packages on the Azure Linux Python base from MCR, running as the
non-root user 65532.

- `requirements.txt` is the exact package set of the upstream LiteLLM image for
  the same release, installed with `--no-deps`, so nothing else is pulled in.
  The differences are deliberate: `litellm-enterprise`, which carries the
  BerriAI Enterprise License, is left out; two backports that Python 3.12
  needs are added; and packages with known security advisories are moved to
  patched versions, with the advisories marked in the file. The build fails
  if the enterprise package is present.
- The prisma CLI and engines are installed at build time, so migrations need
  neither npm nor network access. Measured on a network with no route out:
  migrations applied and the proxy became ready.
- `cetirun-ai-gateway migrate` applies migrations; any other arguments start
  the proxy. Starting never touches a database, because the same image serves
  the per-agent Copilot proxy, which has none. With `CETIRUN_REQUEST_LOG=true`,
  which the chart sets, the proxy starts in the entrypoint's own process with
  the request log callback registered.

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
a PodDisruptionBudget and anti-affinity. LiteLLM supports both.

**Encrypted reasoning affinity fails closed.** With the LiteLLM in the image,
`router.encryptedContentAffinity` fails a conversation whose deployment is
unavailable rather than moving it. Later LiteLLM releases drop the encrypted
reasoning and move the conversation instead; with them it could be on by
default.

**Prices are not pinned to the image.** LiteLLM fetches its price table from
GitHub at startup and falls back to the copy it ships with when that fails, so
a restart can change what `base_model` pricing resolves to. Set
`LITELLM_LOCAL_MODEL_COST_MAP=True` to use the shipped copy only.

**GitOps.** Generated credentials rely on Helm's `lookup`, which returns nothing
under `helm template`, so tools that render that way, such as Argo CD, must
supply the master key, salt key and console password through existing Secrets,
supply the bundled Valkey's password through `redis.passwordSecret` or use
their own Redis, and use an external PostgreSQL: the bundled one's password is
generated the same way, with no option to supply it, and a changed password
locks the Gateway out of data initialised with the old one.
