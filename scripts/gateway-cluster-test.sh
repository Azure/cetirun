#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Install-and-upgrade test for the CetiRun AI Gateway chart, against a real
# cluster. Needs kubectl pointed at a cluster, plus helm, curl and jq.
#
# This exists because the offline render suite cannot see ordering or runtime
# behaviour. Three bugs got past it, all rendering cleanly and passing every
# assertion: a pre-install hook naming a ServiceAccount that did not exist yet,
# the same hook reading a Secret that did not exist yet, and a migration that
# exited zero without creating any of LiteLLM's tables. A fourth only showed up
# here: the Redis wiring switched on response caching, so identical requests
# were answered from cache, across callers, instead of reaching the model. Each
# check below is tied to something that has actually gone wrong, or to a claim
# the chart makes that only a running cluster can confirm.
#
# Five checks have been verified by putting the original problem back and
# confirming the test fails at the check meant to catch it: the empty
# migration, response caching, the deleted auth Secret, retries, and the
# default grace period.
#
# Everything runs against a deterministic mock provider, so no provider account
# or credential is involved.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-gateway-chart}"
namespace="${NAMESPACE:-gateway-cluster-test}"
release_name="${RELEASE_NAME:-gateway}"
# Upstream until MCR onboarding is done. The chart's pinned digest is valid for
# both, because a mirror preserves it.
image_repository="${IMAGE_REPOSITORY:-ghcr.io/berriai/litellm}"
keep="${KEEP:-false}"

for tool in kubectl helm curl jq; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "ERROR: Missing required command: ${tool}" >&2
    exit 1
  fi
done

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

work_dir="$(mktemp -d)"
forward_pids=()

stop_forwards() {
  for pid in "${forward_pids[@]}"; do
    kill "${pid}" 2>/dev/null || true
  done
  forward_pids=()
}

cleanup() {
  stop_forwards
  if [ "${keep}" != "true" ]; then
    helm uninstall "${release_name}" -n "${namespace}" >/dev/null 2>&1 || true
    kubectl delete namespace "${namespace}" --wait=false >/dev/null 2>&1 || true
  fi
  rm -rf "${work_dir}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  echo "--- pods ---" >&2
  kubectl get pods -n "${namespace}" >&2 || true
  exit 1
}

pass() {
  echo "  ok: $*"
}

# ----- Fixtures -------------------------------------------------------------

echo "==> Deploying fixtures into ${namespace}"
kubectl create namespace "${namespace}" >/dev/null

kubectl apply -n "${namespace}" -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: gateway-database
type: Opaque
stringData:
  database-url: postgresql://litellm:litellm@postgres:5432/litellm
---
apiVersion: v1
kind: Secret
metadata:
  name: gateway-provider-keys
type: Opaque
stringData:
  MOCK_PROVIDER_KEY: sk-mock-not-a-real-key
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
spec:
  replicas: 1
  selector:
    matchLabels: {app: postgres}
  template:
    metadata:
      labels: {app: postgres}
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - {name: POSTGRES_USER, value: litellm}
            - {name: POSTGRES_PASSWORD, value: litellm}
            - {name: POSTGRES_DB, value: litellm}
          readinessProbe:
            exec:
              command: ["pg_isready", "-U", "litellm", "-d", "litellm"]
            periodSeconds: 3
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
spec:
  selector: {app: postgres}
  ports:
    - port: 5432
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
spec:
  replicas: 1
  selector:
    matchLabels: {app: redis}
  template:
    metadata:
      labels: {app: redis}
    spec:
      containers:
        - name: redis
          image: redis:7-alpine
          readinessProbe:
            exec:
              command: ["redis-cli", "ping"]
            periodSeconds: 3
---
apiVersion: v1
kind: Service
metadata:
  name: redis
spec:
  selector: {app: redis}
  ports:
    - port: 6379
---
# OpenAI-compatible backend that always reports 1000 prompt and 1000
# completion tokens, so spend per request is exact. A request whose content
# contains "fail429" is answered with a 429, and GET /stats reports how many
# such requests arrived, so the test can count upstream attempts. One whose
# content contains "sleepN" is answered after N seconds.
apiVersion: v1
kind: ConfigMap
metadata:
  name: mock-provider
