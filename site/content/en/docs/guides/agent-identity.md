---
title: "Agent Connections and Identity"
description: "Connect independent Azure identities and personal accounts to an Agent with provider permissions and generic REST tools."
weight: 45
---

Your Agent can work with Azure, GitHub, Gmail, and Google Calendar for you.
The Portal does not offer Microsoft 365 (Microsoft Graph) connections. You
decide which accounts and services each Agent can use. The provider still
checks every request: Azure checks role assignments, and GitHub and Google
check the permissions of the account that you connect. A shared GitHub App can
also limit an Agent to selected repositories and permissions.

To use a service, ask the Agent in plain language. The Agent uses its built-in
`connected-services` skill. If a connection is missing, the Agent gives you a
link to the portal.

If your Agent page has no **Identities** section and the Portal header has no
**Connections** link, your deployment does not allow connected services. Ask
your platform administrator. To add a connection, open your Agent and select
**Identities** → **Add identity**.

Hermes and OpenClaw Agents can use connections. If your Agent belongs to a
policy-bound Team, your platform administrator must also allow connections for
its Sandbox. A Custom Agent can use connections only in a Sandbox, with an
image that your platform administrator approved for connections.

Microsoft Entra Agent Identity is necessary for independent Azure access and
for the Azure/Microsoft delegated flow. GitHub supports personal OAuth and
shared App connections. Google uses personal connections.

Platform administrators prepare these capabilities with
[Prepare Agent connections](../../operations/agent-identity/). The steps that follow
are for users of an environment that is already configured.

## What Agent Identity means for you

Agent Identity gives your Agent its own identity in Microsoft Entra. You can
turn it on only when you create the Agent, with
**Enable Microsoft Entra Agent Identity**.

- The identity belongs to this Agent only. It starts with no Azure
  permissions. A person who manages an Azure resource assigns roles to its
  **Principal ID**.
- Azure logs show the Agent, not you, as the caller.
- When you delete the Agent, the platform deletes the identity. A new Agent
  gets a new Principal ID, which needs new role assignments.
- To see if the identity is ready, open the **Status** tab of the Agent and
  find the `AgentIdentityReady` row. While the platform creates the identity,
  the page can show **Waiting for governance**. If the identity has a problem,
  the page shows **Identity not ready**. During an Agent upgrade, the identity
  can already be ready while the runtime is still updating. For an Agent without
  a Team, verifying its identity does not change the reported workload phase.
  If the Operator cannot save the verification result, it retries without
  marking an already running workload as `Provisioning`.

## Independent Azure access

