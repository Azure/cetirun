---
title: "Set up Microsoft Entra Agent Identity"
description: "Prepare the Entra blueprint, workload federation, and optional delegated access required by CetiRun Agent Identity."
weight: 35
---

This guide prepares the Microsoft Entra resources that let CetiRun provision a
separate **Agent Identity** for each Agent. It is for the platform and Entra
administrators who operate the cluster, not for an Agent owner assigning Azure
RBAC after an identity has been created. Start with [Agent Identity
environment setup](../agent-identity/) for the shared Key Vault, TLS, Broker,
MCP, network, and Portal prerequisites. Those prerequisites apply here as well.

An Agent Identity is a Microsoft Entra `agentIdentity` service principal created
from one deployment-owned **Agent Identity Blueprint**. CetiRun records the
resulting client ID and object ID in Agent status, then uses that identity for
the selected access mode. The deployment does not accept a user-supplied
principal, client ID, or arbitrary Graph endpoint.

## Choose the access modes

| Mode | What the Agent does | Required Entra resources |
| --- | --- | --- |
| Independent Azure | Calls Azure Resource Manager as its own Agent Identity. Azure RBAC decides the resource access. | Blueprint, its service principal, and Kubernetes federation for Operator and Broker. |
| Microsoft delegated | Calls ARM or Microsoft Graph on behalf of the Portal user through the Entra Auth SDK. | Everything for independent Azure, plus an Authorization application, Broker federation for that application, and `access_agent` consent. |

Enable only the modes the deployment needs. Independent Azure uses an
app-only ARM token and does not fall back to a person's account. Delegated
access remains subject to the signed-in person's delegated permission,
licensing, Conditional Access, resource authorization, and the downstream API.

The current Broker chart defaults both modes to enabled, so explicitly set the
values below when introducing the feature. A GitHub- or Google-only deployment
can leave both modes disabled, but still needs the common encrypted credential
infrastructure described in the parent guide.

## Record identifiers before configuring CetiRun

Keep the following values in the deployment's protected configuration system.
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

Do not substitute a service-principal **object ID** where a chart asks for a
client ID. The Operator uses `blueprintClientId` to authenticate; its Graph
create request records the newly created identity's *object ID* for later read
and deletion. The Portal's `portalClientId` is the Portal login application and
is neither the Blueprint nor the Authorization application.

## Create and govern the Blueprint

Microsoft documents Agent ID as a current Microsoft Graph v1.0 surface. A
Blueprint is an `application` subtype, while an Agent Identity is a
`servicePrincipal` subtype. Tenant availability, Agent ID licensing, role
assignment, Conditional Access, and policies can still prevent an otherwise
valid request. Verify access in the target tenant before enabling CetiRun; do
not test against a production Agent namespace first.

The recommended creation path is **Microsoft Entra admin center → Entra ID →
Agents → Agent blueprints → New agent blueprint (Preview)**. Give it a stable,
deployment-specific name such as `cetirun-production-blueprint`, set at least
one accountable sponsor, and add platform administrators as owners. A sponsor
is required by the Agent ID model; owners can manage and create Agent
Identities for their Blueprint. Supported sponsors are users, dynamic
membership groups, and Microsoft 365 groups; Microsoft documents that security
groups and role-assignable groups are not supported as sponsors.

For infrastructure-as-code, use the documented v1.0 request only with an
administrator-approved delegated Graph session. This is a safe shape to review;
replace the placeholders in the deployment pipeline, never in a shell history
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
lists the delegated permissions for a client that creates and configures a
Blueprint: `AgentIdentityBlueprint.Create`,
`AgentIdentityBlueprint.AddRemoveCreds.All`,
`AgentIdentityBlueprint.UpdateAuthProperties.All`, and
`AgentIdentityBlueprintPrincipal.Create`; its documented least-privilege roles
also depend on whether the caller grants application or delegated Graph
permissions. Those bootstrap permissions belong to the administrator's
automation client, **not** to the CetiRun Operator.

The admin-center wizard creates the matching Blueprint principal. For an API
workflow, create and verify that principal before adding FICs or deploying the
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

The returned service principal is the Blueprint's tenant presence. The
`blueprintClientId` chart value remains the Blueprint application's `appId`,
not this principal's object ID.

The running Operator instead authenticates as the Blueprint and uses the
documented Agent Identity create endpoint. Microsoft lists
`AgentIdentity.CreateAsManager` as an application permission that can create
Agent Identities, with `AgentIdentity.Create.All` as the least-privileged
application permission. The Blueprint-owning principal is the intended
delegated authority for CetiRun; have the Entra team approve the least
privilege assignment that satisfies its tenant policy. Do not grant the
Operator a directory-wide `AgentIdentity.ReadWrite.All` merely to make setup
easier.

