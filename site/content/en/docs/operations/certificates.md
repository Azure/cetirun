---
title: "Certificates and credentials"
description: "The certificates, keys, and passwords of a CetiRun installation, their source, how to renew them, and what make deploy checks."
weight: 53
---

A CetiRun installation uses certificates, keys, and passwords that you supply,
and some that the charts generate. CetiRun does not renew the certificates
that you supply. Record the expiry date of each certificate, and renew it
before it expires. `make deploy` examines the certificates before it writes,
so an expired certificate also stops the next run.

The names on this page are the default names of an installation with
`make deploy`, in the default namespaces. The Broker namespace is the
`broker.namespace` of the environment file.

## TLS certificates

| Certificate | Source | How to renew | `make deploy` checks |
| --- | --- | --- | --- |
| `cetirun-tls` in `aks-istio-ingress`, the certificate of the ingress gateway | You. With `publicTLS`, cert-manager | [Replace the ingress certificate](#replace-the-ingress-certificate). With `publicTLS`, cert-manager renews it | Valid now, and covers `domain` |
| `cetirun-sandbox-admission-tls` in `cetirun-system`, and `sandboxAdmission.caBundle` | You | Trust the old and the new CA together first, then replace the certificate. Refer to [Availability and bootstrap]({{< repo "app/cetirun-operator/README.md" >}}#availability-and-bootstrap) | The chain against `caBundle`, and the host name of the admission Service |
| `connections-mcp-tls` and `connections-mcp-ca` in `cetirun-system` | You | [Rotation, shutdown and recovery](../agent-identity/#rotation-shutdown-and-recovery) | The chain against the MCP CA, and the host name of the MCP Service |
| `broker-tls` in the Broker namespace | You | Same | The chain, and the host name of the Broker Service |
| `broker-client-tls` in `cetirun-system` | You | Same | The chain, the client purpose, and the URI of the Connections MCP service account |
| `cetirun-adx-mon-ingestor-tls` in `cetirun-system`, with the ADX-Mon collection | The observability chart. Valid for 3650 days | No action | The Secret must stay |

The admission server, the Broker, and Connections MCP read a replaced
certificate from their mounted Secret without a restart. The clients of a new
CA must trust it before you replace a leaf certificate.

`make deploy` checks the expiry of the ingress certificate. For the other
certificates, it runs `openssl verify`, which also refuses an expired
certificate. The error does not name the certificate. This loop reads only the
public part of each certificate. It shows the subject and the expiry, and it
reports a certificate that expires within 30 days. Replace `<broker-namespace>`
first:

```bash
for s in cetirun-system/cetirun-sandbox-admission-tls cetirun-system/connections-mcp-tls \
  cetirun-system/broker-client-tls aks-istio-ingress/cetirun-tls <broker-namespace>/broker-tls; do
  kubectl get secret "${s#*/}" -n "${s%/*}" -o jsonpath='{.data.tls\.crt}' | base64 -d |
    openssl x509 -noout -subject -enddate -checkend 2592000
done
```

### Replace the ingress certificate

If you supply `cetirun-tls`, replace the Secret before the certificate
expires. `kubectl create` does not change a Secret that exists. Make the new
Secret with a client-side dry run, and replace the existing one with it:

```bash
kubectl create secret tls cetirun-tls --namespace aks-istio-ingress \
  --cert=/absolute/private/ingress-tls/tls.crt \
  --key=/absolute/private/ingress-tls/tls.key \
  --dry-run=client -o yaml | kubectl replace -f -
```

Then make sure that the ingress serves the new certificate:

```bash
openssl s_client -connect example.com:443 -servername example.com </dev/null 2>/dev/null |
  openssl x509 -noout -subject -enddate
```

With `publicTLS`, do not replace `cetirun-tls`. cert-manager owns it.

## Keys and passwords

| Credential | Source | How to renew | `make deploy` checks |
| --- | --- | --- | --- |
| AI Gateway master key and console password: `master-key` and `ui-password` in `cetirun-ai-gateway-auth` | You | Change the value, then restart the AI Gateway. Refer to [Changing a credential takes a restart]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#changing-a-credential-takes-a-restart) | The keys exist |
| AI Gateway salt key: `salt-key` in `cetirun-ai-gateway-auth` | You | Do not change it. Refer to [The salt key must never change]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#the-salt-key-must-never-change) | The key exists |
| AI Gateway Valkey password: `gateway-valkey-auth` | You | Change it, then restart Valkey and the AI Gateway | The key exists |
| AI Gateway database password: `cetirun-ai-gateway-postgresql` | The AI Gateway chart | Change the password in the database, update the Secret, and restart the AI Gateway. Refer to [Credentials]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#credentials) | The Secret exists while the database volume exists |
| Model provider keys: `gateway-provider-keys` | You | Replace the key, then restart the AI Gateway. Keep the old key valid until the restart ends | Each key that a model names |
| OAuth2 Proxy cookie key: `oauth2-proxy-credentials` in `cetirun-istio` | The Istio chart, at the first installation | `make deploy` has no rotation procedure. A new key signs out all users | The Secret exists on an upgrade |
| Session store password: `oauth2-proxy-sessions` in `cetirun-istio` | The Istio chart | Replace the Secret, then run `make deploy`. Refer to [OAuth session storage](../istio/#oauth-session-storage) | The Secret exists |
| Connection-record key: the Key Vault key of `identity.keyVaultKeyUrl` | You | Add a new key version, and keep the old versions enabled. Refer to [Create the encryption foundation](../agent-identity/#2-create-the-encryption-foundation) | Not checked |

A restart of the AI Gateway replaces its Pods. To restart it, run
`kubectl rollout restart deployment/cetirun-ai-gateway -n cetirun-system`. This
command writes to the cluster.

## Provider and integration credentials

| Credential | Source | How to renew | `make deploy` checks |
| --- | --- | --- | --- |
| Operator registry credential: the Secret of `acrDockerSecret`, for example `acr-helm-pull` | You. The ACR token of the example expires after 90 days | Generate a new password and replace the Secret. Refer to [Example: Azure Container Registry](../artifacts/#example-azure-container-registry) | The Secret exists |
| GitHub App: the Secret of the App in the Broker namespace, for example `github-app` | You | [Register the platform App](../../guides/github-app/#register-the-platform-app) | The `client-secret` key |
| GitHub OAuth and Google OAuth clients: their Secrets in the Broker namespace, for example `github-oauth` and `google-oauth` | You | Replace the Secret, then restart the Broker. Refer to [Rotation, refresh, and removal](../identity-providers/#rotation-refresh-and-removal) | The client ID and the client secret |
| Skill Marketplace repository: the Secret of `skillMarketplace.repository.existingSecret` in `cetirun-system`, for example `skills-repository` | You | Replace the Secret. The worker reads it again at each check. Refer to [The repository and its credentials](../skill-marketplace/#the-repository-and-its-credentials) | The keys of the selected `auth` |
| CetiAdmin Slack tokens: the Secret of `monitor.slackSecretName` | You | Replace the Secret | The key names |
| `publicTLS` ACME accounts and certificates in `aks-istio-ingress` | cert-manager | cert-manager renews them. `make deploy-verify` examines the renewal | `make deploy` waits until both certificates are ready |

The Portal sign-in application, the Agent Identity Blueprint, the
Authorization application, and the encryption identity use federated
credentials, not client secrets. So do the managed identities
`<clusterName>-adx-ingestor` and `<clusterName>-gateway-content` of the cluster
template. They have no secret to renew. Their federated credentials name the
OIDC issuer of the cluster, so a new cluster needs new federated credentials.
