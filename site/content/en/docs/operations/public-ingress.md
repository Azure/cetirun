---
title: "Public DNS and TLS"
description: "Prepare one public CetiRun hostname with Azure Public IP DNS or custom DNS and cert-manager HTTP-01."
weight: 25
---

Use this optional path when an existing AKS managed Istio ingress needs one
public CetiRun hostname and an automatically renewed public certificate. It
prepares DNS, cert-manager and the port-80 ACME path before the normal platform
deployment. It does not create AKS, Entra applications, Agent Identity, Broker
or MCP trust, admission certificates, models, or runtimes.

The supported hostname is exact: no wildcard name, wildcard certificate,
DNS-01, CDN/proxy configuration, or certificate migration is included. The
separate [Istio chart](../istio/) still owns HTTPS on port 443 and
authentication. The public-ingress chart owns a port-80 Gateway API listener,
HTTP redirect, ACME Issuers and Certificates only.

## Prerequisites

- An AKS cluster with managed Istio and Gateway API enabled, and its external
  ingress Service already exposing ports 80 and 443.
- A target kubeconfig/context, cluster API identity and `kube-system` namespace
  UID. The helper checks these before it writes.
- Permission to create the requested namespace resources and, for Azure DNS
  label mode, permission to read and update the exact ingress Public IP.
- Helm, kubectl, OpenSSL, Python 3 with PyYAML, and Azure CLI for Azure DNS
  mode. Install mode also needs the repository's pinned cert-manager chart
  archive. The install mode does not download a chart or use a floating version.
- An Entra Web application callback matching the final hostname:
  `https://<domain>/oauth2/callback`. Configure the app after the helper reports
  the resolved hostname and before public Portal sign-in.

The regular platform requirements remain in effect. In particular,
[Agent Identity setup](../agent-identity/), Broker/MCP trust, admission
certificates, provider registrations and qualified runtimes are separate from
this public certificate.

## Configuration

Keep the file outside the repository. The synthetic examples in
[infra/public-ingress/examples]({{< repo "infra/public-ingress/examples" "tree" >}})
show the complete shape.

```yaml
target:
  kubeconfig: /absolute/private/kubeconfig
  context: example-context
  cluster: example-cluster
  server: https://kubernetes.example.invalid:6443
  clusterUID: example-cluster-uid
ingress:
  namespace: aks-istio-ingress
  service: aks-istio-ingressgateway-external
  release: cetirun-public-ingress
  gatewayName: cetirun-acme-http
dns:
  mode: custom
  domain: portal.example.com
certManager:
  mode: install
  namespace: cert-manager
  release: cert-manager
  chartArchive: /absolute/private/cert-manager-v1.21.3.tgz
acme:
  email: operations@example.com
stateDirectory: /absolute/private/cetirun-public-ingress
timeoutSeconds: 600
```

`dns.mode: azure-public-ip` requires `dns.label` and an `azure` mapping with
the explicit subscription and tenant IDs plus the AKS resource group and
cluster name. The helper finds the ingress Public IP from the named Service,
checks that it belongs to that AKS node resource group, and uses Azure's
returned FQDN as the final domain. It never uses the Azure CLI default
subscription or changes it. The supported Azure CLI update preserves the other
Public IP settings; the helper compares the resource ETag before writing and
checks the resulting IP and label. These observations do not lock the Azure
resource. Avoid concurrent Public IP edits or preparations from other state
directories during this operation.

`dns.mode: custom` requires `dns.domain`. Before a production Certificate is
requested, the helper requires its A or CNAME resolution to reach the ingress
Public IP, with exactly that IPv4 address and no AAAA answer from either of
the two public DNS resolvers. Configure that record yourself. The helper does
not create or alter your DNS zone.

`certManager.mode: install` installs the pinned cert-manager release when
the configured release is absent, or resumes its own recorded failed install.
It requires the local pinned chart archive and verifies its checksum and chart
version before use. `existing` requires no chart archive: it checks that the named deployed release has the supported
pinned version and controller image digests, has Gateway API support, and does
not upgrade it.

For install mode, obtain the pinned public chart as a separate networked setup
step. The helper checks it against
[`images.lock.json`]({{< repo "infra/public-ingress/images.lock.json" >}}) before
rendering or installation:

```bash
helm pull oci://quay.io/jetstack/charts/cert-manager \
  --version v1.21.3 --destination /absolute/private
```

## Prepare the endpoint

Run the offline render first:

```bash
make ingress-plan INGRESS_CONFIG=/absolute/path/public-ingress.yaml
```

This command does not read a cluster, Azure, DNS, a certificate or a state
directory. It validates the configuration and renders the chart with synthetic
values only.

