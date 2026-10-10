---
title: "Agent Tools and Identity"
description: "Preauthorize personal-service tools without giving provider credentials to an Agent."
weight: 45
---

CetiRun's **Agent Tools MCP** is a native remote MCP server for Azure, GitHub,
Microsoft 365 mail/calendar, and Google mail/calendar. The Agent Broker manages
account connections and explicit grants. Built-in Hermes and OpenClaw clients
connect directly: there is no client bridge, legacy Azure CLI, direct Agent
provider-token endpoint, or fallback path. Custom and held Sandbox runtimes do
not support this integration in this version.

Microsoft Entra Agent Identity is optional for the runtime, but required for
Azure/Microsoft OBO tools. GitHub and Google use the owner's connected account
and do not require an Entra Agent principal. Enabling an identity alone grants
neither tool permissions nor provider permissions.

## Connect and preauthorize

1. Open the instance overview and its **Connections & agent tool access** panel.
2. Select Azure, Microsoft 365, GitHub, or Google, then follow the provider sign-in.
3. Review the connected account and authorize it for **this Agent**.
4. Select tools and explicit resource limits: subscription IDs, stable GitHub
   repository IDs, calendar IDs, allowed mail recipients/domains, and PR base branches.
5. Choose a lifetime: 30 days by default, at most 90 days. Writes are opt-in;
   calendar invitations also need recipient limits and explicit permission.

A Connection belongs to the Portal owner; a grant binds that Connection to one
Agent's immutable Kubernetes UID. Other Agents need their own grants even when
they share an owner. Changing accounts requires a new Connection; reconnect
cannot silently replace the account behind existing grants. Disconnecting a
Connection or revoking a grant prevents new issuance. Already-started external
requests cannot be recalled; provider tokens already issued remain valid until
provider expiry but never enter the Agent workload.

Authorization happens in the Portal before use, not by approving each chat
request. A chat message cannot extend a grant. The Agent may return a Portal
link when access is missing, but cannot complete user sign-in itself.

## Call tools from chat

```text
Agent native MCP client
  → HTTPS /mcp on Agent Tools MCP
  → independently authenticated Broker authorization/token checks
  → fixed provider API
  → bounded business data to the Agent
```

Examples include `azure_subscriptions_list`, `azure_resources_search`,
`github_contents_get`, `mail_messages_search`, and `calendar_events_list`.
Supply the granted `connection_id`; tools require typed arguments, not raw
URLs, shell commands, KQL, or OAuth scopes. A `truncated: true` list contains
only a bounded first page, not a complete inventory. Narrow the typed filters.
The Azure skill is usage guidance; enforcement lives in the MCP server and
Broker, not the skill's prompt.

Writes additionally require a unique `operation_id`. Matching calls replay a
persisted result, rather than resend the provider request. Conflicting arguments
are rejected. Uncertain outcomes return `execution_unknown`; do not retry with a
new ID without verifying whether the first operation happened. Seven-day result
expiry does not permit old operation IDs to execute again: terminal tombstones
are retained. Automatic tombstone garbage collection is not implemented.

External email, repository files, issues, and calendar descriptions are
untrusted data. Tool output is not an instruction or permission to perform
another operation. Transport authentication and resource grants do not solve
prompt injection; keep write grants narrow and independently inspect results.

## Deployment boundary

The integration is disabled by default. Operators configure three addresses:

- Agent → `toolsMCP.url`, for example `https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp`.
- MCP → `AGENT_BROKER_BASE_URL`, an HTTPS Broker origin using directly verified client mTLS.
- Portal/Operator → the Broker's trusted mesh HTTP management listener.

Agents receive a read-only, 600-second Pod-bound projected token with audience
`agent-tools-mcp`, not a provider token. Each request validates TokenReview's
audience and the current Pod, ServiceAccount, Ready Agent UID, and suspension/
deletion state. The Broker independently repeats this validation. No authorization
or provider-token cache is shared between calls.

The Tool service's server TLS Secret stays in its namespace. Put a **CA-only**
Secret named by `cetirun-operator.toolsMCP.caSecretName` in the Operator namespace;
the Operator copies only `ca.crt` to owned per-Agent Secrets and triggers rollout
on CA changes. Never distribute server/client private keys to Agents. Broker
credentials and MCP operation records use separate namespaces and RBAC.
Istio STRICT mTLS and NetworkPolicies are additional boundaries, not substitutes
for the directly verified Broker client certificate.

OAuth apps, consent, Key Vault encryption, TLS material, and namespace-specific
Istio wiring must be provisioned separately. This change supplies source and
offline tests, not a live deployment or proof of production security. See the
[component README](https://github.com/Azure/cetirun/tree/main/app/agent-tools-mcp)
and [Istio operations](../../operations/istio/) for details.

## Common failures

| Error | Action |
| --- | --- |
| `unauthenticated` | Check the projected token, audience, and current workload binding. |
| `grant_required` | Connect an account and authorize this Agent in the Portal. |
| `permission_denied` | Review the grant and provider permissions; do not auto-grant roles. |
| `interaction_required` | Reconnect the same account in the Portal. |
| `authorization_changed` | Refresh the current grant; the call crossed a revision change. |
| `throttled` | Respect provider limits. |
| `dependency_unavailable` | Inspect the MCP/Broker/provider dependency without logging credentials. |
| `execution_unknown` | Independently inspect whether the write occurred before proceeding. |
| `invalid_request` | Correct the typed arguments. |
