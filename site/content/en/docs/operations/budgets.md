---
title: "Budgets and spend"
description: "Set the monthly AI Gateway budgets and request limits of Agents, and read what each Agent, user, and Team spends."
weight: 22
---

Each Agent has its own [virtual key](../../concepts/glossary/#virtual-key) in
the [AI Gateway](../ai-gateway/). The key has a monthly budget in US dollars.
There is no token allowance. The AI Gateway counts the spend of every model
call against the key, and it refuses calls when the spend reaches the budget.
This page is for platform administrators.

## Where the budget of an Agent comes from

| Agent | Budget of the key | Who can change it |
| --- | --- | --- |
| A standard Agent: an Agent without a Team, or with a Team without an Agent Policy | The operator's `aiGateway.defaultBudget` when the key is created. The default is `10000` USD. The key has no request limits | A platform administrator, in the AI Gateway console. The operator keeps the change |
| The [CetiAdmin](../cetiadmin/) Agent | `cetiadmin.model.initialMonthlyBudgetUSD` when the key is created. `make deploy` requires this value. `aiGateway.defaultBudget` does not apply | A platform administrator, in the AI Gateway console. The operator keeps the change |
| An Agent of a policy-bound Team | `modelAccess.gatewayBudget.defaultMonthlyUSD` of the Agent Policy, or the `spec.modelAccess.monthlyBudgetUSD` of the Agent, up to `maxMonthlyUSD` of the Policy | A Portal administrator, in the Policy. An admission administrator, with `kubectl`. The operator sets the key budget again after a change in the console |

`aiGateway.defaultBudget` applies only when the operator creates a key. A
change to the value does not change the keys that exist. With `make deploy`,
set the value under `platform.values.cetirun-operator.aiGateway.defaultBudget`.
Set it to the amount that one standard Agent can spend in a month.

An Agent Policy that allows models must have a monthly AI Gateway budget. In the
Policy editor, select **Set monthly Gateway budget**. Then set the default and
the maximum.

The budget resets on the first day of each month, UTC. A job of the AI Gateway
applies the resets about every ten minutes. As a result, a key can stay
refused for up to ten minutes after the reset time.

## What the budget counts

- **Shared models.** Each call is charged at the price of the model. Refer to
  [Set the price of a model](../ai-gateway/#set-the-price-of-a-model).
- **GitHub Copilot models.** Each call is charged at the list price of the
  model, from the same budget. This applies also when the subscription of the
  owner pays for the call.
- **Routers.** Each call is charged at the price of the tier model that it
  goes to. The calls of an `llm` or `jev` classifier are also charged. The own
  price of a router in the AI Gateway only passes the check for unpriced models.
- **Internal tasks.** The internal tasks of OpenClaw count like other calls.
  Refer to [How Agents receive their models](../models/#how-agents-receive-their-models).

## When the budget runs out

When the spend reaches the budget, the AI Gateway refuses each request of the key
with HTTP 402. The requests to the GitHub Copilot models of the Agent stop
too. The message of the AI Gateway starts with `CetiRun AI Gateway:`. It says
that the budget of the Agent is spent, when the budget resets, and that a
CetiRun administrator can raise it. When the budget of the AI Gateway team of
the Agent is spent, the message names the Team instead. The Agents show this
in different ways:

- Hermes shows the message. Then it adds its own advice to add credits with
  the provider, which does not apply to the AI Gateway.
- OpenClaw shows a shorter message: the Agent is out of budget on the AI
  Gateway, and a CetiRun administrator can raise the budget. The message omits
  the date of the reset. OpenClaw keeps using the key after the refusal, so its
  next request goes to the AI Gateway again.

To restore an Agent before the reset, do one of these steps:

- **For a standard Agent**, raise the budget of its key in the
  AI Gateway console.
- **For an Agent of a policy-bound Team**, raise the budget of the Agent Policy.
  This change restarts the running Agents of the Policy one time.
- **For one Agent of a policy-bound Team**, suspend the Agent. Then set its
  `spec.modelAccess.monthlyBudgetUSD`, up to the maximum of the Policy. Only an
  admission administrator can make this change, and only while the Agent is
  suspended. Refer to
  [AgentPolicy model access](../../reference/crds/#agentpolicy-model-access).

To select another HTTP status for an exhausted budget, set
`budgetExceededStatusCode` of the AI Gateway chart. The chart accepts `402`, `422`,
and `429`. Refer to the chart README's
[When a budget runs out]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#when-a-budget-runs-out)
for the behavior of the Agents with each status.

## Team totals

The operator creates one AI Gateway team for each CetiRun Team, with the ID
`cetirun-team-<team>`. The keys of the Agents of the Team belong to this
AI Gateway team. The keys also name the owner of each Agent as their AI Gateway
user. The operator creates the AI Gateway team without a budget, and it does not
change the team after that.

To limit the total monthly spend of a Team, set a budget on its AI Gateway team
in the console. Do not change the model access of the team in the console. The
operator gives model access to each key.

## Request limits

An Agent Policy can also limit the AI Gateway traffic of each Agent
(`modelAccess.rateLimits`):

- Parallel requests
- Requests per minute (RPM)
- Tokens per minute (TPM)
- RPM or TPM for individual models

Each Agent of the Policy gets the same limits on its own key. As a result, the
traffic of one Agent does not count against another Agent. To set no limit,
leave a field empty. Standard Agents have no request limits.

The operator applies a change of only the limits to the keys that exist. It
does not stop or restart running Agents. A request over a limit gets HTTP 429
with a `retry-after` header. A TPM limit counts the tokens of finished
requests. Refer to
[AgentPolicy rate limits](../../reference/crds/#agentpolicy-rate-limits).

A per-model limit applies to the model name that the AI Gateway counts:

- For a router, this is the internal name of the router. A limit that a Policy
  sets for the name of a router also applies under the internal name.
- A per-model limit on a tier model does not apply to requests that go
  through a router.

## Read the spend

The Portal does not show spend or remaining budgets. The AI Gateway console
shows the spend and the budget of each key, user, and team. Refer to
[Open the console](../ai-gateway/#open-the-console). The key alias of an Agent
is `cetirun-<agent>-<agent UID>`. The request log and the metrics of the
AI Gateway also record the spend. Refer to
[Logs and metrics](../ai-gateway/#logs-and-metrics).

With the [ADX-Mon collection](../observability/), the function
`GatewayRequests()` of the logs database gives one row for each call. For
example, this query shows the calls and the spend of each Agent, owner, Team,
and model since midnight UTC:

```kusto
GatewayRequests()
| where Timestamp > startofday(now()) and CallOrigin != "health_check"
| summarize Calls = count(), Spend = sum(Spend) by Agent, Owner, Team, Model
```

The metric `litellm_remaining_api_key_budget_metric` shows the remaining budget
of each key. Counters such as `litellm_spend_metric_total` restart from zero
when an AI Gateway Pod restarts. Use the metrics for trends and alerts. For
exact spend, use the request log or the daily totals of the AI Gateway. Refer
to the chart README's
[Request log and metrics]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#request-log-and-metrics).

The budget totals of the keys do not depend on the retention of the spend log.

Sources: [key budgets and teams]({{< repo "app/cetirun-operator/internal/controller/ai_gateway.go" >}}),
[AI Gateway client]({{< repo "app/cetirun-operator/internal/aigateway/client.go" >}}),
[operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}),
[AI Gateway values]({{< repo "app/cetirun-ai-gateway/chart/values.yaml" >}}),
and [log query functions]({{< repo "infra/cluster/modules/adx/cetirun-logs.kql" >}}).