### Current lifecycle permission preflight

Creation permission alone is insufficient. The Operator continuously verifies
the recorded identity, lists an exact display-name match only when recovering an
uncertain create, and deletes the tracked identity during cleanup. The typed
Agent Identity APIs document narrower lifecycle permissions: application
`AgentIdentity.CreateAsManager` is accepted for typed get and delete,
`AgentIdentity.Read.All` is the least privilege for typed list, and
`AgentIdentity.DeleteRestore.All` is the least privilege for typed delete.

The current CetiRun implementation calls the generic `servicePrincipals`
collection for its read, filtered recovery list, and delete rather than the
typed `.../microsoft.graph.agentIdentity` get/delete endpoints. Microsoft
documents `Application.Read.All` for generic reads and
`Application.ReadWrite.OwnedBy` for generic service-principal deletion.
`Application.ReadWrite.OwnedBy` is constrained to service principals the caller
owns. The current create request does not add the Blueprint as an owner of a
child Agent Identity, and the Agent Identity documentation does not establish
that this generic permission applies to identities created by the typed endpoint.

Consequently, do not treat a list of Graph permissions as a proven deployment
recipe. Before enabling lifecycle creation (`enabled`) or cleanup
(`cleanupEnabled`), the Entra team must preflight the exact current calls with
the Blueprint principal: typed create at
`/servicePrincipals/microsoft.graph.agentIdentity`, generic v1.0 get and
filtered list calls, and beta deletion at `/beta/servicePrincipals/<object-id>`.
They must approve the smallest set of grants that succeeds under the tenant's
Agent ID policy, including the ownership behavior needed for deletion.
`Application.*` grants are broader than the typed Agent Identity permissions;
do not replace a failed preflight with directory-wide permission. If the tenant
does not approve and demonstrate this path, leave both lifecycle creation and
cleanup disabled until CetiRun uses the typed endpoints and the permission
design has been reviewed. Creating identities without a verified recovery and
deletion path can leave identities stranded.

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

The Portal-owned Agent supplies the sponsor from its verified owner binding;
the Operator does not accept it from the Agent specification. Microsoft Graph
requires `displayName`, `agentIdentityBlueprintId`, and a valid sponsor
reference, and returns `201 Created` with the service principal. CetiRun then
re-reads the exact object ID and fails closed if its Blueprint, display name,
client ID, or tenant does not match. A previous uncertain create is discovered
by its immutable Agent UID suffix before another create is attempted.

## Federate the AKS workloads to the Blueprint

Enable AKS OIDC issuer and Microsoft Entra Workload ID before deploying the
Operator or Broker. Microsoft documents these commands for an existing AKS
cluster:

```bash
az aks update --resource-group "<resource-group>" --name "<cluster-name>" \
  --enable-oidc-issuer --enable-workload-identity

AKS_OIDC_ISSUER="$(az aks show --resource-group "<resource-group>" \
  --name "<cluster-name>" --query oidcIssuerProfile.issuerUrl --output tsv)"
printf '%s\n' "$AKS_OIDC_ISSUER"
```

Create **two** federated identity credentials on the Blueprint application.
Their issuer must be the exact value returned by AKS, including its trailing
slash. Issuer, subject, and audience are case-sensitive matches. Use
`api://AzureADTokenExchange`, the audience recommended by Microsoft for AKS
Workload Identity.

| FIC name | Application | Subject |
| --- | --- | --- |
| `cetirun-operator` | Blueprint | `system:serviceaccount:cetirun-system:cetirun-operator-controller-manager` |
| `cetirun-broker-blueprint` | Blueprint | `system:serviceaccount:cetirun-credentials:cetirun-agent-broker` |

Use the Entra admin center or Microsoft Graph's documented application
federated-credential API. The following JSON is the request body for each
application FIC; the two values that change are `name` and `subject`:

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
`encryptionClientId`, so create a matching FIC on that encryption identity for
the Broker subject as well. This is separate from **both** Blueprint FICs: the
Operator-to-Blueprint FIC and the Broker-to-Blueprint FIC. When encrypted
connection storage is enabled, retain the encryption FIC too. Delegated mode
also requires the Authorization-application FIC in the next section.

The Operator ServiceAccount annotation uses `azure.clientId` when configured,
falling back to `agentIdentity.blueprintClientId`. The lifecycle code still
authenticates explicitly as the Blueprint client ID. Therefore, retain the
Operator-to-Blueprint FIC even when an existing `azure.clientId` is used for a
different Operator integration; do not overwrite that unrelated identity just
to enable Agent Identity. Do not replace these ServiceAccounts, namespaces, or
names without changing every applicable FIC subject and rendering the charts
together. Federation can take time to propagate after a create or update;
Microsoft advises allowing for that before diagnosing a token request as a
configuration failure.

