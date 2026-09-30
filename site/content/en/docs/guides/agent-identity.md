---
title: "Microsoft Entra Agent Identity"
description: "Connect an instance to Microsoft Entra and inspect Azure resources through delegated identity."
weight: 45
---

CetiRun can give a built-in Hermes or OpenClaw instance its own Microsoft Entra
Agent Identity. The first supported data plane is read-only Azure Resource
Manager access using on-behalf-of (OBO) delegation.

Agent Identity is deployment-controlled and disabled by default. Custom
runtimes do not support it. Enabling it for an instance does not grant Azure
permissions: every request remains limited to the connected user's Azure RBAC.

## Connect an instance

1. Create a Hermes or OpenClaw instance with **Microsoft Entra Agent Identity**
   enabled.
2. Wait until the instance and its Agent Identity are ready.
3. Open the instance overview in the Portal and select
   **Connect Microsoft Entra**.
4. Sign in with the intended account and complete any required consent.
5. Confirm that the overview reports **Connected**.

The connection belongs to one instance. Another instance has a different Agent
Identity and authorization record and must be connected separately. Use
**Reconnect** when Entra requires interaction, and **End connection** to block
new credential issuance. A short-lived token already issued to a workload
remains valid until its Entra expiry.

The Portal never asks users to paste a token into chat or a terminal.

## Use Azure from chat

The built-in `azure-readonly` skill directs the Agent to the typed
`cetirun-agent` CLI. For example:

> List the Azure subscriptions and resource groups I can access.

The CLI supports:

```text
cetirun-agent auth status
cetirun-agent auth start
cetirun-agent azure subscriptions list
cetirun-agent azure resource-groups list --subscription <uuid>
cetirun-agent azure resources search --subscription <uuid> [typed filters]
cetirun-agent azure resource show --id <canonical-resource-id>
```

`auth start` returns a Portal action URL when the instance is not connected.
The Agent can show that URL, but it cannot perform the user's sign-in.

The commands return bounded, filtered JSON. A `truncated: true` result contains
at most 100 items and is not a complete inventory. Narrow the typed filters
instead of constructing raw Resource Graph queries.

## Credential and tool boundary

```text
Chat → managed Azure skill → cetirun-agent
     → Agent Identity Broker → Auth SDK → short-lived ARM token
     → fixed ARM / Resource Graph endpoints → filtered business data
```

The Broker owns the OBO authorization record and accepts only the fixed
`azure-resource-manager` credential service. The CLI keeps the returned ARM
token in process memory and must not print it, store it in a file, place it in
an environment variable or command-line argument, or return it to the model.

This is a trusted-tool boundary, not process isolation. Arbitrary code running
inside the same Agent workload could request a credential directly. Enable
Agent Identity only for runtimes and users that are trusted within the
instance boundary.

The supported v1 managed CLI does not implement Azure write, delete,
deployment, policy, role-assignment, or permission-management operations. This
is not a technical enforcement boundary: arbitrary code in the same workload
could obtain an ARM token and use any operation allowed by the connected
user's RBAC. Users with privileged Azure roles must not connect those accounts
to an Agent workload that can execute untrusted code.

Within the supported Agent Identity tool contract, do not use `az`, Azure
PowerShell, a generic HTTP client, arbitrary ARM URLs, or raw KQL as a fallback.

## Common failures

| Error | Meaning |
| --- | --- |
| `authorization_required` | Open the returned Portal URL and connect Microsoft Entra. |
| `interaction_required` | Reconnect the instance in the Portal. |
| `forbidden` | The delegated user lacks Azure RBAC for the requested resource. |
| `throttled` | Azure rate-limited the read request. |
| `dependency_unavailable` | The Broker, Auth SDK, or Azure dependency is unavailable. |
| `invalid_request` | The typed command arguments were rejected. |

Do not work around `forbidden` by granting roles automatically. An
administrator must review any Azure RBAC change separately.

Platform operators should also review
[Istio authentication](../../operations/istio/) for the trusted Portal ID-token
header and session-store requirements.
