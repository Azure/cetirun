---
title: "Public DNS and TLS"
description: "Use the platform deployment to prepare Azure Public IP DNS or custom DNS and cert-manager HTTP-01 TLS."
weight: 25
---

Use the optional `publicTLS` section of the normal CetiRun environment file
when an AKS managed Istio ingress needs one public hostname and an automatically
renewed certificate. The platform deployment configures DNS, checks or installs
the separately managed cert-manager controller, and renders the HTTP-01
resources from the `cetirun-platform` umbrella chart. There is no separate
public-ingress chart or release.

This path supports one exact hostname and HTTP-01 only. It does not create
wildcard names or certificates, DNS-01 records, a CDN/proxy configuration,
Entra applications, Broker/MCP trust, admission certificates, models, or
runtimes. The CetiRun Istio chart still owns the HTTPS port-443 Gateway and
authentication; the umbrella chart adds the separate port-80 Gateway API
listener, redirect, ACME Issuers, and Certificates.

## Prerequisites

- An AKS cluster with managed Istio, Gateway API, and an external ingress
  Service exposing TCP ports 80 and 443.
- A target kubeconfig, context, cluster API identity, and `kube-system`
  namespace UID. `target.kubeconfig` is mandatory when `publicTLS` is enabled.
- Permission to create the requested namespace resources and, for Azure DNS
  label mode, permission to read and update the exact ingress Public IP.
- Helm, kubectl, OpenSSL, Python 3 with PyYAML, and Azure CLI for Azure DNS
  mode. Install mode also needs the pinned cert-manager archive prepared
  separately; deployment never downloads a floating chart. This mode uses
  Helm's default Secret storage driver and rejects other `HELM_DRIVER` values.
- An Entra Web application callback for the final hostname:
  `https://<domain>/oauth2/callback`. Configure it after Azure reports the
  actual FQDN and before public Portal sign-in.

The normal platform requirements remain in effect. In particular, Agent
Identity, Broker/MCP trust, admission certificates, provider registrations, and
qualified runtimes are separate prerequisites.

## Configuration

Keep the environment file outside the repository. Add this to the existing
platform configuration; the synthetic examples are in
[infra/cert-manager/examples]({{< repo "infra/cert-manager/examples" "tree" >}}).

```yaml
target:
  kubeconfig: /absolute/private/kubeconfig
  context: example-context
  cluster: example-cluster
  server: https://kubernetes.example.invalid:6443
  clusterUID: example-cluster-uid
domain: portal.example.com
istio:
  ingressNamespace: aks-istio-ingress
platform:
  namespace: cetirun-system
  release: cetirun-platform
publicTLS:
  enabled: true
  dns:
    mode: custom
    domain: portal.example.com
  ingressService: aks-istio-ingressgateway-external
  gatewayName: cetirun-acme-http
  certManager:
    mode: install
    namespace: cert-manager
    release: cert-manager
    chartArchive: /absolute/private/cert-manager-v1.21.3.tgz
  acme:
    email: operations@example.com
  stateDirectory: /absolute/private/cetirun-public-tls
  timeoutSeconds: 600
```

For `dns.mode: azure-public-ip`, replace `dns.domain` with `dns.label` and add
the `azure` mapping containing `subscriptionId`, `tenantId`, `resourceGroup`,
and `clusterName`. Azure returns the canonical FQDN after the label assignment.
The top-level `domain` may be omitted for this mode, or supplied only when it
matches that returned FQDN. An offline plan uses a clearly synthetic placeholder
until Azure has returned the actual hostname.

For `dns.mode: custom`, set `dns.domain`; the top-level `domain` is optional
and, when supplied, must match it. Create the A or CNAME record yourself before
deployment. The deployment accepts only an exact IPv4 answer for the ingress
address and no AAAA answer, so that the HTTP-01 challenge has one reachable
target.

`certManager.mode: install` verifies the archive checksum and chart version
before it installs the pinned controller. `existing` requires the named ready
release at the supported pinned version with Gateway API support; it does not
upgrade that release. Obtain the public archive as a separate setup step:

```bash
helm pull oci://quay.io/jetstack/charts/cert-manager \
  --version v1.21.3 --destination /absolute/private
```

The expected archive and image digests are recorded in
[`infra/cert-manager/images.lock.json`]({{< repo "infra/cert-manager/images.lock.json" >}}).

## Deploy and verify

Render the full configuration offline first:

```bash
make deploy-plan DEPLOY_CONFIG=/absolute/path/environment.yaml
```

The plan validates the configuration and renders normal and certificate-only
platform values locally. It does not contact Azure, Kubernetes, DNS, a state
directory, or a certificate authority.

After authorization for the exact target, run:

```bash
make deploy DEPLOY_CONFIG=/absolute/path/environment.yaml
```

For a new deployment, the command validates the normal platform prerequisites,
assigns or checks DNS, checks or installs cert-manager, and creates a
certificate-only `cetirun-platform` revision. That revision contains only the
ACME Gateway, HTTPRoute, two Issuers, and staging/production Certificates. It
waits for both certificates, then reruns the full preflight before installing
Istio, Broker, and the complete platform. The production certificate writes
`cetirun-tls` in `istio.ingressNamespace`; cert-manager owns its private key
and renews it.

The deployment holds a private receipt in `stateDirectory` for the target,
service, Public IP, and controller archive. It can recover an interrupted
certificate-only installation only after it recognizes the latest Helm revision
as exactly that restricted resource set. A deployed or failed full platform
revision is always treated as full: it is never upgraded with certificate-only
values. Enabling this mode on an existing full release, disabling it after use,
or changing its hostname or solver identity requires a separately reviewed
migration; the deployment refuses all of those changes.

Use one deployment writer per target. The `stateDirectory` lock covers local
preparation through the full deployment. Certificate-only Helm values also pin
the expected next revision: a competing full deployment causes a render refusal
or a revision storage conflict before any application resources can be removed.
Azure DNS writes check Public IP
identity and drift before and after the update, but do not provide a distributed
lock against other Azure writers. If a write has an uncertain result, inspect
the private receipt and actual Azure/Helm state before retrying.

After the Portal is installed and the Entra callback is registered, verify the
public endpoint without writing resources:

```bash
make deploy-verify DEPLOY_CONFIG=/absolute/path/environment.yaml
```

It checks public DNS, Gateway/HTTPRoute and Issuer readiness, certificate
renewal and the served certificate, HTTP-to-HTTPS redirection, the exact OAuth
callback with S256 PKCE, an unauthenticated Portal API response, and Portal
health/readiness. It does not perform an interactive sign-in, MFA, a model
request, an Agent run, or an internal mTLS test.

## Existing certificates and retirement

When `publicTLS` is absent, CetiRun keeps the existing external-certificate
path. Supply `cetirun-tls` out of band and keep `istio.tlsMode: external`; an
existing Istio chart-owned certificate uses the documented `retain` mode.
There is no automatic ownership migration.

Keep the ACME Gateway, HTTPRoute, Issuers, Certificates, and cert-manager
controller while the hostname needs renewal. A later retirement needs a
separately authorized migration: move or stop Portal traffic, remove the DNS
record or Public IP label after checking consumers, and explicitly remove the
managed Certificate, Issuer, ACME account Secret, and TLS Secret. Do not remove
the shared ingress Service, Public IP, or cert-manager controller as an
implicit cleanup action.
