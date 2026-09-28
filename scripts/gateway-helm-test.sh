#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Render assertions for the CetiRun AI Gateway chart. Offline: helm template
# only, no cluster and no install.
#
# Several of these guard behaviour that was measured against LiteLLM rather than
# assumed, and where the failure mode is silent. Those cases carry a comment
# explaining what goes wrong without the guard.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-gateway-chart}"
release_name="${RELEASE_NAME:-gateway}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

# A usable baseline. Individual scenarios override parts of it.
base_args=(
  --set-string "models[0].model_name=lint-model"
  --set-string "models[0].litellm_params.model=openai/lint-model"
  --set-string "postgresql.urlSecret.existingSecret=lint-database"
  --set-string "redis.host=lint-redis.example.invalid"
)

render() {
  helm template "${release_name}" "${chart_dir}" "${base_args[@]}" "$@"
}

# Assert a needle is present. Uses a here-string rather than a pipe so that
# grep exiting early cannot trip pipefail.
assert_contains() {
  local needle="$1" description="$2" output="$3"
  if ! grep -q -- "${needle}" <<< "${output}"; then
    echo "FAIL: ${description}" >&2
    exit 1
  fi
}

assert_absent() {
  local needle="$1" description="$2" output="$3"
  if grep -q -- "${needle}" <<< "${output}"; then
    echo "FAIL: ${description}" >&2
    exit 1
  fi
}

assert_rejected() {
  local description="$1"
  shift
  if render "$@" > /dev/null 2>&1; then
    echo "FAIL: ${description} should have been rejected" >&2
    exit 1
  fi
}

# ----- Baseline render -----

echo "==> helm template ${release_name} ${chart_dir} (baseline)"
output="$(render)"
assert_contains "kind: Deployment" "Deployment is rendered" "${output}"
assert_contains "kind: Service" "Service is rendered" "${output}"
assert_contains "kind: ConfigMap" "ConfigMap is rendered" "${output}"
assert_contains "model_name: lint-model" "configured model reaches the LiteLLM config" "${output}"
assert_contains "\- \-\-config" "the config path is passed explicitly" "${output}"

# ----- Required inputs -----

echo "==> expected rejections for missing required input"

# A Gateway with no models starts and then fails every request, so this is
# rejected at install instead.
assert_rejected "empty model list" --set "models=null"

assert_rejected "missing litellm_params.model" --set-string "models[0].litellm_params.model="

assert_rejected "missing database secret" --set-string "postgresql.urlSecret.existingSecret="

assert_rejected "both provider credential sources" --set-string "providerCredentials.existingSecret=a" --set-string "providerCredentials.inline.B=c"

# ----- Reserved model-name prefix -----

# A model added later under a name that already exists does not replace the
# original: both are served under one name and traffic splits between two
# different backends, invisibly to the client. The prefix keeps operator
# generated names out of the administrator's namespace.
echo "==> reserved model-name prefix"
assert_rejected "reserved cetirun- prefix" --set-string "models[0].model_name=cetirun-gpt4"
assert_rejected "reserved prefix in another case" --set-string "models[0].model_name=CetiRun-gpt4"

# ----- Redis and replicas -----

# Without shared state each replica enforces its own copy of every budget and
# rate limit, so a limit is multiplied by the replica count. Measured: a $0.10
# budget permitted $0.20 across two replicas.
echo "==> Redis is required above one replica"
if helm template "${release_name}" "${chart_dir}" \
    --set-string "models[0].model_name=lint-model" \
    --set-string "models[0].litellm_params.model=openai/lint-model" \
    --set-string "postgresql.urlSecret.existingSecret=lint-database" \
    --set replicaCount=2 > /dev/null 2>&1; then
  echo "FAIL: two replicas without Redis should have been rejected" >&2
  exit 1
fi

echo "==> a single replica does not require Redis"
single="$(helm template "${release_name}" "${chart_dir}" \
  --set-string "models[0].model_name=lint-model" \
  --set-string "models[0].litellm_params.model=openai/lint-model" \
  --set-string "postgresql.urlSecret.existingSecret=lint-database" \
  --set replicaCount=1)"
