---
title: "Agent Tools and Identity"
description: "Authorize independent Azure Agent identities and explicit personal-service connections."
weight: 45
---

CetiRun's **Agent Tools MCP** is a native remote MCP server for Azure, GitHub,
Microsoft 365 mail/calendar, and Google mail/calendar. The Agent Broker manages
account connections and explicit grants. Built-in Hermes and OpenClaw clients
connect directly: there is no client bridge, legacy Azure CLI, direct Agent
provider-token endpoint, or fallback path. Custom and held Sandbox runtimes do
not support this integration in this version.

Microsoft Entra Agent Identity is optional for the runtime, but required for
independent Azure access and Azure/Microsoft delegated tools. GitHub and Google
currently use explicit personal connections. Enabling an identity alone grants
neither tool permissions nor provider permissions.

## Authorize an independent Azure Agent

1. Create a supported instance with Agent Identity enabled and wait for it to be
   Ready. For an environment that has not qualified the OpenClaw image, use a
   non-Sandbox Hermes instance.
2. Open **Connections & agent tool access**. The default mode is **Agent identity
   (Azure)**. Choose **Set up Azure Agent identity**. The binding uses the
   Operator's immutable Agent UID, tenant, client ID and service principal ID.
3. Request tools, subscription IDs and an expiry. The request remains **pending**.
4. A resource owner with Azure role-assignment permissions assigns the displayed
   **Agent principal ID** an appropriate role at each requested subscription.
   The current read tool catalog requires subscription-wide read permissions;
   Reader is the least-privilege built-in role for that catalog. For example:

   ```bash
   az role assignment create --subscription "$SUBSCRIPTION_ID" \
     --assignee-object-id "$AGENT_PRINCIPAL_ID" \
     --assignee-principal-type ServicePrincipal --role Reader \
     --scope "/subscriptions/$SUBSCRIPTION_ID"
   ```

5. Select **Verify Azure RBAC**. The Broker obtains an app-only token for this
   Agent and checks Azure role assignments, effective permissions for the selected tools and an
   actual resource-group read. Successful verification enables the grant.
   Propagation delays or missing access leave it pending; retry after the
   resource owner confirms the assignment.

CetiRun administrator access does not confer Azure resource authorization. This
workflow accepts direct, unconditional subscription role assignments to the
Agent service principal. Inherited, group-based and conditional assignments,
resource-group-only scopes and custom partial-reader roles are not yet verified
by this contract. Azure enforces its actual permissions on every tool request.

The Agent uses its own identity without a personal login or refresh token. The
Broker implements the documented two-step autonomous OAuth exchange: the
Blueprint obtains an exchange assertion scoped to the child Agent, then the
child obtains an ARM app-only token. Access tokens remain opaque and are never
sent to the Agent. Failure cannot fall back to a personal connection.

Editing the request resets verification. Revoking it immediately blocks new
Broker issuance; the resource owner must separately remove external Azure RBAC.
Verification is a dated provider-evidence snapshot, not a promise that external
permissions will remain unchanged. The grant expiry still limits execution.

Azure app-only operations execute as the Agent service principal. CetiRun audit
records distinguish that executor from the person managing the request. Many
ARM GET operations do not appear in Activity Log; use applicable resource logs
and Entra Agent sign-in records when checking attribution.

Personal GitHub connections use a separate provider integration described
below. Pre-provisioned Microsoft 365 Agent users remain unavailable; creating a
Graph user alone does not provision a mailbox.
Google mail/calendar remains explicitly delegated. The vendor-neutral enterprise
IAM adapter protocol supports future submit/query/cancel/revoke integrations;
the direct Azure RBAC workflow does not require a mock or external IAM service.

### Enable Azure writes and inspect Activity Log

Azure writes are off by default and available only in **Agent identity (Azure)**
mode. Edit the grant, explicitly select **Create resource groups** or **Merge
resource group tags**, and enter **Writable resource group names**. These names
apply within every listed subscription; use one subscription for a small test.
Saving any change returns the grant to pending verification.

| Selected write tool | Required Azure action | Built-in role in addition to Reader |
| --- | --- | --- |
| `azure_resource_groups_create` | `Microsoft.Resources/subscriptions/resourceGroups/write` | Resource Group Contributor |
| `azure_resource_group_tags_update` | `Microsoft.Resources/tags/write` | Tag Contributor |

