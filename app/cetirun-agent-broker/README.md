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

GitHub App user OAuth and Google OAuth use client credentials injected only from
the configured chart Secret references. If they are not configured, their
connection start request fails closed. Microsoft Azure and Graph credentials use
the local Entra Auth SDK OBO boundary; the chart configures only the fixed ARM
and Microsoft Graph downstream services. Microsoft Connection capabilities are
mapped from the deployment's `agentIdentity.armPermissions` and
`agentIdentity.graphPermissions`, which must match the configured application
consent. The intermediate `access_agent` assertion is not a downstream scope
source. Each actual OBO token must include the tool's required scope before it
is issued; revocation, revisions, expiry, and all three owner bindings are
rechecked after refresh/OBO and before returning it.
