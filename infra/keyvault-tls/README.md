# CetiRun Key Vault TLS synchronizer

Copyright (c) Microsoft Corporation. Licensed under the MIT License.

This chart keeps an external Azure Key Vault certificate available as the
`kubernetes.io/tls` Secret `cetirun-tls` in `aks-istio-ingress`. Istio owns the
Gateway and reads that Secret through SDS; this chart does not modify the
managed Istio Deployment or create a certificate itself.

The certificate must first be issued through an approved CA integration for the
target tenant, cloud, SAN (for example `portal.example.com`) and subscription. Its
policy must permit an exportable software key and a backing Key Vault secret
that contains the certificate chain and private key. The synchronizer identity
only needs the approved Key Vault secret read role (normally Key Vault Secrets
User), never certificate issuance or administration permissions.

The CSI SecretProviderClass follows the latest Key Vault version (`objectVersion:
""`) and syncs one PEM bundle alias into both TLS Secret data keys. The two replicas are
long-lived consumers because the synced Kubernetes Secret is removed when the
last CSI consumer disappears. The PodDisruptionBudget and required hostname
anti-affinity preserve one consumer during voluntary disruption.

For `kubernetes.io/tls` Secrets, the CSI driver's
[`GetSecretData` / `GetCertPart` implementation](https://github.com/kubernetes-sigs/secrets-store-csi-driver/blob/main/pkg/util/secretutil/secret.go)
extracts certificates and the private key from that bundle according to the
destination key. This is not an opaque copy of the entire bundle into both
fields. Verify the installed driver's behavior and the resulting certificate
chain and key match during live qualification.

Install this chart into the managed ingress namespace only after the backing
secret exists and the dedicated managed identity has permission to read it:

```bash
helm upgrade --install cetirun-tls-sync infra/keyvault-tls \
  --namespace aks-istio-ingress --values /private/tls-sync-values.yaml
```

Pass the environment's private kubeconfig to Helm. Keep the tenant and identity
IDs outside the repository. The chart assumes the AKS Key Vault CSI add-on and
secret rotation are enabled, and that two system-pool nodes are schedulable.
Its rollout uses `matchLabelKeys: [pod-template-hash]` to spread each revision
across nodes while allowing old and new revisions to overlap on two nodes.
It requires Kubernetes support for that scheduling field.

Set `istio.tlsMode: external`, leave `publicTLS` absent, and keep the Istio
chart's `tls.enabled: false`. Only CSI manages the generated Secret. For a
single-domain installation, set `istio.values.gateway.includeWildcardHost:
false`; otherwise obtain a certificate covering every configured hostname.

This is not a fail-safe certificate store: a malformed but parseable renewed
certificate, deletion of the Secret by another controller, or loss of both
consumer Pods can still make the Gateway unavailable. Alert on Key Vault
renewal failures, CSI rotation errors, missing Secret, consumer availability,
and the publicly served certificate's SAN, chain, version and expiry.
