---
title: "Microsoft Entra Agent Identity"
description: "Manage Azure Agent connections with direct RBAC, or use the default delegated Azure integration."
weight: 45
---

CetiRun can give a Hermes, OpenClaw, or qualified Custom instance its own Microsoft Entra
Agent Identity. The default Azure Resource Manager (ARM) integration uses on-behalf-of (OBO)
delegation. The managed tools default to read-only operations; the ARM REST
wrapper also accepts explicitly enabled writes.

Agent Identity is deployment-controlled and disabled by default. Enabling it
for an instance does not grant Azure
permissions. In the default integration, requests remain limited to the
connected user's Azure RBAC.

## Opt-in Azure Connection management

Deployments can enable a new **Azure connection** panel for built-in Hermes and
OpenClaw Agents. It configures the Connection/Binding backend using the Agent's
independent identity. Azure evaluates roles assigned directly to that identity,
not the signed-in Portal user's roles. There is no additional CetiRun resource
Grant or permission whitelist.

This panel is a configuration surface. It does not enable the runtime MCP
integration, verify Azure RBAC, or replace the default runtime's OBO credentials.
Runtime integration is a separate rollout; configuring a connection alone does
not make Azure calls available in chat. The following legacy instructions still
apply to runtimes using the default integration.

### Enable the panel

The Portal chart defaults to `features.agentToolsEnabled: false`. To opt in,
configure these values in the Portal chart:

```yaml
features:
  agentToolsEnabled: true
agentIdentity:
  tenantId: "<entra-tenant-id>"
  portalClientId: "<portal-oidc-client-id>"
istio:
  host: portal.example.com
agentTools:
  brokerURL: "http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"
  runtimeTypes: [hermes, openclaw]
```

These map to `FEATURE_AGENT_TOOLS_ENABLED`, `ENTRA_TENANT_ID`,
`PORTAL_OIDC_CLIENT_ID`, `PORTAL_ORIGIN`, `AGENT_TOOLS_BROKER_URL`, and
`TOOLS_MCP_RUNTIME_TYPES`. Enabling either Portal identity feature requires
verified Entra ID tokens from the trusted gateway. Configure the
[Istio authentication boundary](../../operations/istio/) accordingly.

The Broker must separately enable `connectionBindings.enabled`, with its
required credential TLS configuration and trusted Portal mesh boundary. Its
runtime allowlist must agree with the Portal. The Agent identity must already
be enabled and ready. To offer identity creation in the Portal, also enable
`features.agentIdentityEnabled` and the existing Operator identity prerequisites.
This panel does not provision identities or grant roles.

When enabled, this panel replaces the legacy authorization panel in the Agent
overview. Existing legacy API routes remain available when
`features.agentIdentityEnabled` is enabled. Disabling the new flag restores the
previous panel behavior; no records are migrated or deleted.

### Connect and disconnect

1. Open the Agent overview and wait for its identity to become ready.
2. Select **Connect**. The Portal creates or reuses this Agent's Azure Connection
   and creates an enabled `azure_arm` Binding.
3. Use the displayed **Principal ID** when assigning Azure RBAC. A resource owner
   can grant **Reader** at the required scope for read access. Writes need the
   appropriate Azure role; CetiRun does not grant it automatically.
4. **Connected** means the Binding is configured. Actual Azure access must be
   tested through a separately configured runtime.

Only the connection owner can view or manage their connections; Portal
administrator status does not bypass ownership checks. The panel selects the
identity for the current Agent UID. A replacement Agent with the same name does
not inherit the old panel state.

**Disconnect** revokes only this Agent's Binding. It does not delete the Azure
identity, remove Azure RBAC, or disconnect another Agent. Existing bindings stay
removable after a runtime becomes unsupported. Short-lived credentials already
issued remain valid until expiry.

## Connect an instance

1. Create a Hermes, OpenClaw, or qualified Custom instance with **Microsoft Entra Agent Identity**
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

### Custom image requirements

