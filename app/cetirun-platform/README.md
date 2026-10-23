# CetiRun Platform chart

`cetirun-platform` packages the Operator, the Portal and the AI Gateway into one
Helm release. It does not install AKS managed Istio, the CetiRun Istio release,
certificates, credentials, Agent Instances, or retained Agent data.

CetiRun Connections MCP can optionally be enabled as an independent component. Install
the Agent Broker separately in its private credential namespace; the umbrella
rejects colocating an enabled Broker with the Tool service. Provision TLS and
OAuth separately, then configure the MCP Broker HTTPS origin, Portal/Operator
management HTTP origins, and Operator MCP URL/CA-only Secret. The sample
`app/cetirun-connections-mcp/chart/ci/render-values.yaml` contains synthetic render-only
configuration; it does not create certificates or authorize deployment.

The bundled Operator chart manages the four CetiRun CRDs as Helm templates,
including schema upgrades. Use values which explicitly confirm the
separately installed Istio prerequisites:

```bash
ISTIO_REVISION="$(kubectl get pods -n aks-istio-system -l app=istiod \
  -o jsonpath='{.items[0].metadata.labels.istio\.io/rev}')"
test -n "${ISTIO_REVISION}"
kubectl create namespace cetirun-system --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace cetirun-system "istio.io/rev=${ISTIO_REVISION}" --overwrite

helm dependency build app/cetirun-platform
helm upgrade --install cetirun-platform app/cetirun-platform \
  --namespace cetirun-system \
  --set istio.enabled=true \
  --set-string cetirun-operator.domainSuffix=cetirun.example.com \
  --set-string "cetirun-operator.istioRevision=${ISTIO_REVISION}" \
  --set-string cetirun-operator.istioGateway=aks-istio-ingress/cetirun-gateway \
  --set-string cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace=aks-istio-ingress \
  --set cetirun-portal.istio.host=cetirun.example.com \
  --set cetirun-portal.istio.gateway=aks-istio-ingress/cetirun-gateway \
  --set cetirun-portal.istio.gatewayNamespace=aks-istio-ingress \
  --set cetirun-portal.istio.gatewayServiceAccount=aks-istio-ingressgateway-external

kubectl rollout status deployment/cetirun-portal -n cetirun-system --timeout=180s
kubectl get pods -n cetirun-system -l app.kubernetes.io/name=cetirun-portal \
  -o jsonpath='{range .items[*]}{.spec.containers[*].name} {.spec.initContainers[*].name}{"\n"}{end}' | grep -qw istio-proxy
```

The final command verifies that the Portal has the Istio sidecar required for
its authorization policies; it lists init containers too, because Istio can
inject the sidecar as a native sidecar, an init container that keeps running,
as the AKS Istio add-on does. Set `cetirun-operator.istioGateway` to the same
`<namespace>/<name>` as `cetirun-portal.istio.gateway` when using a custom
gateway. `cetirun-portal.istio.gatewayNamespace` and
`gatewayServiceAccount` identify the selected gateway workload, which can run
in a namespace other than its Gateway resource. Also set
`cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` to
that gateway workload's namespace; otherwise the instance NetworkPolicies block
ingress traffic.

