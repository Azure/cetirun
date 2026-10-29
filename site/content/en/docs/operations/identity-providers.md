---
title: "Identity provider setup"
description: "Register GitHub and Google providers for Agent Identity connections."
weight: 35
---

This page prepares the external providers used by owner-scoped Agent Identity
connections. Follow [Agent Identity setup](../agent-identity/) through its
encryption and namespace preparation, then use this page at the provider
registration step before deploying the Broker. For Entra applications and
Azure or Microsoft delegated identities,
use [Entra Agent Identity setup](../entra-agent-identity/).

The provider clients below are for Agent connections. They are separate from
the Portal login application and its `https://portal.example.com/oauth2/callback`
redirect. Do not register that login redirect in a GitHub or Google client for
the Broker.

Use the real Portal origin in place of `https://portal.example.com`. Store
client secrets in access-controlled local files and create the Kubernetes
Secret only in the Broker namespace. Do not commit credentials, place them in
Helm values, or paste a Secret manifest or command output into a ticket.

## Shared GitHub App

Use a shared GitHub App when an Agent needs selected repositories and explicit
Contents, Pull requests, or Issues permissions. A shared App installation is
the platform-wide maximum; every Agent binding then selects repositories and
the allowed permission level. GitHub actions run as the App bot.

Follow the complete [shared GitHub App registration guide](../../guides/github-app/)
to register the App, create its client secret and RSA key, install it, and
configure `providers.github.app`. That guide is the source for the required
callback and setup URLs, App permissions, key rotation, and the platform trust
boundary. Its App credentials are separate from the personal OAuth App below.

The Broker supports personal GitHub OAuth and the shared App at the same time.
An App installation does not make a personal OAuth connection repository
scoped, and a personal OAuth consent never substitutes for App installation
authority.

## GitHub personal OAuth

Personal GitHub OAuth is appropriate only where the connected user's existing
GitHub access is acceptable for the Agent. It is intentionally broad: the
Broker requests the `repo` scope and executes as the connected user. It cannot
limit a personal OAuth token to selected repositories, branches, or individual
GitHub operations. Prefer the shared App for repository-specific access.

### Register the OAuth App

An organization or platform administrator creates a standard GitHub **OAuth
App** in **Settings → Developer settings → OAuth Apps → New OAuth App**.
Use a deployment-specific name and enter these values:

| Setting | Value |
| --- | --- |
| Homepage URL | `https://portal.example.com` |
| Authorization callback URL | `https://portal.example.com/api/v1/connections/callback` |
| Requested OAuth scope | `repo` (requested by the Broker; it is not entered in the App form) |

