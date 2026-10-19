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
Broker permission catalog or Portal operation editor. Runtime skills describe
API versions, bodies, pagination and polling. Skills do not confer authority.
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
account or principal, service, correlation, platform rejection or provider
status and request IDs. Credentials, request paths/query/body and response
bodies are not logged by default. Injected bearer credentials are never returned
to Agents, even if a provider echoes one in JSON.

## Runtime and trust boundaries

MCP protocol `2025-11-25` runs over HTTPS `/mcp`. Native clients send the rotating
Pod-bound proof from `/var/run/cetirun/tools/token` with audience
`agent-tools-mcp`. MCP verifies live Pod/ServiceAccount/Agent state; the Broker
independently repeats verification. Broker list/check/issue use direct mTLS.
Check and issuance must agree on connection and binding revisions. No cross-call
authorization or provider-token cache is maintained by MCP.

Set `AGENT_BROKER_BASE_URL` to an HTTPS origin. The chart mounts the MCP server
TLS Secret and `broker.clientTLSSecretName` containing `tls.crt`, `tls.key`,
`ca.crt`. The client certificate has the dedicated ServiceAccount's exact
SPIFFE URI SAN. Istio is an additional network boundary. Kubernetes RBAC allows
TokenReview and Pod/ServiceAccount/Agent reads only; MCP has no Secret API access
and no operation-record namespace.

## Offline checks

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
make chart-lint
```

These checks use synthetic providers and Kubernetes fixtures. They do not
qualify live Azure, GitHub, Google, Microsoft Graph or native Agent behavior.
