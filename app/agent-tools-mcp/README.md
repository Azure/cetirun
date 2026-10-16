# Agent Tools MCP

`agent-tools-mcp` is the standalone, stateless Streamable HTTP MCP
server for CetiRun's fixed Azure, GitHub, Microsoft, and Google tools. It
accepts only Kubernetes Pod-bound workload tokens at `POST /mcp`, validates the
current Pod, ServiceAccount, and Agent binding, and asks the Agent Broker to
recheck each tool authorization and issue a per-call, in-memory provider token.

The server does not read Broker credential Secrets, store provider credentials,
or expose a credential endpoint to Agent workloads. It has no cross-call access
token or authorization cache. Provider endpoints are fixed in code; redirects
and arbitrary provider URLs are rejected.

## Runtime contract

- MCP protocol: `2025-11-25`, Streamable HTTP, `initialize`, `tools/list`, and
  `tools/call` at HTTPS `/mcp`; no authorization session state is retained.
- Agent clients read their projected token from
  `/var/run/cetirun/tools/token` and send it as `Authorization: Bearer ...`.
  The service never reads that Agent file; it forwards the received proof only
  to the Broker internal APIs for independent TokenReview verification.
- `AGENT_BROKER_BASE_URL` is required and must be an HTTPS origin.
  `WORKLOAD_TOKEN_AUDIENCE` defaults to `agent-tools-mcp`.
- The server itself uses a separate projected Kubernetes API token at
  `/var/run/cetirun/kubernetes/token`. `TLS_CERT_FILE` and `TLS_KEY_FILE` are
  required for the HTTPS listener, defaulted by the chart to mounted secret
  files.

## Fixed tool argument schemas

All tool inputs are JSON objects with `additionalProperties: false`; every
write additionally requires `operation_id` (`8..128` characters matching
`[A-Za-z0-9][A-Za-z0-9._-]*`). Strings and arrays have bounded lengths.

| Tools | Required keys | Optional keys |
| --- | --- | --- |
| `connections_status`, `connections_start_link` | none | none |
| `azure_subscriptions_list` | `connection_id` | none |
| `azure_resource_groups_list` | `connection_id`, `subscription_id` | none |
| `azure_resources_search` | `connection_id`, `subscription_id` | `resource_type`, `name_prefix` |
| `azure_resource_show` | `connection_id`, `resource_id` | none |
| `azure_resource_groups_create` | `connection_id`, `subscription_id`, `resource_group_name`, `location`, `operation_id` | none |
| `azure_resource_group_tags_update` | `connection_id`, `subscription_id`, `resource_group_name`, `tags`, `operation_id` | none |
| `github_repositories_get` | `connection_id`, `repository_id` | none |
| `github_contents_get` | `connection_id`, `repository_id`, `path` | `ref` |
| `github_issues_list`, `github_pull_requests_list` | `connection_id`, `repository_id` | `state` |
| `github_issues_create` | `connection_id`, `repository_id`, `title`, `operation_id` | `body` |
| `github_comments_create` | `connection_id`, `repository_id`, `issue_number`, `body`, `operation_id` | none |
| `github_pull_requests_create` | `connection_id`, `repository_id`, `title`, `head`, `base_branch`, `operation_id` | `body` |
| `mail_messages_search` | `connection_id` | `subject`, `from`, `start`, `end` |
| `mail_messages_get` | `connection_id`, `message_id` | none |
| `mail_send` | `connection_id`, `to`, `subject`, `body`, `operation_id` | `cc`, `bcc` |
| `calendar_events_list` | `connection_id`, `calendar_id`, `start`, `end` | none |
| `calendar_events_create` | `connection_id`, `calendar_id`, `start`, `end`, `subject`, `operation_id` | `body`, `attendees` |

`repository_id` is a numeric stable GitHub ID; calendar and connection IDs are
opaque bounded strings. `start` and `end` require RFC3339 offsets. `to`, `cc`,
`bcc`, and `attendees` are arrays of 1..50 plain email addresses.

## Write records and deployment

Azure writes require independent Agent identity, an explicitly selected write
tool, `subscriptionIds`, and `resourceGroupNames` in the grant. Resource-group
names are exact, case-insensitive allowlist entries; wildcards are rejected.
The Broker verifies direct subscription RBAC for `*/read` and the selected
write actions. Reader alone cannot verify a write grant. Tags use ARM's Merge
operation and preserve unrelated tags. Creation rejects an existing group
before PUT, but a concurrent external creation can still turn that PUT into
an update; this is not an atomic create-only guarantee. Delegated Azure
connections remain read-only.

Before a write is sent, the service CAS-creates a record in the dedicated
operation-record namespace. The key binds instance UID, connection, tool, and
operation ID, so changing a grant does not resend a write. Only a parameter
digest and safe result/status are stored. Results are replayable for seven
days; expired records become compact terminal tombstones when accessed. An
interrupted or uncertain write returns `execution_unknown` and is never retried
automatically. Tombstones are not automatically garbage-collected in this
version: deleting them removes duplicate-prevention history and must be a
separately authorized operator retention decision.

The Helm chart requires an existing TLS Secret, Istio sidecar injection and
STRICT mTLS policy, a dedicated ServiceAccount, and an isolated operation
record namespace. RBAC grants TokenReview plus only the Pod, ServiceAccount,
Agent binding reads and operation-record Secret operations. It deliberately
does not mount provider credentials or a credential Secret.

## Broker service identity

The HTTPS Broker connection uses a mounted client TLS identity, not an API key.
Set `broker.clientTLSSecretName` to a Secret containing `tls.crt`, `tls.key`,
and `ca.crt`. The certificate URI SAN must identify this service's dedicated
ServiceAccount, and the Broker independently verifies it. Istio remains an
additional network/service boundary; encrypted HTTPS is not assumed to carry
proxy-injected identity headers.

## Local checks

```bash
go test ./... -count=1
go vet ./...
make chart-lint
```

These are offline checks. They do not authenticate to Kubernetes, the Broker,
Azure, Microsoft Graph, GitHub, Google, or a live MCP runtime.

GitHub tools use personal OAuth connections (`mode=delegated`). GitHub identifies
the connected user as the actor. CetiRun enforces each instance grant and logs
the Agent UID, connection ID, grant revision and provider request IDs; it does
not accept GitHub App installation identities or distribute provider tokens to
Agent workloads.
