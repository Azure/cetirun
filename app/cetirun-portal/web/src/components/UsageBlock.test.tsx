// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { UsageBlock } from "./UsageBlock";
import type { AIGatewayBudget } from "../types/instance";

const minutesAgo = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

const budget: AIGatewayBudget = {
  budgetUSD: "50",
  spendUSD: "12.4",
  duration: "1mo",
  resetAt: "2026-12-01T00:00:00Z",
  checkedAt: minutesAgo(8),
  state: "active",
};

describe("UsageBlock", () => {
  it("shows the AI Gateway's spend, budget, reset and last check", () => {
    render(<UsageBlock budget={budget} />);
    expect(screen.getByRole("heading", { name: "Usage" })).toBeInTheDocument();
    const group = screen.getByRole("group", { name: "AI Gateway budget" });
    expect(within(group).getByText("Spent 12.40 of 50.00 USD this month.")).toBeInTheDocument();
    expect(within(group).getByText("Resets 2026-12-01 00:00 UTC. Last checked in the AI Gateway 8 minutes ago.")).toBeInTheDocument();
    expect(within(group).queryByText(/remaining|left/i)).not.toBeInTheDocument();
    expect(within(group).queryByText(/GitHub Copilot/)).not.toBeInTheDocument();
    expect(document.querySelector(".status-label")).toBeNull();
  });

  it("says that GitHub Copilot calls count at the list price", () => {
    render(<UsageBlock budget={budget} copilot />);
    expect(screen.getByText(/GitHub Copilot subscription answers count at the list price/)).toBeInTheDocument();
  });

  it.each([
    ["budgetReached", "Budget reached", /until the budget resets/],
    ["suspended", "Suspended", /while the Agent is suspended/],
    ["noModelAccess", "No model access", /grants the Agent no model/],
    ["blocked", "Blocked by an administrator", /An administrator blocked the Agent's key/],
  ] as const)("labels a %s key", (state, label, description) => {
    render(<UsageBlock budget={{ ...budget, state }} />);
    expect(screen.getByText(label)).toHaveClass("status-label");
    expect(screen.getByText(description)).toBeInTheDocument();
  });

  it("renders nothing before the operator has read the key", () => {
    const { container } = render(<UsageBlock />);
    expect(container).toBeEmptyDOMElement();
  });
});
