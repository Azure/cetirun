---
title: "Register GitHub and Google providers"
description: "Register the GitHub and Google providers that Agent connections use."
weight: 35
---

Use this page to prepare the external providers that owner-scoped Agent
connections use. Follow [Prepare Agent connections](../agent-identity/)
through its encryption and namespace preparation. Then use this page at the
provider registration step, before you deploy the Broker. For Entra
applications and Azure or Microsoft delegated identities,
use [Prepare Microsoft Entra Agent Identity](../entra-agent-identity/).

The provider clients below are for Agent connections. They are separate from
the Portal login application and its `https://portal.example.com/oauth2/callback`
redirect. Do not register that login redirect in a GitHub or Google client for
the Broker.

Use the real Portal origin in place of `https://portal.example.com`. Store
client secrets in local files that have access control. Create the Kubernetes
Secret only in the Broker namespace. Do not commit credentials or put them in
Helm values. Do not paste a Secret manifest or command output into a ticket.

With `make deploy`, put the Broker overlays of this page under `broker.values`
in the environment file. `make deploy` stops if a provider Secret that the
environment file names does not exist. Remove the entries of the providers that
you do not register.

## Shared GitHub App

Use a shared GitHub App when selected repositories and explicit Contents, Pull
requests, or Issues permissions are necessary for an Agent. A shared App
installation is the platform-wide maximum. Then every Agent binding selects
repositories and the allowed permission level. GitHub actions run as the App
bot.

