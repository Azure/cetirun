---
title: "Set up Microsoft Entra Agent Identity"
description: "Prepare the Entra blueprint, workload federation, and optional delegated access that are necessary for CetiRun Agent Identity."
weight: 35
---

This guide prepares the Microsoft Entra resources that let CetiRun provision a
separate **Agent Identity** for each Agent. It is for the platform and Entra
administrators who operate the cluster. It is not for an Agent owner who
assigns Azure RBAC after an identity exists. Start with [Agent Identity
environment setup](../agent-identity/) for the shared Key Vault, TLS, Broker,
MCP, network, and Portal prerequisites. Those prerequisites apply here also.

An Agent Identity is a Microsoft Entra `agentIdentity` service principal
created from one deployment-owned **Agent Identity Blueprint**. CetiRun records
the client ID and object ID of this identity in Agent status. Then it uses that
identity for the selected access mode. The deployment does not accept a
user-supplied principal, client ID, or arbitrary Graph endpoint.

## Choose the access modes

| Mode | What the Agent does | Required Entra resources |
| --- | --- | --- |
| Independent Azure | Calls Azure Resource Manager as its own Agent Identity. Azure RBAC decides the resource access. | Blueprint, its service principal, and Kubernetes federation for Operator and Broker. |
| Microsoft delegated | Calls ARM or Microsoft Graph on behalf of the Portal user through the Entra Auth SDK. | Everything for independent Azure, plus an Authorization application, Broker federation for that application, and `access_agent` consent. |

Enable only the modes that are necessary for the deployment. Independent Azure
uses an app-only ARM token. It does not use the account of a person as a
fallback. Delegated access is still subject to the delegated permission of the
signed-in person. It is also subject to licenses, Conditional Access, resource
authorization, and the downstream API.

By default, the current Broker chart enables both modes. For this reason, when
you introduce the feature, explicitly set the values below. A GitHub- or
Google-only deployment can keep both modes disabled. But this deployment still
must have the common encrypted credential infrastructure that the parent guide
describes.

## Record identifiers before configuring CetiRun

Keep these values in the protected configuration system of the deployment.
They are identifiers, not credentials. The examples use placeholders, not
deployment IDs.

| Purpose | Value used by CetiRun | Example |
| --- | --- | --- |
| Entra tenant | Tenant (directory) ID | `<tenant-id>` |
| Blueprint | Application (client) ID, also called `appId` | `<blueprint-client-id>` |
| Authorization application | Application (client) ID | `<authorization-client-id>` |
| Encryption workload identity | Client ID | `<encryption-client-id>` |
| Portal sign-in application | Application (client) ID | `<portal-client-id>` |
| Created Agent Identity | Service principal object ID and client ID, from Agent status | Created per Agent |

Do not use a service-principal **object ID** where a chart asks for a client
ID. The Operator uses `blueprintClientId` to authenticate. Its Graph create
request records the *object ID* of the new identity for later read and
deletion. The `portalClientId` of the Portal is the Portal login application.
It is neither the Blueprint nor the Authorization application.

## Create and govern the Blueprint

Microsoft documents Agent ID as a current Microsoft Graph v1.0 surface. A
Blueprint is an `application` subtype. An Agent Identity is a
`servicePrincipal` subtype. Tenant availability, Agent ID licenses, role
assignment, Conditional Access, and policies can still prevent a request that
is otherwise valid. Before you enable CetiRun, make sure that access works in
the target tenant. Do not test against a production Agent namespace first.

The recommended creation path is **Microsoft Entra admin center → Entra ID →
Agents → Agent blueprints → New agent blueprint (Preview)**. Give it a stable,
deployment-specific name such as `cetirun-production-blueprint`. Set at least
one accountable sponsor. Add platform administrators as owners.

A sponsor is necessary in the Agent ID model. Owners can manage and create
Agent Identities for their Blueprint. Supported sponsors are users, dynamic
membership groups, and Microsoft 365 groups. Microsoft documents that security
groups and role-assignable groups are not supported as sponsors.

