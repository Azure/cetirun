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
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-ai-gateway/chart}"
release_name="${RELEASE_NAME:-gateway}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

app_version="$(awk -F': *' '$1=="appVersion" {gsub(/"/, "", $2); print $2; exit}' "${chart_dir}/Chart.yaml")"

# A usable baseline: the defaults, which bundle PostgreSQL and Valkey, plus one
# model configured at install.
base_args=(
  --set-string "models[0].model_name=lint-model"
  --set-string "models[0].litellm_params.model=openai/lint-model"
)
# The production path: an external database and Redis.
external_args=(
  --set "postgresql.bundled.enabled=false"
  --set-string "postgresql.urlSecret.existingSecret=lint-database"
  --set "redis.bundled.enabled=false"
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

# The initContainers block of the Gateway Deployment.
init_block() {
  awk '/^      initContainers:/{f=1} /^      containers:/{f=0} f' <<< "$1"
}

# object_name KIND COMPONENT RENDER: the name of the rendered object of that
# kind whose own labels carry app.kubernetes.io/component=COMPONENT. Prints
# one line: awk runs END even after exit, so END checks whether it printed.
object_name() {
  awk -v kind="$1" -v component="$2" '
    /^---/ { if (k == kind && c) { print n; found = 1; exit } k = ""; n = ""; c = 0; meta = 0; next }
    /^kind: / { k = $2 }
    /^metadata:/ { meta = 1; next }
    /^[^ ]/ { meta = 0 }
    meta && /^  name: / && n == "" { n = $2 }
    meta && /^    app\.kubernetes\.io\/component: / && $2 == component { c = 1 }
    END { if (!found && k == kind && c) print n }' <<< "$3"
}

# A name read from the render must be a single word. grep reads a pattern with
# a newline as two alternatives, so a doubled name would still match.
single_name() {  # single_name NAME DESCRIPTION
  if [ -z "$1" ] || [[ "$1" =~ [[:space:]] ]]; then
    echo "FAIL: ${2} is not a single name: '${1}'" >&2
    exit 1
  fi
}

# ----- Baseline render -----

echo "==> helm template ${release_name} ${chart_dir} (baseline)"
output="$(render)"
external="$(render "${external_args[@]}")"
assert_contains "kind: Deployment" "Deployment is rendered" "${output}"
assert_contains "kind: Service" "Service is rendered" "${output}"
assert_contains "kind: ConfigMap" "ConfigMap is rendered" "${output}"
assert_contains "model_name: lint-model" "configured model reaches the LiteLLM config" "${output}"
assert_contains "\- \-\-config" "the config path is passed explicitly" "${output}"

# Names are read from the rendered objects rather than rebuilt here: the chart
# shortens long names, and uses the release name alone when it already
# contains the chart's, so RELEASE_NAME can be any valid release name.
postgresql_name="$(object_name StatefulSet postgresql "${output}")"
valkey_name="$(object_name Service valkey "${output}")"
single_name "${postgresql_name}" "the bundled PostgreSQL in the default render"
single_name "${valkey_name}" "the bundled Valkey in the default render"

# ----- Required inputs -----

# ----- No models at install -----

# A Gateway can start without models: administrators add them through the
# console later, and a CetiRun deployment may use only the GitHub Copilot
# models the operator registers for each Agent.
echo "==> a Gateway without models renders"
no_models="$(helm template "${release_name}" "${chart_dir}")"
assert_contains "model_list:" "the configuration still lists models" "${no_models}"
assert_absent "model_name:" "no model is configured" "${no_models}"

echo "==> expected rejections for missing or conflicting input"

assert_rejected "missing litellm_params.model" --set-string "models[0].litellm_params.model="
assert_rejected "no database at all" --set "postgresql.bundled.enabled=false"
assert_rejected "an external database while the bundled one is on" \
  --set-string "postgresql.urlSecret.existingSecret=lint-database"
assert_rejected "an external Redis while the bundled one is on" --set-string "redis.host=r.example.invalid"
assert_rejected "both provider credential sources" \
  --set-string "providerCredentials.existingSecret=a" --set-string "providerCredentials.inline.B=c"

# ----- Reserved model-name prefix -----

# A model added later under a name that already exists does not replace the
# original: both are served under one name and traffic splits between two
# different backends, invisibly to the client. The prefix keeps operator
# generated names out of the administrator's namespace.
echo "==> reserved model-name prefix"
assert_rejected "reserved cetirun- prefix" --set-string "models[0].model_name=cetirun-gpt4"
assert_rejected "reserved prefix in another case" --set-string "models[0].model_name=CetiRun-gpt4"
assert_rejected "reserved copilot/ prefix" --set-string "models[0].model_name=copilot/gpt-5"

# ----- Image -----

# The Gateway runs our own image, built from app/cetirun-ai-gateway, and follows
# the other CetiRun charts in taking its registry and version from the umbrella
# chart's global values.
echo "==> the Gateway runs the CetiRun image"
assert_contains "image: cetirun.azurecr.io/cetirun-ai-gateway:${app_version}$" \
  "the default image is cetirun-ai-gateway at the chart's appVersion" "${output}"
assert_absent "berriai" "no upstream LiteLLM image is referenced" "${output}"
umbrella="$(render --set-string global.registry=registry.example.invalid/ --set-string global.version=9.9.9)"
assert_contains "image: registry.example.invalid/cetirun-ai-gateway:9.9.9$" \
  "global.registry and global.version select the image" "${umbrella}"
pinned="$(render --set-string image.digest=sha256:0000000000000000000000000000000000000000000000000000000000000000)"
assert_contains "cetirun-ai-gateway:${app_version}@sha256:0000" "a digest pins the image" "${pinned}"

echo "==> the Gateway runs as a non-root user"
gateway_pod="$(render -s templates/deployment.yaml)"
assert_contains "runAsNonRoot: true" "the Gateway pods refuse to run as root" "${gateway_pod}"
assert_contains "runAsUser: 65532" "the Gateway pods run as the image's nonroot user" "${gateway_pod}"

# ----- Redis and replicas -----

# Without shared state each replica enforces its own copy of every budget and
# rate limit, so a limit is multiplied by the replica count. Measured: a $0.10
# budget permitted $0.20 across two replicas.
echo "==> Redis is required above one replica"
assert_rejected "two replicas without Redis" --set "redis.bundled.enabled=false" --set replicaCount=2

echo "==> a single replica does not require Redis"
single="$(render --set "redis.bundled.enabled=false" --set replicaCount=1)"
assert_contains "replicas: 1" "single replica renders" "${single}"
assert_absent "redis_host" "no Redis configuration without Redis" "${single}"
assert_absent "component: valkey" "no Valkey without Redis" "${single}"

# Redis declared in only one block is inert: it stays unused while budgets keep
# doubling, and the deployment still looks correctly configured. Both blocks
# have to be present, and both have to carry the password.
echo "==> Redis is wired into both configuration blocks"
assert_contains "redis_host: \"${valkey_name}\"" "router_settings points at the bundled Valkey" "${output}"
assert_contains "type: redis" "litellm_settings carries the Redis cache block" "${output}"
assert_contains "host: \"${valkey_name}\"" "the cache block points at the bundled Valkey" "${output}"
assert_contains "redis_password: os.environ/REDIS_PASSWORD" "router_settings carries the password" "${output}"
assert_contains "password: os.environ/REDIS_PASSWORD" "the cache block carries the password" "${output}"
assert_contains "name: REDIS_PASSWORD" "the proxy receives the Valkey password" "${output}"
assert_contains "redis_host: \"lint-redis.example.invalid\"" "an external Redis is used when configured" "${external}"

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

# Migrations run in an init container of every Gateway pod, and the image makes
# pods take turns. A pre-install hook, the earlier design, runs before the
# release's own resources exist, so it cannot reach the bundled PostgreSQL; and
# when it referenced the chart's ServiceAccount or Secret the install hung. No
# hook is rendered at all.
echo "==> migrations run in an init container, not a hook"
init="$(init_block "${gateway_pod}")"
assert_contains "name: migrate" "the Gateway pods migrate before starting" "${init}"
assert_contains "\- migrate$" "the init container runs the image's migrate command" "${init}"
assert_contains "name: DATABASE_URL" "the migrate init container gets the database connection" "${init}"
assert_absent "LITELLM_SALT_KEY" "the migrate init container gets no credential it does not need" "${init}"
assert_absent "LITELLM_MASTER_KEY" "the migrate init container gets no credential it does not need" "${init}"
assert_contains "DISABLE_SCHEMA_UPDATE" "proxies do not migrate on startup" "${output}"
assert_absent "helm.sh/hook" "no Helm hook is rendered" "${output}"
assert_absent "helm.sh/hook" "no Helm hook is rendered with external dependencies" "${external}"

migrations_off="$(render --set migrations.enabled=false)"
assert_absent "DISABLE_SCHEMA_UPDATE" "proxies migrate when the init container is disabled" "${migrations_off}"
assert_absent "initContainers:" "no migrate init container when disabled" \
  "$(render --set migrations.enabled=false -s templates/deployment.yaml)"

# ----- Bundled PostgreSQL and Valkey -----

echo "==> PostgreSQL and Valkey are bundled by default"
assert_contains "kind: StatefulSet" "the bundled PostgreSQL is a StatefulSet" "${output}"
assert_contains "volumeClaimTemplates:" "the bundled PostgreSQL keeps its data on a volume" "${output}"
# The PostgreSQL image declares its data directory a volume. Mounted anywhere
# else, the claim sat unused while the runtime gave that path an anonymous
# volume, discarded with each container: every restart of the pod began from an
# empty database. Only an install-level test showed it; this keeps the mount
# where the data actually goes, and on a subdirectory that initdb accepts.
postgresql="$(render -s templates/postgresql.yaml)"
assert_contains "mountPath: /var/lib/postgresql/data$" \
  "the claim is mounted at the image's declared data directory" "${postgresql}"
assert_contains "subPath: data$" \
  "the data sits in a subdirectory, so a formatted disk's lost+found does not stop initdb" "${postgresql}"
assert_contains "image: mcr.microsoft.com/azurelinux/base/postgres:" "PostgreSQL comes from MCR" "${output}"
assert_contains "image: mcr.microsoft.com/azurelinux/base/valkey:" "Valkey comes from MCR" "${output}"
assert_contains "\- --requirepass" "the bundled Valkey requires a password" "${output}"
db_ref="$(grep -A4 'name: DATABASE_URL' <<< "${output}")"
assert_contains "name: ${postgresql_name}$" "the proxy connects to the bundled PostgreSQL" "${db_ref}"
assert_contains "key: database-url" "the connection URL comes from the bundled PostgreSQL's Secret" "${db_ref}"

echo "==> an external database and Redis replace the bundled ones"
assert_absent "kind: StatefulSet" "no bundled PostgreSQL with an external database" "${external}"
assert_absent "component: valkey" "no bundled Valkey with an external Redis" "${external}"
assert_contains "name: lint-database" "the administrator's database Secret is referenced" "${external}"

# The bundled pods share the release's name and instance labels. Selecting the
# Gateway by those alone put the PostgreSQL and Valkey pods behind the Gateway
# Service, so a share of API traffic went to them. Each pod template's labels
# are compared with the Service selector.
echo "==> the Gateway Service selects only the Gateway pods"
selector="$(render -s templates/service.yaml | awk '/^  selector:/{f=1; next} f && /^    /{sub(/^ +/, ""); print; next} f{f=0}')"
[ -n "${selector}" ] || { echo "FAIL: the Gateway Service has no selector" >&2; exit 1; }
matching=0
while IFS= read -r template; do
  [ -n "${template}" ] || continue
  labels="$(render -s "templates/${template}" | awk '
    /^  template:/{t=1} t && /^      labels:/{l=1; next}
    l && /^        [^ ]/{sub(/^ +/, ""); print; next} l{l=0; t=0}')"
  all=true
  while IFS= read -r pair; do
    grep -qxF -- "${pair}" <<< "${labels}" || all=false
  done <<< "${selector}"
  if [ "${all}" = "true" ]; then
    [ "${template}" = "deployment.yaml" ] || { echo "FAIL: the Gateway Service also selects the pods of ${template}" >&2; exit 1; }
    matching=$((matching + 1))
  fi
done < <(printf '%s\n' deployment.yaml postgresql.yaml valkey.yaml)
[ "${matching}" -eq 1 ] || { echo "FAIL: the Gateway Service does not select the Gateway pods" >&2; exit 1; }

# The bundled PostgreSQL's volume outlives the release, and PostgreSQL keeps the
# password it was initialised with. A password regenerated on upgrade or
# reinstall would lock the Gateway out of its own data, so the Secret is read
# back and kept, like the salt key. lookup returns nothing under helm template,
# so this checks the source; the cluster test checks the behaviour.
echo "==> the bundled dependencies' passwords are read back and kept"
dependency_secrets="$(render -s templates/secret-dependencies.yaml)"
assert_contains "helm.sh/resource-policy: keep" "the dependency Secrets are kept on uninstall" "${dependency_secrets}"
dependency_template="${chart_dir}/templates/secret-dependencies.yaml"
if [ "$(grep -c 'lookup "v1" "Secret"' "${dependency_template}")" -lt 2 ]; then
  echo "FAIL: a dependency Secret is regenerated instead of read back; the next upgrade would change its password" >&2
  exit 1
fi

# The bundled PostgreSQL is a StatefulSet, whose pods carry the label
# controller-revision-hash=<name>-<hash>. The hash has up to ten characters and
# a label value at most 63, so above 52 characters Kubernetes creates no pod:
# measured on 1.37, none from 54 characters, and 53 only while the hash had
# nine. The Gateway then waits for its database indefinitely. Long release
# names are shortened to fit, with a hash that keeps two of them apart.
echo "==> long release names still fit the bundled PostgreSQL and Valkey"
long_release="release-name-long-enough-to-need-shortening"
long_one="$(helm template "${long_release}-one" "${chart_dir}" "${base_args[@]}")"
long_two="$(helm template "${long_release}-two" "${chart_dir}" "${base_args[@]}")"
pg_one="$(object_name StatefulSet postgresql "${long_one}")"
pg_two="$(object_name StatefulSet postgresql "${long_two}")"
valkey_one="$(object_name Service valkey "${long_one}")"
valkey_two="$(object_name Service valkey "${long_two}")"
for name in "${pg_one}" "${pg_two}" "${valkey_one}" "${valkey_two}"; do
  single_name "${name}" "a bundled resource of a long release name"
done
for name in "${pg_one}" "${pg_two}"; do
  [ -n "${name}" ] && [ "${#name}" -le 52 ] || {
    echo "FAIL: the bundled PostgreSQL is named '${name}' (${#name} characters); above 52 Kubernetes creates no pod for it" >&2
    exit 1
  }
done
for name in "${valkey_one}" "${valkey_two}"; do
  [ -n "${name}" ] && [ "${#name}" -le 63 ] || {
    echo "FAIL: the bundled Valkey is named '${name}' (${#name} characters), more than a Service name allows" >&2
    exit 1
  }
done
if [ "${pg_one}" = "${pg_two}" ] || [ "${valkey_one}" = "${valkey_two}" ]; then
  echo "FAIL: two long release names sharing a prefix get the same bundled resource names" >&2
  exit 1
fi
assert_contains "name: ${pg_one}$" "the Gateway connects to the shortened PostgreSQL name" \
  "$(grep -A4 'name: DATABASE_URL' <<< "${long_one}")"
assert_contains "redis_host: \"${valkey_one}\"" "the Gateway connects to the shortened Valkey name" "${long_one}"

# Names that fit are never shortened. The data volume's claim is named after
# the StatefulSet, so renaming it in an upgrade would start an empty database
# and leave the data behind on the old claim.
default_names="$(helm template gateway "${chart_dir}" "${base_args[@]}")"
if [ "$(object_name StatefulSet postgresql "${default_names}")" != "gateway-cetirun-ai-gateway-postgresql" ] \
  || [ "$(object_name Service valkey "${default_names}")" != "gateway-cetirun-ai-gateway-valkey" ]; then
  echo "FAIL: the default release's bundled resources were renamed; an upgrade would leave the data on the old volume" >&2
  exit 1
fi

# Under helm template, which GitOps tools such as Argo CD use, lookup returns
# nothing, so a generated Valkey password changed with every render. Neither
# Valkey nor the Gateway restarts when only that Secret changes, so the next
# restart of just one of them left the two with different passwords.
# redis.passwordSecret serves the bundled Valkey too, and the documented GitOps
# configuration renders the same way every time.
echo "==> the bundled Valkey can take its password from an existing Secret"
valkey_env="$(render -s templates/valkey.yaml | grep -A4 'name: VALKEY_PASSWORD')"
assert_contains "name: ${valkey_name}$" "by default the bundled Valkey uses the generated password" "${valkey_env}"
supplied=(--set-string "redis.passwordSecret.existingSecret=lint-redis")
valkey_env="$(render "${supplied[@]}" -s templates/valkey.yaml | grep -A4 'name: VALKEY_PASSWORD')"
assert_contains "name: lint-redis$" "the bundled Valkey reads a supplied password Secret" "${valkey_env}"
assert_contains "key: redis-password$" "the bundled Valkey reads the supplied Secret's key" "${valkey_env}"
redis_env="$(render "${supplied[@]}" -s templates/deployment.yaml | grep -A4 'name: REDIS_PASSWORD')"
assert_contains "name: lint-redis$" "the Gateway reads the same supplied Secret as Valkey" "${redis_env}"
assert_absent "component: valkey" "no Valkey password is generated when one is supplied" \
  "$(render "${supplied[@]}" -s templates/secret-dependencies.yaml)"
gitops_args=(
  "${supplied[@]}"
  --set-string "auth.masterKey.existingSecret=lint-auth"
  --set-string "auth.saltKey.existingSecret=lint-auth"
  --set-string "auth.adminUI.passwordSecret.existingSecret=lint-auth"
  --set "postgresql.bundled.enabled=false"
  --set-string "postgresql.urlSecret.existingSecret=lint-database"
)
if [ "$(render "${gitops_args[@]}")" != "$(render "${gitops_args[@]}")" ]; then
  echo "FAIL: the documented GitOps configuration renders differently each time; every sync would change a credential" >&2
  exit 1
fi

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
