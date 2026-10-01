# CetiRun Platform chart

`cetirun-platform` packages the Operator and Portal into one Helm release.
It does not install AKS managed Istio, the CetiRun Istio release, certificates,
credentials, Agent Instances, or retained Agent data.

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

When using the umbrella, configure the separate Istio release to reach the
co-located Portal before installing it:

```bash
PORTAL_AUTH_NAMESPACE=cetirun-system ./infra/istio/deploy.sh
```

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.
