// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { StatusTone } from "../components/StatusLabel";
import type { AIGatewayBudget, AIGatewayKeyState } from "../types/instance";

const usd = new Intl.NumberFormat("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const cents = new Intl.NumberFormat("en-US", { maximumSignificantDigits: 2 });

/**
 * A USD amount of the AI Gateway, rounded to cents, or to two significant
 * digits under a cent. A value that is not a number is shown as the AI
 * Gateway reported it.
 */
export function formatUSD(value: string): string {
  const amount = Number(value);
  if (value.trim() === "" || !Number.isFinite(amount)) return value;
  if (amount > 0 && amount < 0.01) return cents.format(amount);
  return usd.format(amount);
}

/**
 * The spend against the budget, such as "12.40 of 50.00", or the spend alone
 * for a key without a budget. A spend that the AI Gateway did not report is
 * none.
 */
export function budgetAmounts(budget: AIGatewayBudget): string {
  const spent = formatUSD(budget.spendUSD || "0");
  return budget.budgetUSD ? `${spent} of ${formatUSD(budget.budgetUSD)}` : spent;
}

/** The spend of the current period against the budget, as a sentence. */
export function budgetSentence(budget: AIGatewayBudget): string {
  const period = budget.duration === "1mo" ? " this month" : budget.duration ? " this period" : "";
  const sentence = `Spent ${budgetAmounts(budget)} USD${period}.`;
  return budget.budgetUSD ? sentence : `${sentence} The key has no budget.`;
}

/** An RFC 3339 time as "2026-12-01 00:00 UTC": budgets reset in UTC. */
export function formatUTC(value: string): string {
  const time = new Date(value);
  if (Number.isNaN(time.getTime())) return value;
  return `${time.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** How long ago an RFC 3339 time was, in words. */
export function timeAgo(value: string, now: number = Date.now()): string {
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return value;
  const minutes = Math.floor(Math.max(0, now - then) / 60_000);
  if (minutes < 1) return "less than a minute ago";
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.floor(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

export interface KeyStateText {
  label: string;
  tone: StatusTone;
  description: string;
}

/** Why the AI Gateway refuses a key, as the Portal names it. */
const KEY_STATES: Record<Exclude<AIGatewayKeyState, "active">, KeyStateText> = {
  budgetReached: {
    label: "Budget reached",
    tone: "danger",
    description: "The AI Gateway refuses the Agent's model calls until the budget resets.",
  },
  suspended: {
    label: "Suspended",
    tone: "informative",
    description: "The AI Gateway refuses the Agent's model calls while the Agent is suspended.",
  },
  noModelAccess: {
    label: "No model access",
    tone: "warning",
    description: "The Agent Policy grants the Agent no model now, so the AI Gateway refuses its model calls.",
  },
  blocked: {
    label: "Blocked by an administrator",
    tone: "danger",
    description: "An administrator blocked the Agent's key in the AI Gateway, which refuses its model calls.",
  },
};

/** The label of a refused key; undefined for an active key or a state this Portal does not know. */
export function keyStateText(state?: string): KeyStateText | undefined {
  return state && Object.prototype.hasOwnProperty.call(KEY_STATES, state) ? KEY_STATES[state as keyof typeof KEY_STATES] : undefined;
}
