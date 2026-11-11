---
title: "Manage your Agent"
description: "Edit, restart, suspend, resume, upgrade, or delete your Agent in the portal, and know what each action keeps."
weight: 40
---

You manage your Agent on its **Overview** tab in the portal. To open the tab,
select the Agent in **My Agents**. The buttons under the Agent details start
these actions:

| Button | What it does |
| --- | --- |
| **Edit Agent** | Changes the display name, the image, the gateway token, or the Tailscale key. |
| **Restart Agent** | Starts the Agent again. The home directory is kept. |
| **Suspend Agent** and **Resume Agent** | Stops the Agent and keeps its home directory. Then starts it again. |
| **Upgrade to Latest** | Moves the Agent to the agent chart version that your administrator selected. The button shows only when a newer version is available. |
| **Delete Agent** | Removes the Agent and its data. You cannot undo this action. |

For the data that each action keeps or removes, refer to
[Protect your data](../data/#what-each-action-keeps).

## Edit your Agent

1. Select **Edit Agent**. The **Edit Agent** dialog opens.
2. Change the fields that you need.
3. Select **Save**.

| Field | What you can do |
| --- | --- |
| **Display Name** | Change the name that the portal shows. Leave the field blank to show the Agent name. |
| **Team** | Nothing. Only an administrator can change the Team, and only while the Agent is suspended. |
| **Agent Type** and **Agent Username** | Nothing. These values cannot change after you create the Agent. |
| **Agent Image** | Enter another image. The platform must approve the image, and the image must be compatible with the Agent type. You cannot remove an image after it is set. |
| **HTTP Port (optional)** | For a Hermes or OpenClaw Agent, keep the field empty. An empty field uses the default port of the pinned agent chart version. |
| **Gateway Token** | Enter a new gateway token for OpenClaw. Leave the field empty to keep the current token. |
| **Tailscale Auth Key** | Enter a new Tailscale key, or select **Disable Tailscale**. The dialog shows this field only when the deployment allows Tailscale. |

When you change the image, the gateway token, or the Tailscale key, the Agent
restarts. During the restart, **Agent UI** is unavailable for a short time.

To change the default model, use the **Models** section on the **Overview**
tab. Refer to [Choose a model](../models/#select-and-switch-models).

## Restart while retaining HOME

Restart the Agent when it does not respond, or when the portal tells you that
a restart is necessary. For example, the **Team skills** section tells you when
the skills of your Team changed after the Agent started.

1. Select **Restart Agent**.
2. In the **Restart Agent** dialog, select **Restart**.

The page shows "Restart requested; waiting for the replacement Pod to become
ready." until the new Agent process is ready. The restart keeps the files in
your home directory (HOME), and it stops the active sessions. For the other
data, refer to [What each action keeps](../data/#what-each-action-keeps).

**Restart Agent** is available only when the Agent is **Ready** and no other
restart waits. It is not available while the Agent is suspended.

If the dialog shows an error, select **Retry request**. A retry sends the same
request again, so the Agent does not restart two times. If the dialog says that
the request was not accepted, close the dialog. Then select **Restart Agent**
again.

## Suspend and resume your Agent

Suspend the Agent when you do not use it for some time. A suspended Agent does
not run, and it does not answer messages. Its home directory and its Gateway
key are kept.

> **Warning:** Suspension removes the GitHub Copilot sign-in of the Agent.
> After you resume the Agent, sign in to GitHub Copilot again.

To suspend the Agent, do these steps:

1. Select **Suspend Agent**.
2. In the **Suspend Agent** dialog, select **Suspend**.

The page shows "Suspension requested" while the platform stops the Agent. The
phase badge changes to **Suspended** when the controller reports the stop.

To resume the Agent, do these steps:

1. Select **Resume Agent**.
2. In the **Resume Agent** dialog, select **Resume**.

The page shows "Waiting for the current workload to become ready." Wait until
the phase is **Ready** before you open **Agent UI**. If the page shows "The
workload has not recovered.", open the **Status** tab.

The platform can suspend an Agent only when its home directory is on
persistent storage. If the storage prevents suspension, the page shows the
reason under the buttons. You can also suspend an Agent that waits
for governance prerequisites before its first start.

## Upgrade your Agent

The agent chart installs the Agent software. When your administrator makes a
newer version available, **Upgrade to Latest** shows on the **Overview** tab.

1. Select **Upgrade to Latest**.
2. Wait until the phase is **Ready** again.

The Agent restarts with the new version. The files in your home directory are
kept. The platform updates only the configuration files that it manages.

## Delete your Agent

> **Warning:** Deletion removes the home directory, the memory, and the Agent
> Identity of the Agent. You cannot undo the deletion. Before you delete the
> Agent, copy the files that you need. Refer to
> [Keep a copy of your files](../data/#keep-a-copy-of-your-files).

1. Select **Delete Agent**.
2. In the **Delete Agent** dialog, select **Delete**.

The portal returns to **My Agents**. The Agent shows **Deleting** until the
platform removes it. You can create a new Agent with the same name. The new
Agent does not get the data, the memory, or the Agent Identity of the deleted
Agent.

## Administrator tasks

Some lifecycle tasks need permission to change Agent resources in the cluster.
You cannot do them in the portal. If you need one of these tasks, ask your
administrator. For the procedures, administrators refer to
[Administer Agents](../../operations/agent-lifecycle/).

### Pin and update a chart

Refer to
[Pin and update a chart](../../operations/agent-lifecycle/#pin-and-update-a-chart).

### Planned node migration

An administrator can move an Agent to another Kubernetes node and keep its
home directory. Refer to
[Planned node migration](../../operations/agent-lifecycle/#planned-node-migration).

Sources: [Agent overview]({{< repo "app/cetirun-portal/web/src/components/OverviewSection.tsx" >}}),
[restart control]({{< repo "app/cetirun-portal/web/src/components/RestartAgentButton.tsx" >}}),
and [suspension control]({{< repo "app/cetirun-portal/web/src/components/SuspendAgentButton.tsx" >}}).