## Configure independent Azure access

Apply the Broker and platform overlays in [Deploy the configuration](#deploy-the-configuration)
with independent Azure enabled. The Broker exchanges its projected workload
token through the Blueprint, then obtains an ARM token for the selected Agent
Identity. The Agent's owner or a resource owner assigns Azure RBAC after the
identity becomes Ready. CetiRun does not create role assignments and does not
precompute a subscription-wide permission grant.

For example, a resource owner can assign a narrow Reader role after reading
the Agent's `status.agentIdentity.objectId`:

```bash
az role assignment create --subscription "<subscription-id>" \
  --assignee-object-id "<agent-identity-object-id>" \
  --assignee-principal-type ServicePrincipal \
  --role Reader --scope "/subscriptions/<subscription-id>/resourceGroups/<resource-group>"
```

Use the smallest Azure scope and role that permit the intended service call.
The Agent Identity itself is distinct from the Blueprint and from the Broker;
never assign resource roles to either shared identity as a substitute.

## Add Microsoft delegated access when needed

This optional path is for personal Azure or Microsoft Graph calls that should
use the Portal user's authority. It uses the Entra Auth SDK sidecar in the
Broker Pod, and it needs a separate confidential **Authorization application**.
It is not the Portal login application.

1. Register a single-tenant confidential **Web** application named, for
   example, `cetirun-delegated-authorization`. Under its Web platform, add the
   exact HTTPS redirect URI
   `https://portal.example.com/api/v1/connections/callback`. Record its client
   ID as `authorizationClientId`; do not create a client secret for CetiRun.
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
   The documented request needs
   `AgentIdentityBlueprint.UpdateAuthProperties.All` when managed through
   Graph:

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
   **Authorization application**, then grant the consent required by the tenant
   policy. This is the client that obtains the user assertion whose audience is
   the Blueprint; it is not the identity that holds downstream ARM or Graph
   permission grants.
5. Configure downstream delegated access on the **Blueprint**. Add the intended
   ARM and Microsoft Graph permissions to its required-resource-access
   declaration, configure scope inheritance for each downstream resource app,
   and grant only those delegated permissions to the Blueprint principal.
   Microsoft’s Agent ID OBO guidance requires this because child Agent
   Identities cannot initiate interactive consent; delegated permissions must
   be preauthorized through their parent Blueprint and receive administrator
   consent there. For scopes only, the documented inheritable-permissions shape
   is:

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
   `allAllowed` inherits only the scopes actually granted to the Blueprint
   principal; it does not grant every scope offered by the resource. The default
   Broker configuration asks the Auth SDK for
   `https://management.azure.com/user_impersonation` and
   `https://graph.microsoft.com/.default`; limit the Blueprint grants and chart
   scopes to the exact ARM and Graph operations the deployment exposes.
6. Apply the delegated overlay. The Broker verifies that the user assertion has
   the deployment tenant and Portal owner object ID, audience equal to the
   Blueprint client ID, authorized party equal to the Authorization client ID,
   a non-application token, and `access_agent` in `scp`. It then asks the local
   Auth SDK only for ARM or Microsoft Graph headers.

The OAuth authorization redirect is the Broker value
`https://portal.example.com/api/v1/connections/callback`; it must match the
redirect URI on the Authorization application exactly. The example domain is a
placeholder. Use the actual public Portal origin, HTTPS only, and the same
origin already configured for Portal sign-in.

## Deploy the configuration

Merge each overlay with the environment's existing values; do not use these
partial files as complete chart values. They assume the parent guide has
already configured Broker TLS, Key Vault encryption, MCP trust, namespaces,
and the Portal host. The values use the standard CetiRun namespace split:
Portal, Operator, and MCP are in `cetirun-system`; the Broker is in
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
UI in the platform's existing release process:

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
client ID; it does not configure the Blueprint.

### broker-entra-delegated.yaml

Layer this addition over `broker-entra.yaml` only after the Authorization
application, its Broker FIC, scope, downstream delegated permissions, and
consent are ready:

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

The Broker chart rejects a missing Blueprint client ID whenever either Entra
mode is enabled, and rejects a missing Authorization client ID when delegated
mode is enabled. With delegated mode on it adds the Auth SDK sidecar and its
in-memory scratch volume. Disable it again if no user-delegated Azure or
Microsoft Graph flow is needed; saved connections are not deleted by the flag.

## Verify, operate, and remove

1. Render the Broker, Operator, and Portal charts with the composed values.
   Confirm that the Operator ServiceAccount annotation uses either its existing
   `azure.clientId` or the Blueprint fallback as intended, Broker Pods carry
   `azure.workload.identity/use: "true"`, and all FIC subjects exactly match
   the rendered ServiceAccounts. Independently confirm that the lifecycle
   credential can authenticate as the Blueprint client ID.
2. Deploy the shared infrastructure from the parent guide, then the Broker,
   Operator, Portal, and MCP components through the approved platform process.
   Wait for Workload Identity federation propagation before judging the first
   token attempt.
3. Create or update one non-production supported Agent with Agent Identity
   enabled. Confirm `status.agentIdentity.phase` becomes `Ready`, record its
   `clientId` and `objectId`, and confirm its display name has the Agent UID
   suffix. A missing or mismatched Blueprint, principal, sponsor, or owner
   binding must leave the feature unavailable rather than select another
   identity.
4. For independent Azure, add narrowly scoped RBAC to that Agent Identity and
   make an authorized ARM call. For delegated access, sign in through the
   Portal, confirm consent, and test the smallest intended Graph or ARM action
   as a non-production user. Test denial and revocation as well as success.
5. Review Entra sign-in/audit records and Azure Activity Log with your
   organization’s retention and privacy policy. Do not put tokens, complete
   values files, principal IDs from production, or browser data in tickets.

To retire the feature, first remove Agent bindings and resource RBAC, then
remove any Microsoft connections that use delegated access and disable Agent
Identity on every Agent. Wait for deletion to complete. Keep the Operator
lifecycle credentials available (`cleanupEnabled: true`) until all tracked
Agent Identities are gone. Only then disable the Operator lifecycle and Broker
modes, revoke the Authorization application's `access_agent` consent and the
Blueprint's downstream delegated grants, remove the Authorization application's
FIC, and finally remove the Blueprint's FICs and Blueprint according to the
Entra team's retention policy. Deleting a Blueprint or FIC first strands the
Operator's cleanup path.

## Microsoft references and limits

- [Create an agent identity blueprint](https://learn.microsoft.com/en-us/entra/agent-id/create-blueprint) documents Blueprint creation, bootstrap permissions, FICs, and exposing a scope.
- [Agent identity blueprints](https://learn.microsoft.com/en-us/entra/agent-id/agent-blueprint) describes Blueprint ownership, sponsors, inherited permissions, and the special creation authority.
- [Create agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-post?view=graph-rest-1.0) documents the v1.0 create endpoint, request fields, and permission table used by the Operator.
- [Get agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-get?view=graph-rest-1.0), [list agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-list?view=graph-rest-1.0), and [delete agentIdentity](https://learn.microsoft.com/en-us/graph/api/agentidentity-delete?view=graph-rest-1.0) document the scoped lifecycle permissions; [list servicePrincipal](https://learn.microsoft.com/en-us/graph/api/serviceprincipal-list?view=graph-rest-1.0) and [delete servicePrincipal](https://learn.microsoft.com/en-us/graph/api/serviceprincipal-delete?view=graph-rest-1.0) document the generic calls currently made by CetiRun.
- [Create federatedIdentityCredential](https://learn.microsoft.com/en-us/graph/api/application-post-federatedidentitycredentials?view=graph-rest-1.0) and [AKS Workload Identity](https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview) document application federation and Kubernetes trust.
- [Agent Identity on-behalf-of OAuth flow](https://learn.microsoft.com/en-us/entra/agent-id/agent-on-behalf-of-oauth-flow) and [configure inheritable permissions for Agent Identity Blueprints](https://learn.microsoft.com/en-us/entra/agent-id/configure-inheritable-permissions-blueprints) document the Agent ID delegated flow, parent consent, and scope inheritance.
- [On-behalf-of flow](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-on-behalf-of-flow), [permissions and consent](https://learn.microsoft.com/en-us/entra/identity-platform/permissions-consent-overview), and [expose a web API](https://learn.microsoft.com/en-us/entra/identity-platform/quickstart-configure-app-expose-web-apis) describe the delegated protocol, consent controls, and scope metadata.

Microsoft's public API reference confirms the create/read/update/delete
`agentIdentity` operations, but no chart render can prove that a particular
tenant has the required Agent ID entitlement, supported administrator role, or
policy approval. Treat those as an Entra administrator preflight. CetiRun
cannot bypass tenant policy, make a sponsor valid, grant Graph consent, assign
Azure RBAC, or turn a delegated user token into app-only authority.
