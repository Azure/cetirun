// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { budgetAmounts, budgetSentence, formatUSD, formatUTC, keyStateText, timeAgo } from "./budget";

describe("AI Gateway budget text", () => {
  it.each([
    ["12.4", "12.40"],
    ["10000", "10,000.00"],
    ["0", "0.00"],
    ["0.004999", "less than 0.01"],
    ["0.005", "0.01"],
    ["n/a", "n/a"],
    ["", ""],
  ])("formats %j as %j", (value, text) => {
    expect(formatUSD(value)).toBe(text);
  });

  it("shows the spend against the budget, without computing a balance", () => {
    expect(budgetAmounts({ budgetUSD: "50", spendUSD: "12.4", state: "active" })).toBe("12.40 of 50.00");
    expect(budgetSentence({ budgetUSD: "50", spendUSD: "12.4", duration: "1mo", state: "active" })).toBe("Spent 12.40 of 50.00 USD this month.");
    expect(budgetSentence({ budgetUSD: "50", duration: "7d", state: "active" })).toBe("Spent 0.00 of 50.00 USD this period.");
    expect(budgetSentence({ spendUSD: "3", state: "active" })).toBe("Spent 3.00 USD. The key has no budget.");
  });

  it("gives times in UTC and as an age", () => {
    expect(formatUTC("2026-12-01T00:00:00Z")).toBe("2026-12-01 00:00 UTC");
    expect(formatUTC("2026-12-01T08:30:00+08:00")).toBe("2026-12-01 00:30 UTC");
    expect(formatUTC("soon")).toBe("soon");
    const now = Date.parse("2026-11-25T10:00:00Z");
    expect(timeAgo("2026-11-25T09:59:30Z", now)).toBe("less than a minute ago");
    expect(timeAgo("2026-11-25T09:59:00Z", now)).toBe("1 minute ago");
    expect(timeAgo("2026-11-25T09:52:00Z", now)).toBe("8 minutes ago");
    expect(timeAgo("2026-11-25T07:00:00Z", now)).toBe("3 hours ago");
    expect(timeAgo("2026-11-23T10:00:00Z", now)).toBe("2 days ago");
    // A clock ahead of the browser's is not a time in the future.
    expect(timeAgo("2026-11-25T10:05:00Z", now)).toBe("less than a minute ago");
  });

  it("names why the AI Gateway refuses a key, and nothing for an active key", () => {
    expect(keyStateText("budgetReached")?.label).toBe("Budget reached");
    expect(keyStateText("suspended")?.label).toBe("Suspended");
    expect(keyStateText("noModelAccess")?.label).toBe("No model access");
    expect(keyStateText("blocked")?.label).toBe("Blocked by an administrator");
    expect(keyStateText("active")).toBeUndefined();
    expect(keyStateText("toString")).toBeUndefined();
    expect(keyStateText(undefined)).toBeUndefined();
  });
});
