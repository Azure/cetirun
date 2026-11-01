---
title: "Shared GitHub App"
description: "Give each Agent explicit repository and permission limits through a shared GitHub App."
weight: 46
---

A shared GitHub App gives repository-scoped access to Agents. Personal GitHub
OAuth connections stay available separately. The App installation defines the
maximum access of the platform. Each Agent binding narrows the installation
token to selected repositories and permissions.

## Register the platform App

We recommend that platform administrators first follow
[Agent Identity setup](../../operations/agent-identity/) until the namespace,
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
| Webhook active | Disabled. This version examines authorization when it issues credentials |
| Repository permissions | Contents, Pull requests, Issues: Read and write |
| Metadata permission | Read-only, mandatory in GitHub |
| Organization and account permissions | None |
| Where can this GitHub App be installed? | Any account |

Do not grant Repository administration, Actions, or other permissions for this
integration. Record the App ID, the Client ID, and the slug from the public App
URL. Generate a client secret and an RSA private key. Keep both outside the
repository. Do not paste them into chat, logs, or Helm values.

Create a Secret in the Broker namespace with the keys `client-secret` and
`private-key.pem`. Use files that are stored with restrictive permissions. For
example:

```bash
KUBECONFIG=/private/environment/kubeconfig-admin kubectl \
  --namespace cetirun-credentials create secret generic github-app \
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
environment. Keep the previous GitHub key until you make sure that the new key
works. Then revoke the previous key in GitHub.

## Give an Agent access

1. Open the Agent's **Identities**. Select **Add identity**. Then select
   **GitHub**. The default flow is **Selected repositories**, which uses the
   shared GitHub App. The personal-account flow is under **Other access
   methods → Personal account**. You cannot narrow this flow to selected
   repositories.
2. Sign in to GitHub. CetiRun authenticates this account separately from your
   Portal account. A GitHub installation ID alone never grants access.
3. Install the App, or select an installation that already exists. On GitHub,
   select **Only select repositories** and the repositories that the platform
   can access. After you install the App or get organization approval, return
   to CetiRun. Select **Refresh access**.
4. Select repositories for this Agent. Then select **No access**, **Read**, or
   **Read and write** for **Code**, **Pull requests**, and **Issues**. Code
   starts as **Read**, and Pull requests and Issues start as **No access**. At
   least one repository and one permission are necessary. The same permission
   selection applies to every repository in this binding. Metadata read access
   is implicit.
5. Review the access for this Agent. Then select **Allow access**. Other Agents
   must have their own explicit bindings, even when they have the same owner
   and reuse this authorization.

Personal repositories must belong to the signed-in GitHub user. In this
version, for organization repositories, the user who configures the access must
have repository **Admin** permission. GitHub organization policy still controls
installation and approval. Read or write access to an organization's repository
does not by itself let you install an App there. An installation on your
personal account does not grant access to organization repositories.

To change repositories or permissions, use **Manage access**. Then select
**Save access**. The Agent's **Remove access** menu item removes only the
association of that Agent. Multiple Agents can share saved authorizations. The
removal of access from one Agent does not affect the other Agents.

**Connections** can remove a connection only after no Agent uses it. The
removal of a connection does not uninstall the App or change its GitHub
permissions. Manage or uninstall the App separately in GitHub settings.

## Authorization and trust

Before the Broker issues each execution credential, it examines the current
installation, the user's repository administration authority, and the selected
repositories and permissions. If the user authorization is revoked, the user
must sign in again. Removed repositories, suspended installations, unavailable
GitHub checks, and changed bindings prevent further issuance. There is no
personal OAuth fallback.

Execution uses a GitHub installation token with explicitly limited repository
and permission fields. Tokens stay in Broker/MCP and never enter Agent
workloads. Installation tokens expire after one hour. In this version, the
Broker does not cache them. Requests that were already sent cannot be recalled.

The platform holds the App private key and can mint tokens up to the full GitHub
authorization of the installation. Per-Agent limits are not a replacement for
trust in the platform operator. Separate installations isolate individual
tokens, but a compromised private key of the shared App can affect all its
installations.

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

For a functional test, use disposable private repositories:

- Allow one Agent to read repository A.
- Allow another Agent to manage Issues in repository B.
- Make sure that ungranted writes and cross-repository requests fail.

Repeat the test after you edit or remove Agent access. Do not use customer data
for acceptance tests.