For infrastructure-as-code, use the documented v1.0 request only with an
administrator-approved delegated Graph session. This is a safe shape to review.
Replace the placeholders in the deployment pipeline, never in a shell history
or repository:

```http
POST https://graph.microsoft.com/v1.0/applications/microsoft.graph.agentIdentityBlueprint
OData-Version: 4.0
Content-Type: application/json

{
  "@odata.type": "Microsoft.Graph.AgentIdentityBlueprint",
  "displayName": "cetirun-production-blueprint",
  "sponsors@odata.bind": [
    "https://graph.microsoft.com/v1.0/users/<sponsor-object-id>"
  ],
  "owners@odata.bind": [
    "https://graph.microsoft.com/v1.0/users/<owner-object-id>"
  ]
}
```

Record the response `appId` as the Blueprint client ID. The Entra creation guide
lists these delegated permissions for a client that creates and configures a
Blueprint: `AgentIdentityBlueprint.Create`,
`AgentIdentityBlueprint.AddRemoveCreds.All`,
`AgentIdentityBlueprint.UpdateAuthProperties.All`, and
`AgentIdentityBlueprintPrincipal.Create`. Its documented least-privilege roles
also depend on the type of Graph permissions that the caller grants:
application or delegated. Those bootstrap permissions belong to the automation
client of the administrator, **not** to the CetiRun Operator.

The admin-center wizard creates the related Blueprint principal. For an API
workflow, create and examine that principal before you add FICs or deploy the
Operator. Microsoft documents this v1.0 request and the
`AgentIdentityBlueprintPrincipal.Create` bootstrap permission:

```http
POST https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal
OData-Version: 4.0
Content-Type: application/json

{
  "appId": "<blueprint-client-id>"
}
```

The service principal that the request returns is the tenant presence of the
Blueprint. The `blueprintClientId` chart value is still the `appId` of the
Blueprint application. It is not the object ID of this principal.

The running Operator instead authenticates as the Blueprint and uses the
documented Agent Identity create endpoint. Microsoft lists
`AgentIdentity.CreateAsManager` as an application permission that can create
Agent Identities, with `AgentIdentity.Create.All` as the least-privileged
application permission. The principal that owns the Blueprint is the intended
delegated authority for CetiRun. Ask the Entra team to approve the least
privilege assignment that satisfies its tenant policy. Do not grant the
Operator a directory-wide `AgentIdentity.ReadWrite.All` only to make setup
easier.

### Current lifecycle permission preflight

Creation permission alone is not enough. The Operator continuously examines the
recorded identity. It lists an exact display-name match only when it recovers
an uncertain create. It deletes the tracked identity during cleanup.

The typed Agent Identity APIs document narrower lifecycle permissions. The
application permission `AgentIdentity.CreateAsManager` is accepted for typed
get and delete. `AgentIdentity.Read.All` is the least privilege for typed list.
`AgentIdentity.DeleteRestore.All` is the least privilege for typed delete.

For its read, filtered recovery list, and delete, the current CetiRun
implementation calls the generic `servicePrincipals` collection. It does not
call the typed `.../microsoft.graph.agentIdentity` get/delete endpoints.
Microsoft documents `Application.Read.All` for generic reads and
`Application.ReadWrite.OwnedBy` for generic service-principal deletion.
`Application.ReadWrite.OwnedBy` applies only to service principals that the
caller owns. The current create request does not add the Blueprint as an owner
of a child Agent Identity. Also, the Agent Identity documentation does not show
that this generic permission applies to identities that the typed endpoint
creates.

As a result, do not treat a list of Graph permissions as a proven deployment
recipe. Before lifecycle creation (`enabled`) or cleanup (`cleanupEnabled`) is
enabled, the Entra team must preflight the exact current calls with the
Blueprint principal. These calls are typed create at
`/servicePrincipals/microsoft.graph.agentIdentity`, generic v1.0 get and
filtered list calls, and beta deletion at `/beta/servicePrincipals/<object-id>`.
They must approve the smallest set of grants that succeeds under the tenant's
Agent ID policy, with the ownership behavior that is necessary for deletion.