Follow the complete [shared GitHub App registration guide](../../guides/github-app/#register-the-platform-app)
to register the App, create its client secret and RSA key, install it, and
configure `providers.github.app`. That guide is the source for the necessary
callback and setup URLs, App permissions, key rotation, and the platform trust
boundary. Its App credentials are separate from the personal OAuth App below.

The Broker supports personal GitHub OAuth and the shared App at the same time.
An App installation does not make a personal OAuth connection repository
scoped. A personal OAuth consent is never a substitute for App installation
authority.

## GitHub personal OAuth

Personal GitHub OAuth is applicable only if the GitHub access that the
connected user already has is acceptable for the Agent. This method is
intentionally broad. The Broker requests the `repo` scope and acts as the
connected user. This method cannot limit a personal OAuth token to selected
repositories, branches, or individual GitHub operations. For
repository-specific access, we recommend that you use the shared App.

### Register the OAuth App

An organization or platform administrator creates a standard GitHub **OAuth
App** in **Settings → Developer settings → OAuth Apps → New OAuth App**.
Use a name that is specific to the deployment. Enter these values:

| Setting | Value |
| --- | --- |
| Homepage URL | `https://portal.example.com` |
| Authorization callback URL | `https://portal.example.com/api/v1/connections/callback` |
| Requested OAuth scope | `repo`. The Broker requests this scope. You do not enter it in the App form. |

Register the exact URL that the Broker sends as `redirect_uri`, not the Portal
login `/oauth2/callback` path. Refer to GitHub's
[OAuth App registration instructions](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app) and
[organization OAuth restrictions](https://docs.github.com/organizations/managing-oauth-access-to-your-organizations/about-oauth-app-access-restrictions).

GitHub organization owners can restrict third-party OAuth Apps. They can also
make approval or SAML SSO authorization necessary. These policies still apply
after a user grants `repo`. They can prevent access to organization
repositories. Do not describe a user's ordinary repository membership as
installation or administrator permission. To get organization access, a user
must complete the organization's approval and SSO process under its own policy.

Generate a client secret. Record the Client ID that is shown. Save the secret
to a local file with restricted access. Then create the Secret in the Broker
namespace:

```bash
kubectl --namespace cetirun-credentials create secret generic github-oauth \
  --from-file=client-id=/secure/cetirun/github-oauth/client-id \
  --from-file=client-secret=/secure/cetirun/github-oauth/client-secret
```

Each file contains only its own value. The command creates the `client-id` and
`client-secret` keys. It does not put either value in shell history, Helm
values, or terminal output. To update a Secret that exists, use your approved
secret-management workflow. Do not copy rendered Secret YAML into an
environment file.

### Broker overlay

After the Secret exists, apply this non-secret overlay to the Broker release:

```yaml
providers:
  github:
    credentialsSecretName: github-oauth
    clientIDKey: client-id
    clientSecretKey: client-secret
```

`credentialsSecretName` alone enables personal GitHub OAuth. The defaults for
the two key names already match this Secret. As a result, the final two fields
are shown only to make the mapping explicit. This configuration does not
enable or change the optional shared GitHub App.

The chart injects these values into the Broker process as
`GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`. Before you offer
sign-in, make sure that the Broker runs with them. Refer to
[Restart the Broker](#restart-the-broker).

To make sure that the setup works, open an Agent's **Identities**. Select
**Add identity → GitHub → Other access methods → Personal account**. Complete
the sign-in in the popup. Select the GitHub REST API service. Then select
**Allow access**.

Make sure that the identity shows the expected GitHub account. Also make sure
that the connection is visible in **Connections**. Test with a disposable
repository whose result is safe to examine. A successful sign-in does not
prove organization approval. It also does not prove access to every
repository.

## Google personal OAuth

Google connections use a Web OAuth client and act as the connected Google
account. The Broker supports Gmail and Google Calendar, and it must have a
refreshable offline grant. It does not use a Google service account or a
Workspace domain-wide delegation grant.

### Configure the Google Cloud project

In a dedicated Google Cloud project, enable **Gmail API** and **Google Calendar
API**. In Google Auth Platform, configure **Branding** and **Audience** before
you create the client:

1. Select the audience that matches the deployment. An External audience is
   necessary for personal Google accounts and for users outside one Workspace
   organization.
2. While the app is in **Testing**, add every account that will test it. Users
   outside that test-user list cannot complete the flow.
3. Under **Data Access**, add `openid`,
   `https://www.googleapis.com/auth/userinfo.email`, `https://www.googleapis.com/auth/gmail.readonly`,
   `https://www.googleapis.com/auth/gmail.send`, and
   `https://www.googleapis.com/auth/calendar`.
4. Create an OAuth client of type **Web application**, with this exact
   Authorized redirect URI:
   `https://portal.example.com/api/v1/connections/callback`.

The Broker requests the `email` alias for `userinfo.email`, and also the other
four scopes above. It always sends `access_type=offline`, `prompt=consent`,
and `include_granted_scopes=false`. It must get a refresh token at the initial
connection and at reconnection. Addition of a service in the Portal does not
request a smaller set of scopes. Provider consent and the actual granted
scopes control the requests.

For external apps that use Gmail scopes, Google verification can be necessary
before broad production use. Before you change the audience or publishing
status, read Google's [OAuth consent configuration](https://developers.google.com/workspace/guides/configure-oauth-consent),
[scope categories](https://developers.google.com/identity/protocols/oauth2/scopes),
and [conditions to publish an app](https://support.google.com/cloud/answer/13464321).
In Testing mode, refresh tokens for these scopes expire after seven days. After
that, users must sign in again. Publication does not grant Google API access by
itself. Workspace administrators can still block or limit the app.

Download the JSON of the Web client to a local file with restricted access. If
you use the downloaded JSON, extract its `web.client_id` and
`web.client_secret` into separate files that each contain only the value. The
Broker cannot use the JSON file as a secret value. For example, these **local
file operations** do not print the values:

```bash
umask 077
jq -erj '.web.client_id' /secure/cetirun/google-oauth/downloaded-client.json \
  > /secure/cetirun/google-oauth/client-id
jq -erj '.web.client_secret' /secure/cetirun/google-oauth/downloaded-client.json \
  > /secure/cetirun/google-oauth/client-secret
```

Create the Broker Secret from those files (**connected, mutating operation**):

```bash
kubectl --namespace cetirun-credentials create secret generic google-oauth \
  --from-file=client-id=/secure/cetirun/google-oauth/client-id \
  --from-file=client-secret=/secure/cetirun/google-oauth/client-secret
```

### Broker overlay

Apply these non-secret values to the Broker release:

```yaml
providers:
  google:
    credentialsSecretName: google-oauth
    clientIDKey: client-id
    clientSecretKey: client-secret
```

The chart exposes the configured keys as `GOOGLE_OAUTH_CLIENT_ID` and
`GOOGLE_OAUTH_CLIENT_SECRET`. If you mount the Secret in a different location,
that mount does not configure the Broker. Before you offer sign-in, refer to
[Restart the Broker](#restart-the-broker).

To make sure that the setup works, use **Identities → Add identity → Google**.
Finish the consent in the popup. Select Gmail, Calendar, or both. Then select
**Allow access**. Examine the reported Google account and the granted scopes.
Then use a disposable message or calendar that has no customer data.

If an `invalid_grant` is recorded, the user must sign in again. For a provider
401 error, examine the connection status and the token refresh before you ask
for reauthentication. Addition of another Agent binding does not repair
expired or revoked provider authorization.

## Restart the Broker

The Broker reads the provider client credentials from environment variables
when it starts. Kubernetes does not refresh environment variables in a running
container:

- A change to a Broker overlay changes the Broker Pods. The next `make deploy`
  run or Helm upgrade replaces them. A restart is not necessary.
- A change to only a provider Secret does not replace the Pods. Restart the
  Broker.

The commands use the default Broker namespace. After `make deploy`, run only
the second command:

```bash
kubectl rollout restart deployment/cetirun-agent-broker -n cetirun-credentials
kubectl rollout status deployment/cetirun-agent-broker -n cetirun-credentials
```

Before you offer sign-in, wait until the rollout is complete and all replicas
are ready.

## Rotation, refresh, and removal

Rotate an OAuth client secret at the provider. Then update the local file and
the Kubernetes Secret for that client secret through the approved secret
workflow. After that, [restart the Broker](#restart-the-broker).
Before you retire any prior provider credential, make sure that a new
connection works. Keep the provider client ID
stable unless the provider's rotation procedure makes a new client necessary.
If you use a new client, a new consent flow is necessary.

When Google omits replacements during a refresh, the Broker keeps a returned
Google refresh token and granted scope set. It records a provider
`invalid_grant` as sign-in required. But temporary provider failures do not
discard a connection that works. GitHub personal OAuth tokens possibly do not
expire. But the Broker still issues short-lived internal credentials for each
request.

When you remove an Agent's access in CetiRun, the Broker stops future
credential issuance for that Agent. Removal of an unused connection from
**Connections** removes only the local encrypted connection record. It does
not revoke external consent. Users or provider administrators must revoke the
OAuth App in GitHub or Google separately. For the shared App, they must also
uninstall or change the App's GitHub installation.

Platform operators are still trusted to operate the Broker and its credential
encryption authority. Encryption at rest does not make a hosted provider
credential unavailable to the platform.