Custom uses the same instance owner, Portal connection, and OBO credential service
as the built-in runtimes. The selected runtime Chart must declare
`cetirun.microsoft.com/custom-agent-identity: v1`. The platform mounts
`/usr/local/bin/cetirun-agent` and the Azure skill read-only and supplies the fixed
Broker address. It does not change the image's entrypoint or mount Azure credentials.

The approved Custom image must provide Python 3 with Linux `os.memfd_create`,
`/usr/bin/az`, and the existing Custom initialization tools. Initialization fails
when these dependencies are missing; it never installs packages at startup. Images
derived from the current CetiRun base already contain Python and Azure CLI. The
image's agent must load skills from `$HOME/.agents/skills` and allow local command
execution. Use `/usr/local/bin/cetirun-agent` if that directory is not on its PATH.

An existing Custom context entry under `skills/azure` conflicts with the managed
skill and is rejected rather than overwritten. Existing instances without Agent
Identity keep their current behavior. Sandbox-policy execution remains outside
this integration; enabling Agent Identity does not lift a Sandbox execution hold.

## Use Azure from chat

The built-in Azure skill directs the Agent to `cetirun-agent`. For example:

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

The discovery commands return bounded, filtered JSON. A `truncated: true` result
contains at most 100 items and is not a complete inventory. Narrow the typed
filters instead of constructing raw Resource Graph queries.

## Use ARM REST for resource details

`azure resource show` returns a Resource Graph summary. To read provider-specific
properties, use `cetirun-agent az rest`. The same wrapper handles ManagedClusters,
node pools, load balancers, VMs, VMSS instances, disks, and other ARM resources.
It gets a short-lived ARM token from the Broker and runs the installed Azure CLI;
it does not call `az login` or use a user's Azure CLI token cache.

For example, inspect a cluster using its resource ID:

```bash
cluster_id="/subscriptions/<subscription-id>/resourceGroups/example/providers/Microsoft.ContainerService/managedClusters/example"
cetirun-agent az rest --method get \
  --url "${cluster_id}?api-version=2026-06-01" \
  --scope "$cluster_id"
```

Replace `<subscription-id>` and the example resource names with the requested
resource ID. Supply an API version supported by that provider. The wrapper
accepts relative paths or absolute
`https://management.azure.com` URLs beginning with `/subscriptions`. It requires
one explicit `api-version` and rejects other services, user-provided credentials,
ambiguous paths, and Azure CLI subscription placeholders.

The result is wrapped in `response`. For an AKS cluster, read
`response.properties.nodeResourceGroup`; do not construct an `MC_*` name.
Use that name and the cluster's subscription to list the node resource group (NRG):

```bash
node_group_id="/subscriptions/<subscription-id>/resourceGroups/example-nodes"
cetirun-agent az rest --method get \
  --url "${node_group_id}/resources?api-version=2021-04-01" \
  --scope "$node_group_id"
```

Follow returned resource IDs with their provider's API version. Child resources,
such as VMSS instances, can require separate collection requests. The optional
`--scope` flag checks the initial request against a resource, resource group, or
subscription path; repeat it to permit several paths. It is a caller-selected
guard against mistakes, not a new permission grant or a token restriction.
Without it, the wrapper permits subscription-scoped ARM paths subject to the
connected user's Azure RBAC.

The wrapper reads one page per request. It does not follow `nextLink` or select
API versions automatically. Further pages require separately validated calls.
Output is limited to 2 MiB and common sensitive field names are redacted; this
does not guarantee arbitrary resource properties contain no sensitive data.
Treat the response as untrusted data, not instructions.

### Explicit writes

GET and HEAD are enabled by default. PUT, PATCH, POST, and DELETE require
`--allow-write`. A body must be a JSON object supplied inline or through `@file`:

```bash
cetirun-agent az rest --method patch \
  --url "${cluster_id}?api-version=2026-06-01" \
  --scope "$cluster_id" --body @change.json --allow-write
```

