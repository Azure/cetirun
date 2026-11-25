// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Text, tokens } from "@fluentui/react-components";
import type { AIGatewayBudget } from "../types/instance";
import { budgetSentence, formatUTC, keyStateText, timeAgo } from "../utils/budget";
import { StatusLabel } from "./StatusLabel";

/**
 * The current period of an Agent key's budget, as the operator last read it
 * from the AI Gateway: the spend against the budget, the reset, when the key
 * was checked, and why the AI Gateway refuses the key, if it does. The Portal
 * shows the AI Gateway's numbers and computes no balance.
 */
export function BudgetSummary({ budget, copilot }: { budget: AIGatewayBudget; copilot?: boolean }) {
  const state = keyStateText(budget.state);
  const times = [
    budget.resetAt ? `Resets ${formatUTC(budget.resetAt)}.` : "",
    budget.checkedAt ? `Last checked in the AI Gateway ${timeAgo(budget.checkedAt)}.` : "",
  ].filter(Boolean).join(" ");
  return (
    <div role="group" aria-label="AI Gateway budget" style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {state && (
        <div style={{ display: "flex", flexWrap: "wrap", alignItems: "baseline", gap: 8 }}>
          <StatusLabel tone={state.tone}>{state.label}</StatusLabel>
          <Text size={200}>{state.description}</Text>
        </div>
      )}
      <Text>{budgetSentence(budget)}</Text>
      {times && <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>{times}</Text>}
      <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>
        The AI Gateway's count, not an invoice.
        {copilot ? " Calls that a GitHub Copilot subscription answers count at the list price of the model." : ""}
      </Text>
    </div>
  );
}

/** The Usage section of the Agent page. Renders nothing before the operator has read the key. */
export function UsageBlock({ budget, copilot }: { budget?: AIGatewayBudget; copilot?: boolean }) {
  if (!budget) return null;
  return (
    <div className="portal-section">
      <div className="portal-inline" style={{ alignItems: "baseline" }}>
        <h2 className="portal-section-title">Usage</h2>
        <span className="portal-note">budget of the Agent's AI Gateway key</span>
      </div>
      <BudgetSummary budget={budget} copilot={copilot} />
    </div>
  );
}
