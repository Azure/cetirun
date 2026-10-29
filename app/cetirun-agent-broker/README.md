# CetiRun Agent Connection Broker

The Broker manages owner-scoped Connections and explicit per-Agent connection
bindings. Azure RBAC and provider permissions are the resource authorization
authorities. GitHub App bindings narrow installation tokens to selected
repositories and permissions. There are no business-level AgentGrants,
per-tool permissions or prerequisite Azure RBAC verification.

Platform administrators should start with the
[Agent Identity setup guide](../../site/content/en/docs/operations/agent-identity.md)
for encryption, federation, certificates, provider registrations and deployment
order. This README describes the component contracts after those prerequisites.

A binding associates an immutable Agent UID with a Connection and selected
services. New associations are always enabled. Sharing an owner does not share
connections automatically.
Every Agent needs an explicit binding. Services are defined with fixed origins
and audiences in `app/internal/agenttools`, shared with MCP.

## Management API

Portal APIs require the verified Portal mesh identity plus owner tenant/object
headers. Agent-scoped operations additionally require `X-CetiRun-Instance-UID`
and match the live Agent owner. Provider credentials are absent from API views.

- `GET /v1/connections`
- `POST /v1/connections/start`
- `GET /v1/connections/github-app/config`
- `GET /v1/connections/{id}/github/installations`
- `GET /v1/connections/{id}/github/repositories?installationId=123&page=1`
- `POST /v1/connections/{id}/github/installation`
- `GET|DELETE /v1/connections/{id}`
- `POST /v1/connections/{id}/reconnect`
- `GET /v1/connection-authorizations/{id}` (owner-scoped OAuth result)
- `GET /v1/connections/{id}/usage` (associated Agents and `canRemove`)
- `POST /v1/instances/{name}/tool-identities` (independent Azure connection)
- `GET|POST /v1/instances/{name}/connection-bindings`
- `PUT|DELETE /v1/instances/{name}/connection-bindings/{id}`

Binding input is `connectionId`, `services`, `enabled: true`; PUT includes `revision`,
and DELETE requires `If-Match`. A deterministic Agent/Connection record key and
Kubernetes resourceVersion CAS prevent duplicate binding races and lost updates.
Revocation retains a tombstone so re-creation cannot reuse an issued revision.
Disconnect revokes the binding and blocks future issuance. New disabled writes
are rejected; historical disabled records require explicit reconnection. Requests already sent
to providers cannot be recalled; provider-side revocation remains external.

GitHub App connection start additionally supplies `mode: installation` with
`provider: github`. After GitHub user authorization, the owner selects an
installation with `{ "installationId": 123 }`. A selected installation is
immutable; connect again to use a different installation. Binding input requires
`github: { "repositoryIds": [456], "permissions": { "contents": "read" } }`.
Supported permission keys are `contents`, `pull_requests`, and `issues`, each
`read` or `write`; omit a permission for no access. Metadata read is implicit.
The Broker rejects an empty repository list, duplicates, more than 500 IDs,
unknown permissions, and grants exceeding current installation authority.

## Internal credential issuance

The three internal routes are registered only on the direct mTLS listener:

- `POST /internal/v1/service-authorizations/list`
- `POST /internal/v1/service-authorizations/check`
- `POST /internal/v1/service-credentials/issue`

Input contains `callerProof`, `connectionId`, `service`, `contractVersion`, and
(for issuance) connection/binding revisions. It contains no REST arguments or
operation policy. The Broker independently TokenReviews the Pod-bound proof,
checks the live Pod and ServiceAccount, follows the namespace Agent owner
reference to a named Agent GET, validates its immutable UID, owner and binding,
and resolves the service from the trusted profile. Identity, connection and
binding are rechecked
after OAuth refresh or app-only/OBO acquisition before returning a credential.

Independent Azure uses the platform-derived tenant, child Agent client ID and
principal ID. Its two-step autonomous OAuth exchange obtains an ARM app-only
token; it never falls back to a personal account. Personal Azure and Microsoft
Graph retain the Entra Auth SDK OBO flow and require a ready Agent Identity.
GitHub supports personal OAuth or App installation credentials; Google uses
personal OAuth. These connections need only a supported ready workload.
Returned tokens exist only in the trusted MCP process for execution.