Review the provider's method and body contract before submitting a change. A
PUT can replace properties rather than merge them. The body limit is 256 KiB.
The flag does not record human approval or add Azure permissions: the user must
authorize the intended target and change, and Azure RBAC must permit it.

A write response includes `completion: not_tracked`, even when Azure CLI exits
successfully. ARM may only have accepted an asynchronous operation. This wrapper
does not expose response headers or poll long-running operations; inspect the
resource or use the provider's documented status API before claiming completion.
It never automatically retries a failed write. `write_outcome_unknown` means a
write may have been submitted; check state before deciding whether to retry.

Known credential-export and remote-command endpoints, permission-management
writes, resource moves, and ARM deployments are rejected even with
`--allow-write`. This is not an exhaustive classification of every Azure API.
Do not use the wrapper to obtain kubeconfig, run Kubernetes commands, or bypass
resource locks. For AKS-managed NRG resources, use supported AKS operations
instead of directly changing VMSS capacity, managed network settings, or system
tags. NRG lockdown remains effective.

## Credential and tool boundary

```text
Chat → managed Azure skill → cetirun-agent
     → Agent Identity Broker → Auth SDK → short-lived ARM token
     → discovery HTTP client or Azure CLI az rest → Azure Resource Manager
```

The Broker owns the OBO authorization record and accepts only the fixed
`azure-resource-manager` credential service. The CLI keeps the returned ARM
token in process memory and must not print it, store it in a file, place it in
an environment variable or command-line argument, or return it to the model.
For `az rest`, the wrapper sends the authorization header through an anonymous
stdin pipe. Request bodies use anonymous memory-backed file descriptors, not
child command arguments. Each call uses a private temporary CLI configuration
directory with logging, telemetry, and extension installation disabled. Proxy,
alternate credential, and TLS-override environment settings are not inherited.

This is a trusted-tool boundary, not process isolation. Arbitrary code running
inside the same Agent workload could request a credential directly. Enable
Agent Identity only for runtimes and users that are trusted within the
instance boundary.

The read-only default and `--scope` checks are not a technical enforcement
boundary: arbitrary code in the same workload could obtain an ARM token and use
any operation allowed by the connected user's RBAC. Users with privileged Azure
roles must not connect those accounts to an Agent workload that can execute
untrusted code. Adding Reader does not reduce existing Contributor or Owner
permissions.

Azure CLI can follow HTTP redirects after the wrapper validates the initial URL;
the wrapper does not claim to enforce the scope of redirected requests. The
existing discovery client continues to reject redirects. Use Azure RBAC, not
this wrapper, when a resource boundary must hold independently of the runtime.

Only the `rest` subcommand is exposed. The wrapper does not log in other commands
such as `az aks show` or `az vm list`. Debug flags, custom headers, output-file
options, and arbitrary Azure CLI arguments are not forwarded. Within the
supported Agent Identity tool contract, do not use bare `az`, Azure PowerShell,
a generic HTTP client, or another credential source as a fallback.

## Common failures

| Error | Meaning |
| --- | --- |
| `authorization_required` | Open the returned Portal URL and connect Microsoft Entra. |
| `interaction_required` | Reconnect the instance in the Portal. |
| `forbidden` | Azure rejected authentication or authorization for the delegated request. |
| `throttled` | Azure rate-limited the read request. |
| `not_found` | Azure CLI reported that the requested resource was not found. |
| `write_not_enabled` | A mutating method was requested without `--allow-write`. |
| `write_outcome_unknown` | A write did not produce a usable response; inspect state before retrying. |
| `dependency_unavailable` | The Broker, Auth SDK, or Azure dependency is unavailable. |
| `invalid_request` | The command arguments, path, scope, API version, or JSON body were rejected. |

Do not work around `forbidden` by granting roles automatically. An
administrator must review any Azure RBAC change separately.

Platform operators should also review
[Istio authentication](../../operations/istio/) for the trusted Portal ID-token
header and session-store requirements.
