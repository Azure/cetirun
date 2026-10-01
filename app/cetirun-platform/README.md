# CetiRun Platform chart

`cetirun-platform` packages the Operator and Portal into one Helm release.
It does not install AKS managed Istio, the CetiRun Istio release, certificates,
credentials, Agent Instances, or retained Agent data.

The bundled Operator chart installs the CetiRun CRDs before its other resources
on the first Helm installation. Use values which explicitly confirm the
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
  -o jsonpath='{range .items[*]}{.spec.containers[*].name}{"\n"}{end}' | grep -qw istio-proxy
```

The final command verifies that the Portal has the Istio sidecar required for
its authorization policies. Set `cetirun-operator.istioGateway` to the same
`<namespace>/<name>` as `cetirun-portal.istio.gateway` when using a custom
gateway. `cetirun-portal.istio.gatewayNamespace` and
`gatewayServiceAccount` identify the selected gateway workload, which can run
in a namespace other than its Gateway resource. Also set
`cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` to
that gateway workload's namespace; otherwise the instance NetworkPolicies block
ingress traffic.

Helm creates CRDs from a chart's `crds/` directory but deliberately does not
upgrade or delete them. The deployment helper applies the Operator's CRDs around
the upgrade in the order the Operator's
[schema upgrade rules](../cetirun-operator/README.md#schema-and-binary-upgrades)
require: it creates missing CRDs before the upgrade and waits until they are
established, and it applies changes to existing ones only after the Operator,
and Sandbox admission when enabled, have rolled out and the admission Pods the
rollout replaced have exited. When upgrading without the helper, follow the
same order; never apply changes to existing CRDs while an admission Pod,
including one that is still shutting down, runs an older binary.

The deployment helper accepts `ISTIO_GATEWAY_NAME` and
`ISTIO_GATEWAY_NAMESPACE` for the Gateway resource, and
`ISTIO_INGRESS_NAMESPACE` (defaulting to the Gateway namespace) plus
`ISTIO_GATEWAY_SERVICE_ACCOUNT` for the Gateway workload identity.

When using the umbrella, configure the separate Istio release to reach the
co-located Portal before installing it:

```bash
PORTAL_AUTH_NAMESPACE=cetirun-system ./infra/istio/deploy.sh
```

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.
