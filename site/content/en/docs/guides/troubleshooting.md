---
title: "Troubleshoot your Agent"
description: "Find the cause of common portal and Agent problems, know the default limits, and know what to send to your administrator."
weight: 80
---

Each section starts with what you see in the portal. Then it gives the cause
and the action. Some problems need an administrator. For the information to
send, refer to [Ask your administrator for help](#ask-your-administrator-for-help).

## You cannot sign in to the portal

| What you see | Cause | What to do |
| --- | --- | --- |
| The sign-in page rejects your account. | Your account is not in the group that the portal allows. | Ask your administrator for access. |
| "Invalid user identity: must be a lowercase DNS-1123 label ..." | The portal uses the part of your account name before `@` as your alias. This part must start with a letter, contain only letters, digits, or hyphens, and end with a letter or a digit. It can have a maximum of 63 characters. For example, `first.last` is not a valid alias. | Ask your administrator. You cannot change the alias in the portal. |
| "Session expired, please refresh the page" | Your portal session ended. | Refresh the page, and sign in again. |

## You cannot create an Agent

| What you see | Cause | What to do |
| --- | --- | --- |
| **New Agent** is unavailable and shows "Limit of 1 agent reached". | You have the maximum number of Agents. | Delete an Agent that you do not use, or ask your administrator for a higher limit. |
| "Instance name too long: ... the limit is 35" | The alias, the hyphen, and the name that you entered have more than 35 characters. | Enter a shorter name. |
| "Instance name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens" | The name has a character that is not allowed. | Use lowercase letters, digits, and hyphens. End the name with a letter or a digit. |
| "Instance name '...' is reserved" | The portal uses this name for its own pages, for example `admin`, `api`, or `ui`. This can occur when you leave **Agent Name (optional)** empty and your alias is such a name. | Enter a name in **Agent Name (optional)**. |
| "No models are available for new agents under the selected Team's Policy and deployment settings." | No model source is available for the selected Team. | Select another Team, or ask your administrator to examine model access. |
| "The AI Gateway has not published its models yet. Try again in a minute." | The platform did not finish its start. | Wait one minute, and try again. |
| "The operator has not read its agent chart yet. Try again in a minute." | The platform did not finish its start. | Wait one minute, and try again. |

## The Agent does not become ready

First, read the phase badge. For the meaning of each phase, refer to
[Wait until the Agent is ready](../../getting-started/first-instance/#wait-until-the-agent-is-ready).

If the platform holds the Agent, a message box shows above the tabs. The list
in **My Agents** shows the same status under the Agent. The message box gives
the reason and a next step.

| Status | Meaning | What to do |
| --- | --- | --- |
| **Waiting for governance** | The platform examines the prerequisites of the Team's Agent Policy. | Wait. If the status does not change, ask your administrator to examine the conditions. |
| **Blocked by policy** | A setting of the Agent is not allowed by the Team's Agent Policy, for example the model or the image. | Send the reason to your administrator. |
| **Identity not ready** | The Agent Identity of the Agent is not ready. | Ask your administrator to examine the Agent Identity setup. |
| **Identity owner binding required** | The platform cannot confirm that you own the Agent Identity. | Ask your administrator to examine the owner binding. |
| **Binding or identity approval required** | The Team binding or an identity approval is missing. | Ask your administrator to examine the approvals. |
| **Platform support required** | The cluster does not support a Sandbox control that the policy requests. | Ask your administrator. |
| **Governance dependency unavailable** | A policy, Sandbox, or platform dependency is not available. | Ask your administrator. |

Then open the **Status** tab. In the **Conditions** table, find the rows with
the status `False`. The **Reason** and **Message** columns tell you which step
fails. For example, `CustomBindingsPending` tells you that a Custom Agent
waits until an administrator approves its image.

If the phase is **Failed**, or if the Agent stays in one phase for more than
10 minutes, send the rows to your administrator.

## Agent UI, Terminal, or Desktop is unavailable

These entries are unavailable until the phase is **Ready**. If the Agent is
**Ready** and an entry stays unavailable, the route of the Agent is not ready
yet. Wait some minutes. Then open the **Status** tab and find the
`IngressReady` row.

For a Custom Agent, **Terminal** and **Desktop** stay unavailable when the
deployment does not configure them for its image. A Custom Agent shows
**Web UI** only when it serves a Web UI.

## The Agent UI rejects your sign-in

| What you see | What to do |
| --- | --- |
| The Hermes Webapp rejects the username or the password. | Enter the username `cetirun`, not `vibe`. For the password, copy **Hermes API / Webapp Key (user: cetirun)** from the **Overview** tab. |
| The OpenClaw Control UI asks for the gateway token. | Copy **Gateway Token** from the **Overview** tab. If you changed the token in **Edit Agent**, use the new token. |
| The OpenClaw Control UI asks to pair the browser again. | Each new browser, and each browser with cleared site data, must be paired. Approve the request in **Terminal**. Refer to [Open an OpenClaw Agent](../../getting-started/first-instance/#open-an-openclaw-agent). |

## A model is missing

The **Models** section on the **Overview** tab tells you why a model is
missing. The section does not show while the Agent is suspended.

| What you see | What to do |
| --- | --- |
| "Sign in to GitHub Copilot to add your Copilot models." | Sign in to GitHub Copilot. Refer to [Sign in to GitHub Copilot](../../getting-started/first-instance/#sign-in-to-github-copilot). |
| "No shared models are available yet." | Ask your administrator to share a model. |
| "The agent's AI Gateway key is not ready (...). The operator retries automatically." | Wait. If the message stays, send the reason in the parentheses to your administrator. |
| "Not offered because the Gateway has no price for them: ..." | The platform cannot limit the cost of these Copilot models, so it does not offer them. Ask your administrator. |
| "Not offered because a shared model already has their name: ..." | A shared model has the same name. Use the shared model. |
| "Not offered because the Agent Policy does not allow these models: ..." | The Agent Policy of your Team does not allow these models. Ask your administrator. |
| "Smart-routing model ... is not available to this agent: ..." | Read the reason. For example, the Agent Policy does not allow a model that the router uses. |

## GitHub Copilot does not become ready

| What you see | What to do |
| --- | --- |
| The **GitHub Copilot** badge stays at **Starting**. | Wait some minutes. Do not sign in again. If the badge does not change, send the Agent name to your administrator. |
| **Degraded** with "Signing in again may help." | Select **Sign in again**. |
| "GitHub Copilot models are unavailable: the Agent Policy no longer allows one of them ..." | Wait. The models are available again when the platform removes the model that is not allowed. Shared models continue to work. |
| "GitHub Copilot models are unavailable until the operator can check them against the Agent Policy." | Wait. Shared models continue to work. |
| **Sign-in required** after you resume the Agent. | Suspension removes the sign-in. Select **Sign in with GitHub**. |
| Copilot models stop working, but the badge shows **Ready**. | GitHub keeps a maximum of ten Copilot tokens for each GitHub account. A sign-in on another Agent or with another tool can revoke the oldest token. Select **Sign in again**. |

## The Agent reports a billing or credits error

The Agent used its monthly model budget, and the AI Gateway refuses its model
requests. OpenClaw then reports a billing error of its model provider, and
Hermes tells you to add credits. The portal does not show the budget or the
spend. Ask your administrator to raise the budget, or wait until it resets on
the first day of the month (UTC). For the details, refer to
[When the budget runs out](../models/#when-the-budget-runs-out).

If the Agent Policy of your Team sets request limits, the **Models** section
shows them. A request over a limit fails with HTTP 429. The Agent can send the
request again later.

## A connected service does not work

| What you see | What to do |
| --- | --- |
| The Agent page has no **Identities** section. | Your deployment does not allow connected services. Ask your administrator. |
| "Allow popups for this Portal to sign in." | Allow pop-up windows for the portal in your browser. Then try again. |
| The Agent asks you to connect an account. | On the **Overview** tab, select **Identities** → **Add identity**. Refer to [Agent Connections and Identity](../agent-identity/). |
| The connection shows **Sign-in required**. | Select **Sign in again**. |

## Default limits

These limits are the defaults of the platform charts. Your deployment can use
other values. Administrators find the settings in
[Helm configuration](../../reference/helm/).

| Limit | Default | Where you see it |
| --- | --- | --- |
| Agents for each user | 1 | **New Agent** is unavailable. |
| Length of the Agent name | 35 characters, including your alias | The form rejects the name. |
| Home directory storage | 100 GiB | The disk of the Agent is full. |
| CPU and memory | 4 CPUs and 8 GiB are reserved. The maximum is 8 CPUs and 16 GiB. | The Agent becomes slow, or a process stops. |
| Monthly model budget | 10,000 US dollars for each Agent. An Agent of a policy-bound Team uses the budget of its Agent Policy. | Not in the portal. Refer to [The Agent reports a billing or credits error](#the-agent-reports-a-billing-or-credits-error). |
| Model request limits | The Agent Policy of your Team sets them. | The **Models** section shows them. |
| Network access | Private network addresses and the cloud metadata service are blocked. | The Agent cannot connect to internal services. |
| Duration of a Hermes tool | 420 seconds for one foreground tool | Hermes stops a tool that runs longer. For longer tools, refer to [Bounded long-running tools]({{< repo "app/cetirun-chart/agents/hermes/README.md" >}}#bounded-long-running-tools). |
| Size of a connected-service request | 256 KiB for a request and for a response | The Agent tells you that the request failed. |
| Portal Chat | Refer to [Limits and failures](../custom-chat/#limits-and-failures). | The Chat page tells you. |

## Ask your administrator for help

Send these items to your administrator:

- The Agent name.
- The phase, and the governance status if the portal shows one.
- The rows of the **Status** tab with the status `False`.
- The time when the problem started, and what you did before it.

Do not send keys, tokens, passwords, or the text of private conversations.

Sources: [governance status]({{< repo "app/cetirun-portal/web/src/components/GovernanceNotice.tsx" >}}),
[model section]({{< repo "app/cetirun-portal/web/src/components/AIGatewayBlock.tsx" >}}),
[creation form]({{< repo "app/cetirun-portal/web/src/components/CreateForm.tsx" >}}),
and [runtime chart values]({{< repo "app/cetirun-chart/values.yaml" >}}).
