# Enterprise IAM adapter protocol

This package is a vendor-neutral, mTLS-only client for a service that processes
Agent permission requests. It does not create provider permissions and it does
not accept bearer tokens, delegated user tokens, or a redirect-based login.

The configured endpoint must be one `https://host[:port]` origin. The client
uses a configured client certificate, rejects `InsecureSkipVerify`, follows no
redirects, and can call only these paths:

| Operation | Method and path |
| --- | --- |
| Submit | `POST /v1/permission-requests` |
| Query | `GET /v1/permission-requests/{id}` |
| Cancel | `POST /v1/permission-requests/{id}:cancel` |
| Revoke | `POST /v1/permission-requests/{id}:revoke` |

`PermissionRequest.ID` is immutable and is sent as `Idempotency-Key` on every
operation. A service must return the same result when the same ID and complete
binding are submitted again, and reject reuse of an ID whose binding differs.
The binding includes the Agent UID, provider, mode, tenant, provider principal,
client, requested scopes/actions, expiry, requester, and revision.

Every successful response has this shape:

```json
{
  "externalRequestId": "iam-3482",
  "state": "applied",
  "evidence": {
    "id": "request-123",
    "agentUid": "agent-uid",
    "provider": "azure",
    "mode": "app-only",
    "tenantId": "tenant-id",
    "principalId": "provider-principal-id",
    "clientId": "provider-client-id",
    "scopes": ["/subscriptions/example/resourceGroups/rg"],
    "actions": ["Microsoft.Resources/subscriptions/resourceGroups/read"],
    "expiresAt": "2026-10-15T10:00:00Z",
    "requester": "resource-owner-id",
    "revision": "7",
    "evaluatedAt": "2026-10-15T09:00:00Z"
  }
}
```

The response evidence must exactly match the submitted binding. The client
rejects malformed, oversized, stale, expired, and cross-principal responses.
`approved` records an approval workflow result only. It is never permission to
execute; only a current `applied` decision can be used as effective
authorization. Cancel and revoke must return `revoked`.

The direct Azure RBAC deployment path remains separate: a resource owner grants
Azure RBAC to the Agent service principal and CetiRun verifies provider-derived
evidence. This adapter neither replaces that direct grant nor treats an IAM
workflow approval as proof that RBAC was applied.
