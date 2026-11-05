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

### Migrate an existing external-certificate deployment

Use a reviewed migration procedure for an installed full platform. Do not run
the certificate-only bootstrap against that release or remove the ownership
checks to make it pass. A hostname change requires a short interruption and a
new browser sign-in.

1. Record the current full Helm revisions, rendered resources, ingress Service
   UID and address, and TLS Secret UID/resource version. Save the certificate
   and private key in restricted storage outside the repository. Preserve Agent
   owners, HOME claims, identity realm, internal trust, and application images.
2. Verify Gateway API v1 and an accepted `istio` GatewayClass. For an AKS
   cluster without the CRDs, the supported `az aks update --enable-gateway-api`
   option enables their managed installation. Supply the target subscription,
   resource group, and cluster explicitly. Keep the existing Istio service mesh;
   the ingress-only App Routing Istio option is a different configuration.
3. Assign the DNS label to the existing Public IP, use Azure's returned FQDN,
   and verify public DNS. Install the pinned cert-manager controller. Create
   only the reviewed ACME resources for staging, with the intended platform
   Helm ownership, and record their returned UIDs. Wait for staging issuance.
4. Upgrade the **full** platform with the new `publicTLS.domain`, both
   Certificates enabled, and `bootstrapOnly: false`. Initially set
   `publicTLS.acme.productionSecretName` to a separate, unused Secret such as
   `cetirun-tls-next`. Keep the old application hostname and certificate in
   service. Compare the complete render: the TLS additions must not remove or
   change unrelated application resources, CRDs, Secrets, or hooks. Verify the
   new production certificate's chain, hostname, validity, and renewal schedule.
5. Add the new callbacks to the existing Entra applications. During the
   maintenance window, conditionally update the original `cetirun-tls` using
   its recorded UID and resource version. Transfer the verified certificate
   material and the relevant ownership metadata from the actual cert-manager
   output Secret. Preserve the canonical Secret UID; do not invent Certificate
   identities or add Helm ownership to its output Secret. Upgrade the full
   platform to direct the production Certificate to `cetirun-tls`, then update
   all application, ingress, callback, and runtime origin settings.
6. Wait for the current Certificate generation and workloads to become Ready.
   Verify that the Certificate is the only writer of the canonical Secret,
   and that normal publicTLS ownership checks accept the complete release.
   Resume the standard deployment and verification workflow. Let it create its
   own state receipts; do not fabricate a completed preparation record.

The umbrella TLS templates and retained component charts can have different
source revisions during a focused migration. Record both sources and artifact
hashes, and require an exact render comparison. Do not introduce unrelated
controller or API changes merely to add certificate management.

After an interrupted step, inspect the actual release revision, object UIDs,
and certificate target before retrying. For rollback after cert-manager starts
writing `cetirun-tls`, first use a full-release upgrade to move that Certificate
back to the temporary Secret. Wait for the new generation to become Ready and
confirm no Certificate still targets the canonical Secret. Then conditionally
restore the old certificate metadata/material and application hostname. A
Helm rollback alone does not restore certificate contents or external callbacks.

Keep the ACME Gateway, HTTPRoute, Issuers, Certificates, and cert-manager
controller while the hostname needs renewal. A later retirement needs a
separately authorized migration: move or stop Portal traffic, remove the DNS
record or Public IP label after checking consumers, and explicitly remove the
managed Certificate, Issuer, ACME account Secret, and TLS Secret. Do not remove
the shared ingress Service, Public IP, or cert-manager controller as an
implicit cleanup action.