data:
  mock_provider.py: |
    import json, re, time
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
    failed = {"count": 0}
    class H(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        def _send(self, body, code=200):
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        def do_POST(self):
            n = int(self.headers.get("Content-Length", 0))
            raw = self.rfile.read(n) if n else b""
            if b"fail429" in raw:
                failed["count"] += 1
                self._send(b'{"error":{"message":"rate limited (mock)","code":"429"}}', 429)
                return
            delay = re.search(rb"sleep(\d+)", raw)
            if delay:
                time.sleep(int(delay.group(1)))
            self._send(json.dumps({
                "id": "chatcmpl-mock", "object": "chat.completion",
                "created": int(time.time()), "model": "mock-model",
                "choices": [{"index": 0, "finish_reason": "stop",
                             "message": {"role": "assistant", "content": "mock-ok"}}],
                "usage": {"prompt_tokens": 1000, "completion_tokens": 1000,
                          "total_tokens": 2000},
            }).encode())
        def do_GET(self):
            if self.path.startswith("/stats"):
                self._send(json.dumps({"failed": failed["count"]}).encode())
                return
            self._send(b'{"status":"ok"}')
        def log_message(self, *a):
            pass
    ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mock-provider
spec:
  replicas: 1
  selector:
    matchLabels: {app: mock-provider}
  template:
    metadata:
      labels: {app: mock-provider}
    spec:
      containers:
        - name: mock
          image: python:3.12-alpine
          command: ["python", "/app/mock_provider.py"]
          volumeMounts:
            - {name: src, mountPath: /app}
      volumes:
        - name: src
          configMap: {name: mock-provider}
---
apiVersion: v1
kind: Service
metadata:
  name: mock-provider
spec:
  selector: {app: mock-provider}
  ports:
    - port: 8080
EOF

kubectl wait --for=condition=available deployment --all -n "${namespace}" --timeout=300s >/dev/null
pass "PostgreSQL, Redis and the mock provider are running"

values_file="${work_dir}/values.yaml"
cat > "${values_file}" <<EOF
image:
  repository: ${image_repository}
replicaCount: 2
models:
  # Priced, so it is served and budgets apply to it.
  - model_name: mock-priced
    litellm_params:
      model: openai/mock-model
      api_base: http://mock-provider:8080
      api_key: os.environ/MOCK_PROVIDER_KEY
    model_info:
      input_cost_per_token: 0.00001
      output_cost_per_token: 0.00001
  # Deliberately unpriced, to check the chart refuses it.
  - model_name: mock-unpriced
    litellm_params:
      model: openai/mock-model
      api_base: http://mock-provider:8080
      api_key: os.environ/MOCK_PROVIDER_KEY
providerCredentials:
  existingSecret: gateway-provider-keys
postgresql:
  urlSecret:
    existingSecret: gateway-database
redis:
  host: redis
  port: 6379
EOF

# ----- Helpers against the running Gateway ----------------------------------

fullname="${release_name}-cetirun-gateway"

secret_value() {
  kubectl get secret "${fullname}-auth" -n "${namespace}" -o "jsonpath={.data.$1}" | base64 -d
}

# Counts LiteLLM's own tables, not every table. `prisma migrate deploy` creates
# its bookkeeping table even when it finds no migrations to apply, so counting
# all tables passes on exactly the empty database this check exists to catch.
# That was found by putting the original bug back and watching the check pass.
litellm_table_count() {
  local pod
  pod="$(kubectl get pods -n "${namespace}" -l app=postgres -o jsonpath='{.items[0].metadata.name}')"
  kubectl exec -n "${namespace}" "${pod}" -- \
    psql -U litellm -d litellm -tAc \
    "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name LIKE 'LiteLLM\_%'"
}

# Forward each Gateway pod on its own local port. A port-forward to the Service
# lands on a single pod, which would make every multi-replica check below
# indistinguishable from a single replica.
#
# Pods are chosen by readiness rather than phase: after an upgrade the old pods
# are still in phase Running while they terminate. The migration hook shares the
# selector labels, so it is excluded by component.
forward_pods() {
  stop_forwards
  local pods port=18101
  mapfile -t pods < <(kubectl get pods -n "${namespace}" \
    -l "app.kubernetes.io/instance=${release_name},app.kubernetes.io/name=cetirun-gateway,app.kubernetes.io/component!=migration" \
    -o json | jq -r '.items[]
      | select(.metadata.deletionTimestamp == null)
      | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))
      | .metadata.name')
  if [ "${#pods[@]}" -lt 2 ]; then
    fail "expected two ready Gateway pods, found ${#pods[@]}"
  fi
  pod_ports=()
  for pod in "${pods[@]:0:2}"; do
    kubectl port-forward -n "${namespace}" "pod/${pod}" "${port}:4000" >/dev/null 2>&1 &
    forward_pids+=("$!")
    pod_ports+=("${port}")
    port=$((port + 1))
  done
  for p in "${pod_ports[@]}"; do
    local ready=false
    for _ in $(seq 1 30); do
      if curl -fs -m 5 "http://127.0.0.1:${p}/health/readiness" >/dev/null 2>&1; then
        ready=true
        break
      fi
      sleep 2
    done
    [ "${ready}" = "true" ] || fail "pod on local port ${p} never became reachable"
  done
}

