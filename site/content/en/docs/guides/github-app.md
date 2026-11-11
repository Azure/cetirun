---
title: "Shared GitHub App"
description: "Give each Agent explicit repository and permission limits through a shared GitHub App."
weight: 46
---

A shared GitHub App gives your Agent access to the GitHub repositories that you
select, with the permissions that you select. Each Agent gets its own access.
The App installation sets the maximum access of the platform. The access of
each Agent can only narrow it.

Personal GitHub OAuth connections stay available separately. In the wizard,
this method is under **Other access methods** → **Personal account**, and you
cannot limit it to selected repositories. Refer to
[Authorize personal GitHub access](../agent-identity/#authorize-personal-github-access).

Before you can use the App, an administrator must register it for your
deployment. If the wizard says "Selected repository access is not configured in
this environment. Contact your platform administrator.", the App is not
registered. Administrators refer to
[Register the platform App](#register-the-platform-app).

## Give an Agent access

1. On the **Overview** tab of the Agent, find the **Identities** section.
   Select **Add identity**. Then select **GitHub**. The default method is
   **Selected repositories**, which uses the shared GitHub App.
2. Select **Sign in to GitHub**, and complete the sign-in in the pop-up window.
   If you already connected a GitHub account, you can select it instead.
   CetiRun authenticates this GitHub account separately from your portal
   account. A GitHub installation ID alone never grants access.
3. Select the personal account or the organization that has the repositories.
   If it is not in the list, select **Set up GitHub access**. On GitHub, select
   **Only select repositories** and the repositories that the platform can
   access. After you install the App or get the approval of your organization,
   return to CetiRun, and select **Refresh access**.
4. Select the repositories for this Agent. Then select **No access**,
   **Read**, or **Read and write** for **Code**, **Pull requests**, and
   **Issues**. Code starts as **Read**, and Pull requests and Issues start as
   **No access**.
5. Review the access for this Agent. Then select **Allow access**.

You must select one or more repositories and one or more permissions. The same
permissions apply to each repository of this access. Read access to the
repository metadata is always included. Other Agents must have their own
access, also when they have the same owner and use the same authorization.

Personal repositories must belong to the GitHub user who signs in. For
organization repositories, the user who configures the access must have the
**Admin** permission on each repository. GitHub organization policy still
controls installation and approval. Read or write access to an organization's
repository does not by itself let you install an App there. An installation on
your personal account does not give access to organization repositories.

## Change or remove access

To change the repositories or the permissions, select **Manage access**. Then
select **Save access**.

To remove the access of an Agent or a connection, refer to
[Manage identities and connections](../agent-identity/#manage-identities-and-connections).
The removal of a connection does not uninstall the App or change its GitHub
permissions. To uninstall the App, use the GitHub settings.

## Authorization and trust

Before the Broker issues each credential, it examines the current installation,
your repository administration authority, and the selected repositories and
permissions. If you revoke the user authorization, you must sign in again.
Removed repositories, suspended installations, unavailable GitHub checks, and
changed access prevent further issuance. There is no fallback to personal
OAuth.

The Agent uses a GitHub installation token with explicitly limited repository
and permission fields. Tokens stay in the platform services and never enter
the workload of the Agent. Installation tokens expire after one hour, and the
Broker does not cache them. You cannot recall requests that the Agent already
sent.

The platform holds the App private key and can mint tokens up to the full
GitHub authorization of the installation. Per-Agent limits are not a
replacement for trust in the platform operator. Separate installations isolate
individual tokens, but a compromised private key of the shared App can affect
all its installations.

The Broker also stores encrypted GitHub App user access and refresh tokens.
With these tokens, the Broker examines again the authority of the user who
configured the access. These tokens are not personal access tokens, but
operators with access to the Broker and its decryption authority can access
them. Encryption at rest does not prevent the use of credentials by the
platform. To limit the GitHub authority of the platform, install the App on
selected repositories.

GitHub attributes operations to the shared App bot. CetiRun audit identifies
the Agent, the connection, and the binding. For branch restrictions and merge
approvals, GitHub rulesets are still necessary. These permissions do not give
directory-level controls, or a guarantee that an Agent can open a PR but cannot
merge it. If information is public and accessible without authentication, a
repository allowlist does not make it private.

## Register the platform App

We recommend that platform administrators first follow
[Prepare Agent connections](../../operations/agent-identity/) until the namespace,
encryption, and certificate preparation is complete. After you register the
App with the steps below, return to that guide to finish the Broker and
platform deployment.

An environment maintainer creates the App in GitHub **Settings → Developer
settings → GitHub Apps → New GitHub App**. Replace
`https://portal.example.com` with the actual Portal origin of the deployment:

| Setting | Value |
| --- | --- |
| GitHub App name | A unique name for this CetiRun deployment |
| Homepage URL | `https://portal.example.com` |
| Callback URL | `https://portal.example.com/api/v1/connections/callback` |
| Expire user authorization tokens | Enabled |
| Request user authorization during installation | Disabled. CetiRun starts authorization separately |
| Enable Device Flow | Disabled |
| Setup URL | `https://portal.example.com/api/v1/connections/github-app/setup` |
| Redirect on update | Disabled |
| Webhook active | Disabled. CetiRun examines authorization when it issues credentials |
| Repository permissions | Contents, Pull requests, Issues: Read and write |
| Metadata permission | Read-only, mandatory in GitHub |
| Organization and account permissions | None |
| Where can this GitHub App be installed? | Any account |

With **Any account**, users can install the App on their personal accounts and
on the organizations that they manage. With **Only on this account**, only the
account that owns the App can install it. Select **Only on this account** if
all the repositories of the Agents belong to that account.

Do not grant Repository administration, Actions, or other permissions for this
integration. Record the App ID, the Client ID, and the slug from the public App
URL. Generate a client secret and an RSA private key. Keep both outside the
repository. Do not paste them into chat, logs, or Helm values.

Create a Secret in the Broker namespace with the keys `client-secret` and
`private-key.pem`. Use files that are stored with restrictive permissions. For
example:

```bash
kubectl --namespace cetirun-credentials create secret generic github-app \
  --from-file=client-secret=/private/github-app/client-secret \
  --from-file=private-key.pem=/private/github-app/private-key.pem
```

Configure the Broker chart with non-secret values and the Secret reference:

```yaml
providers:
  github:
    app:
      id: "12345"
      slug: cetirun-example
      clientId: Iv1.example
      credentialsSecretName: github-app
```

The private key is mounted only in the Broker. The signer of the Broker reads
the mounted key when it issues credentials. After you update the Secret, let
the Kubernetes Secret volume refresh. When you change the client secret,
restart the Broker, because the client secret is supplied through the
environment. The commands use the default Broker namespace:

```bash
kubectl rollout restart deployment/cetirun-agent-broker -n cetirun-credentials
kubectl rollout status deployment/cetirun-agent-broker -n cetirun-credentials
```

Before you offer sign-in, wait until the rollout is complete. Keep the previous
GitHub key until you make sure that the new key works. Then revoke the
previous key in GitHub.

### Test the GitHub App

For a functional test, use disposable private repositories:

- Allow one Agent to read repository A.
- Allow another Agent to manage Issues in repository B.
- Make sure that ungranted writes and cross-repository requests fail.

Repeat the test after you edit or remove Agent access. Do not use customer data
for acceptance tests.
