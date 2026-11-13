---
title: "Public DNS and TLS"
description: "Use the platform deployment to prepare Azure Public IP DNS or custom DNS and cert-manager HTTP-01 TLS."
weight: 25
---

Use the optional `publicTLS` section of the normal CetiRun environment file for
one public hostname with a certificate that renews automatically. This section
works with the ingress of AKS managed Istio. The platform deployment configures DNS, checks or installs
the separately managed cert-manager controller, and renders the HTTP-01
resources from the `cetirun-platform` umbrella chart. There is no separate
public-ingress chart or release.

This path supports one exact hostname and HTTP-01 only. It does not create
wildcard names or certificates, DNS-01 records, a CDN/proxy configuration,
Entra applications, Agent Identity Broker/MCP trust, admission certificates,
models, or runtimes. The CetiRun Istio chart still owns the HTTPS port-443
Gateway and authentication. The umbrella chart adds the separate port-80
Gateway API listener, redirect, ACME Issuers, and Certificates.

## Prerequisites

- An AKS cluster with managed Istio, Gateway API, and an external ingress
  Service exposing TCP ports 80 and 443.
- A target kubeconfig, context, cluster API identity, and `kube-system`
  namespace UID. `target.kubeconfig` is mandatory when `publicTLS` is enabled.
- Permission to create the requested namespace resources and, for Azure DNS
  label mode, permission to read and update the exact ingress Public IP.
- Helm, kubectl, OpenSSL, Python 3 with PyYAML, and Azure CLI for Azure DNS
  mode. Install mode also needs the pinned cert-manager archive prepared
  separately; deployment never downloads a floating chart.
- Helm's default Secret storage driver. With `publicTLS`, `make deploy`
  refuses every other `HELM_DRIVER` value, in all `certManager` modes.
- An Entra Web application callback for the final hostname:
  `https://<domain>/oauth2/callback`. Configure it after Azure reports the
  actual FQDN and before public Portal sign-in.
- Let's Encrypt as the certificate authority. The Issuers use the staging and
  the production ACME directories of Let's Encrypt, and cert-manager registers
  an ACME account for each of them. Before you use this mode, review the
  subscriber agreement and the rate limits of Let's Encrypt. Repeated tests
  against the production directory can reach these limits. If the domain has
  CAA records, they must allow `letsencrypt.org`. `acme.email` is the contact
  address of the ACME accounts.
- HTTPS access from the workstation to the public DNS resolvers
  `https://dns.google/resolve` and `https://cloudflare-dns.com/dns-query`. The
  deployment asks both resolvers, not the DNS of your network. Both must
  return only the IPv4 address of the ingress, and no AAAA record.
- For `make deploy-verify`, HTTP and HTTPS access from the workstation to the
  public hostname.
- Pull access to the cert-manager images in `quay.io`. The deployment accepts
  no other registry for them. Refer to
  [Private registries and restricted networks](../artifacts/#private-registries-and-restricted-networks).

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

If the first installation of cert-manager fails, correct the cause. Then run
`make deploy` again with the same archive and configuration. The deployment
finds its own failed attempt in its receipt in `stateDirectory`, and it
installs the controller again with `helm install --replace`. It does this only
if the resources that the failed release left and the stored Helm records
match that attempt.

If the run stops with a message that contains `recover it separately` or
`before retrying`, the deployment cannot confirm the earlier attempt.
Examine the release and the receipt before you run it again:

```bash
helm status cert-manager -n cert-manager
helm history cert-manager -n cert-manager
```

Do not delete the release or the receipt to force a retry. A failed
cert-manager release that this deployment did not install needs a separate
recovery.

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

The deployment keeps a private receipt in `stateDirectory`. The receipt
records the target, the ingress Service, the Public IP, and the controller
archive. Use the same `stateDirectory` for all runs against one target, keep it
private, and do not edit it.

If a run stops during the certificate-only installation, run `make deploy`
again. The deployment continues only if the latest Helm revision of the
platform is exactly the certificate-only resource set. It never upgrades a
full platform revision with certificate-only values. These changes need a
separately reviewed migration, and the deployment refuses them:

- `publicTLS` on for a full platform release that exists
- `publicTLS` off after it was on
- A change of the hostname or of the solver identity

Run only one deployment at a time for a target. A lock file in
`stateDirectory` stops a second run that uses the same directory, with the
message `Another ingress preparation is using this stateDirectory`. The lock
does not stop a run from another workstation. A competing full deployment
causes a render refusal or a Helm revision conflict before it removes
application resources. The deployment examines the Public IP before and after
it writes the DNS label, but it does not lock out other Azure writers. If a
write has an uncertain result, examine the receipt, the Public IP, and the Helm
releases before you run the deployment again.

After the Portal is installed and the Entra callback is registered, verify the
public endpoint without writing resources:

```bash
make deploy-verify DEPLOY_CONFIG=/absolute/path/environment.yaml
```

It checks these items:

- Public DNS
- The readiness of the ACME Gateway, the HTTPRoute, and the Issuers
- The renewal of the certificate, and the certificate that the ingress serves
- The redirect from HTTP to HTTPS
- The exact OAuth callback with S256 PKCE, and the redirect to Microsoft
  sign-in at `login.microsoftonline.com`
- The response of the Portal API without authentication, and the health and
  readiness of the Portal

It does not perform an interactive sign-in, MFA, a model request, an Agent run,
or an internal mTLS test.

## Existing certificates and retirement

When `publicTLS` is absent, CetiRun keeps the existing external-certificate
path. Supply `cetirun-tls` out of band and keep `istio.tlsMode: external`; an
existing Istio chart-owned certificate uses the documented `retain` mode.
There is no automatic ownership migration.

### Migrate an existing external-certificate deployment

This migration has no command. Plan it as a separately reviewed change, and
test it in a separate environment first. Do not run the certificate-only
bootstrap against an installed full platform, and do not remove the ownership
checks to make it pass. A hostname change requires a short interruption and a
new browser sign-in.

Review the complete values and manifest from the same installed Helm revision
before following this procedure. Offline rendering alone cannot prove a
TLS-only change. The bundled database and AI Gateway Secrets use server-side
`lookup`, and they generate replacement values when lookup is unavailable.
CetiAdmin also has lookup-dependent resources and lifecycle hooks. Do not ignore Secret or
non-TLS differences to obtain a clean comparison, and do not bypass the hooks.
The migration must preserve those retained values and satisfy the normal
lifecycle checks before either full-release upgrade.

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
separately authorized migration:

1. Move or stop Portal traffic.
2. Make sure that nothing else uses the DNS record or the Public IP label. Then
   remove them.
3. Explicitly remove the managed Certificate, Issuer, ACME account Secret, and
   TLS Secret.

Do not remove
the shared ingress Service, Public IP, or cert-manager controller as an
implicit cleanup action.