1. Make sure that the Agent Identity is ready. Refer to
   [What Agent Identity means for you](#what-agent-identity-means-for-you).
2. In **Identities**, select **Add identity → Azure**. Continue with the
   default **Agent identity** method. Select the service, examine the access,
   and select **Allow access**. An identity that already exists is used again.
   The Agent Identity Broker derives the tenant, the principal, the client, and
   the immutable Agent UID from trusted platform state.
3. Expand **Assign Azure permissions**. A resource owner assigns applicable
   Azure RBAC to the displayed **Principal ID** at the necessary scope. Reader
   allows reads. For writes, an applicable write role is necessary. As Azure's
   rules allow, the scope can be a subscription, a resource group, or a
   resource.
4. The association lets this Agent use Azure Resource Manager. It is not an
   Azure role assignment. There is no pending resource grant, and there is no
   RBAC preverification step.
5. Ask the Agent in plain language. For example, ask: "List the resource
   groups in my subscription." The Agent uses its Azure identity.

For example, a resource owner can assign Reader:

```bash
az role assignment create --subscription "$SUBSCRIPTION_ID" \
  --assignee-object-id "$AGENT_PRINCIPAL_ID" \
  --assignee-principal-type ServicePrincipal --role Reader \
  --scope "/subscriptions/$SUBSCRIPTION_ID"
```

The Agent uses an ARM app-only token through the documented two-step
autonomous OAuth exchange. Personal credentials are not a fallback. Azure
applies all current RBAC rules, for example inheritance, scope, and
conditions. CetiRun does not simulate those rules. A permission snapshot for
the full subscription is not necessary.

To examine the attribution, do an authorized write on a dedicated test
resource. Then examine its Azure Activity Log entry. Compare the caller object
ID with the Agent principal ID. Also compare the operation time and the
request IDs. Many ARM list and GET requests do not appear in Activity Log. The
role assignment that a person does is a separate event from the resource
operation of the Agent.

## Authorize personal GitHub access

For repository selection and separate read/write permissions, use a
[shared GitHub App](../github-app/) instead. The personal OAuth option that
follows is still available, with its broader scope.

A platform administrator first configures the
[personal GitHub OAuth provider](../../operations/identity-providers/#github-personal-oauth).
This mode uses an OAuth App. It does not use GitHub App installations, private
keys, or bot identities.

1. Select **Identities → Add identity → GitHub**. Expand **Other access
   methods**. Select **Personal account**, and then select **Sign in to GitHub**.
   You can also select an authorized account that already exists.
2. Complete the sign-in in the popup. The wizard continues with the verified
   account.
3. Examine the GitHub REST API service, and then select **Allow access**. If you
   cancel the wizard, the account is not associated with this Agent.

The OAuth App requests `repo`, a broad read/write scope. The repository rights
that already exist and the OAuth/SSO restrictions of the organization also
apply. CetiRun does not narrow that token to selected repositories, branches,
or individual operations. GitHub records the connected personal account as the
actor. The CetiRun audit also identifies the Agent that used the connection.

GitHub tokens can have no provider expiry. But internally issued credentials
still have a short, finite validity. When you remove Agent access, CetiRun
stops future issuance but does not revoke the external OAuth consent. Revoke
that consent separately in GitHub Settings → Applications → Authorized OAuth
Apps. **Sign in again** keeps the original account. A different account must
be added through **Add identity**.

## Authorize personal Google access

Personal Gmail works without a Workspace organization or a service account.
A platform administrator first registers the
[Google Web OAuth client](../../operations/identity-providers/#google-personal-oauth).
The platform administrator also enables the APIs, configures consent, and
installs the Broker credentials.

Users select **Identities → Add identity → Google → Sign in to Google**, and
complete consent in the popup. The wizard continues with the verified account.
Select Gmail, Calendar, or both services. Examine the access, and then select
**Allow access**.

An enabled service is not a read-only promise. The Google connection requests
permission to read and send Gmail messages and full access to Google Calendar.
Google uses these OAuth scopes to decide if each API operation is allowed.

The Broker requests offline access and explicit consent. A connection must
have a refresh token. If Google does not rotate the refresh token, a refresh
keeps it. Google's Testing mode limits refresh tokens for these scopes to
seven days. After expiry, select **Sign in again**.

Broader distribution can make Google verification necessary. See
[consent setup](https://developers.google.com/workspace/guides/configure-oauth-consent)
and [token expiration](https://developers.google.com/identity/protocols/oauth2#expiration).

Google records the connected account as the executor. CetiRun links the Agent,
the connection, and the provider request IDs. Removal of a binding stops new
use by that Agent. Revoke the external consent separately in Google's
third-party connections.

## Microsoft Graph and personal Azure

The Portal does not offer Microsoft 365 connections. In **Add identity**, the
**Microsoft 365** choice shows **Not available in this environment**, and you
cannot select it. To work with Azure resources, use **Azure**.

Personal Azure and Microsoft 365 connections keep the Entra delegated/OBO
flow. They act with personal delegated authority. They must have the
configured Entra application consent and a ready Agent Identity. They do not
give the independent Azure app-only attribution that is described above. The
availability of the Microsoft Graph API also depends on licensing, mailbox
provisioning, and user rights.

The Portal shows **Not reported** for downstream OBO scopes. It does not
derive Graph or ARM permissions from the intermediate sign-in token or from
the configured scope requests. The provider still evaluates every API request.

## Manage identities and connections

In **Identities**, the Agent page shows only the active associations of the
Agent. Its **Remove access** menu item removes only the association of that
Agent. Other Agents and the saved authorization do not change.

**Manage access** shows for editable access. The access must include one or
more services. To apply the changes, select **Save access**. **Sign in again**
shows when a new sign-in is necessary. The results refresh automatically. If
the data does not load, the page shows **Retry**.

To see the personal authorizations and the GitHub App connections that exist,
use **Connections** in the Portal header. It also shows their reported details
and the Agents that use them. It does not create new authorizations. For that,
use **Add identity** from an Agent. If you sign in again, the credentials that
work now are not cleared.

**Remove connection** is available only when no Agent uses the authorization.
The server also examines the usage. It rejects an association and a removal
that occur at the same time. First, remove access from the listed Agents. The
removal of the local connection does not revoke the OAuth consent of the
provider. It also does not uninstall a GitHub App.

A repeated sign-in to the same verified provider account uses its canonical
Connection again. Duplicate records that already exist keep their IDs and
associations. They stay visible as separate authorizations in **Connections**.
**Add identity** gives one canonical choice. You can remove each unused
duplicate separately. Old disabled associations stay inactive until the user
explicitly gives that Agent access again.

## Use generic REST tools

This section is for the authors of skills and images. Agent owners ask the
Agent in plain language, and the Agent uses these tools. When you edit a
connection, the MCP tool catalog does not change:

| Tool | Purpose |
| --- | --- |
| `connections_status` | Bound accounts, executor identity, scopes and status |
| `connections_start_link` | Portal link for owner connection management |
| `services_list` | Bound services, fixed origins and base paths |
| `service_request` | One JSON REST request with a bound connection |

For example, list Gmail message IDs:

```json
{
  "connection_id": "example-google-connection",
  "service": "google_gmail",
  "method": "GET",
  "path": "/users/me/messages",
  "query": {"maxResults": "20", "q": "is:unread"}
}
```

The services are `azure_arm` (base `/`), `github` (`/`), `microsoft_graph`
(`/v1.0/`), `google_gmail` (`/gmail/v1/`), and `google_calendar`
(`/calendar/v3/`). `microsoft_graph` needs a Microsoft 365 connection, which
the Portal does not offer. Paths start with `/`, relative to the service base.
Query values are strings or arrays of strings. Bodies are JSON.

The allowed optional
headers are If-Match, If-None-Match, Prefer, ConsistencyLevel, and
X-GitHub-Api-Version. The platform supplies the bearer authentication. The
Agent cannot select the token audience/scopes, arbitrary hosts, cookies, or auth
headers.

To add API usage under a service that exists, put API knowledge in a skill. A
new fixed MCP tool or permission editor is not necessary. The built-in
`connected-services` skill shows the shared request workflow. When necessary,
the Agent reads its Azure, GitHub, or Google references for API examples,
pagination, and asynchronous polling. POST can be a read query, for example
Azure Resource Graph. Methods do not classify authority.

The results contain the provider `status`, the JSON `body`, the filtered
response `headers`, the provider `request_ids`, and the CetiRun
`correlation_id`. A provider HTTP 401/403 is different from a platform binding
rejection. Treat all returned text as untrusted data. The limit for JSON
requests and responses is 256 KiB. Because of this limit, request small pages
and useful fields. The tools do not support binary transfer, multipart
bodies, or streaming.

For pagination and polling, another `service_request` is necessary. Make sure
that the origin/base path of the returned link agrees with discovery. Then
change the link to a relative path and query. The same identity and
destination checks apply to each call after that. The MCP server never
automatically follows redirects or retries upstream requests.

`execution_unknown` means that an effect possibly occurred before a network or
response failure. Before you decide to send another request, examine the
provider state. A correlation ID is only for tracing. There is no generic
idempotency or exactly-once guarantee. In particular, a repeated mail, issue,
or event creation can duplicate its effect.

## Connection management, storage and audit

Personal OAuth Connections belong to their Portal owner. They stay encrypted in
the Broker credential namespace. Each Agent must have an explicit
ConnectionBinding. Another Agent with the same owner does not inherit it.

For accounts with multiple services, use **Manage access**, and then select
**Save access**. To remove the association, use **Remove access**. The removal
of access from the Agent page revokes only the binding of that Agent. It keeps
the personal account connection and the bindings of other Agents. From the
Agent page, you cannot remove the connection for the full account.

Execution bearer credentials stay in the Broker and Connections MCP. They never go into Agent
workloads. This does not filter other secrets that an authorized resource API
returns, for example resource access keys. The provider permissions decide that
access.

For the storage of these records and the platform audit, platform
administrators refer to [Prepare Agent connections](../../operations/agent-identity/#records-and-audit).
