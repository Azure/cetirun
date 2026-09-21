# CetiRun Platform chart

`cetirun-platform` packages the Operator and Portal into one Helm release.
It does not install AKS managed Istio, the CetiRun Istio release, certificates,
credentials, Agent Instances, or retained Agent data.

Create the control-plane namespace before installation, then use values which
explicitly confirm the separately installed Istio prerequisites:

```bash
helm dependency build app/cetirun-platform
helm upgrade --install cetirun-platform app/cetirun-platform \
  --namespace cetirun-system --create-namespace \
  --set istio.enabled=true \
  --set cetirun-portal.istio.host=cetirun.example.com \
  --set cetirun-portal.istio.gateway=aks-istio-ingress/cetirun-gateway \
  --set cetirun-portal.istio.gatewayNamespace=aks-istio-ingress
```

`cetirun-portal.istio.gateway`, `gatewayNamespace`, and
`gatewayServiceAccount` must identify the same ingress gateway. The deployment
helper configures all three from `ISTIO_GATEWAY_NAME`,
`ISTIO_GATEWAY_NAMESPACE`, and `ISTIO_GATEWAY_SERVICE_ACCOUNT`.

Each component chart remains independently installable. Moving existing
standalone releases into this umbrella requires a separately planned handover;
the chart never adopts resources owned by another release.
