---
title: "Tour the portal"
description: "Find the parts of the CetiRun portal: the header, the Agent list, the Agent sidebar, and the Overview and Status tabs."
weight: 5
---

The CetiRun portal is the web application where you create, open, and manage
your Agents. This page shows where to find each part of the portal.

## Header

The header shows on every page of the portal.

| Item | What it does | When it shows |
| --- | --- | --- |
| **CetiRun Portal** | Opens **My Agents**. | Always. |
| **Admin** | Opens the administration pages. | Only for portal administrators. |
| **Connections** | Lists the personal accounts that you connected, and the Agents that use them. Refer to [Agent Connections and Identity](../agent-identity/#manage-identities-and-connections). | Only when the deployment allows connected services. |
| **Local history** | Opens the Chat conversations that this browser keeps. Refer to [Chat with a Custom Agent](../custom-chat/#where-history-lives). | Only when the deployment allows Portal Chat. |
| **Theme** | Selects **System**, **Light**, or **Dark**. | Always. |
| **Sign out** | Signs you out of the portal. | Always. |

If the deployment allows Portal Chat, **Sign out** opens a dialog. In the
dialog, you can clear the local chat history of your account in this browser.

## My Agents

**My Agents** is the home page of the portal. It lists your Agents. Each Agent
card shows these items:

- The Agent name and the display name.
- A badge with the Agent type: **Hermes**, **OpenClaw**, or **Custom**.
- A badge with the phase, for example **Ready** or **Provisioning**. For the
  phases, refer to
  [Wait until the Agent is ready](../../getting-started/first-instance/#wait-until-the-agent-is-ready).
- A governance status, if the platform holds the Agent. Refer to
  [Troubleshoot your Agent](../troubleshooting/#the-agent-does-not-become-ready).

To create an Agent, select **New Agent**. To open an Agent, select its card.

## Agent sidebar

When you open an Agent, a sidebar shows on the left side of the page.

| Entry | What it opens |
| --- | --- |
| **All Agents** | Opens **My Agents** again. |
| **Overview** | The details and the controls of the Agent. Refer to [Overview tab](#overview-tab). |
| **Chat** | The Portal Chat page. It shows only for a Custom Agent that serves the Chat API, and only when the deployment allows Portal Chat. Refer to [Chat with a Custom Agent](../custom-chat/). |
| **Agent UI** | The browser interface of the Agent: the Hermes Webapp or the OpenClaw Control UI. For a Custom Agent with a Web UI, this entry is **Web UI**. |
| **Terminal** | A `zsh` shell in the container of the Agent. In a Hermes or OpenClaw Agent, the shell runs as the Linux user `vibe`, and the home directory is `/home/vibe`. The files in the home directory are kept when the Agent restarts. |
| **Desktop** | A remote desktop with Google Chrome. The browser tools of Hermes and OpenClaw use this Chrome, so you can see what the Agent does in the browser. |

**Agent UI**, **Terminal**, and **Desktop** are unavailable until the phase is
**Ready**. They open in the same browser tab, and they leave the portal. To
return to the portal, use the back button of the browser. **Web UI** of a Custom Agent opens
in a new tab.

## Overview tab

The **Overview** tab of an Agent shows these items, from top to bottom:

- The display name and the phase badge.
- A governance message, if the platform holds the Agent. The message gives a
  next step.
- The Agent name, the owner, the Team, and the configuration: type, image,
  username, and port.
- The credential that opens the Agent UI. A Hermes Agent shows
  **Hermes API / Webapp Key (user: cetirun)**. An OpenClaw Agent shows
  **Gateway Token**. Use the eye button to show the value, and the copy button
  to copy it.
- **Models**: the models that the Agent can use, the default model, the
  request limits of the Agent Policy, and the GitHub Copilot sign-in. Refer to
  [Choose a model](../models/#select-and-switch-models). This section does not
  show while the Agent is suspended.
- **Team skills**: the skills that your Team selected for the Agent. Refer to
  [Use skills](../skills/#team-skills).
- **Identities**: the accounts and the Azure identity that the Agent uses for
  external services. Refer to
  [Agent Connections and Identity](../agent-identity/).
- The lifecycle buttons: **Upgrade to Latest**, **Edit Agent**,
  **Restart Agent**, **Suspend Agent** or **Resume Agent**, and
  **Delete Agent**. Refer to [Manage your Agent](../lifecycle/).

Some items show only when the deployment allows them, for example
**Identities**.

## Status tab

The **Status** tab shows the progress and the health of the Agent.

- **Conditions** lists the checks of the platform. Each row has a type, a
  status (`True`, `False`, or `Unknown`), a reason, and a message. A row with
  `False` shows which step fails and why.
- **Pods** lists the workload of the Agent. Each Pod shows its phase, if it is
  ready, and the number of restarts.
- **Agent chart unavailable** shows above **Conditions** when the platform
  cannot download the chart of the Agent. Refer to
  [The Agent chart is unavailable](../troubleshooting/#the-agent-chart-is-unavailable).

Use the **Status** tab when the Agent does not become **Ready**. Refer to
[Troubleshoot your Agent](../troubleshooting/).

Sources: [header]({{< repo "app/cetirun-portal/web/src/components/Header.tsx" >}}),
[Agent list]({{< repo "app/cetirun-portal/web/src/pages/InstanceListPage.tsx" >}}),
[Agent page]({{< repo "app/cetirun-portal/web/src/pages/WorkspacePage.tsx" >}}),
[Overview tab]({{< repo "app/cetirun-portal/web/src/components/OverviewSection.tsx" >}}),
and [Status tab]({{< repo "app/cetirun-portal/web/src/components/StatusTab.tsx" >}}).
