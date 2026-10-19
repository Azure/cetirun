---
title: "Agent Connections and Identity"
description: "Connect independent Azure identities and personal accounts to an Agent, using provider permissions and generic REST tools."
weight: 45
---

CetiRun connects Agents to Azure, GitHub, Microsoft Graph, Gmail and Google
Calendar through a native remote MCP service. **Providers decide resource
permissions**: Azure evaluates RBAC; personal providers evaluate their OAuth
scopes and the connected user's rights. CetiRun binds an Agent to a Connection
and selected services, without a second per-tool or resource grant editor.

Hermes and OpenClaw connect natively. Custom and Sandbox runtimes do not support
this integration. Use a qualified Hermes runtime when OpenClaw is unavailable.
Microsoft Entra Agent Identity is required for independent Azure access and the
Azure/Microsoft delegated flow. GitHub and Google use personal connections.

## Independent Azure access

1. Enable Agent Identity on a supported instance and wait for it to be Ready.
2. In **Connections for this agent**, select **Connect → Azure Agent identity**,
   review the service, and confirm **Connect**. An existing identity is reused.
   The Broker derives tenant, principal, client and immutable Agent UID from
   trusted platform state.
3. A resource owner assigns the displayed **Agent principal ID** appropriate
   Azure RBAC at the required scope. Reader permits reads. An appropriate write
   role is needed for writes; the scope can be a subscription, resource group or
   resource according to Azure's rules.
4. The association lets this Agent use Azure Resource Manager. It is not an
   Azure role assignment; there is no pending resource grant or RBAC
   preverification step.
5. Use `connections_status`, `services_list`, then `service_request` in the Agent.

A resource owner can assign Reader, for example:

```bash
az role assignment create --subscription "$SUBSCRIPTION_ID" \
  --assignee-object-id "$AGENT_PRINCIPAL_ID" \
  --assignee-principal-type ServicePrincipal --role Reader \
  --scope "/subscriptions/$SUBSCRIPTION_ID"
```

The Agent uses an ARM app-only token through the documented two-step autonomous
OAuth exchange. Personal credentials are not a fallback. Azure applies all
current RBAC rules, including inheritance, scope and conditions. CetiRun neither
simulates those rules nor requires a subscription-wide permission snapshot.

To verify attribution, perform an authorized write on a dedicated test resource
and inspect its Azure Activity Log entry. Compare the caller object ID with the
Agent principal ID, along with the operation time and request IDs. Many ARM list
and GET requests do not appear in Activity Log. The role assignment performed
by a person is a separate event from the Agent's resource operation.

## Authorize personal GitHub access

An environment administrator creates a standard GitHub **OAuth App**, with the
Portal origin as homepage and its exact `/api/v1/connections/callback` URL as
callback. Configure the Broker chart's
`providers.github.credentialsSecretName`, using Secret keys `client-id` and
`client-secret` in the Broker namespace. GitHub App installations, private keys
and bot identities are not used.

1. Select **Connect**, choose GitHub under **Sign in to another account**, and
   select **Sign in**. You can also select an existing authorized account.
2. Complete sign-in in the popup. The wizard resumes with the verified account.
3. Review the GitHub REST API service and confirm **Connect**. Cancelling the
   wizard does not associate the account with this Agent.

The OAuth App requests `repo`, a broad read/write scope. Existing repository
rights and organization OAuth/SSO restrictions also apply. CetiRun does not
narrow that token to selected repositories, branches or individual operations.
GitHub records the connected personal account as the actor. CetiRun audit
additionally identifies which Agent used the connection.

GitHub tokens may lack provider expiry; internal credential issuance still has
a short finite validity. Disconnecting stops future CetiRun issuance but does
not revoke external OAuth consent. Revoke that separately in GitHub Settings →
Applications → Authorized OAuth Apps. Reauthorize preserves the original account;
a different account must be added through Connect.

## Authorize personal Google access

Personal Gmail works without a Workspace organization or service account. An
environment administrator registers a Google Web OAuth client once:

