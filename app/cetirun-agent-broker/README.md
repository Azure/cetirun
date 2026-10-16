# CetiRun Agent Tools Broker

The Broker owns owner-scoped provider connections and explicit per-instance
tool grants. It encrypts Connection credentials and AgentGrant records in its
private Kubernetes Secret namespace, uses `resourceVersion` CAS for all
updates, and retains revocation tombstones. Agent workloads cannot call this
service to obtain provider credentials.

Portal management endpoints require both the Portal mesh workload identity and
the verified owner headers `X-CetiRun-Owner-Tenant-ID` and
`X-CetiRun-Owner-Object-ID`. Grant operations additionally require
`X-CetiRun-Instance-UID`. The public JSON views contain no provider tokens:

- `GET /v1/connections`
- `POST /v1/connections/start`
- `GET|DELETE /v1/connections/{id}`
- `POST /v1/connections/{id}/reconnect`
- `GET|POST /v1/instances/{name}/tool-grants`
- `PUT|DELETE /v1/instances/{name}/tool-grants/{id}`

The trusted Agent Tools MCP Service is the only internal credential caller. It
must authenticate through Istio mTLS as `TOOL_SERVICE_NAMESPACE` /
`TOOL_SERVICE_ACCOUNT`, then send its validated, rotating Kubernetes workload
token as `callerProof`. The Broker independently TokenReviews the proof for
`WORKLOAD_TOKEN_AUDIENCE` (default `agent-tools-mcp`), verifies the current
ServiceAccount and Pod UID, resolves the active Agent binding, and checks the
Connection, Grant, revisions, fixed tool catalog (`2026-10-10`), and resource
constraints before returning a short-lived access token. The supported routes
are:

- `POST /internal/v1/tool-authorizations/list`
- `POST /internal/v1/tool-authorizations/check`
- `POST /internal/v1/tool-credentials/issue`

GitHub and Google grants use a ready, non-suspended workload binding and do
not require Entra Agent Identity. Azure and Microsoft OBO issuance additionally
requires a ready Agent Identity and its client ID. Every Agent resource must
retain the trusted owner annotations even when Entra Agent Identity is disabled.

No `/v1/auth/*` or `/v1/credentials/issue` route exists. Refresh tokens and
authorization codes never cross the Broker boundary.

The fixed Portal callback is `GET|POST /api/v1/connections/callback`. It is
bounded by the one-time OAuth state transaction, which contains the owner
binding and CSRF nonce; callback requests do not carry owner headers.

## TLS and service binding

The chart keeps HTTP `:8080` for Portal and Operator mesh clients. These routes
use the proxy-verified XFCC identity and never expose the MCP internal APIs.
Set `tls.enabled=true` and provide `tls.secretName` with the server
`tls.crt`/`tls.key` and client trust bundle `ca.crt` (or the configured keys).
The HTTPS listener on `:8443` is exposed as Service port `443` and registers
only the three MCP internal APIs. It requires a directly verified client
certificate; XFCC is never accepted on this listener.

The MCP service should use `https://<broker-service>` as its
`AGENT_BROKER_BASE_URL`. Agents read their projected caller proof from
`/var/run/cetirun/tools/token` with audience `agent-tools-mcp`; the MCP server
forwards the request's proof without mounting that Agent file. The Broker does
not accept this URL, an owner, an instance UID, a provider endpoint, or an OAuth
scope from an Agent-facing request.

The MCP worker must mount a client certificate/key and the Broker server CA for
this HTTPS connection. Its client certificate must have exactly this URI SAN:
`spiffe://cluster.local/ns/<TOOL_SERVICE_NAMESPACE>/sa/<TOOL_SERVICE_ACCOUNT>`.
The Broker verifies the chain against `TLS_CLIENT_CA_FILE` and requires that SAN
before it TokenReviews `callerProof`.

GitHub connections use a standard OAuth App to act as the connected personal
account. Configure `GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`
through the Broker chart's `providers.github.credentialsSecretName` with keys
`client-id` and `client-secret`. GitHub App installation IDs, private keys and
bot identities are not supported. Existing GitHub `mode=agent` records are
rejected; users must create a personal connection and a separate instance grant.

GitHub connection start and reconnect require a Portal-generated browser
binding. The Broker stores its hash in the OAuth transaction and checks a
matching proof from the trusted Portal before exchanging the callback code.
The Portal keeps the proof in a state-specific Secure, HttpOnly host cookie;
it is separate from OAuth state and PKCE and is never returned in JSON.

OAuth requests `repo` scope for private repository access. This provider scope
is broader than individual tools: CetiRun enforces repository, tool and PR base
branch restrictions on every call, with writes explicitly enabled by the owner.
A CetiRun grant cannot increase the user's GitHub permissions. GitHub records
the personal user as the actor; CetiRun correlates the Agent UID, connection ID,
grant revision and provider request IDs.

GitHub OAuth tokens may have no provider expiry. The Broker records that case
explicitly rather than inventing an expiry or refresh token; every tool issuance
still has a finite internal validity bounded by the grant. This internal
validity does not change the token's lifetime at GitHub. Disconnect/revoke stops
new CetiRun calls; external OAuth consent is revoked separately in GitHub.
Provider credentials remain encrypted at rest and never enter Agent workloads.

