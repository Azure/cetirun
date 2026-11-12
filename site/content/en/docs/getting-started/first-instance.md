---
title: "Create your first Agent"
description: "Create a Hermes, OpenClaw, or Custom Agent in the portal, open it, and sign in to its models."
weight: 10
---

An Agent is your own long-lived AI agent. You create it in the CetiRun portal.
It keeps its files in a persistent home directory when it restarts. This page shows
how to create an Agent, open it, and connect its models.

## Before you begin

- Get the portal address from your administrator. In these docs,
  `https://example.com/` is only a placeholder.
- Make sure that you are a member of the Microsoft Entra ID group that the
  portal allows. Your administrator manages this group.
- Know where your models come from. Your Agent gets its models from the
  platform's AI Gateway: the models that your administrators share, or the
  models of your own GitHub Copilot subscription.

## Decide before you create

Some choices cannot change in the portal after you create the Agent. These
choices are the Agent name, the agent type, the Team, the model sources, and
Agent Identity.
Nobody can change the Agent name or the agent type. For the other choices,
ask your administrator. For example, an administrator can change the Team of
a suspended Agent.

To use another name or agent type, delete the Agent and create a new one. A
new Agent starts with an empty home directory. For the data that deletion
removes, refer to [Protect your data](../../guides/data/).

