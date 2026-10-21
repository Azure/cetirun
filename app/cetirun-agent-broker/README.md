# CetiRun Agent Identity Broker

The Agent Identity Broker is the trusted execution boundary between CetiRun
agent workloads, the Microsoft Entra Agent ID Auth SDK, and approved downstream
services.

The Broker owns the OBO authorization session, validates the calling workload
and instance binding, and asks the Auth SDK for a short-lived downstream access
token. It does not implement service-specific tool operations. A trusted CLI in
the Agent workload receives the downstream token, keeps it in process memory,
calls the downstream service, and returns only business data to the Agent.

The initial credential service is `azure-resource-manager`. Callers cannot
select an arbitrary audience, scope, Agent Identity, Auth SDK endpoint, or user
authorization record. The Broker never returns the user's Blueprint access
token, refresh token, authorization code, or encrypted authorization cache.

The broker and the Microsoft Auth SDK are separate containers in one Pod. This
keeps the vendor authentication component isolated while avoiding a reusable
credential protocol between independently reachable services.

Returning a bearer token to an Agent workload is an explicit trust decision,
not a hard process boundary. The supported CLI must not print, persist, place in
an environment variable, or include the token in model context. Code with
arbitrary execution inside the same workload can still request and misuse a
token. Ending the CetiRun connection blocks new issuance but cannot revoke a
token already returned to the workload; that token remains usable until its
Microsoft Entra expiry or external revocation takes effect.

## Azure app-only connection backend

An optional backend adds per-Agent Connections and ConnectionBindings alongside
those existing authorization routes. It uses the final service connection model:
a binding selects an identity and services; Azure RBAC determines the permitted
resource operations. There is no additional resource/action grant in CetiRun.
The Portal, CLI, and runtime keep using the existing flow until their separate
integration changes land.

For an Azure connection, the Broker reads the Agent UID, owner, tenant, client
ID, and principal ID from platform state. It validates the current identity on
every issuance, exchanges its federated workload assertion through the Blueprint
for a child identity assertion, and requests an app-only ARM token for that
Agent. This path never uses an owner token, personal refresh token, or OBO.
Azure resource owners assign RBAC directly to the Agent principal. Creating a
Connection or Binding does not assign or verify an Azure role.

### Storage and lifecycle

Connection and Binding records are envelope-encrypted Kubernetes Secrets in the
Agent's own namespace, with Agent owner references and
`agenttools.cetirun.microsoft.com/` labels for record type, Agent name/UID,
Connection ID, and owner. Namespace ownership and encrypted record ownership
must agree; there is no central-record fallback for these Agent identities.
The existing OBO authorization records remain in the Broker namespace.

Binding updates require the current revision. Disconnect requires `If-Match`
and leaves a revoked tombstone so reconnect cannot reuse an earlier revision.
The Operator establishes namespace-scoped Broker permissions before cleanup;
cleanup validates ownership and deletes with UID/resource-version preconditions
before the Agent namespace is removed. Keep the Broker storage configuration
available until all associated records have been cleaned up.

### Opt-in configuration

The Broker chart defaults `connectionBindings.enabled` and `tls.enabled` to
false. To enable the backend, set both to true and configure:

- `tls.secretName`: an existing Secret containing `tls.crt`, `tls.key`, and the
  trusted client `ca.crt` (key names are configurable).
- `portal.namespace` and `portal.serviceAccount`: the trusted management caller.
- `toolService.namespace` and `toolService.serviceAccount`: the trusted internal
  credential caller. Its verified client certificate must contain exactly the
  corresponding `spiffe://cluster.local/ns/<namespace>/sa/<serviceaccount>` URI.
- `toolService.workloadTokenAudience`: the audience for the separate Agent
  workload proof, defaulting to `agent-tools-mcp`.

On the Operator chart, explicitly set `toolsMCP.brokerNamespace` and
`toolsMCP.brokerServiceAccount` together, plus `agentIdentity.brokerURL` for
cleanup. These two storage settings do not install or enable an MCP server.
The existing identity, encryption, authorization, and Istio settings are still
required while the legacy flow coexists.

Before enabling Operator Broker storage, each existing built-in Agent namespace
must have an owner reference matching the current Agent name and UID. This
requirement also applies to Agents without Connections. Automatic migration or
adoption of older namespaces without owner references is not supported. For
disposable development/test instances, delete and recreate the Agents while
Broker storage is still disabled. To retain an instance, an administrator must
verify its persisted Agent/namespace binding and repair ownership explicitly;
a matching namespace name alone is not sufficient evidence.

Unowned or conflicting namespaces block reconciliation and deletion cleanup;
the finalizer remains until ownership is repaired. The Operator reports
`BrokerStorageAccessFailed` events and logs the expected Agent name and UID.
New namespaces receive the Agent owner reference at creation. If a built-in
Agent's namespace is missing, the full reconcile recreates it and establishes
Broker RBAC before continuing; namespace recovery does not restore lost records
or other deleted namespace contents.

The extra ClusterRole permissions only read namespaces, Pods, and
ServiceAccounts and create TokenReviews. Secret permissions are granted in
individual Agent namespaces, not cluster-wide. Client TLS material is mounted
only in the Broker container. Valid replacement material is reloaded for new
handshakes; invalid replacements retain the last valid set. Existing TLS
connections and already issued provider tokens are not immediately revoked.

### Interfaces

Management routes stay on the Istio-protected HTTP listener and require a
verified Portal principal and owner headers. Per-Agent routes also require the
current Agent UID.

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/v1/connections` | List the owner's Agent connections |
| GET | `/v1/connections/{id}` | Read credential-free connection metadata |
| POST | `/v1/instances/{name}/tool-identities` | Idempotently connect the current Azure Agent identity |
| GET, POST | `/v1/instances/{name}/connection-bindings` | List or create service bindings |
| PUT, DELETE | `/v1/instances/{name}/connection-bindings/{id}` | Update services or disconnect using revision checks |

Only the mutual TLS listener on port 8443 exposes the internal APIs:

| Method | Route | Purpose |
| --- | --- | --- |
| POST | `/internal/v1/service-authorizations/list` | List bindings for the proven workload |
| POST | `/internal/v1/service-authorizations/check` | Validate a connection/service selection |
| POST | `/internal/v1/service-credentials/issue` | Issue an ARM token after checking current revisions |

The shared `app/internal/agenttools` contract fixes the service ID `azure_arm`,
ARM origin, and audience. It includes `callerProof`, `contractVersion`, and
connection/binding revisions. TokenReview, live Pod/ServiceAccount checks, and
the owning namespace's live Agent must agree. The credential endpoint rechecks
these bindings after obtaining the token and rejects changes before returning
it to the trusted service. Management responses and audit logs contain no
credentials. This backend does not expose a new token endpoint to Agent Pods.
Only supported native Hermes/OpenClaw Agents qualify; sandbox/custom runtimes
are rejected.

Structured management and issuance audit events include operation, outcome,
Agent/binding/connection identifiers, and correlation ID. They require external
log retention for durable auditing. Actual Azure Activity Log attribution must
be verified against a provider operation; offline tests do not establish it.

### Offline verification

Run the repository checks and focused race tests with the pinned Go toolchain:

```bash
make check
make docs-check
go -C app/cetirun-agent-broker test -race ./... -count=1
go -C app/internal test -race ./agenttools ./tlsreload -count=1
```

Tests use synthetic Kubernetes state and token endpoints. The backend PR does
not deploy a new Portal, runtime, or MCP service and needs no live Azure access.
