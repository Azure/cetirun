# Agent Tools MCP

A stateless Streamable HTTP MCP service for JSON REST access through trusted
Agent connections. Provider RBAC and OAuth permissions determine resource
access. CetiRun checks which Agent can use a connection and its services; it
does not maintain a second per-operation or per-resource permission catalog.

## Tools and request contract

- `connections_status`: this Agent's enabled bindings, accounts, identity mode,
  services, actual reported OAuth scopes and connection status; no credentials.
- `connections_start_link`: the configured Portal URL for connection management.
- `services_list`: bound services, their fixed origins and base paths.
- `service_request`: execute one JSON REST request.

Example arguments:

```json
{
  "connection_id": "example-connection",
  "service": "azure_arm",
  "method": "POST",
  "path": "/providers/Microsoft.ResourceGraph/resources",
  "query": {"api-version": "2022-10-01"},
  "body": {"query": "Resources | project id, name | take 10"}
}
```

All paths start with `/` and are relative to the service's base path. Query
values are strings or arrays of strings. Headers are optional and restricted
to `If-Match`, `If-None-Match`, `Prefer`, `ConsistencyLevel`, and
`X-GitHub-Api-Version`. `body` is an optional JSON value, limited to 256 KiB;
GET and HEAD bodies are rejected. Methods are GET, HEAD, POST, PUT, PATCH,
DELETE and OPTIONS. HTTP method is not a permission classification.

| Service | Origin | Base path |
| --- | --- | --- |
| `azure_arm` | `https://management.azure.com` | `/` |
| `github` | `https://api.github.com` | `/` |
| `microsoft_graph` | `https://graph.microsoft.com` | `/v1.0/` |
| `google_gmail` | `https://gmail.googleapis.com` | `/gmail/v1/` |
| `google_calendar` | `https://www.googleapis.com` | `/calendar/v3/` |

The shared `app/internal/agenttools` package fixes services, providers and token
audiences. A new JSON API route under an existing service requires no MCP tool,
Broker permission catalog or Portal operation editor. The runtime's
`connected-services` skill provides the shared request workflow and references
for Azure, GitHub and Google API versions, bodies, pagination and polling.
References are read only for the selected service. Skills do not confer authority.
Binary transfer, streaming and multipart requests are outside v1.

Results contain `status` (provider HTTP status), `body`, filtered `headers`,
`request_ids`, and a CetiRun `correlation_id`. Provider 401/403 errors remain
provider results; platform identity or binding failures are separate tool
errors. External data is untrusted. Pagination fields are preserved.

## Execution guarantees

The Agent cannot choose an origin, token audience, OAuth scopes or authentication
header. Encoded traversal, scheme-relative/absolute URLs, redirects, proxy
headers, cookies and authentication overrides are rejected. The production
transport validates public DNS answers, dials a checked IP without another
lookup, verifies TLS, disables proxies, HTTP/2 and connection reuse, and does
not automatically retry any request. This includes POST queries.

Each request is bounded by a 20-second provider timeout, 256 KiB response body
and 32 KiB response headers. The whole MCP call has a 30-second deadline.
Pagination and async polling are separate calls, subject to the same checks.
Convert a provider next link to the discovered service's relative path/query
only after checking its origin and base path; never pass an absolute URL.

`execution_unknown` means an effect may already have occurred, including when
reading a response failed or exceeded the limit. Inspect provider state before
considering another call. There is no operation ledger or exactly-once promise;
a correlation ID is not an idempotency key. Provider-specific conditional
requests may be used where documented.

Audit records identify the real Agent, connection/binding revisions, executor
account display and stable provider account ID or principal, service, HTTP
method and validated target path (including the service base path). Records
include duration, platform/provider stage, started/completed phase, outcome,
provider status and request IDs. The MCP-generated correlation ID is forwarded
to Broker check/issue as `X-Correlation-ID` and to the provider as
`X-CetiRun-Correlation-ID` (plus native tracing headers for Azure/Graph).
Providers may ignore tracing headers; use their returned request IDs for
external log lookup.
A provider-stage start without a completion does not prove failure: execution
may have occurred. Logs are observations, not a durable execution ledger.

Query strings, request/response bodies, credentials and caller-supplied headers
are excluded from audit. Paths can identify resources, accounts or mailboxes;
restrict access to these logs accordingly. Injected bearer credentials are
never returned to Agents, even if a provider echoes one in JSON. This protects
the execution credential; provider-authorized APIs may still return other
secrets, such as resource access keys. Provider permissions must account for
that access.

## Runtime and trust boundaries

MCP protocol `2025-11-25` runs over HTTPS `/mcp`. Native clients send the rotating
Pod-bound proof from `/var/run/cetirun/tools/token` with audience
`agent-tools-mcp`. MCP verifies the live Pod and ServiceAccount, follows the
workload namespace's Agent owner reference, then reads that named Agent and
compares its UID, namespace, runtime state and ServiceAccount. The Broker
independently repeats verification. Missing, stale or ambiguous ownership fails closed; normal
workload requests never fall back to listing all Agents. Broker list/check/issue
use direct mTLS. Check and issuance must agree on connection and binding
revisions. No cross-call authorization or provider-token cache is maintained
by MCP.

Set `AGENT_BROKER_BASE_URL` to an HTTPS origin. The chart mounts the MCP server
TLS Secret and `broker.clientTLSSecretName` containing `tls.crt`, `tls.key`,
`ca.crt`. The client certificate has the dedicated ServiceAccount's exact
SPIFFE URI SAN. Istio is an additional network boundary. Kubernetes RBAC allows
TokenReview and named Pod/ServiceAccount/Namespace/Agent reads only; MCP has no
Secret API access and no operation-record namespace.

MCP and Broker validate TLS material at startup and poll their mounted
certificate/key/CA files every five seconds. Complete valid snapshots replace
identity and trust atomically. Invalid updates retain the last valid snapshot;
expired leaf material fails new handshakes and outbound requests. New handshakes
use current material, while the MCP Broker client replaces old idle connection
pools on rotation. In-flight requests can complete; rotation is not immediate
revocation of established connections.

For CA rollover, distribute a bundle containing both old and new CAs to all
peers first, wait for projected mounts and reloads, rotate leaf certificates,
then remove the old CA after all peers trust and present the new material.
Keep certificate/key pairs in the same projected Secret and avoid `subPath`
mounts, which do not receive Secret updates. Agent-side trust distribution is
separate from the Broker/MCP reload loops.

## Offline checks

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
make chart-lint
```

These checks use synthetic providers and Kubernetes fixtures. They do not
qualify live Azure, GitHub, Google, Microsoft Graph or native Agent behavior.
