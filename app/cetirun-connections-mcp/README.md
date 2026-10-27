# CetiRun Connections MCP

CetiRun Connections MCP is a stateless Streamable HTTP MCP service that executes JSON
REST requests through a workload-bound Connection. It verifies the
calling Agent workload, asks the Agent Identity Broker to check the Agent's
enabled connection binding, obtains a short-lived token, executes the
fixed-service REST request, and returns the provider result. The
credential never enters Agent context, logs, files, command arguments, or tool
results.

Services include Azure Resource Manager, GitHub, Microsoft Graph, Gmail and
Google Calendar. Provider RBAC, OAuth permissions, and GitHub App installation
permissions determine resource access. GitHub App bindings include explicit
repositories and permissions; the Broker narrows each installation token to
that grant. MCP checks this metadata across authorization and issuance without
maintaining a REST operation permission catalog.

## Tools and request contract

- `connections_status`: enabled bindings, account or principal, and status;
  never credentials.
- `connections_start_link`: configured Portal URL for account and Agent connection management.
- `services_list`: enabled services and their fixed origin/base path.
- `service_request`: execute one JSON REST request.

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

`azure_arm` always targets `https://management.azure.com/`. Paths begin with
`/` and are relative to that base path. Query values are strings or arrays of
strings. The optional headers are `If-Match`, `If-None-Match`, `Prefer`,
`ConsistencyLevel`, and `X-GitHub-Api-Version`.
The JSON body is limited to 256 KiB; GET and HEAD bodies are rejected. Methods
are GET, HEAD, POST, PUT, PATCH, DELETE, and OPTIONS. HTTP method does not
classify permission.

Results contain provider `status`, `body`, filtered `headers`, `request_ids`,
and a CetiRun `correlation_id`. Provider 401/403 responses remain provider
results; workload and binding failures are separate tool errors. External data
is untrusted.

## Execution and trust boundaries

The service rejects traversal, absolute or scheme-relative URLs, redirects,
proxy headers, cookies, authentication overrides, unsafe DNS answers and
oversized requests or responses. Its transport validates public DNS results,
dials the checked address, verifies TLS, disables proxies, HTTP/2 and
connection reuse, and never retries automatically. Every provider call has a
20-second timeout; each MCP call has a 30-second deadline.

`execution_unknown` means an effect might already have occurred, including when
the response cannot be read safely. Inspect provider state before another call. A
correlation ID supports tracing only; it is not an idempotency key.

MCP runs over HTTPS `/mcp`. Native clients present a rotating Pod-bound proof
for audience `cetirun-connections-mcp`. MCP validates the live Pod, ServiceAccount,
namespace Agent owner reference and Agent state. The Broker repeats this
verification and independently checks the owner-scoped connection and Agent binding. MCP checks
that Broker check and issuance responses agree on connection and binding
revisions before sending a request.

Workload authentication failures return HTTP 401 with `unauthenticated` when
the proof is invalid or its live binding is missing, changed or inactive.
Kubernetes API timeouts, failed API requests, invalid responses, and
unavailable server credentials return HTTP 503 with
`dependency_unavailable`. These failures stop the request before Broker or
provider execution; they do not require reconnecting the Agent account.
Failed notifications use the same HTTP status without a JSON-RPC body.

Authenticated `ping` requests return `result: {}` with the same request ID.
They use the same workload proof and protocol-version validation, without
contacting the Broker or Azure.

The chart requires an MCP server TLS Secret (`tls.crt`, `tls.key`) and a
dedicated Broker client TLS Secret (`tls.crt`, `tls.key`, `ca.crt`). The server
certificate must cover the MCP service hostname used by clients. The Broker
client certificate must carry the URI
`spiffe://cluster.local/ns/<mcp-namespace>/sa/<mcp-serviceaccount>`; its CA must be
trusted by the Broker, and its `ca.crt` must trust the Broker server. Configure
the Broker's `toolService` identity and workload audience to match this chart,
following the [Broker prerequisites](../cetirun-agent-broker/README.md#internal-credential-issuance).

Mount whole Secrets, without `subPath`, so valid certificate and CA replacements
can reload for new connections. Invalid replacements retain the last valid
material; established connections and issued provider tokens are not immediately
revoked. MCP can create TokenReviews and read named workload identity objects;
it has no Secret API access. Its release namespace must differ from the Broker
namespace.

Audit records identify the Agent, connection and binding revisions, Azure
principal, request method, target path, duration, outcome and Azure request
IDs. They exclude query strings, request and response bodies, supplied headers
and credentials. The injected bearer token is never returned, including when a
provider echoes it. Azure APIs can still return other sensitive resource data,
such as access keys, when Azure RBAC permits that action; assign Azure roles
with that possibility in mind.

## Deployment configuration

Follow the [Agent Identity setup guide](../../site/content/en/docs/operations/agent-identity.md)
for the complete Broker/MCP certificate and trust layout, platform values, and
provider preparation before enabling this service.

The image, standalone Helm chart, default ServiceAccount, and workload token
audience use `cetirun-connections-mcp`. The platform umbrella chart enables this
component through `cetirun-connections-mcp.enabled`.

Configure the Operator and runtime charts through `connectionsMCP`. The Operator
reads `CONNECTIONS_MCP_*` environment variables; native OpenClaw and Hermes
clients use `CETIRUN_CONNECTIONS_MCP_URL` and the `cetirun-connections` MCP server
entry. Workloads mount their rotating proof at
`/var/run/cetirun/connections/token` and their MCP CA at
`/var/run/cetirun/connections-ca/ca.crt`.

Deploy the renamed MCP service, Broker configuration, Operator, and runtime
assets together. Configure the server certificate for the new service DNS name
and the Broker client certificate for the matching ServiceAccount SPIFFE URI.
Recreate Agent workloads with the new configuration, projected token audience,
and mount paths. Previous component and configuration names have no aliases.
Connection records and provider authorization grants do not require migration.

## Offline checks

```bash
go test ./... -count=1
go test -race ./...
go vet ./...
make chart-lint
```

Checks use synthetic Azure and Kubernetes fixtures. They do not qualify a live
Azure tenant, cluster, Portal UI, or native runtime bootstrap.
