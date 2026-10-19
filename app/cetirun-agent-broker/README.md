# CetiRun Agent Connection Broker

The Broker manages owner-scoped Connections and explicit per-Agent connection
bindings. Azure RBAC and personal-provider OAuth permissions are the resource
authorization authorities. There are no business-level AgentGrants, resource
allowlists, per-tool permissions or prerequisite Azure RBAC verification.

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

## Internal credential issuance

The three internal routes are registered only on the direct mTLS listener:

- `POST /internal/v1/service-authorizations/list`
- `POST /internal/v1/service-authorizations/check`
- `POST /internal/v1/service-credentials/issue`

Input contains `callerProof`, `connectionId`, `service`, `contractVersion`, and
(for issuance) connection/binding revisions. It contains no REST arguments or
operation policy. The Broker independently TokenReviews the Pod-bound proof,
checks the live Pod/ServiceAccount/Agent, owner and binding, and resolves the
service from the trusted profile. Identity, connection and binding are rechecked
after OAuth refresh or app-only/OBO acquisition before returning a credential.

Independent Azure uses the platform-derived tenant, child Agent client ID and
principal ID. Its two-step autonomous OAuth exchange obtains an ARM app-only
token; it never falls back to a personal account. Personal Azure and Microsoft
Graph retain the Entra Auth SDK OBO flow and require a ready Agent Identity.
GitHub and Google use personal OAuth credentials and need only a supported ready
workload. Returned tokens exist only in the trusted MCP process for execution.

HTTP `:8080` serves Portal/Operator mesh APIs; HTTPS `:8443` serves MCP internal
APIs. The latter validates a client certificate with exactly the configured
MCP ServiceAccount SPIFFE URI SAN, never an XFCC header. Configure `tls.enabled`,
server certificate and client CA, plus MCP's client certificate and Broker CA.

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
Expired transactions are removed on the owner's next sign-in after a one-hour
result retention period.

GitHub uses an OAuth App with `repo` scope, configured by
`providers.github.credentialsSecretName` (`client-id`, `client-secret`). Calls
execute as the connected user. OAuth and repository/organization permissions
apply in full; CetiRun does not narrow them to repositories or operations.
GitHub App installation identities are not used. Non-expiring GitHub tokens are
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
support inventory only and cannot establish authority.

The Operator supplies Broker Secret RBAC in ordinary managed Agent namespaces.
This is namespace-wide Secret access, not label-scoped isolation. Records are
never mounted in Agent workloads. The Operator cleanup endpoint deletes checked
records with UID/resourceVersion preconditions before releasing the Agent
finalizer. Namespace/Agent garbage collection also owns their lifetime. Personal
credentials survive deletion of any individual Agent. Unsupported runtimes,
including Sandbox, do not receive Broker Secret access.

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