GitHub and Google OAuth client credentials are injected only from configured
chart Secret references. When absent, connection start requests fail closed. Microsoft Azure and Graph credentials use
the local Entra Auth SDK OBO boundary; the chart configures only the fixed ARM
and Microsoft Graph downstream services. Microsoft Connection capabilities are
mapped from the deployment's `agentIdentity.armPermissions` and
`agentIdentity.graphPermissions`, which must match the configured application
consent. The intermediate `access_agent` assertion is not a downstream scope
source. Each actual OBO token must include the tool's required scope before it
is issued; revocation, revisions, expiry, and all three owner bindings are
rechecked after refresh/OBO and before returning it.

## Agent namespace storage and lifecycle

Enable `agentRecordNamespaces: true` only after configuring the Operator's
`toolsMCP.brokerNamespace` and `toolsMCP.brokerServiceAccount` and migrating any
existing records. The corresponding Broker environment variable is
`AGENT_RECORD_NAMESPACES`. The default remains false to avoid silently switching
existing installations before migration.

| Record | Storage | Lifetime |
| --- | --- | --- |
| AgentGrant | Agent workload namespace | Agent-owned |
| Independent Azure Connection | Agent workload namespace | Agent-owned |
| Delegated connection reference | Agent workload namespace | Agent-owned, no credential data |
| Personal OAuth Connection | Broker credential namespace | Owner-managed; shared by Agents |
| OAuth App configuration and Broker TLS | Broker namespace | Platform-managed |

A delegated reference uses the familiar `agent-tools-connection-<id>` Secret
name and `record-type=connection-reference`, with no Secret data. It is an
inventory link, not an authorization source. Grants and the decrypted shared
Connection still determine permissions. The Broker never copies a personal
token into an Agent namespace or mounts these records into Agent workloads.

The Broker resolves namespaces from live Agent UID/status and verifies the
namespace's Agent owner reference. Local records also carry an Agent UID owner
reference. Encryption, immutable IDs, business revisions, owner checks and CAS
updates are preserved. Namespaced reads never fall back to legacy central
Grants or independent Connections. User-level Connection APIs aggregate the
owner's shared Connections and independent Connections from owned Agent
namespaces without cluster-wide Secret permissions. Secret LISTs are paginated.

The Operator grants the Broker Secret access using RoleBindings in ordinary
managed Agent namespaces, including runtimes with tools disabled so deletion
can still clean records. Kubernetes RBAC cannot constrain Secret LIST/create
by label or name prefix: this permission covers all Secrets in those namespaces.
The Broker remains a trusted platform component; record labels are inventory
metadata, not an RBAC isolation boundary. Disabling runtime/configuration does not remove an
existing binding prematurely: finalizer cleanup may still need it, and Agent
namespace GC owns its lifetime. The Broker rejects new instance grants and
connections when the Agent is deleting. The Operator's authorization cleanup
physically deletes verified instance records using Secret UID/resourceVersion
preconditions, including legacy central records. Shared credentials survive;
external Entra identity cleanup remains an explicit finalizer operation.
Unsupported runtimes, including Sandbox, never receive this Secret access.
Their deletion still cleans legacy central records; any Agent-owned local
records from a previous supported runtime are left to Agent/namespace GC.

### Migration

`agent-broker migrate-agent-records` is an explicit maintenance command. It must
run with **every Broker replica stopped**, under the Broker service identity,
after per-Agent RBAC is ready. It does not run automatically at startup.

1. Capture encrypted source backups, release values and rollback artifacts.
2. Quiesce Broker replicas and prevent another deployment from starting writers.
3. Run the command with `AGENT_RECORD_NAMESPACES=true` and the existing Broker
   workload identity/Key Vault/Kubernetes configuration.
4. The command decrypts and verifies source ownership, copies instance records
   with their original IDs/revisions, verifies destinations, creates delegated
   references and deletes sources with UID/resourceVersion preconditions.
5. Start the Broker with namespaced storage enabled and verify existing grants.

Matching destinations resume after interruption; divergent data, missing Agent
owners, invalid namespace ownership and concurrent source changes fail closed.
Shared personal credentials are retained. Migration output contains aggregate
counts only. Keep backups private. After the new layout accepts writes, rolling
back an image alone is unsafe: reverse migration or restoring an appropriate
backup while quiesced is required. Kubernetes namespace deletion does not revoke
already issued provider tokens or clean external directory/RBAC resources.

### Inspecting records by Agent

Use the read-only [inventory helper](../../scripts/README.md#agent-tool-authorization-metadata-inventory)
from the repository root:

```bash
python3 -B scripts/agent-tools-inspect.py --kubeconfig "$TEST_KUBECONFIG"
python3 -B scripts/agent-tools-inspect.py --kubeconfig "$TEST_KUBECONFIG" --agent my-agent
kubectl --kubeconfig "$TEST_KUBECONFIG" -n cetirun-my-agent get secrets \
  -l 'agenttools.cetirun.microsoft.com/record-type in (connection,connection-reference,grant)' \
  -L agenttools.cetirun.microsoft.com/record-type,agenttools.cetirun.microsoft.com/instance-name
```

The inventory lists namespaces and UID-linked records, including potentially
revoked/expired grants; it does not establish effective permissions. Agent name
and namespace annotations are display snapshots only. Short DNS-label values
are also labels; longer names remain in annotations. `--agent` scopes collection
to that Agent plus central credentials, so other Agents' references are explicitly
outside the filtered inventory. See the script documentation for RBAC and
client-side JSONPath projection limits.
