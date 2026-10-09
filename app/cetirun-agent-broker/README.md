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

## Credential policy enforcement

The Operator applies the current AgentPolicy credential projection through the
operator-authenticated `PUT /operator/v1/instances/{uid}/credential-policy`
endpoint and verifies the same value with `GET`. The only currently supported
canonical policy is a SHA-256 revision with the
`azure-resource-manager` service allowlist. The Broker persists that non-secret
evidence in an Agent-owned ConfigMap in its namespace. It issues a credential
only when the Agent's desired revision, Operator read-back status revision, and
stored ConfigMap revision exactly agree, both before and after the Auth SDK
exchange. Deleting the policy or any disagreement fails closed.

Returning a bearer token to an Agent workload is an explicit trust decision,
not a hard process boundary. The supported CLI must not print, persist, place in
an environment variable, or include the token in model context. Code with
arbitrary execution inside the same workload can still request and misuse a
token. Ending the CetiRun connection blocks new issuance but cannot revoke a
token already returned to the workload; that token remains usable until its
Microsoft Entra expiry or external revocation takes effect.