Register the exact URL the Broker sends as `redirect_uri`, rather than the
Portal login `/oauth2/callback` path. See GitHub's
[OAuth App registration instructions](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app) and
[organization OAuth restrictions](https://docs.github.com/organizations/managing-oauth-access-to-your-organizations/about-oauth-app-access-restrictions).

GitHub organization owners can restrict third-party OAuth Apps, require
approval, or require SAML SSO authorization. These policies still apply after
a user grants `repo`; they can prevent access to organization repositories.
Do not represent a user's ordinary repository membership as installation or
administrator permission. A user who needs organization access must complete
the organization's approval and SSO process under its own policy.

Generate a client secret and record the displayed Client ID. Save the secret
to a restrictive local file, then create the Secret in the Broker namespace:

```bash
kubectl --namespace cetirun-credentials create secret generic github-oauth \
  --from-file=client-id=/secure/cetirun/github-oauth/client-id \
  --from-file=client-secret=/secure/cetirun/github-oauth/client-secret
```

The files contain only their respective values. The command creates the
`client-id` and `client-secret` keys without putting either value in shell
history, Helm values, or terminal output. Use your approved secret-management
workflow to update an existing Secret; do not copy rendered Secret YAML into
an environment file.

### Broker overlay

Apply this non-secret overlay to the Agent Identity Broker release after the
Secret exists:

```yaml
providers:
  github:
    credentialsSecretName: github-oauth
    clientIDKey: client-id
    clientSecretKey: client-secret
```

`credentialsSecretName` alone enables personal GitHub OAuth. The defaults for
the two key names already match this Secret, so the final two fields are shown
only to make the mapping explicit. This configuration does not enable or
change the optional shared GitHub App.

The chart injects these values into the Broker process as
`GITHUB_OAUTH_CLIENT_ID` and `GITHUB_OAUTH_CLIENT_SECRET`. Kubernetes does not
refresh environment variables in a running container. After installing or
updating the Secret or this overlay, restart the Broker Deployment through the
approved rollout process and wait for its `/readyz` probe and all replicas to
be ready before offering sign-in.

To verify, open an Agent's **Identities**, select **Add identity → GitHub →
Other access methods → Personal account**, and complete the popup sign-in.
Choose the GitHub REST API service, then select **Allow access**. Confirm that
the identity reports the expected GitHub account and that the connection is
visible in **Connections**. Test with a disposable repository whose result is
safe to inspect. A successful sign-in proves neither organization approval nor
access to every repository.

## Google personal OAuth

Google connections use a Web OAuth client and act as the connected Google
account. The Broker supports Gmail and Google Calendar, and requires a
refreshable offline grant. It does not use a Google service account or a
Workspace domain-wide delegation grant.

### Configure the Google Cloud project

In a dedicated Google Cloud project, enable **Gmail API** and **Google Calendar
API**. In Google Auth Platform, configure **Branding** and **Audience** before
creating the client:

1. Choose the audience that matches the deployment. An External audience is
   required for personal Google accounts and users outside one Workspace
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

The Broker requests the `email` alias for `userinfo.email`, along with the
other four scopes above. It always sends `access_type=offline`, `prompt=consent`,
and `include_granted_scopes=false`. It requires a
refresh token at initial connection and reconnect. Adding a service in the
Portal does not request a smaller scope set; provider consent and the actual
granted scopes control requests.

External apps using Gmail scopes can require Google verification before broad
production use. Review Google's [OAuth consent configuration](https://developers.google.com/workspace/guides/configure-oauth-consent),
[scope categories](https://developers.google.com/identity/protocols/oauth2/scopes),
and [publishing requirements](https://support.google.com/cloud/answer/13464321)
before changing the audience or publishing status. Testing-mode refresh tokens
for these scopes expire after seven days; users must sign in again. Publishing
does not grant Google API access by itself, and Workspace administrators may
still block or limit the app.

Download the Web client's JSON to a restrictive local file. If using the
downloaded JSON, extract its `web.client_id` and `web.client_secret` into
separate value-only files; the Broker cannot consume the JSON file as a secret
value. For example, these **local file operations** do not print the values:

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

Apply the following non-secret values to the Agent Identity Broker release:

```yaml
providers:
  google:
    credentialsSecretName: google-oauth
    clientIDKey: client-id
    clientSecretKey: client-secret
```

The chart exposes the configured keys as `GOOGLE_OAUTH_CLIENT_ID` and
`GOOGLE_OAUTH_CLIENT_SECRET`. Restart the Broker Deployment after a client
credential or overlay change, then wait for the rollout and `/readyz` probe to
complete. Mounting the Secret elsewhere does not configure the Broker.

To verify, use **Identities → Add identity → Google**, finish consent in the
popup, select Gmail, Calendar, or both, and select **Allow access**. Check the
reported Google account and granted scopes, then use a disposable message or
calendar that has no customer data. A recorded `invalid_grant` requires the
user to sign in again. For a provider 401, check connection status and token
refresh before asking for reauthentication; adding another Agent binding does
not repair expired or revoked provider authorization.

## Rotation, refresh, and removal

Rotate an OAuth client secret at the provider, update the matching local file
and Kubernetes Secret through the approved secret workflow, restart the Broker
Deployment, and verify a new connection before retiring any prior provider
credential. Keep the provider client ID stable unless the provider's rotation
procedure requires a new client; a new client requires a new consent flow.

The Broker preserves a returned Google refresh token and granted scope set when
Google omits replacements during a refresh. It records a provider
`invalid_grant` as sign-in required, while temporary provider failures do not
discard a working connection. GitHub personal OAuth tokens may be non-expiring;
the Broker still issues short-lived internal credentials for each request.

Removing an Agent's access in CetiRun stops future Broker credential issuance
for that Agent. Removing an unused connection from **Connections** removes only
the local encrypted connection record; it does not revoke external consent.
Users or provider administrators must separately revoke the OAuth App in
GitHub or Google and, for the shared App, uninstall or change the App's GitHub
installation. Platform operators remain trusted to operate the Broker and its
credential encryption authority; encryption at rest does not make a hosted
provider credential unavailable to the platform.