`Application.*` grants are broader than the typed Agent Identity permissions.
Do not replace a failed preflight with directory-wide permission. If the tenant
does not approve and demonstrate this path, keep both lifecycle creation and
cleanup disabled. Keep them disabled until CetiRun uses the typed endpoints and
the permission design is reviewed. Identities that are created without a proven
recovery and deletion path can become stranded.

For each Agent, CetiRun sends the equivalent of:

```http
POST https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentity
Content-Type: application/json

{
  "@odata.type": "#Microsoft.Graph.AgentIdentity",
  "displayName": "cetirun-agent-<agent-uid>",
  "agentIdentityBlueprintId": "<blueprint-client-id>",
  "sponsors@odata.bind": [
    "https://graph.microsoft.com/v1.0/users/<portal-owner-object-id>"
  ]
}
```

The Portal-owned Agent supplies the sponsor from its verified owner binding.
The Operator does not accept the sponsor from the Agent specification. For
Microsoft Graph, `displayName`, `agentIdentityBlueprintId`, and a valid sponsor
reference are necessary. Microsoft Graph returns `201 Created` with the service
principal.

Then CetiRun reads the exact object ID again. If its Blueprint, display name,
client ID, or tenant does not match, CetiRun fails closed. Before CetiRun tries
another create, it finds a previous uncertain create by its immutable Agent UID
suffix.

## Federate the AKS workloads to the Blueprint

Before you deploy the Operator or Broker, enable AKS OIDC issuer and Microsoft
Entra Workload ID. Microsoft documents these commands for an AKS cluster that
already exists:

```bash
az aks update --resource-group "<resource-group>" --name "<cluster-name>" \
  --enable-oidc-issuer --enable-workload-identity

AKS_OIDC_ISSUER="$(az aks show --resource-group "<resource-group>" \
  --name "<cluster-name>" --query oidcIssuerProfile.issuerUrl --output tsv)"
printf '%s\n' "$AKS_OIDC_ISSUER"
```

Create **two** federated identity credentials on the Blueprint application.
Their issuer must be the exact value that AKS returns, with the slash at the
end. Issuer, subject, and audience are case-sensitive matches. Use
`api://AzureADTokenExchange`, which is the audience that Microsoft recommends
for AKS Workload Identity.

| FIC name | Application | Subject |
| --- | --- | --- |
| `cetirun-operator` | Blueprint | `system:serviceaccount:cetirun-system:cetirun-operator-controller-manager` |
| `cetirun-broker-blueprint` | Blueprint | `system:serviceaccount:cetirun-credentials:cetirun-agent-broker` |

Use the Entra admin center or Microsoft Graph's documented application
federated-credential API. This JSON is the request body for each application
FIC. The two values that change are `name` and `subject`:

```json
{
  "name": "cetirun-operator",
  "issuer": "https://<region>.oic.prod-aks.azure.com/<tenant>/<cluster-id>/",
  "subject": "system:serviceaccount:cetirun-system:cetirun-operator-controller-manager",
  "audiences": ["api://AzureADTokenExchange"]
}
```

The Broker has a second, independent workload-identity use: Key Vault
encryption. Its ServiceAccount annotation is the configured
`encryptionClientId`. For this reason, also create a FIC for the Broker
subject on that encryption identity. This is separate from **both** Blueprint
FICs: the Operator-to-Blueprint FIC and the Broker-to-Blueprint FIC. When
encrypted connection storage is enabled, also keep the encryption FIC. For
delegated mode, the Authorization-application FIC in the next section is also
necessary.

When `azure.clientId` is configured, the Operator ServiceAccount annotation uses
it. Otherwise, the annotation uses `agentIdentity.blueprintClientId`. The
lifecycle code still authenticates explicitly as the Blueprint client ID. For
this reason, keep the Operator-to-Blueprint FIC, even when a different Operator
integration uses an `azure.clientId` that already exists. Do not overwrite that
unrelated identity only to enable Agent Identity.

Do not replace these ServiceAccounts, namespaces, or names unless you change
every applicable FIC subject and render the charts together. After a create or
update, federation can take time to propagate. Microsoft recommends that you
allow for this delay before you diagnose a token request as a configuration
failure.

## Configure independent Azure access