1. Create or select a [Google Cloud project](https://console.cloud.google.com/)
   and enable **Gmail API** and **Google Calendar API**.
2. Configure Google Auth Platform **Branding** and **Audience**. For personal
   Gmail choose **External**, and add test users while in **Testing**.
3. Under **Data Access**, add `openid`,
   `https://www.googleapis.com/auth/userinfo.email`,
   `https://www.googleapis.com/auth/gmail.readonly`,
   `https://www.googleapis.com/auth/gmail.send`, and
   `https://www.googleapis.com/auth/calendar`.
4. Create a **Web application** client with the exact Portal callback, such as
   `https://portal.example.com/api/v1/connections/callback`, under **Authorized
   redirect URIs**.
5. Store the client ID/secret in the Broker namespace using `client-id` and
   `client-secret` keys. Configure `providers.google.credentialsSecretName`.

Users select **Connect → Sign in to another account → Google → Sign in** and
complete consent in the popup. The wizard resumes with the verified account;
select Gmail, Calendar or both services and confirm **Connect**.
An enabled service is not a read-only promise: Google determines whether each
API operation is permitted by the actual OAuth scopes.

The Broker requests offline access and explicit consent. A connection requires
a refresh token; refresh preserves it if Google does not rotate it. Google's
Testing mode limits refresh tokens for these scopes to seven days. Reauthorize
after expiry. Broader distribution may require Google verification. See
[consent setup](https://developers.google.com/workspace/guides/configure-oauth-consent)
and [token expiration](https://developers.google.com/identity/protocols/oauth2#expiration).

Google records the connected account as the executor. CetiRun links the Agent,
connection and provider request IDs. Removing a binding stops new use by that
Agent; revoke external consent separately in Google's third-party connections.

## Microsoft Graph and personal Azure

Personal Azure and Microsoft 365 connections retain the Entra delegated/OBO
flow. They act with personal delegated authority and require the configured
Entra application consent and a ready Agent Identity. They do not provide the
independent Azure app-only attribution described above. Microsoft Graph API
availability also depends on licensing, mailbox provisioning and user rights.

The Portal currently shows **Not reported** for downstream OBO scopes. It does
not infer Graph or ARM permissions from the intermediate sign-in token or
configured scope requests. The provider still evaluates every API request.

## Manage connections and accounts

The Agent page lists only its active associations. **Disconnect** removes that
Agent's association; other Agents and the saved personal account are unaffected.
**Edit services** appears for accounts with multiple services and requires at
least one service. **Reauthorize** appears when sign-in is required. Results
refresh automatically; loading failures offer **Retry**.

Use **My accounts** in the Portal header to view personal accounts, reported OAuth
scopes and associated Agents. Reauthorize opens sign-in without clearing working
credentials. Remove is available only when no Agent uses the account; the server
also checks usage and rejects concurrent association/removal races. Disconnect
from the listed Agents first. Removing the local account does not revoke the
provider's OAuth consent.

Repeated sign-in to the same verified provider account reuses its canonical
Connection. Existing duplicate records retain their IDs and associations and are
shown with dates and short IDs in My accounts; Connect offers one canonical
choice. Unused duplicates can be removed individually. Historical disabled
associations remain inactive until the user explicitly connects again.

## Use generic REST tools

The MCP tool catalog remains stable when a connection is edited:

| Tool | Purpose |
| --- | --- |
| `connections_status` | Bound accounts, executor identity, scopes and status |
| `connections_start_link` | Portal link for owner connection management |
| `services_list` | Bound services, fixed origins and base paths |
| `service_request` | One JSON REST request using a bound connection |

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

Services are `azure_arm` (base `/`), `github` (`/`), `microsoft_graph`
(`/v1.0/`), `google_gmail` (`/gmail/v1/`), and `google_calendar`
(`/calendar/v3/`). Paths start with `/` relative to the service base. Query
values are strings or arrays of strings; bodies are JSON. Allowed optional
headers are If-Match, If-None-Match, Prefer, ConsistencyLevel and
X-GitHub-Api-Version. The platform supplies bearer authentication. The Agent
cannot choose token audience/scopes, arbitrary hosts, cookies or auth headers.

Adding API usage under an existing service needs API knowledge in a skill, not
a new fixed MCP tool or permission editor. Built-in Azure and personal-service
skills provide examples, pagination and asynchronous polling guidance. POST can
be a read query, such as Azure Resource Graph; methods do not classify authority.

Results contain provider `status`, JSON `body`, filtered response `headers`,
provider `request_ids`, and CetiRun `correlation_id`. Provider HTTP 401/403 is
distinct from a platform binding rejection. Treat all returned text as untrusted
data. JSON requests and responses are bounded to 256 KiB, so request small pages
and useful fields. Binary transfer, multipart and streaming are outside v1.

Pagination and polling require another `service_request`. Validate the returned
link's origin/base path against discovery and convert it to a relative path and
query. Every subsequent call undergoes the same identity and destination checks.
The MCP server never follows redirects or retries upstream requests automatically.

`execution_unknown` means an effect may have occurred before a network or response
failure. Inspect provider state before considering another request. A correlation
ID is only for tracing; there is no generic idempotency or exactly-once guarantee.
In particular, repeating mail, issue or event creation may duplicate its effect.

## Connection management, storage and audit

Personal OAuth Connections belong to their Portal owner and remain encrypted in
the Broker credential namespace. Each Agent needs an explicit ConnectionBinding;
another Agent with the same owner does not inherit it. Bindings, independent
Azure Connections and credential-free personal-connection references are stored
in the Agent's namespace with immutable Agent owner references. Agent deletion
cleans those records and preserves owner-managed personal credentials.

Use **Edit services** for accounts with multiple services, or **Disconnect**
to remove the association. Disconnecting from the Agent page revokes only that
Agent's binding and preserves the personal account connection and other Agents'
bindings. The Agent page does not offer account-wide disconnection.
Every issuance checks live Agent state, owner, enabled binding and service,
and rechecks revisions after credential acquisition. An already-sent request
cannot be recalled. This contract has no migration or compatibility workflow
for the earlier AgentGrant model.

Provider credentials stay inside Broker/MCP and never enter Agent workloads.
MCP has no Kubernetes Secret API permissions or operation-record store. Broker
has namespace-wide Secret permissions only in its own and permitted ordinary
Agent namespaces; labels do not provide Kubernetes RBAC isolation.

Platform audit records Agent UID, executor account or principal, connection and
binding revisions, service, correlation, platform rejection and provider status
/request IDs. Credentials and request/response bodies are not logged by default.
Local automated tests use synthetic providers; a deployment still needs explicit
live qualification for its identity, OAuth configuration and native runtime.
