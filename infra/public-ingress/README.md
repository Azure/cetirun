# CetiRun public ingress

This chart and its helper prepare one public HTTPS hostname for a CetiRun
deployment that already has AKS managed Istio and Gateway API. They assign an
Azure Public IP DNS label or verify a user-managed DNS record, install or
verify the pinned cert-manager controller, and issue a Let's Encrypt HTTP-01
certificate. The resulting `cetirun-tls` Secret remains owned by cert-manager.

Read the [public ingress operations guide](../../site/content/en/docs/operations/public-ingress.md)
before using this helper. It describes prerequisites, Azure and custom-domain
modes, ownership, failure recovery, and the complete configuration schema.

The helper has three entry points:

```bash
make ingress-plan INGRESS_CONFIG=/absolute/path/public-ingress.yaml
make ingress-prepare INGRESS_CONFIG=/absolute/path/public-ingress.yaml
make ingress-verify INGRESS_CONFIG=/absolute/path/public-ingress.yaml
```

`ingress-plan` is offline: it validates the input and renders the charts
without connecting to Azure or Kubernetes. `ingress-prepare` is a connected,
mutating operation and stores receipts only in the configured private state
directory. `ingress-verify` reads the finished public endpoint after the
normal platform installation.

The chart owns only the port-80 Gateway API listener, redirect, ACME Issuers
and Certificates. It binds that listener to the existing ingress Service in
the **same namespace**, so it does not provision another LoadBalancer. The
separate `cetirun-istio` release owns the port-443 Gateway and authentication
resources. Do not use this chart to take over an existing certificate or an
unmanaged Gateway.

This feature supports one exact hostname and HTTP-01 only. It does not create
wildcard DNS, wildcard certificates, DNS-01 records, a CDN configuration, or
internal Broker, MCP, or admission certificates.
