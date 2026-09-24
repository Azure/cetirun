---
title: "Connect Microsoft Teams"
description: "Configure the operator-managed Teams bot path and diagnose identity or webhook failures."
weight: 20
---

Connect an instance to Microsoft Teams with the Azure bot integration.
Enabling Teams lets the operator create or update an Entra app, federated credential,
service principal, Azure Bot resource, and Teams channel. Both OpenClaw and
Hermes have chart integration, but use different adapters and sender controls.

## Prerequisites and portability

- An existing CetiRun deployment with HTTPS and the shared Istio chart.
- AKS OIDC issuer and Workload Identity.
- An operator identity authorized to manage the selected Azure Bot resource
  group and the necessary Entra application resources.
- Operator `azure.subscriptionId`, `azure.resourceGroup`,
  `azure.botNamePrefix`, and `azure.tenantId` configuration; `azure.clientId`
  configures the operator workload identity.
- Tenant approval for the Bot app and Teams distribution.
- Runtime sender restrictions configured for the intended users; portal
  ownership and `spec.team` do not automatically become a bot allowlist.

The Graph implementation contains tenant-specific application registration and
federated-issuer handling (`azure.serviceTreeId` and `azure.ficIssuerIds`).
Some paths use the Graph beta API. In particular, FIC creation requires a
tenant-to-issuer mapping and uses `trustedIssuer@odata.bind`; without a mapping,
FIC creation fails rather than falling back to a generic API path.
These settings are tenant-specific. Have your identity team review the
[Azure integration source]({{< repo "app/cetirun-operator/internal/azure" "tree" >}})
and [FIC creation]({{< repo "app/cetirun-operator/internal/controller/teams.go" >}})
before enabling automatic provisioning.

## Enable the integration

On a configured deployment, add this block to an instance:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: alice
spec:
  agent:
    type: hermes
  user:
    alias: alice
    displayName: Alice
  gatewayTokenSecretRef: gateway-token
  msteams:
    enabled: true
    webhookPort: 3978
```

Applying this resource starts Teams provisioning through the operator.
Omitting `enabled: true` does not enable
Teams even if other fields are present. The YAML describes instance
configuration, not a complete Azure identity or sender-allowlist setup.
The portal's Teams toggle requests the same provisioning path.

The controller first tries a tracked app object, then a matching display name,
then app creation, and finally the supplied `appId` fallback. Supplying
`spec.msteams.appId` is therefore not a guaranteed override of an already
resolved app. Use a dedicated bot name prefix and do not reuse display names
of unrelated apps: successful provisioning can clean up same-name app objects.
If app creation is
unavailable, arrange a single-tenant app registration with your identity
administrator, record its application/client and tenant IDs, and supply
`spec.msteams.appId` and `spec.msteams.tenantId`.

When the fallback supplies an app without its object ID, the operator cannot
manage its federated credential. An administrator must verify the federated
trust before using the bot.

The expected subject for the example is:

```text
system:serviceaccount:cetirun-alice:openclaw-alice-cetirun
```

Bind it to the actual cluster OIDC issuer with audience
`api://AzureADTokenExchange`. Keep the generated ServiceAccount name unless the
identity and controller contracts have been reviewed together. Configure or
verify an Azure Bot linked to the app and its Microsoft Teams channel, using:

```text
https://<your-domain>/<instance>/api/messages
```

## What gets configured and what Ready means

The operator supplies `msteamsWebhookPort`, `msteamsAuthType: managedIdentity`,
the resolved workload-identity client ID, and a ServiceAccount. The StatefulSet
maps this mode to `MSTEAMS_AUTH_TYPE=federated`. The operator attempts to update
`MSTEAMS_APP_ID` and `MSTEAMS_TENANT_ID` in the instance gateway Secret.
The chart exposes the webhook port and permits ingress from the Istio gateway.
Workload Identity uses projected tokens, not access to Azure IMDS.

`TeamsReady` records the provisioning path, not a successful authenticated
conversation. Service-principal and final Secret-update failures are
best-effort, and the supplied-app fallback can skip FIC management when the app
object ID is unknown. The ready fast path also skips Azure API calls; it is not
continuous verification of an app, endpoint, or channel. Independently verify
identity, route, sender policy, and delivery.

Once Teams status is `Ready` and contains an app ID, the owner can download the
Teams app package from the portal. Installing or distributing that package is
still subject to the tenant's Teams policy. See the
[package handler]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

The CRD fields are `enabled`, `appId`, `tenantId`, and `webhookPort`.
There is no `spec.msteams.authType`, `certificateSecretRef`, or
`certificateThumbprint` field in this API.

### Configure who can message the agent

For OpenClaw, the template sets `channels.msteams.dmPolicy` and `groupPolicy`
to `allowlist`. The initializer fills an absent `allowFrom` with the nil UUID,
not the instance owner's identity. Configure approved sender IDs explicitly in
`openclaw.config.channels.msteams.allowFrom` and review the upstream plugin's
group/channel rules before use. The image installs `@openclaw/msteams`; its
complete implementation is not vendored here.