After authorization for the exact target, prepare it:

```bash
make ingress-prepare INGRESS_CONFIG=/absolute/path/public-ingress.yaml
```

The helper records sanitized receipts and generated files under `stateDirectory`
with restrictive permissions. It locks that directory against concurrent helper
writers and records the chart version, source checksum and install archive
checksum. A run uses private chart snapshots, so changes to the original
archive during DNS or ACME waits do not change the artifact being installed.
It first confirms the exact ingress Service and its public IP. The ACME Gateway references the Service by hostname in the same
namespace; it does not create a second LoadBalancer. It installs or checks
cert-manager, creates the staging Issuer and Certificate, waits for staging
success, then creates the production Certificate. The final Certificate writes
`cetirun-tls` in the ingress namespace. cert-manager owns the private key and
renews it.

The helper refuses a hostname or certificate already owned by another release,
a replaced ingress Service, a DNS mismatch, an unready Gateway, or an
unresolved Certificate. It does not rename DNS labels, adopt unrelated
resources, replace an existing certificate, migrate a POC, or delete resources
after a failed run. Inspect the saved receipt and current resource identities
before a recovery attempt. Re-running the same configuration preserves an
existing production Certificate. A failed cert-manager install can be resumed
only if its attempt is in this private receipt, its artifact checksum matches,
and all surviving resources have the expected ownership. A Helm release with
a pending operation stops preparation for separate inspection.

If an Azure DNS update loses its response, a repeat accepts the already-assigned
matching label and Azure-returned FQDN. It refuses to replay an earlier DNS
write if that label cannot be observed. Retain the receipt while investigating;
do not clear it to bypass an unresolved operation. Existing ACME account or TLS
Secrets without the matching managed Issuer or Certificate also stop the run.

The generated `deployment-values.yaml` contains only the values that the
platform deployment needs:

```yaml
domain: portal.example.com
istio:
  tlsMode: external
  values:
    tls:
      enabled: false
    gateway:
      includeWildcardHost: false
```

Merge it with the private environment file for `make deploy`; do not replace
the rest of that file. Use the same target context and cluster identity, set
`istio.ingressNamespace` to this helper's `ingress.namespace`, and pass
`KUBECONFIG` matching `target.kubeconfig` when running `make deploy`. The
platform command remains independent of Azure DNS and cert-manager. It verifies
the already-issued certificate before it changes the Istio, Broker and platform
releases.

## Verify after platform deployment

After `make deploy` and the Entra callback configuration, run:

```bash
make ingress-verify INGRESS_CONFIG=/absolute/path/public-ingress.yaml
```

The read-only verification checks public DNS, current Gateway and HTTPRoute
acceptance, hostname and certificate-chain validation, HTTP-to-HTTPS redirection, the exact OAuth callback and S256 PKCE
in the Portal sign-in redirect, an unauthenticated Portal API 401, and Portal
health/readiness. It also checks current Issuer readiness and the production
Certificate's renewal schedule; a passed check is a point-in-time observation.
It does not complete an interactive browser sign-in, MFA, a model request, an Agent run, or an internal mTLS test.

## Ownership and renewal

Keep the normal Istio chart at `tls.enabled: false` and platform
`istio.tlsMode: external`. In that mode CetiRun reads the `cetirun-tls` Secret
but does not create, update or delete it. Do not switch an existing chart-owned
certificate to this mode in the same change; certificate ownership migration
needs its own reviewed procedure.

cert-manager renews the Certificate. Check Certificate readiness, the served
certificate hostname and expiry before every deployment and after a renewal.
The helper and chart do not prove availability between observations.

This feature was validated in the public DNS/TLS POC for Azure Public IP DNS
and HTTP-01. The supported helper and custom-domain path still require their
own authorized live acceptance before they can be described as production
qualified.

## Decommissioning

Keep the port-80 Gateway and cert-manager running for as long as the hostname
uses this certificate. Uninstalling only `cetirun-public-ingress` removes the
HTTP-01 listener but deliberately retains the Issuers and Certificates through
Helm's `keep` policy. Existing TLS can continue until expiry while renewal fails.

There is no automatic cleanup command. After separately authorizing retirement,
inspect the target and receipt, move or stop the Portal traffic, then remove the
old DNS A/CNAME record or Azure Public IP label after checking that nothing else
uses it. Remove the two managed Certificates and Issuers to stop issuance, then
uninstall the ingress release and explicitly remove its unused TLS and ACME
account Secrets. Do not delete the shared ingress Service, Public IP, or
cert-manager release as part of this cleanup. Retain the private receipt until
resource ownership and removal have been verified.