# POST a chat completion; prints the HTTP status. The content is the same every
# time on purpose: identical requests are what a response cache would answer,
# so a test built from varied requests cannot notice one being switched on.
chat() {
  local port="$1" key="$2" model="$3" content="${4:-test}"
  curl -s -m 60 -o "${work_dir}/last.json" -D "${work_dir}/last.headers" -w '%{http_code}' \
    -X POST "http://127.0.0.1:${port}/v1/chat/completions" \
    -H "Authorization: Bearer ${key}" -H 'Content-Type: application/json' \
    -d "{\"model\":\"${model}\",\"messages\":[{\"role\":\"user\",\"content\":\"${content}\"}]}"
}

served_from_cache() {
  tr -d '\r' < "${work_dir}/last.headers" | grep -qi '^x-litellm-cache-key:'
}

api_post() {
  local port="$1" path="$2" body="$3"
  curl -fs -m 30 -X POST "http://127.0.0.1:${port}${path}" \
    -H "Authorization: Bearer ${master_key}" -H 'Content-Type: application/json' -d "${body}"
}

# ----- Install ---------------------------------------------------------------

# A pre-install hook that names a chart-managed ServiceAccount or Secret can
# never start, so the install hangs until this timeout. Reaching the next line
# is the check.
echo "==> Installing ${release_name}"
helm install "${release_name}" "${chart_dir}" -n "${namespace}" -f "${values_file}" \
  --wait --timeout 10m >/dev/null || fail "install did not complete"
pass "install completed, including the migration hook"

# The migration once exited zero against a schema copy with no migrations beside
# it, leaving no LiteLLM tables behind a successful install. The proxies still
# started, passed readiness, and served models from the config file. Only the
# tables tell them apart.
tables="$(litellm_table_count)"
[ "${tables}" -gt 0 ] || fail "install reported success but none of LiteLLM's tables exist"
pass "migrations applied (${tables} LiteLLM tables)"

master_key="$(secret_value master-key)"
salt_before="$(secret_value salt-key)"
[ -n "${salt_before}" ] || fail "no salt key was generated"

forward_pods
port_a="${pod_ports[0]}"
port_b="${pod_ports[1]}"
pass "both replicas reachable individually"

# ----- Behaviour -------------------------------------------------------------

echo "==> End-to-end request"
[ "$(chat "${port_a}" "${master_key}" mock-priced)" = "200" ] || fail "request to a priced model failed"
grep -q 'mock-ok' "${work_dir}/last.json" || fail "response did not come from the backend"
pass "request reached the backend and returned"

# Redis is configured through LiteLLM's cache so that budgets are shared across
# replicas. Left at its default, that cache also answers identical requests from
# Redis: recorded at zero spend, not counted against any budget, and shared
# across every caller who sends the same content. An agent retrying after a bad
# answer would get the same answer back. The chart sets it to default_off.
echo "==> Responses are not cached"
chat "${port_a}" "${master_key}" mock-priced >/dev/null
if served_from_cache; then
  fail "an identical request was answered from the response cache"
fi
pass "identical repeat reached the backend rather than the cache"

