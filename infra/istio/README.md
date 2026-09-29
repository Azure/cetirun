# CetiRun Istio authentication

Read [Istio authentication and egress](../../site/content/en/docs/operations/istio.md)
for identity, DNS/TLS, helper inputs, external authorization, ownership checks,
webhook exceptions, egress configuration, and removal caveats.

`values.yaml` configures the shared chart. `templates/ingress/` holds gateway
and authentication resources; `templates/egress/` holds mesh-wide destination
and logging controls. The operator manages per-instance VirtualServices.

`deploy.sh` mutates both Helm resources and the AKS Istio shared ConfigMap.
A manual Helm install requires separate external authorization configuration.
Neither the chart nor helper issues or renews TLS certificates.

Install the deployment helper's pinned Python dependency before running it:

```bash
python3 -m pip install --requirement infra/istio/requirements.txt
```

## Portal ownership endpoint

For the platform umbrella, set `PORTAL_AUTH_NAMESPACE=cetirun-system` before
running `deploy.sh`. The standalone helper default is `cetirun-portal`.
`PORTAL_AUTH_SERVICE` and `PORTAL_AUTH_CLUSTER_HOST` support custom service
locations. See the
[ownership endpoint configuration](../../site/content/en/docs/operations/istio.md#portal-ownership-endpoint).
