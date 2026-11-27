---
title: "Choose a model"
description: "Select shared models or GitHub Copilot through the AI Gateway."
weight: 10
---

Your Agent gets its models from the platform's AI Gateway. The models come
from two sources: the models that your platform administrators share, and the
models of your own GitHub Copilot subscription. The platform does not configure
model provider keys of your own for the Agent.

Platform administrators configure the AI Gateway and decide which models each
Agent can use. Refer to [AI Gateway](../../operations/ai-gateway/) and
[Model access](../../operations/models/).

## Select and switch models

The **Models** section on the **Overview** tab of your Agent lists the models
that the Agent can use. The default model shows `(default)` after its name.
New conversations start with the default model.

- **Change the model of one conversation.** In **Agent UI**, select another
  model of the list. The Hermes and OpenClaw configurations contain all the
  models of the list. This change does not restart the Agent.
- **Change the default model.** In the **Models** section, select the model in
  **Default model**, and select **Save**. The Agent restarts. **Default model**
  shows only when the Agent has two or more models.
- **Use GitHub Copilot models.** Sign in to GitHub Copilot in the **Models**
  section. Your Copilot models show in the list after the platform prepares
  them. Refer to
  [Sign in to GitHub Copilot](../../getting-started/first-instance/#sign-in-to-github-copilot).
- **Select a model for a Custom Agent.** Select the model in Chat, or in the
  configuration of the image. The portal does not set a default model for a
  Custom Agent.

If you change the default model in the Hermes Webapp or the Hermes CLI, the
platform resets it at the next start. To keep another default model, use
**Default model** in the portal.

## Models through the AI Gateway

The platform AI Gateway is part of every platform installation. OpenClaw,
Hermes, and approved Custom Agents take their platform-supplied models from
the AI Gateway. These models come from two sources:

- **Shared models** are the models that a platform administrator configures
  in the AI Gateway chart's `models`. Shared models keep the names from the
  platform administrator. All shared models are offered to standard Agents.
  Sandbox Agents are offered the shared models that their Agent Policy grants.
- **GitHub Copilot** models come from the owner's own Copilot subscription.
  They go through a small proxy for each Agent, and only the AI Gateway can
  reach this proxy.

An Agent sees one name for each model. If a Copilot model is the same model as
a shared model that the Agent can use, it takes the name of that shared model.
The Agent lists it one time.

The create form offers both sources, and both are on by default. Every new
Agent must have a model to use: shared models when at least one is offered, or
GitHub Copilot when the deployment offers it. GitHub Copilot stays on when
shared models are off or when no shared model is offered. Shared models stay on
without GitHub Copilot. With neither source, the portal creates no Agent. The
form then says "No models are available for new agents under the selected
Team's Policy and deployment settings. Ask an administrator to check model
access."

The Portal reads the sources from the catalog that the operator publishes. As a
result, it also waits until the catalog exists. The catalog lists the agent
types that the selected agent chart routes through the AI Gateway. The Portal
creates an Agent only after it reads a compatible chart. A Team stays optional.
When a Team is selected, its Agent Policy further restricts the model sources.

To add Copilot models, sign in to GitHub Copilot in the **Models** section of
the Agent. Refer to
[Sign in to GitHub Copilot](../../getting-started/first-instance/#sign-in-to-github-copilot).
The portal stores the token for the Agent's proxy and records the account's
chat models.

GitHub keeps at most ten of these tokens for each GitHub account. Each sign-in
makes a new token. When an account has ten, the next sign-in revokes the
oldest. Sign-ins with other tools that use the same GitHub app can also count.

As a result, many sign-ins with one account can sign an older Agent out of
Copilot. That Agent's Copilot models stop within a day, but the Agent still
shows Copilot as signed in. To restore it, sign in on that Agent again.

A model that the AI Gateway has no price for is listed as not offered, because
the Agent's budget cannot limit that model.

The token and the models carry the same sign-in generation. The operator uses
models only with the token that they came with, after the Portal completes that
sign-in. If a sign-in stops or is cancelled after it stores its token, the
Agent offers no Copilot models. The Agent reports `SignInIncomplete` until you
sign in again.

Each new sign-in also restarts the proxy. Without this restart, the proxy keeps
the Copilot token that it exchanged until that token expires. As a result,
another GitHub account takes effect only when that token expires.

## Runtime configuration

Each Agent has its own virtual key with a monthly budget. The Agent page lists
its models. For OpenClaw and Hermes, the page also sets the default. When you
save a new default, the Agent restarts. Custom images select a model from the
models that are available through the AI Gateway. They make this selection
through their own configuration or Chat UI.

A change to the shared models can restart an Agent, or stop it until a
platform administrator corrects the change. Refer to
[When model changes reach Agents](../../operations/ai-gateway/#when-model-changes-reach-agents).
When an Agent restarts, it shows as provisioning for a short time. Its UI
is unavailable until it is ready again.

Source: [portal sign-in]({{< repo "app/cetirun-portal/internal/handler/copilot_login.go" >}}).

### Images

OpenClaw and Hermes send images with a request to the models that accept them.

For the other models:

- Hermes first sends the image to the model in a separate call, and the model
  describes it.
- OpenClaw sends only text to these models. Another model that accepts images
  describes the image. The `computer` tool is not available with these models.

### Reasoning

OpenClaw offers its reasoning levels for the models that reason. It sends the
level that the owner selects as the reasoning effort of each request. The
default level is `medium`. The operator reads from the AI Gateway which models
reason. For the rules, platform administrators refer to
[Reasoning](../../operations/ai-gateway/#reasoning).

For the other models, OpenClaw offers only the level `off`, and it sends no
reasoning effort. A model that reasons then uses its default effort. For
example, this occurs for a router when one of its tier models does not reason.

Hermes sends its own reasoning effort to each model. The AI Gateway removes the
effort from a request when no deployment that can answer the request reasons.

### Spend and budget

The **Usage** section on the **Overview** tab shows the spend of the Agent's
key in the current month, its budget, and when the budget resets. The spend is
the count of the AI Gateway. The invoice of the model provider can differ from
it. The section does not calculate a remaining amount.

The numbers come from the AI Gateway. The operator reads them about every ten
minutes. The section shows the time of the last check in UTC and how long ago
it was. If the last check is much older than ten minutes, the numbers can be
out of date.

When the AI Gateway refuses the calls of the key, the section shows why:

| Label | Cause |
| --- | --- |
| **Budget reached** | The spend reached the budget. The AI Gateway refuses the calls until the budget resets. |
| **Suspended** | The Agent is suspended. |
| **No model access** | The Agent Policy of the Team grants the Agent no model. |
| **Blocked by an administrator** | An administrator blocked the key in the AI Gateway console. |

The section shows only the budget of the key. A platform administrator can
also set a budget on the AI Gateway team of a Team in the console. When that
budget runs out, the AI Gateway refuses the calls, and the section shows no
label.

### When the budget runs out

Each Agent's key has a monthly budget in US dollars. For an Agent of a
policy-bound Team, the Agent Policy sets the budget. For other Agents, the key
gets the operator's `aiGateway.defaultBudget` when it is created. A platform
administrator can change this budget in the AI Gateway console.

When the spend of the key reaches the budget, the AI Gateway refuses every
request of the key with HTTP 402. Its message says that the Agent's budget
is spent, when the budget resets, and that a CetiRun administrator can raise
it. Hermes shows this message, and then its own advice to add credits with the
provider, which does not apply to the AI Gateway. OpenClaw shows a shorter
message: the Agent is out of budget on the CetiRun AI Gateway, and a CetiRun
administrator can raise the budget. The requests to the Agent's GitHub
Copilot models count against the same budget, so they stop too.

The budget resets on the first day of each month, UTC, up to about ten minutes
late. To restore a standard Agent before that, a platform administrator raises
the budget of its key. For a Sandbox Agent, a Portal administrator raises the
budget of the Agent Policy, which restarts its running Agents one time. As an
alternative, an admission administrator suspends the Agent and sets its
`modelAccess.monthlyBudgetUSD`, up to the maximum of the Agent Policy. Refer to
[AgentPolicy model access](../../reference/crds/#agentpolicy-model-access).
For budgets and spend, platform administrators refer to
[Budgets and spend](../../operations/budgets/).

### Smart-routing models

A Team can also define its own smart-routing models in the Portal. See
[Configure a team](../teams/#smart-routing). An Agent that can use one of
these models lists it under its name, such as `auto-team`. The create form
offers the names that the Team's Agent Policy allows as the default model, with
`(smart routing)` after each name.

If you select the router's name as the default model, you select the router.
The Agent is configured with the smallest context window and output limit
among the router's tier models that the AI Gateway reports. As a result, a request
sized for the router fits every tier. When the owner's GitHub Copilot model is
merged into a tier's shared model, the Agent's requests for that tier can
also reach its Copilot route. As a result, the router's limits include the
limits of the route, as the Agent's entry for that shared model does.

Each request is charged to the Agent's key at the price of the tier model
that it is sent to. The calls of an `llm` or `jev` classifier are also charged.
The router's own price in the AI Gateway only satisfies the check for unpriced
models. The key's budget applies as usual.

An Agent that cannot use one of its Team's routers shows why on its page.
For example:

- The Agent Policy does not list the router.
- A tier uses a model that the Agent Policy does not allow.
- A GitHub Copilot tier waits for its owner to sign in.

Before the operator registers a router, the Agent shows it as not
registered yet.

## GitHub Copilot in the portal

Every agent type uses the Portal sign-in described above. The Portal stores
the owner's Copilot credential through the AI Gateway management flow. It does
not call an in-agent authentication helper. After the owner completes sign-in
and the operator publishes access, select a model from the AI Gateway catalog.
If the AI Gateway or catalog is unavailable, resolve that condition before you
create an Agent or select a new model.

The Portal offers only the models in the AI Gateway catalog that the Agent
Policy of the selected Team allows.

## When discovery fails

Examine the Agent's AI Gateway conditions and the Agent Policy of the selected
Team, if any. If authentication expired, complete Portal sign-in again.
After a temporary AI Gateway failure, retry. Do not print or paste token files
to diagnose these errors. For the messages and their causes, platform
administrators refer to [Troubleshoot model requests](../../operations/ai-gateway/#troubleshoot-model-requests).