assert_contains "replicas: 1" "single replica renders" "${single}"
assert_absent "redis_host" "no Redis configuration without a host" "${single}"

# Redis declared in only one block is inert: it stays unused while budgets keep
# doubling, and the deployment still looks correctly configured. Both blocks
# have to be present.
echo "==> Redis is wired into both configuration blocks"
assert_contains "redis_host:" "router_settings carries Redis" "${output}"
assert_contains "type: redis" "litellm_settings carries the Redis cache block" "${output}"

# The Redis-backed cache is what makes budgets coordinate across replicas, but
# at its default it also answers identical requests from Redis, at zero spend,
# outside any budget, and across callers. Measured: with it on, a $0.10 budget
# allowed every one of sixteen identical requests.
echo "==> response caching is off unless a request asks for it"
assert_contains "mode: default_off" "the Redis cache does not answer requests by default" "${output}"

# ----- Retries -----

# LiteLLM retries a failed upstream call twice by default. Agents retry too, so
# the two multiply: one failing request was measured at three upstream calls.
echo "==> the Gateway does not retry upstream failures"
assert_contains "num_retries: 0" "LiteLLM's default retries are turned off" "${output}"

# ----- Shutdown -----

# LiteLLM finishes requests in flight when stopped and sets no limit of its own,
# so the pod's grace period is the limit. At the Kubernetes default of 30
# seconds, a rolling update cut a streamed response off partway through.
echo "==> a stopping replica has time to finish its requests"
assert_contains "terminationGracePeriodSeconds: 600" \
  "the grace period is longer than the Kubernetes default" "${output}"

# ----- Unpriced models -----

# A model missing from LiteLLM's price table costs zero, and budget checks are
# skipped entirely for zero-cost models, so a budget on a key silently does
# nothing. Azure OpenAI deployment names are not in that table, so this is the
# ordinary case. Measured against a real deployment: a $0.0001 budget allowed
# every request until this setting was turned on.
echo "==> unpriced models are blocked by default"
assert_contains "block_requests_for_models_without_pricing: true" \
  "unpriced models are refused unless the operator opts out" "${output}"

unpriced_allowed="$(render --set blockUnpricedModels=false)"
assert_absent "block_requests_for_models_without_pricing" \
  "the block can be turned off deliberately" "${unpriced_allowed}"

# ----- Migrations -----

# Several replicas applying migrations at once race each other, so the work
# moves to a hook and the proxies start with schema updates disabled.
echo "==> migrations run from a hook, not from the proxies"
assert_contains "helm.sh/hook: pre-install,pre-upgrade" "migration job is a hook" "${output}"
assert_contains "DISABLE_SCHEMA_UPDATE" "proxies do not migrate" "${output}"

# A pre-install hook runs before the chart's ordinary resources exist, so it
# cannot reference any of them: naming the chart's ServiceAccount stops the pod
# being scheduled, and mounting the chart's Secret leaves the container stuck in
# CreateContainerConfigError. Either way the install hangs until it times out.
# Rendering shows every object and cannot reveal the ordering, so the hook's
# references are checked directly.
job_manifest="$(render -s templates/migration-job.yaml)"
if grep -q 'serviceAccountName' <<< "${job_manifest}"; then
  echo "FAIL: the migration hook names a ServiceAccount that does not exist when it runs" >&2
  exit 1
fi
if grep -qE 'name: .*-auth$' <<< "${job_manifest}"; then
  echo "FAIL: the migration hook reads a chart-managed Secret that does not exist when it runs" >&2
  exit 1
fi
assert_contains "DATABASE_URL" "the migration hook gets the database connection" "${job_manifest}"
if grep -q 'LITELLM_SALT_KEY' <<< "${job_manifest}"; then
  echo "FAIL: the migration hook asks for credentials it does not need" >&2
  exit 1
fi