HTTP `:8080` serves Portal/Operator mesh APIs; HTTPS `:8443` serves MCP internal
APIs. The latter validates a client certificate with exactly the configured
MCP ServiceAccount SPIFFE URI SAN, never an XFCC header. Configure `tls.enabled`,
server certificate and client CA, plus MCP's client certificate and Broker CA.
Set the Broker chart's `workloadTokenAudience` to the same value as the MCP
chart's `workloadTokenAudience` and the Operator chart's `connectionsMCP.audience`.
All three default to `cetirun-connections-mcp`; a custom audience must match across
all three releases because both MCP and the Broker independently verify the
Agent's projected workload token.

## Availability and identity modes

Both chart switches default to `true`:

```yaml
identityModes:
  azureAgent:
    enabled: true
  microsoftDelegated:
    enabled: true
```

They map to `AZURE_AGENT_ENABLED` and `MICROSOFT_DELEGATED_ENABLED`. Disabling a
mode blocks new connections and credential issuance for that mode; it does not
delete saved Connections or bindings. The blueprint client ID is required when
either Entra mode is enabled; the authorization client ID is required only for
Microsoft delegated mode. Common tenant and Key Vault encryption configuration
remains required, including for GitHub/Google-only deployments.

Disabling Microsoft delegated mode removes the Auth SDK container and its
scratch volumes from the chart. Broker `/readyz` reports local readiness without
calling the SDK. SDK request failures affect delegated Azure/Microsoft Graph
issuance, not independent Azure or GitHub/Google flows. When enabled, the SDK
still shares the Broker Pod: scheduling, resource exhaustion and container/Pod
failure can affect the deployment. This change is not Pod-level isolation.