Apply the Broker and platform overlays in
[Deploy the configuration](#deploy-the-configuration) with independent Azure
enabled. The Broker exchanges its projected workload token through the
Blueprint. Then it gets an ARM token for the selected Agent Identity. The
Agent's owner or a resource owner assigns Azure RBAC after the identity becomes
Ready. CetiRun does not create role assignments and does not precompute a
subscription-wide permission grant.

For example, after a resource owner reads the Agent's
`status.agentIdentity.objectId`, the resource owner can assign a narrow Reader
role:

```bash
az role assignment create --subscription "<subscription-id>" \
  --assignee-object-id "<agent-identity-object-id>" \
  --assignee-principal-type ServicePrincipal \
  --role Reader --scope "/subscriptions/<subscription-id>/resourceGroups/<resource-group>"
```

Use the smallest Azure scope and role that allow the intended service call.
The Agent Identity itself is separate from the Blueprint and from the Broker.
Never assign resource roles to either shared identity as a substitute.

## Add Microsoft delegated access when needed

This optional path is for personal Azure or Microsoft Graph calls that are
intended to use the Portal user's authority. It uses the Entra Auth SDK sidecar
in the Broker Pod. A separate confidential **Authorization application** is
also necessary for it. This application is not the Portal login application.

1. Register a single-tenant confidential **Web** application named, for
   example, `cetirun-delegated-authorization`. Under its Web platform, add the
   exact HTTPS redirect URI
   `https://portal.example.com/api/v1/connections/callback`. Record its client
   ID as `authorizationClientId`. Do not create a client secret for CetiRun.
2. Add a FIC to that Authorization application for the Broker workload:

   ```json
   {
     "name": "cetirun-broker-authorization",
     "issuer": "https://<region>.oic.prod-aks.azure.com/<tenant>/<cluster-id>/",
     "subject": "system:serviceaccount:cetirun-credentials:cetirun-agent-broker",
     "audiences": ["api://AzureADTokenExchange"]
   }
   ```

3. On the Blueprint, expose the identifier URI
   `api://<blueprint-client-id>` and the delegated user scope `access_agent`.
   When you use Graph for this step,
   `AgentIdentityBlueprint.UpdateAuthProperties.All` is necessary for the
   documented request:

   ```json
   {
    "identifierUris": ["api://<blueprint-client-id>"],
     "api": {
       "oauth2PermissionScopes": [{
         "id": "<new-guid>",
         "isEnabled": true,
         "type": "User",
         "value": "access_agent",
         "adminConsentDisplayName": "Access agent",
         "adminConsentDescription": "Allow the application to access the agent on behalf of the signed-in user.",
         "userConsentDisplayName": "Access agent",
         "userConsentDescription": "Allow the application to access the agent on your behalf."
       }]
     }
   }
   ```

4. Add the Blueprint's `access_agent` delegated permission to the
   **Authorization application**. Then grant the consent that the tenant policy
   makes necessary. The Authorization application is the client that gets the
   user assertion with the Blueprint as its audience. It is not the identity
   that holds downstream ARM or Graph permission grants.
5. Configure downstream delegated access on the **Blueprint**. Add the intended
   ARM and Microsoft Graph permissions to its required-resource-access
   declaration. Configure scope inheritance for each downstream resource app,
   and grant only those delegated permissions to the Blueprint principal. The
   Microsoft OBO guidance for Agent ID makes this necessary, because child
   Agent Identities cannot start interactive consent. Delegated permissions
   must be preauthorized through their parent Blueprint, and they must receive
   administrator consent there. For scopes only, the documented
   inheritable-permissions shape is:

   ```json
   {
     "resourceAppId": "<downstream-resource-app-id>",
     "inheritableScopes": {
       "@odata.type": "#microsoft.graph.allAllowedScopes",
       "kind": "allAllowed"
     },
     "inheritableRoles": {
       "@odata.type": "#microsoft.graph.noRoles",
       "kind": "none"
     }
   }
   ```

   POST one entry per downstream resource app to
   `https://graph.microsoft.com/v1.0/applications/microsoft.graph.agentIdentityBlueprint/<blueprint-client-id>/inheritablePermissions`.
   `allAllowed` inherits only the scopes that are granted to the Blueprint
   principal. It does not grant every scope that the resource offers. The
   default Broker configuration asks the Auth SDK for
   `https://management.azure.com/user_impersonation` and
   `https://graph.microsoft.com/.default`. Limit the Blueprint grants and chart
   scopes to the exact ARM and Graph operations that the deployment exposes.
6. Apply the delegated overlay. The Broker verifies that the user assertion has
   the deployment tenant and Portal owner object ID. It verifies that the
   audience equals the Blueprint client ID and the authorized party equals the
   Authorization client ID. It also verifies that the assertion is a
   non-application token with `access_agent` in `scp`. Then it asks the local
   Auth SDK only for ARM or Microsoft Graph headers.

The OAuth authorization redirect is the Broker value
`https://portal.example.com/api/v1/connections/callback`. It must match the
redirect URI on the Authorization application exactly. The example domain is a
placeholder. Use the actual public Portal origin, HTTPS only, and the same
origin that is already configured for Portal sign-in.

## Deploy the configuration

Merge each overlay with the values that already exist for the environment. Do
not use these partial files as complete chart values. They expect that the
parent guide already configured Broker TLS, Key Vault encryption, MCP trust,
namespaces, and the Portal host. The values use the standard CetiRun namespace
split: Portal, Operator, and MCP are in `cetirun-system`, and the Broker is in
`cetirun-credentials`.

### broker-entra.yaml

This **independent Azure** overlay enables the Blueprint-based Broker flow and
explicitly keeps delegated access off:

```yaml
identityModes:
  azureAgent:
    enabled: true
  microsoftDelegated:
    enabled: false

agentIdentity:
  tenantId: "<tenant-id>"
  blueprintClientId: "<blueprint-client-id>"
```

### platform-entra.yaml

This umbrella-chart overlay enables identity creation and the Portal identity
UI in the release process that the platform already has:

```yaml
cetirun-operator:
  agentIdentity:
    enabled: true
    cleanupEnabled: true
    tenantId: "<tenant-id>"
    blueprintClientId: "<blueprint-client-id>"
    namePrefix: "cetirun-agent"
    brokerURL: "http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"

cetirun-portal:
  features:
    agentToolsEnabled: true
    agentIdentityEnabled: true
  agentIdentity:
    tenantId: "<tenant-id>"
    portalClientId: "<portal-client-id>"
    brokerURL: "http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"
```

The two `agentIdentity` blocks above target different subcharts. Keep their
tenant IDs consistent. The Portal block deliberately uses the Portal login
client ID. It does not configure the Blueprint.

### broker-entra-delegated.yaml

Make sure that the Authorization application, its Broker FIC, scope,
downstream delegated permissions, and consent are ready. Only after that, layer
this addition over `broker-entra.yaml`:

```yaml
identityModes:
  microsoftDelegated:
    enabled: true

agentIdentity:
  authorizationClientId: "<authorization-client-id>"
  redirectUri: "https://portal.example.com/api/v1/connections/callback"
  portalOrigin: "https://portal.example.com"
  portalBaseUrl: "https://portal.example.com"
  armScopes:
    - "https://management.azure.com/user_impersonation"
  graphScopes:
    - "https://graph.microsoft.com/.default"
```

When either Entra mode is enabled, the Broker chart rejects values that do not
have a Blueprint client ID. When delegated mode is enabled, it also rejects
values that do not have an Authorization client ID. With delegated mode on, it
adds the Auth SDK sidecar and its in-memory scratch volume. If no
user-delegated Azure or Microsoft Graph flow is necessary, disable delegated
mode again. The flag does not delete saved connections.

## Verify, operate, and remove

1. Render the Broker, Operator, and Portal charts with the composed values.
   Make sure that the Operator ServiceAccount annotation uses its
   `azure.clientId` that already exists, or the Blueprint fallback, as
   intended. Make sure that Broker Pods carry
   `azure.workload.identity/use: "true"`. Make sure that all FIC subjects
   exactly match the rendered ServiceAccounts. Independently, make sure that
   the lifecycle credential can authenticate as the Blueprint client ID.
2. Deploy the parent guide's shared infrastructure, then the Broker, Operator,
   Portal, and MCP components through the approved platform process. Before you
   judge the first try to get a token, wait for the Workload Identity
   federation to propagate.
3. Create or update one non-production supported Agent with Agent Identity
   enabled. Make sure that `status.agentIdentity.phase` becomes `Ready`. Record
   its `clientId` and `objectId`. Make sure that its display name has the Agent
   UID suffix. An absent or mismatched Blueprint, principal, sponsor, or owner
   binding must leave the feature unavailable rather than select another
   identity.
4. For independent Azure, add narrowly scoped RBAC to that Agent Identity and
   make an authorized ARM call. For delegated access, use a non-production
   user. Sign in through the Portal, confirm consent, and test the smallest
   intended Graph or ARM action. Test denial and revocation as well as success.
5. Review Entra sign-in/audit records and Azure Activity Log with your
   organization’s retention and privacy policy. Do not put tokens, complete
   values files, principal IDs from production, or browser data in tickets.

To retire the feature, first remove Agent bindings and resource RBAC. Then
remove any Microsoft connections that use delegated access, and disable Agent
Identity on every Agent. Wait for deletion to complete. Keep the Operator
lifecycle credentials available (`cleanupEnabled: true`) until all tracked
Agent Identities are gone.

Only then disable the Operator lifecycle and Broker modes. After that, revoke
the Authorization application's `access_agent` consent and the Blueprint's
downstream delegated grants, and remove the Authorization application's FIC.
Finally, remove the Blueprint's FICs and Blueprint as the Entra team's
retention policy says. If you delete a Blueprint or FIC first, this strands the
Operator's cleanup path.

## Microsoft references and limits

- [Create an agent identity blueprint](https://learn.microsoft.com/en-us/entra/agent-id/create-blueprint) documents Blueprint creation, bootstrap permissions, FICs, and how to expose a scope.
- [Agent identity blueprints](https://learn.microsoft.com/en-us/entra/agent-id/agent-blueprint) describes Blueprint ownership, sponsors, inherited permissions, and the special creation authority.
- [Create agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-post?view=graph-rest-1.0) documents the v1.0 create endpoint, request fields, and permission table used by the Operator.
- [Get agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-get?view=graph-rest-1.0), [list agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-list?view=graph-rest-1.0), and [delete agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-delete?view=graph-rest-1.0) document the scoped lifecycle permissions. Also, [list servicePrincipal](https://learn.microsoft.com/en-us/graph/api/serviceprincipal-list?view=graph-rest-1.0) and [delete servicePrincipal](https://learn.microsoft.com/en-us/graph/api/serviceprincipal-delete?view=graph-rest-1.0) document the generic calls that CetiRun makes now.
- [Create federatedIdentityCredential](https://learn.microsoft.com/en-us/graph/api/application-post-federatedidentitycredentials?view=graph-rest-1.0) and [AKS Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview) document application federation and Kubernetes trust.
- [Agent Identity on-behalf-of OAuth flow](https://learn.microsoft.com/en-us/entra/agent-id/agent-on-behalf-of-oauth-flow) and [configure inheritable permissions for Agent Identity Blueprints](https://learn.microsoft.com/en-us/entra/agent-id/configure-inheritable-permissions-blueprints) document the Agent ID delegated flow, parent consent, and scope inheritance.
- [On-behalf-of flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-on-behalf-of-flow), [permissions and consent](https://learn.microsoft.com/en-us/entra/identity-platform/permissions-consent-overview), and [expose a web API](https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-configure-app-expose-web-apis) describe the delegated protocol, consent controls, and scope metadata.

Microsoft's public API reference confirms the create/read/update/delete
`agentIdentity` operations. But no chart render can prove that a particular
tenant has the necessary Agent ID entitlement, supported administrator role, or
policy approval. Treat those as an Entra administrator preflight. CetiRun
cannot bypass tenant policy, make a sponsor valid, grant Graph consent, assign
Azure RBAC, or turn a delegated user token into app-only authority.