Assign the role directly to the Agent principal at the requested subscription,
then select **Verify Azure RBAC**. This version verifies subscription assignments;
a role assigned only at a resource group is not sufficient for its verification
contract. A custom role limited to the listed write action can replace the
built-in writer role. The CetiRun resource-group allowlist limits MCP calls; it
does not narrow the Azure role assignment itself.

For a small audit test, enable only the tag tool, list a dedicated existing test
resource group, and grant Reader plus Tag Contributor. Ask Hermes to merge
`{"cetirun-agent-test":"verified"}` into that group's tags using the native MCP
tool and a new `operation_id`. The tool preserves other tags. If creating a new
test group, enable the create tool and its role, specify a unique group name and
Azure location, then invoke it through Hermes. Creation checks for an existing
group first; a concurrent external creator can still cause the ARM PUT to
update that group. Do not use a shared name or race another creator.

After the write, open the subscription or resource group's **Activity log**,
filter by the test resource and time, and inspect the successful write event.
Check the JSON claims' object ID against the **Agent principal ID**, alongside
the operation time and available request/correlation IDs. The person who
assigned the Azure role will appear on the separate role-assignment event;
that event is not the Agent's resource write. Activity Log ingestion may lag.

Use the same operation ID for an identical retry. An `execution_unknown` result
requires inspecting Azure before attempting a new operation ID. Remove the
write tools from the grant and have the resource owner remove their Azure write
roles after testing. This version has no resource-group deletion tool.

## Authorize personal GitHub access

GitHub tools execute as your connected personal account. An Azure Agent
principal is not a GitHub repository permission subject. GitHub shows your
account as the Issue, comment or PR author; CetiRun audit links each execution
to the immutable Agent UID, connection ID, grant revision and provider request
IDs. This integration does not create a GitHub bot or use a GitHub App.

An environment administrator registers a standard **OAuth App** at GitHub
Settings → Developer settings → OAuth Apps. Set its authorization callback to
the deployment's existing `/api/v1/connections/callback` URL and its homepage
to the Portal origin. Configure its client ID and client secret through the
Broker chart's `providers.github.credentialsSecretName` (`client-id` and
`client-secret`). No installation, App private key, webhook or device flow is
needed. The Secret belongs only to the Broker namespace.

The login requests GitHub's `repo` scope, which covers private repositories and
includes broad read/write permissions. OAuth App tokens cannot be narrowed to
the selected CetiRun tools or repositories. CetiRun enforces those limits before
every tool call; effective access also depends on the user's repository rights
and any organization OAuth/SSO restrictions. Default read-only grants describe
the Agent's CetiRun access, not a read-only provider token.

1. In **Connections & agent tool access**, choose **Personal delegated account**,
   select **GitHub**, and click **Connect account**. Complete GitHub sign-in and
   consent in the same browser, return to CetiRun and refresh the connection
   list. Start a new connection from the Portal if the sign-in window expires.
2. Review the displayed account, select it and click **Authorize account**.
   Enter stable numeric **GitHub repository IDs**. Read tools are selected by
   default; explicitly enable each write tool you need. PR creation also needs
   allowed **Pull request base branches** and an existing head branch.
3. Select a lifetime (30 days by default, at most 90) and save. This authorizes
   this Agent to use the personal connection within those limits. It does not
   grant new permissions on GitHub, and requires no repository administrator
   proof or installation verification step.
4. In Hermes Chat, run `/reload-mcp now` and wait for **MCP servers reloaded**.
   Use `connections_status` to select the GitHub `mode=delegated` connection,
   then read an allowed repository.
5. To test writes, enable **Create issues**, create a test Issue with a unique
   `operation_id`, and check that its author is the connected personal account.
   Identical retries reuse the operation ID; inspect GitHub before retrying an
   `execution_unknown` result with a different ID.

Edit or revoke the instance grant in CetiRun to change future tool access.
Disconnecting the account also stops new issuance. Revoke external OAuth consent
separately under GitHub Settings → Applications → Authorized OAuth Apps.
GitHub tokens can lack a provider expiry; CetiRun's grant expiry and short tool
issuance validity do not revoke or shorten that external token. Tokens remain
inside the trusted Broker/MCP services and never enter an Agent workload.
If GitHub rejects a revoked token with `interaction_required`, select
**Reconnect**. A `permission_denied` response can instead mean insufficient
repository access, organization restrictions or a CetiRun grant limit.

## Connect a personal account and preauthorize

1. Open the instance overview and its **Connections & agent tool access** panel.
2. Explicitly choose **Personal delegated account**, select a provider, then follow its sign-in.
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
