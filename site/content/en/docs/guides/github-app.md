---
title: "Shared GitHub App"
description: "Give each Agent explicit repository and permission limits through a shared GitHub App."
weight: 46
---

A shared GitHub App provides repository-scoped access for Agents. Personal
GitHub OAuth connections remain available separately. The App installation
defines the platform's maximum access; each Agent binding narrows the
installation token to selected repositories and permissions.

## Register the platform App

An environment maintainer creates the App in GitHub **Settings → Developer
settings → GitHub Apps → New GitHub App**. Use the deployment's actual Portal
origin in place of `https://portal.example.com`:

| Setting | Value |
| --- | --- |
| GitHub App name | A unique name for this CetiRun deployment |
| Homepage URL | `https://portal.example.com` |
| Callback URL | `https://portal.example.com/api/v1/connections/callback` |
| Expire user authorization tokens | Enabled |
| Request user authorization during installation | Disabled; CetiRun starts authorization separately |
| Enable Device Flow | Disabled |
| Setup URL | `https://portal.example.com/api/v1/connections/github-app/setup` |
| Redirect on update | Disabled |
| Webhook active | Disabled; this version checks authorization when issuing credentials |
| Repository permissions | Contents, Pull requests, Issues: Read and write |
| Metadata permission | Read-only, required by GitHub |
| Organization and account permissions | None |
| Where can this GitHub App be installed? | Any account |

Do not grant Repository administration, Actions or other permissions for this
integration. Record the App ID, Client ID and the slug from its public App URL.
Generate a client secret and an RSA private key. Keep both outside the repository
and do not paste them into chat, logs or Helm values.

Create a Secret in the Broker namespace with keys `client-secret` and
`private-key.pem`, using files stored with restrictive permissions. For example:

```bash
KUBECONFIG=/private/environment/kubeconfig-admin kubectl \
  --namespace cetirun-credentials create secret generic github-app \
  --from-file=client-secret=/private/github-app/client-secret \
  --from-file=private-key.pem=/private/github-app/private-key.pem
```

Configure the Broker chart using non-secret values and the Secret reference:

```yaml
providers:
  github:
    app:
      id: "12345"
      slug: cetirun-example
      clientId: Iv1.example
      credentialsSecretName: github-app
```

The private key is mounted only in the Broker. Its signer reads the mounted key
when issuing credentials. After updating the Secret, allow the Kubernetes Secret
volume to refresh; restart the Broker when changing the client secret because it
is supplied through the environment. Keep the previous GitHub key until the new
key has been verified, then revoke the previous key in GitHub.

## Give an Agent access

1. Open the Agent's **Service access**, choose **Add service**, then choose
   **GitHub**. The default flow is **Selected repositories**, which uses the
   shared GitHub App. The personal-account flow is under **Other access
   methods → Personal account** and cannot be narrowed to selected repositories.
2. Sign in to GitHub. CetiRun verifies this account separately from your Portal
   account; a GitHub installation ID alone never grants access.
3. Install the App or select an existing installation. On GitHub, choose
   **Only select repositories** and the repositories the platform may access.
   Return to CetiRun and select **Refresh access** after installing or receiving
   organization approval.
4. Select repositories for this Agent, then choose **No access**, **Read**, or
   **Read and write** for **Code**, **Pull requests**, and **Issues**. Code
   starts as **Read**; Pull requests and Issues start as **No access**. At least
   one repository and one permission are required. The same permission selection
   applies to every repository in this binding; Metadata read access is implicit.
5. Review the access for this Agent and select **Allow access**. Other Agents
   need their own explicit bindings, even when they have the same owner and
   reuse this authorization.

Personal repositories must belong to the signed-in GitHub user. For organization
repositories, this version requires the configuring user to have repository
**Admin** permission. GitHub organization policy still governs installation and
approval. Read or write access to an organization's repository does not by
itself permit installing an App there. Installing on your personal account does
not grant access to organization repositories.

Use **Manage access** to change repositories or permissions, then select
**Save access**. The Agent's **Remove access** menu item removes only that
Agent's association. Saved authorizations can be shared by multiple Agents;
removing access from one Agent does not affect the others. **Connected services**
can remove a connection only after no Agent uses it. Removing it does not
uninstall the App or change its GitHub permissions; manage or uninstall the App
separately in GitHub settings.

## Authorization and trust

Before issuing each execution credential, the Broker checks the current
installation, the user's repository administration authority, and the selected
repositories and permissions. Revoked user authorization requires signing in
again. Removed repositories, suspended installations, unavailable GitHub checks,
and changed bindings prevent further issuance; there is no personal OAuth fallback.

Execution uses a GitHub installation token with explicitly reduced repository
and permission fields. Tokens remain in Broker/MCP and never enter Agent
workloads. Installation tokens expire after one hour; the Broker does not cache
them in this version. Already-sent requests cannot be recalled.

The platform holds the App private key and can mint tokens up to the installation's
full GitHub authorization. Per-Agent limits do not remove the need to trust the
platform operator. Separate installations isolate individual tokens, but a
compromised shared App private key can affect all its installations.

The Broker also stores encrypted GitHub App user access and refresh tokens to
recheck the configuring user's authority. These are not personal access tokens,
but operators with access to the Broker and its decryption authority can access
them. Encryption at rest does not make the platform unable to use credentials.
Install the App on selected repositories to limit the platform's GitHub authority.

GitHub attributes operations to the shared App bot. CetiRun audit identifies
the Agent, connection and binding. Branch restrictions and merge approvals still
need GitHub rulesets; these permissions do not provide directory-level controls
or a guarantee that an Agent can open a PR but cannot merge it. Public information
accessible without authentication is not made private by a repository allowlist.

For a functional test, use disposable private repositories: allow one Agent to
read repository A, another to manage Issues in repository B, and verify that
ungranted writes and cross-repository requests fail. Repeat after editing or
removing Agent access. Do not use customer data for acceptance tests.