For Hermes, the bundled `cetirun_teams` adapter reads the union of
`CETIRUN_TEAMS_ALLOWED_USERS` and `GATEWAY_ALLOWED_USERS`. A non-empty list
without `*` restricts sender IDs; an empty list does not impose a sender
restriction in this adapter. Chart `msteamsAllowedUsers` sets
`CETIRUN_TEAMS_ALLOWED_USERS`; it does not configure OpenClaw's `allowFrom`.
Use reviewed deployment values or another approved environment configuration.
The [CRD's Helm-value shape limitation](../../reference/crds/#helm-value-admission)
matters when expressing per-instance scalar or array overrides.

Sources: [OpenClaw template]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}}),
[OpenClaw initializer]({{< repo "app/cetirun-chart/agents/openclaw/bootstrap/init-home.sh" >}}),
[Hermes adapter]({{< repo "app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/adapter.py" >}}),
and [environment mapping]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).

### Certificate and password values are not supported CRD workflows

The chart accepts `msteamsAuthType: certificate`, but its StatefulSet sets
`MSTEAMS_AUTH_TYPE=federated` for that value too. The bundled Hermes adapter
uses `DefaultAzureCredential`, with no certificate-path or thumbprint handling.
Use the operator-managed Workload Identity flow. Certificate and app-password
authentication are not supported operator-managed workflows; setting chart
values or mounting a certificate does not enable them. See the
[Hermes auth code]({{< repo "app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/auth.py" >}}).

## Webhook authentication

The shared Istio policy exempts **POST** requests matching `*/api/messages`
from OAuth2 Proxy. The Bot adapter must authenticate the Bot Framework request.
Other methods still go through OAuth2 Proxy.

Per-instance VirtualServices route enabled bots to the listener; a disabled
instance gets an explicit 404 deny route **while its VirtualService exists**.
A suspended or nonexistent instance has no such route. The shared suffix
exception still applies, so a request can fall through to the portal's SPA
response. That response is not bot readiness. This static exception is not a
per-instance allowlist. Do not add broad anonymous paths to make a failing bot
work.

Sources: [shared authorization policy]({{< repo "infra/istio/templates/ingress/authorization-policy.yaml" >}}),
[instance routes]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
and [portal fallback]({{< repo "app/cetirun-portal/internal/server/server.go" >}}).

## Disable and cleanup

Disabling Teams or deleting the instance attempts to remove the channel, Bot
resource, service principal, and app object recorded in status. Cleanup is
best-effort; disabling also clears Teams status even if cleanup failed.
An app found by display name can have its object ID recorded too, so do not
treat a manually created matching app as protected from cleanup. Inspect the
dedicated resource set before disabling, and verify removal afterward.
Suspending an instance does not perform this cleanup.

## Troubleshoot without exposing credentials

Inspect Teams status and routing without changing cluster resources:

```bash
kubectl get agents.cetirun.microsoft.com alice -o jsonpath='{.status.msteams.state}{"\n"}'
kubectl get agents.cetirun.microsoft.com alice -o jsonpath='{.status.msteams.message}{"\n"}'
kubectl get virtualservice cetirun-alice -n cetirun-alice
kubectl get serviceaccount openclaw-alice-cetirun -n cetirun-alice \
  -o jsonpath='{.metadata.annotations.azure\.workload\.identity/client-id}{"\n"}'
kubectl get pods -n cetirun-alice
```

For a missing token projection, inspect the pod’s volume definitions and
ServiceAccount annotation; do not dump token contents. Restrict log inspection
to the relevant container and time range and redact identities before sharing.

| Symptom | Check |
| --- | --- |
| `PendingAppRegistration` | Operator configuration, Graph permissions, or supplied app fallback |
| FIC failure | Issuer, subject, tenant policy, and app object ownership |
| Bot Service failure | Subscription, resource group, operator identity permissions |
| Login redirect on webhook POST | Shared policy method/path match |
| 404 | Instance Teams enablement and whether its VirtualService exists |
| Ready but no reply | Runtime sender allowlist, Secret update, federated trust, and channel setup |
| Adapter authentication failure | Bot app identity, federated trust, endpoint, and channel setup |

Only after separate authorization for a live endpoint, a probe with no Bot
Framework token should fail authentication, not invoke the agent:

```bash
curl -i -X POST https://example.com/alice/api/messages \
  -H 'Content-Type: application/json' \
  -d '{"type":"message","text":"test"}'
```

The bundled Hermes adapter returns 401 for this request. Validate the selected
OpenClaw plugin separately. Expect 404 for a disabled instance with its
VirtualService present, and OAuth handling for non-POST requests. Never treat
an HTTP success alone as proof of a correctly authenticated channel.

Provisioning and cleanup source:
[Teams controller]({{< repo "app/cetirun-operator/internal/controller/teams.go" >}}).
