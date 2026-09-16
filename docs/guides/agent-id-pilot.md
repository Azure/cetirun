# Microsoft Entra Agent ID pilot

This guide deploys the Microsoft Entra ID Auth SDK as an optional, pod-local
sidecar for a single CetiRun instance. It is a pilot integration. It does not
create Microsoft Entra objects, change OAuth2 Proxy, or enable Agent ID by
default.

## Security model

Create one dedicated Agent ID blueprint, Agent Identity, and User-Assigned
Managed Identity for each CetiRun instance trust boundary. Do not reuse the
Portal OAuth application, Teams application, operator identity, or a Managed
Identity from another instance.

The Auth SDK sidecar has no Kubernetes Service, ingress route, or NetworkPolicy
ingress rule. It binds the pod network address so Kubernetes health probes work,
while the agent runtime uses `127.0.0.1:5000`. Agent ID requires the default
deny NetworkPolicy to remain enabled. The runtime receives these environment
variables when the feature is enabled:

- `ENTRA_SDK_URL`: pod-local Auth SDK endpoint.
- `CETIRUN_AGENT_IDENTITY_CLIENT_ID`: Agent Identity client ID.
- `CETIRUN_AGENT_IDENTITY_REQUIRED_SCOPE`: expected delegated scope.

On AKS, the sidecar uses `SignedAssertionFilePath`. The AKS Workload Identity
webhook projects and rotates the credential. Do not use client secrets for
this deployment.

## Entra and Azure prerequisites

Perform these steps in an approved corporate Windows session. The current WSL
test environment must be accessed through its configured corporate tenant,
not an unrelated Azure CLI subscription selection.

1. Assign the operator of this pilot the Agent ID Developer or Agent ID
   Administrator role and the Microsoft Graph permissions documented for
   Agent ID blueprint creation.
2. Create an Agent Identity blueprint with a human owner and sponsor, an
   identifier URI, and the delegated `access_agent` scope.
3. Create the corresponding Agent Identity blueprint principal and one test
   Agent Identity. Record the blueprint client ID and Agent Identity client ID.
4. Create one User-Assigned Managed Identity for the test CetiRun instance.
   Add it as a federated credential of the blueprint, then create an AKS
   federated credential binding it to the instance ServiceAccount.
5. Grant only the delegated permissions used by the pilot. Start with
   `User.Read` for Microsoft Graph. Keep approval and audit records outside
   this repository.

The existing Teams Operator FIC is unrelated to this pilot and is not a
substitute for the dedicated Agent ID Managed Identity.

## Enable the sidecar

Supply the following values through the operator Helm values or the target
instance's `spec.helm.values`. Values below are placeholders and must not be
committed with tenant identifiers or credentials.

```yaml
serviceAccount:
  create: true
azureWorkloadIdentity:
  enabled: true
  clientId: <user-assigned-managed-identity-client-id>
agentIdentity:
  enabled: true
  tenantId: <tenant-id>
  blueprintClientId: <agent-identity-blueprint-client-id>
  agentIdentityClientId: <agent-identity-client-id>
  audience: api://<agent-identity-blueprint-client-id>
  requiredScope: access_agent
  downstreamApis:
    - name: Graph
      baseUrl: https://graph.microsoft.com/v1.0
      scopes:
        - User.Read
```

The chart rejects an enabled Agent ID configuration without all identity IDs,
a ServiceAccount, a workload identity client ID, a valid sidecar image, or a
valid downstream API definition. It also rejects non-HTTPS downstream URLs.

## OBO validation

The current OAuth2 Proxy deployment intentionally sets
`--pass-access-token=false`; its headers establish Portal access but do not
provide a bearer token usable for Agent ID OBO. Do not treat a successful
Portal session as an OBO test.

For a real OBO test, use a dedicated client application that requests
`api://<agent-identity-blueprint-client-id>/access_agent`. The receiving agent
endpoint must forward that bearer token to the sidecar:

1. Call `GET /Validate` on `ENTRA_SDK_URL` with the incoming bearer token.
2. Call `GET /AuthorizationHeader/Graph?AgentIdentity=<agent-identity-client-id>`
   with the same bearer token.
3. Use the returned authorization header for `GET /me` on Microsoft Graph.
4. Assert only the HTTP status, token validation result, correlation ID, and
   Agent Identity audit entry. Do not log the token or Graph response body.

Before Graph, validate the same flow against a dedicated protected echo API
using only synthetic request and response data. Test missing token, malformed
token, wrong audience, missing scope, disabled Agent Identity, and downstream
403 responses.

## Offline validation

Run the chart checks before any tenant write:

```bash
bash scripts/cetirun-helm-lint.sh
bash scripts/cetirun-helm-test.sh
```

The render test verifies that the sidecar is pod-local, uses the AKS workload
identity credential source, configures the Graph downstream API, and rejects
missing Workload Identity configuration. These checks do not establish Entra
tenant access or real OBO behavior.