By default, you can have one Agent. For this limit and the other limits,
refer to [Default limits](../../guides/troubleshooting/#default-limits).

To compare Hermes, OpenClaw, and Custom, refer to
[Choose an Agent type](../../guides/agent-types/).

## Create the Agent

1. Open the portal and sign in. The **My Agents** page opens.
2. Select **New Agent**. The creation form opens.
3. Complete the fields of the form. The table that follows lists them in the
   order of the form. Select the Team first. A change of the Team can reset the
   Agent type and clear the fields that you completed.
4. Select **Create Agent**.

| Field | What to enter |
| --- | --- |
| **Agent Name (optional)** | A short name, or nothing. The portal puts your alias in front of it. For the synthetic owner `alice`, the name `example` gives the Agent name `alice-example`. If you leave the field empty, the Agent name is your alias. Use lowercase letters, digits, and hyphens. The complete Agent name can have a maximum of 35 characters. |
| **Team (optional)** | Your Team, or **(none)**. The Agent Policy of a Team can limit the Agent types, the models, and the other options that you can select. If the Team has an Agent Policy, the form shows its name and its request limits. An Agent of a policy-bound Team runs in the Sandbox of that Team. In this Sandbox, Tailscale is not available. |
| **Agent Type** | **Hermes**, **OpenClaw**, or **Custom**. Hermes is the default. The form shows only the types that the deployment and the Team allow. |
| **Agent Image (optional)** | Nothing, unless your administrator gives you an image. An empty field uses the platform default image. A Custom Agent must have one of the approved images that the field suggests. Refer to [Choose an Agent type](../../guides/agent-types/#custom). |
| **Display Name (optional)** | The name that the portal shows. The default is your sign-in name, for example `alice@example.com`. |
| **Use private registry credentials** | Keep it off, unless your administrator tells you that the image is in a private registry. |
| **Use shared models** | On, to use the models that your administrators share. The hint lists these models. The form shows this switch only when shared models are available. |
| **Use GitHub Copilot** | On, to use the models of your own GitHub Copilot subscription. You sign in to GitHub after you create the Agent. The form shows this switch only when the deployment offers GitHub Copilot. At least one model source stays on. |
| **Default model (optional)** | **(automatic)**, or the model that new conversations start with. **(automatic)** uses the deployment default. Smart-routing models show `(smart routing)` after their names. You can select GitHub Copilot models only after you sign in. The form shows this field only when shared models or smart-routing models are available, and not for Custom Agents. |
| **Gateway Token (optional)** | Nothing. The platform then generates the token. The OpenClaw gateway token protects the OpenClaw Control UI. It is not a model credential. Hermes uses its own key. |
| **Tailscale Auth Key (optional)** | Nothing, unless you use Tailscale. The form shows this field only for Hermes and OpenClaw, and only when the deployment allows Tailscale. Refer to [Connect Tailscale](../../guides/tailscale/). |
| **Enable Microsoft Entra Agent Identity** | On, if the Agent must work with Azure resources through its own identity. The new identity has no Azure permissions until a resource owner assigns them. The form shows this switch only when the deployment and the Team allow it. Refer to [Agent Connections and Identity](../../guides/agent-identity/). |

Under the fields, the form tells you about memory. When the deployment installs
the Memory component, memory is on, and your Agent keeps private notes from
your conversations. All the conversations of the Agent share this memory, also
group chats and channels. Before you connect the Agent to a shared
conversation, read [Use memory](../../guides/memory/#which-conversations-use-memory).

If the form says that no models are available, you cannot create the Agent
with this Team. Ask your administrator to examine model access.

## Wait until the Agent is ready

After you select **Create Agent**, the form closes, and the new Agent shows in
**My Agents**. The badge next to the Agent name shows its phase:

| Phase | Meaning | What to do |
| --- | --- | --- |
| **Pending** | The platform accepted the request and did not start work yet. | Wait. If a governance message shows, follow it. |
| **Provisioning** | The platform creates the storage, the workload, and the route of the Agent. | Wait. The **Overview** tab of the Agent shows "Provisioning your agent...". |
| **Ready** | The Agent runs, and you can open it. | Open the Agent. Model sign-in can still be in progress. |
| **Failed** | A step failed. | Open the **Status** tab. Send the reason to your administrator. |
| **Suspended** | The Agent is stopped. Its home directory is kept. | Select **Resume Agent**. |
| **Deleting** | The platform removes the Agent. | No action is possible. |

Provisioning can take several minutes, for example while the platform
downloads the image. If the phase does not change after 10 minutes, open the
Agent and select the **Status** tab. The **Conditions** table shows which step
waits and why.

A message box above the tabs can show a governance status, for example
**Blocked by policy** or **Waiting for governance**. For each status and the
next step, refer to [Troubleshoot your Agent](../../guides/troubleshooting/#the-agent-does-not-become-ready).

## Open your Agent

Open the Agent from **My Agents**. In the sidebar, select **Agent UI**. The
Agent UI opens in the same browser tab. **Agent UI**,
**Terminal**, and **Desktop** are available only when the phase is **Ready**.
For the other parts of the Agent page, refer to
[Tour the portal](../../guides/portal/).

### Open a Hermes Agent

Select **Agent UI**. The Portal reads the current Agent key, signs you in to
Hermes, and opens the Webapp in the same tab. The button shows
**Opening Agent UI...** while the sign-in completes. If it fails, the Portal
shows the reason below the button. Follow that message and try again.

Automatic sign-in uses the Webapp on the Portal address. If you open the
Webapp directly and it asks you to sign in, use the username `cetirun`.
For the password, copy **Hermes API / Webapp Key (user: cetirun)** from the
**Overview** tab. If a Webapp session expires, you can also return to the
Portal and select **Agent UI** again.

The key stays the same after a restart, and after you suspend and resume the
Agent. The **Username** line on the **Overview** tab shows the Linux user in
the container, not the Webapp user.

The Webapp runs inside the Hermes gateway process. When you close the
browser, this process continues to run. A restart of the process ends the
shells of the Webapp terminal. Your files, sessions, and scheduled jobs stay in the home
directory. If you connect Hermes to a message channel, such as Discord, you can
continue a session of that channel in the Webapp. Hermes then sends its replies
to the channel too.

### Open an OpenClaw Agent

1. Select **Agent UI**. The OpenClaw Control UI asks for the gateway token.
2. Copy **Gateway Token** from the **Overview** tab, and enter it.
3. If the Control UI asks to pair the browser, select **Terminal** in the
   portal sidebar. A shell in the Agent's container opens.
4. Run `openclaw devices list`. Find the pending request of your browser.
5. Run `openclaw devices approve <request-id>`.
6. Reload the Control UI.

OpenClaw asks to pair each new browser one time.

## Sign in to GitHub Copilot

Do these steps if you selected **Use GitHub Copilot** in the form. Find the
**GitHub Copilot** part of the **Models** section on the **Overview** tab.

1. Select **Sign in with GitHub**. The dialog "Sign in to GitHub Copilot" shows
   a code.
2. Copy the code, and select **Open GitHub**. GitHub opens in a new tab.
3. On GitHub, enter the code and authorize the sign-in. The dialog shows
   "Waiting for authorization..." until GitHub confirms the sign-in.
4. When the dialog shows the result, select **Close**. Do not select
   **Cancel** while the dialog waits, because it cancels the sign-in.

The badge next to **GitHub Copilot** then shows the sign-in progress. The page
refreshes by itself.

| Badge | Meaning | What to do |
| --- | --- | --- |
| **Sign-in required** | The Agent has no GitHub sign-in. | Select **Sign in with GitHub**. |
| **Starting** | The platform prepares your Copilot models. | Wait. This usually takes a few minutes. Then the Agent restarts one time to get the models. |
| **Ready** | Your Copilot models are in the model list. | No action is necessary. |
| **Degraded** | The platform cannot use your sign-in or your models. | Read the message under the badge. If it says "Signing in again may help", select **Sign in again**. |

If the badge does not become **Ready**, refer to
[GitHub Copilot does not become ready](../../guides/troubleshooting/#github-copilot-does-not-become-ready).
For the other model tasks, refer to [Choose a model](../../guides/models/).

## Give the Agent access to Azure and other services

If the Agent must work with Azure, GitHub, or Google, give it access on the
**Overview** tab. In the **Identities** section, select **Add identity**. Then
select the service. For the steps, refer to
[Agent Connections and Identity](../../guides/agent-identity/).

The **Identities** section shows only when your deployment allows connected
services. If you cannot find it, ask your administrator.

## Next steps

- To manage the Agent, refer to [Manage your Agent](../../guides/lifecycle/).
- To keep your work safe, refer to [Protect your data](../../guides/data/).
- If something does not work, refer to
  [Troubleshoot your Agent](../../guides/troubleshooting/).

Administrators can also create Agents with the Kubernetes API. Refer to
[Administer Agents](../../operations/agent-lifecycle/#create-an-agent-with-kubernetes).

Sources: [creation form]({{< repo "app/cetirun-portal/web/src/components/CreateForm.tsx" >}}),
[Agent overview]({{< repo "app/cetirun-portal/web/src/components/OverviewSection.tsx" >}}),
[model section]({{< repo "app/cetirun-portal/web/src/components/AIGatewayBlock.tsx" >}}),
and [portal instance handler]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).