Helm upgrades the CRD templates with this release; no separate CRD apply script
is required for compatible changes. The `helm.sh/resource-policy: keep`
annotation retains CRDs and their objects on uninstall. It does not protect
against an incompatible schema change or downgrade. The Operator chart defaults
`sandboxAdmission.validationMode` to `compatible`, retaining known validation and
authorization checks while tolerating newer CR fields. See the
[schema and binary upgrade contract](../cetirun-operator/README.md#schema-and-binary-upgrades).
Previously unmanaged CRDs and cross-release ownership transfers still require
a separately reviewed handover; this chart does not forcibly adopt them.

The deployment helper accepts `ISTIO_GATEWAY_NAME` and
`ISTIO_GATEWAY_NAMESPACE` for the Gateway resource, and
`ISTIO_INGRESS_NAMESPACE` (defaulting to the Gateway namespace) plus
`ISTIO_GATEWAY_SERVICE_ACCOUNT` for the Gateway workload identity.

To install in a namespace other than `cetirun-system`, set both
`cetirun-operator.namespace` and `cetirun-portal.aiGateway.catalogNamespace` to
it: the operator publishes the shared models in its own namespace, where the
Portal reads them. The deployment helper sets both from `NAMESPACE`, and the
chart refuses a Portal that would look for them elsewhere.

When using the umbrella, configure the separate Istio release to reach the
co-located Portal before installing it:

```bash
PORTAL_AUTH_NAMESPACE=cetirun-system ./infra/istio/deploy.sh
```

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.

## Sandbox creation profiles

Configure `cetirun-portal.sandboxRuntimeProfiles` for each supported built-in
Sandbox runtime. Each entry contains `chartVersion`, a digest-pinned `image`,
and `port` (18789 for OpenClaw, 8642 for Hermes). The chart version must match
the Operator's configured runtime chart, and that chart's profile must approve
the image and port. Only configure harnesses supported by the selected platform;
the common native executor supports OpenClaw and Hermes, while retained
single-start records are OpenClaw-only.

The Portal records these inputs in the initial Agent creation request, so users
do not enter deployment coordinates in the creation form. Explicit image/port
requests remain subject to admission. By default, missing or invalid deployment
profiles reject creation before any Agent, namespace or Secret is written.

With the Operator's `sandboxSources.nativeExecution` enabled, the deployment may set
`cetirun-portal.sandboxRuntimeDefaultsFromController: true`. This permits missing
profiles to use trusted chart/image/port defaulting in the common executor.
Configured profiles are still validated. The platform chart rejects this Portal
opt-in unless the Operator and its native sources are enabled.
For standalone Portal releases, verify the matching Operator configuration
before enabling it, and turn it off before disabling native execution.
These defaults do not authorize a runtime or change the
selected AgentPolicy/Sandbox checks.

## AI Gateway

OpenClaw and Hermes Agents take their models from the AI Gateway, so the
platform requires it: the chart refuses to install with
`cetirun-operator.aiGateway.enabled` or `cetirun-portal.features.aiGatewayEnabled`
turned off. Hermes Agents pinned to a chart version without Hermes' Gateway
provider keep their own model provider and sign-in until their chart is
upgraded. By default the release
also installs the Gateway with its PostgreSQL and Valkey, named
`cetirun-ai-gateway`, in the release namespace, which must be the operator's
namespace: the operator reads the Gateway master key from there. See the
[Gateway chart](../cetirun-ai-gateway/chart/README.md) for its settings, under
`cetirun-ai-gateway` here.

Shared models are the Gateway's models in the access group `cetirun-shared`.
Agents are offered only those, whether a model is set in
`cetirun-ai-gateway.models` or added later in the Gateway console: a model
without the access group is served by the Gateway but given to no Agent. While
the Gateway blocks unpriced models, as it does by default, each one also needs
a price. For example, with the provider key in a Secret you create in the
release namespace:

```yaml
cetirun-ai-gateway:
  providerCredentials:
    existingSecret: gateway-provider-keys  # holds AZURE_OPENAI_KEY
  models:
    - model_name: gpt-4o
      litellm_params:
        model: azure/my-gpt-4o-deployment
        api_base: https://example.openai.azure.com/
        api_key: os.environ/AZURE_OPENAI_KEY
        # Must support the Responses API: agents' tool calls are sent there.
        api_version: "2025-04-01-preview"
      model_info:
        access_groups: ["cetirun-shared"]
        # The price; or set input_cost_per_token and output_cost_per_token.
        base_model: azure/gpt-4o
```

Shared models are optional. Without them OpenClaw Agents use their owners'
GitHub Copilot; with `cetirun-operator.aiGateway.copilotProxy.enabled` turned
off, the Portal creates no OpenClaw Agent until a shared model is offered. To
add shared models in the console, give them the access group and a price
there, and open the console with:

```bash
kubectl get secret cetirun-ai-gateway-auth -n cetirun-system \
  -o jsonpath='{.data.ui-password}' | base64 -d; echo
kubectl port-forward -n cetirun-system svc/cetirun-ai-gateway 4000:4000
```

Then open <http://localhost:4000/ui> and sign in as `admin`.

A Gateway installed separately can be used instead: set
`cetirun-ai-gateway.enabled=false` and install it in the operator namespace
under the name `cetirun-ai-gateway`, with `operatorIntegration: true`.
Elsewhere, set `cetirun-operator.aiGateway.url` to it and copy its master key
into the operator namespace as the `cetirun-ai-gateway-auth` Secret.

New Agents need a Team. Create one before users create Agents, in the Portal's
administration pages or with a manifest:

```bash
kubectl apply -f - <<'EOF'
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Team
metadata:
  name: example-team
spec: {}
EOF
```