migrations_off="$(render --set migrations.enabled=false)"
assert_absent "DISABLE_SCHEMA_UPDATE" "proxies migrate when the hook is disabled" "${migrations_off}"

# ----- Credentials -----

echo "==> platform credentials"
assert_contains "LITELLM_MASTER_KEY" "master key is passed" "${output}"
assert_contains "LITELLM_SALT_KEY" "salt key is passed" "${output}"
assert_contains "salt-key:" "a salt key is generated when none is supplied" "${output}"

# LiteLLM accepts the master key as the console password when no UI password is
# set, which turns a console sign-in into full management API access.
assert_contains "UI_PASSWORD" "the console has its own password" "${output}"

ui_off="$(render --set auth.adminUI.enabled=false)"
assert_contains "DISABLE_ADMIN_UI" "the console is marked disabled when turned off" "${ui_off}"
assert_absent "UI_PASSWORD" "no console password when the console is off" "${ui_off}"

# An administrator-supplied Secret must be referenced, not copied.
byo="$(render --set-string "auth.saltKey.existingSecret=my-salt" --set-string "auth.saltKey.key=custom")"
assert_contains "name: my-salt" "an existing salt Secret is referenced" "${byo}"
assert_absent "salt-key:" "the chart does not generate a salt key when one is supplied" "${byo}"

# Provider keys reach the proxy as environment variables, read once at startup.
# Changing an inline value with helm upgrade updated the chart's Secret while
# both pods went on sending the old key, so the pods must roll with it.
echo "==> changing an inline provider key rolls the pods"
inline_one="$(render --set-string "providerCredentials.inline.LINT_KEY=one" | grep 'checksum/providers:' || true)"
inline_two="$(render --set-string "providerCredentials.inline.LINT_KEY=two" | grep 'checksum/providers:' || true)"
if [ -z "${inline_one}" ] || [ "${inline_one}" = "${inline_two}" ]; then
  echo "FAIL: a changed inline provider key does not change the pod template" >&2
  exit 1
fi
assert_absent "checksum/providers" "no provider checksum without inline credentials" "${output}"

# ----- Salt key preservation -----

# LiteLLM has no rotation path for the salt key. When it changes, the proxy
# starts normally and reports healthy while every model stored in the database
# silently disappears; restoring the original value brings them back. So a chart
# that regenerates it on upgrade breaks the install unrecoverably, and readiness
# probes do not notice.
#
# This is a source assertion, not a behaviour test: `lookup` returns nothing
# under `helm template`, so preservation across a real upgrade can only be
# verified against a cluster. What this catches is the generation branch being
# reached without first reading the existing value back.
echo "==> the salt key is read back rather than regenerated"
secret_template="${chart_dir}/templates/secret.yaml"
if ! grep -q 'lookup "v1" "Secret"' "${secret_template}"; then
  echo "FAIL: secret.yaml no longer looks up the existing Secret; generated values would change on every upgrade" >&2
  exit 1
fi
if ! grep -A4 'salt-key' "${secret_template}" | grep -q 'b64dec'; then
  if ! grep -B6 'salt-key:' "${secret_template}" | grep -q 'b64dec'; then
    echo "FAIL: the salt key branch does not read an existing value back" >&2
    exit 1
  fi
fi

# PostgreSQL outlives the release. If helm uninstall removed the auth Secret,
# the next install generated a new salt key against the old database, and every
# model added through the console silently disappeared. The cluster test checks
# the behaviour; this catches the annotation going missing before it gets there.
echo "==> the auth Secret survives helm uninstall"
auth_secret="$(render -s templates/secret.yaml)"
assert_contains "helm.sh/resource-policy: keep" "the auth Secret is kept on uninstall" "${auth_secret}"

# ----- Exposure -----

# The console is reached through a port-forward. A chart that ships an Ingress
# for an admin console eventually ships one by accident.
echo "==> the chart creates no Ingress"
assert_absent "kind: Ingress" "no Ingress is rendered" "${output}"

echo "All Gateway chart render assertions passed."