Broker TLS and MCP serving/client TLS reload mounted material every five
seconds without a restart. Invalid updates retain the previous valid snapshot;
expired leaf material fails new TLS handshakes or outbound requests. Rotate CAs
by distributing old+new trust first, then replacing leaf certificates, then
removing old trust. Allow for Kubernetes projection and poll delays. In-flight
requests and established server connections can finish. See the
[MCP trust boundary](../cetirun-connections-mcp/README.md#execution-and-trust-boundaries)
for mount and rollover details. Broker shutdown cancels reload/maintenance loops
and allows HTTP requests up to ten seconds to drain.

## Shared GitHub App

Configure `providers.github.app` with `id`, `slug`, `clientId`, and
`credentialsSecretName`. The Secret contains `client-secret` and `private-key.pem`;
only the Broker mounts the private key. Partial App configuration is rejected,
while an entirely absent configuration leaves personal OAuth available.
See the [registration and user guide](../../site/content/en/docs/guides/github-app.md)
for exact callback URLs, App permissions, rotation, and trust boundaries.

App connections store the verified GitHub user's refreshable authorization in
the existing encrypted credential store. That credential verifies installation
access and repository ownership/Admin authority; it is never returned to MCP.
Each execution issuance rechecks live GitHub authority and signs a new
installation token with explicit repository IDs and permission limits. No
installation token cache or webhook is used. Binding revisions are checked again
after acquisition, and there is no fallback to personal OAuth.

The platform's App key can mint tokens up to the full installation grant.
Per-Agent limits protect normal execution but do not remove trust in platform
administrators. GitHub sees the App bot; internal audit distinguishes Agents.

Selecting an installation reuses the Connection for the same CetiRun owner,
verified GitHub user ID and stable installation ID. App renames do not create a
new connection; GitHub verifies which App owns the installation. A conditional
index serializes concurrent selections. Healthy existing credentials and Agent grants are
preserved; each Agent still has its own repository and permission selection.
Personal OAuth connections, different users and different installations remain
separate. A temporary sign-in record is consumed only after reuse succeeds;
retrying its selection returns the completed result. Historical duplicates keep
their IDs and bindings, and `canonicalConnectionId` identifies the connection
offered for new access. Connections labels these historical duplicates
and retains their usage and removal controls. Removing an unused duplicate does
not revoke the provider authorization or uninstall the App.

## Personal OAuth

The callback is `/api/v1/connections/callback`. OAuth transactions bind owner,
state, nonce and PKCE. GitHub/Google additionally require a Portal-generated
same-browser proof stored in a state-specific Secure HttpOnly host cookie.
Reconnect must preserve the original provider account; changing accounts needs
Connect. Single-use state and storage CAS protect concurrent callbacks.
Transactions are separate encrypted records and start responses include an
`authorizationId`. The popup includes this ID; the Portal validates its origin,
window source and ID, then fetches the owner-scoped result before selecting an
account. Only explicit confirmation creates an Agent binding.

A conditional account index keys owner tenant/object IDs, provider and the
provider-verified stable account ID. Concurrent logins select one Connection ID;
healthy repeat logins preserve credentials and bindings. Reauthorization keeps
old credentials until successful sign-in to the same account. Explicit refresh
`invalid_grant` marks sign-in required; transport failures do not. Old duplicate
Connections retain their IDs, with `canonicalConnectionId` projected for selection.
Expired transactions are removed at startup, every five minutes, and on the
owner's next sign-in, once one hour has elapsed after transaction expiry.
Cleanup scans paginated authorization Secrets in the Broker credential
namespace, checks encrypted record identity and uses UID/resourceVersion delete
preconditions. Corrupt records are skipped with a warning; concurrent cleanup
conflicts are harmless. Connections, account indexes, references and revocation
tombstones are outside this sweep.

GitHub uses an OAuth App with `repo` scope, configured by
`providers.github.credentialsSecretName` (`client-id`, `client-secret`). Calls
execute as the connected user. OAuth and repository/organization permissions
apply in full; CetiRun does not narrow them to repositories or operations.
The personal OAuth mode does not use App installations. Non-expiring GitHub tokens are
explicitly marked; internal issuance still has a finite five-minute validity.

Google uses a Web OAuth client configured by
`providers.google.credentialsSecretName` with the same keys. Enable Gmail and
Calendar APIs and register the exact Portal callback URI. Authorization requests
openid/email, Gmail read/send and Calendar with offline access and explicit
consent. Initial/reconnect exchange requires a refresh token. Refresh preserves
an existing refresh token or scope set when the provider omits replacements.
Actual returned scopes are retained and displayed; they are not translated into
a fixed operation catalog. Google's testing-mode token expiry still applies.

## Storage and lifecycle

| Record | Storage | Owner |
| --- | --- | --- |
| ConnectionBinding | Agent workload namespace | Agent UID |
| Independent Azure Connection | Agent workload namespace | Agent UID |
| Credential-free delegated connection reference | Agent workload namespace | Agent UID |
| Personal OAuth Connection | Broker credential namespace | Portal owner |
| OAuth client configuration / Broker TLS | Broker namespace | Platform |

Per-Agent storage is mandatory for this contract. There is no migration command
or compatibility reader for the previous grant model. The Broker resolves the
namespace from live Agent state and checks its owner reference. Encrypted
records bind owner, immutable IDs and revisions. Labels and display snapshots
support inventory only and cannot establish authority. Workload authentication
uses namespace ownership and named Agent reads, without a cluster-wide Agent
list fallback. Management inventory scans use pages of 50 Agents, with a 1 MiB
response bound per page; a failed, oversized or repeated page fails the entire
scan rather than returning partial results.

The Operator supplies Broker Secret RBAC in ordinary managed Agent namespaces.
This is namespace-wide Secret access, not label-scoped isolation. Records are
never mounted in Agent workloads. The Operator cleanup endpoint deletes checked
records with UID/resourceVersion preconditions before releasing the Agent
finalizer. Namespace/Agent garbage collection also owns their lifetime. Personal
credentials survive deletion of any individual Agent. Unsupported runtimes,
including Sandbox, do not receive Broker Secret access.

## Audit

Broker emits structured started/completed events for OAuth start/reauthorize/
completion, independent identity connection, binding connect/update/disconnect,
account removal, Operator cleanup and internal credential operations. Events
include correlation ID, duration, HTTP status, outcome and verified owner/target
IDs when available. A confirmed storage commit is recorded as succeeded;
ambiguous storage failures after a mutation starts are `execution_unknown`.
An OAuth error page is not a successful account connection. An interrupted
request may have only a started event, so logs are not a durable transaction
ledger. Queries, credentials, request/response bodies and headers are not logged.

MCP audit adds the validated provider method/path, stable executor account ID,
Agent and connection/binding revisions, and upstream results. MCP uses the same
correlation for its Broker calls and provider request. These IDs correlate
observations; they do not deduplicate execution.

## Offline checks

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
bash ../../scripts/cetirun-agent-broker-helm-test.sh
```

No live provider or Kubernetes access is needed for these checks.

Account removal requires no active binding and no pending binding registration.
Each binding write first reserves its own token on the Connection with Kubernetes
resourceVersion CAS. After the namespace Binding commits, superseded tokens are
released; Disconnect persists a tombstone before releasing its token. Usage
checks include deleting and unsupported Agents. Interrupted registrations are
reclaimed only after a Binding revision fence proves a delayed write cannot
commit. Personal account deletion uses the same Connection CAS, preventing a
check-then-delete race. Local removal never revokes provider-wide OAuth consent.
