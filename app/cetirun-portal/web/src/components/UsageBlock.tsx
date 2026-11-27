// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState } from "react";
import { Text, tokens } from "@fluentui/react-components";
import type { AIGatewayBudget } from "../types/instance";
import { budgetSentence, formatUTC, keyStateText, timeAgo } from "../utils/budget";
import { StatusLabel } from "./StatusLabel";

/** How often the age of the last check is computed again. */
const CHECK_AGE_REFRESH_MS = 30_000;

/**
 * The current time, rendered again every CHECK_AGE_REFRESH_MS while the
 * section shows the age of a check. A poll of the page that returns the same
 * numbers does not render the section again, so without this timer the age
 * would stay at its first value, also after the operator stops.
 */
function useCheckAgeNow(active: boolean): number {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!active) return undefined;
    const timer = window.setInterval(() => setTick((tick) => tick + 1), CHECK_AGE_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [active]);
  return Date.now();
}

/**
 * The current period of an Agent key's budget, as the operator last read it
 * from the AI Gateway: the spend against the budget, the reset, when the key
 * was checked, and why the AI Gateway refuses the key, if it does. The Portal
 * shows the AI Gateway's numbers and computes no balance.
 */
export function BudgetSummary({ budget, copilot }: { budget: AIGatewayBudget; copilot?: boolean }) {
  const now = useCheckAgeNow(Boolean(budget.checkedAt));
  const state = keyStateText(budget.state);
  const times = [
    budget.resetAt ? `Resets ${formatUTC(budget.resetAt)}.` : "",
    budget.checkedAt ? `Last checked in the AI Gateway ${timeAgo(budget.checkedAt, now)} (${formatUTC(budget.checkedAt)}).` : "",
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
