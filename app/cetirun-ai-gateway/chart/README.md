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

The model list can be empty. Administrators can add models through the console
later, and with CetiRun a deployment may rely only on the GitHub Copilot models the
operator registers for each Agent.

The last two lines of the model entry are not optional in practice. Without a
price the model is refused, and for newer Azure models an older `api_version`
serves plain chat but fails every tool call an agent makes. See [Budgets and
unpriced models](#budgets-and-unpriced-models) and [Azure
OpenAI](#azure-openai).

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
`copilot/`. Shared models added through the console with these prefixes, or with a
provider prefix such as `openai/`, are not given to Agents.

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

For shared models, the chart derives a matching `cetirun-shared-...`
deployment ID and `cetirun-model-...` access group from every configured model.
The operator grants a group only when the live Gateway reports that exact,
unique, statically configured binding. A database model, an unknown source, a
mismatched ID, or any second deployment reusing the group invalidates the
binding. An already-issued key may reach a copied group until the next catalog
refresh, normally within two minutes; the operator then holds affected governed
Agents and blocks their existing keys. Configure governed shared models through
Helm values, not the console.

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

The console password and master key are control-plane credentials, not tenant
credentials. Their holders can change routes and budgets or cause a governance
hold even though forged reserved groups are not accepted as authorization.
Give them only to administrators trusted to change the platform release.

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

`model_list`; `general_settings.store_model_in_db`; in `litellm_settings`,
`num_retries`, the unpriced-model block and, when Redis is configured, the Redis
cache; and `router_settings` when Redis is configured. Everything else in
LiteLLM's configuration is left alone.

### Models added through the console survive upgrades

They live in the database rather than in the configuration file, and a chart
upgrade does not remove them. Note that a console-added model whose name matches
one in `models` becomes an additional backend rather than an override.

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
  the per-agent Copilot proxy, which has none.

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
migrate in turn, and that the bundled services refuse clients without their
passwords.

Its checks were confirmed by putting each original problem back and watching
the test fail at the check meant to catch it. Doing that also exposed a
weakness in the test itself, since fixed: counting every table passed on an
empty database, because migration creates its own bookkeeping table even when
it applies nothing.

## Known gaps

Recorded and deferred: none of these stops the Gateway from serving agents.

**Per-request logs are incomplete with Redis configured.** In
`LiteLLM_SpendLogs`, failed requests were recorded but successful ones mostly
were not, even minutes later, with no error logged. Without Redis the same table
was complete. This affects the console's request log view.

**The daily aggregate does not reconcile for cached requests.** For an agent
session where most input came from Azure's prompt cache, the token counts in
`LiteLLM_DailyUserSpend`, priced at the rates LiteLLM holds for the model, come
to about a third more than the spend recorded beside them. Either the token
columns overcount or the spend undercounts. The spend figure is what budgets are
enforced against, and it matches the key's spend in the console.

What is established: the cost LiteLLM computes for an individual request is
correct. In a controlled test through the same Responses API bridge an agent
uses, non-streaming, with and without cache hits, each request's charge matched
the model's rates to the token. What is not: that a streamed agent session's
recorded spend equals the sum of those per-request costs. Settling it needs
per-request logs, which is the first gap.

**Not in the chart yet.** Redis TLS, which Azure Cache for Redis requires; a
PodDisruptionBudget and anti-affinity; and LiteLLM's routing, fallback and
health-check settings. LiteLLM supports all of them. Measured on the current
settings: with one of two endpoints behind a model name unreachable, 6 of 30
requests failed, and adding a fallback group recovered all 30.

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