# A model missing from the price table costs zero, and budget checks are skipped
# for zero-cost models, so a budget would silently not apply. The chart refuses
# such requests by default.
echo "==> Unpriced models are refused"
[ "$(chat "${port_a}" "${master_key}" mock-unpriced)" = "403" ] || fail "an unpriced model was served"
pass "unpriced model refused with 403"

# LiteLLM retries a failed upstream call twice by default, and agents retry on
# their own, so the two multiply: one failing request was measured at three
# upstream calls. The chart turns LiteLLM's retries off; this checks that the
# running version honours it, which rendering alone cannot show.
echo "==> Upstream failures are not retried"
status="$(chat "${port_a}" "${master_key}" mock-priced fail429)"
[ "${status}" = "429" ] || fail "an upstream 429 reached the client as ${status}"
mock_pod="$(kubectl get pods -n "${namespace}" -l app=mock-provider -o jsonpath='{.items[0].metadata.name}')"
attempts="$(kubectl exec -n "${namespace}" "${mock_pod}" -- \
  python -c "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080/stats').read().decode())" \
  | jq -r .failed)"
[ "${attempts}" = "1" ] || fail "one failing request produced ${attempts} upstream attempts; the Gateway is retrying"
pass "an upstream 429 went straight back to the client, one attempt"

# Without shared state each replica enforces its own copy of a budget, so two
# replicas allow twice the configured spend. Alternating between the two pods
# directly is what makes this a test of coordination rather than of one pod.
echo "==> A budget holds across replicas"
virtual_key="$(api_post "${port_a}" /key/generate '{"models":["mock-priced"],"max_budget":0.10}' | jq -r .key)" \
  || fail "could not create a virtual key through the management API"
[ -n "${virtual_key}" ] && [ "${virtual_key}" != "null" ] || fail "could not create a virtual key"
allowed=0
for i in $(seq 1 16); do
  if [ $((i % 2)) -eq 1 ]; then port="${port_a}"; else port="${port_b}"; fi
  if [ "$(chat "${port}" "${virtual_key}" mock-priced)" = "200" ]; then
    allowed=$((allowed + 1))
  fi
  sleep 0.3
done
# $0.10 at $0.02 a request is five. Allow one request of slack for a request in
# flight while spend was being reserved. Missing coordination shows as about
# ten; response caching shows as all sixteen, because cache hits cost nothing.
[ "${allowed}" -ge 5 ] && [ "${allowed}" -le 6 ] \
  || fail "a \$0.10 budget allowed ${allowed} requests across two replicas; expected 5"
pass "\$0.10 budget allowed ${allowed} requests across both replicas"

# A model stored in the database is encrypted with the salt key. It is what an
# upgrade has to preserve.
echo "==> A database-backed model"
api_post "${port_a}" /model/new '{"model_name":"mock-db","litellm_params":{"model":"openai/mock-model","api_base":"http://mock-provider:8080","api_key":"sk-literal-encrypted-at-rest"},"model_info":{"input_cost_per_token":0.00001,"output_cost_per_token":0.00001}}' >/dev/null \
  || fail "could not add a model through the management API"
[ "$(chat "${port_a}" "${master_key}" mock-db)" = "200" ] || fail "database-backed model was not served"
pass "model added through the management API and served"

# LiteLLM finishes requests in flight when it is stopped, with no limit of its
# own, so the pod's grace period decides whether a long response survives a
# rollout or a node drain. At the Kubernetes default of 30 seconds, a rolling
# update cut a streamed response off partway through. The request goes from
# inside the cluster to the pod's own address, as a client's connection does
# once the Service has routed it, and outlasts the default by a clear margin.
echo "==> A stopping replica finishes the request it is serving"
target_pod="$(kubectl get pods -n "${namespace}" \
  -l "app.kubernetes.io/instance=${release_name},app.kubernetes.io/name=cetirun-gateway,app.kubernetes.io/component!=migration" \
  -o jsonpath='{.items[0].metadata.name}')"
