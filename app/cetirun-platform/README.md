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
gateway.

Helm creates CRDs from a chart's `crds/` directory but deliberately does not
upgrade or delete them. Upgrade CRD schemas through an explicit, reviewed
migration before upgrading the Operator chart.

`cetirun-portal.istio.gateway`, `gatewayNamespace`, and
`gatewayServiceAccount` must identify the same ingress gateway. The deployment
helper configures all three from `ISTIO_GATEWAY_NAME`,
`ISTIO_GATEWAY_NAMESPACE`, and `ISTIO_GATEWAY_SERVICE_ACCOUNT`.

When using the umbrella, configure the separate Istio release to reach the
co-located Portal before installing it:

```bash
PORTAL_AUTH_NAMESPACE=cetirun-system ./infra/istio/deploy.sh
```

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.
