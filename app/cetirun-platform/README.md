# CetiRun Platform chart

`cetirun-platform` packages the Operator and Portal into one Helm release.
It does not install AKS managed Istio, the CetiRun Istio release, certificates,
credentials, Agent Instances, or retained Agent data.

Install the CetiRun CRDs first (or use `scripts/cetirun-platform-deploy.sh`,
which installs and waits for them), then use values which explicitly confirm
the separately installed Istio prerequisites:

```bash
kubectl apply -f app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_openclawinstances.yaml
kubectl apply -f app/cetirun-operator/config/crd/bases/cetirun.microsoft.com_openclawteams.yaml
kubectl wait --for=condition=Established \
  crd/openclawinstances.cetirun.microsoft.com \
  crd/openclawteams.cetirun.microsoft.com --timeout=30s

helm dependency build app/cetirun-platform
helm upgrade --install cetirun-platform app/cetirun-platform \
  --namespace cetirun-system --create-namespace \
  --set istio.enabled=true \
  --set-string cetirun-operator.domainSuffix=cetirun.example.com \
  --set cetirun-portal.istio.host=cetirun.example.com \
  --set cetirun-portal.istio.gateway=aks-istio-ingress/cetirun-gateway \
  --set cetirun-portal.istio.gatewayNamespace=aks-istio-ingress \
  --set cetirun-portal.istio.gatewayServiceAccount=aks-istio-ingressgateway-external
```

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