target_ip="$(kubectl get pod "${target_pod}" -n "${namespace}" -o jsonpath='{.status.podIP}')"
mock_pod="$(kubectl get pods -n "${namespace}" -l app=mock-provider -o jsonpath='{.items[0].metadata.name}')"
kubectl exec -n "${namespace}" "${mock_pod}" -- python -c '
import json, sys, urllib.error, urllib.request
body = {"model": "mock-priced", "messages": [{"role": "user", "content": "sleep45"}]}
req = urllib.request.Request(sys.argv[1], data=json.dumps(body).encode(),
    headers={"Authorization": "Bearer " + sys.argv[2], "Content-Type": "application/json"})
try:
    print(urllib.request.urlopen(req, timeout=120).status)
except urllib.error.HTTPError as e:
    print(e.code)
except Exception as e:
    print(type(e).__name__)
' "http://${target_ip}:4000/v1/chat/completions" "${master_key}" > "${work_dir}/slow.out" 2>&1 &
slow_pid=$!
sleep 5
kubectl delete pod "${target_pod}" -n "${namespace}" --wait=false >/dev/null
wait "${slow_pid}" || true
slow_status="$(tail -n 1 "${work_dir}/slow.out")"
[ "${slow_status}" = "200" ] \
  || fail "a request in flight was cut off when its replica stopped (${slow_status}); the grace period is too short"
# A long grace period is free only if a replica exits once its work is done.
# An idle replica was measured exiting four seconds after SIGTERM.
gone=false
for _ in $(seq 1 30); do
  if ! kubectl get pod "${target_pod}" -n "${namespace}" >/dev/null 2>&1; then
    gone=true
    break
  fi
  sleep 2
done
[ "${gone}" = "true" ] || fail "the replica was still running a minute after its last request finished"
pass "a 45-second request completed while its replica shut down, and the replica then exited"

# ----- Upgrade ---------------------------------------------------------------

# LiteLLM has no rotation path for the salt key. When it changes the proxy
# starts, reports healthy, and silently stops serving every database-backed
# model. Offline tests cannot check preservation, because lookup returns
# nothing under helm template.
echo "==> Upgrading ${release_name}"
helm upgrade "${release_name}" "${chart_dir}" -n "${namespace}" -f "${values_file}" \
  --set-string podAnnotations.cluster-test/revision=2 --wait --timeout 10m >/dev/null \
  || fail "upgrade did not complete"
pass "upgrade completed"

[ "$(secret_value salt-key)" = "${salt_before}" ] || fail "the salt key changed across upgrade"
[ "$(secret_value master-key)" = "${master_key}" ] || fail "the master key changed across upgrade"
pass "salt key and master key unchanged"

forward_pods
port_a="${pod_ports[0]}"
[ "$(chat "${port_a}" "${master_key}" mock-db)" = "200" ] \
  || fail "database-backed model stopped working after upgrade; stored credentials may be unreadable"
pass "database-backed model still decrypts and serves after upgrade"

# ----- Uninstall and install again -------------------------------------------

# The database outlives the release. When helm uninstall removed the auth
# Secret, the next install generated a new salt key against the old database:
# every database-backed model vanished from the model list, the previous master
# key stopped working, and only a log warning said why. The Secret is now kept on
# uninstall, so the reinstall's lookup finds and reuses it.
echo "==> Uninstalling and installing again"
stop_forwards
helm uninstall "${release_name}" -n "${namespace}" --wait >/dev/null || fail "uninstall did not complete"
kubectl get secret "${fullname}-auth" -n "${namespace}" >/dev/null 2>&1 \
  || fail "helm uninstall deleted the auth Secret; the next install would generate a new salt key"
pass "auth Secret kept on uninstall"

helm install "${release_name}" "${chart_dir}" -n "${namespace}" -f "${values_file}" \
  --wait --timeout 10m >/dev/null || fail "install after uninstall did not complete"
[ "$(secret_value salt-key)" = "${salt_before}" ] || fail "the salt key changed across uninstall and install"
[ "$(secret_value master-key)" = "${master_key}" ] || fail "the master key changed across uninstall and install"
pass "salt key and master key reused by the new install"

forward_pods
port_a="${pod_ports[0]}"
[ "$(chat "${port_a}" "${master_key}" mock-db)" = "200" ] \
  || fail "database-backed model stopped working after uninstall and install"
pass "database-backed model still decrypts and serves"

echo "All Gateway cluster checks passed."
