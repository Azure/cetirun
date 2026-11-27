// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { act, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UsageBlock } from "./UsageBlock";
import type { AIGatewayBudget } from "../types/instance";

const budget: AIGatewayBudget = {
  budgetUSD: "50",
  spendUSD: "12.4",
  duration: "1mo",
  resetAt: "2026-12-01T00:00:00Z",
  checkedAt: "2026-11-25T09:52:00Z",
  state: "active",
};

describe("UsageBlock", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-25T10:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("shows the AI Gateway's spend, budget, reset and last check", () => {
    render(<UsageBlock budget={budget} />);
    expect(screen.getByRole("heading", { name: "Usage" })).toBeInTheDocument();
    const group = screen.getByRole("group", { name: "AI Gateway budget" });
    expect(within(group).getByText("Spent 12.40 of 50.00 USD this month.")).toBeInTheDocument();
    expect(within(group).getByText("Resets 2026-12-01 00:00 UTC. Last checked in the AI Gateway 8 minutes ago (2026-11-25 09:52 UTC).")).toBeInTheDocument();
    expect(within(group).queryByText(/remaining|left/i)).not.toBeInTheDocument();
    expect(within(group).queryByText(/GitHub Copilot/)).not.toBeInTheDocument();
    expect(document.querySelector(".status-label")).toBeNull();
  });

  it("keeps the age of the last check current while the polls return the same numbers", async () => {
    const answer = { ...budget, checkedAt: "2026-11-25T09:59:30Z" };
    const read = vi.fn(async () => ({ ...answer }));
    function Page() {
      const { data } = useQuery({ queryKey: ["budget"], queryFn: read, refetchInterval: 10_000 });
      return <UsageBlock budget={data} />;
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><Page /></QueryClientProvider>);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText(/Last checked in the AI Gateway less than a minute ago \(2026-11-25 09:59 UTC\)/)).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5 * 60_000);
    });
    expect(read.mock.calls.length).toBeGreaterThan(10);
    expect(screen.getByText(/Last checked in the AI Gateway 5 minutes ago \(2026-11-25 09:59 UTC\)/)).toBeInTheDocument();
    client.clear();
  });

  it("says that GitHub Copilot calls count at the list price", () => {
    render(<UsageBlock budget={budget} copilot />);
    expect(screen.getByText(/GitHub Copilot subscription answers count at the list price/)).toBeInTheDocument();
  });

  it.each([
    ["budgetReached", "Budget reached", /until the budget resets/],
    ["suspended", "Suspended", /while the Agent is suspended/],
    ["noModelAccess", "No model access", /grants the Agent no model/],
    ["blockPending", "Block pending", /Until the AI Gateway confirms the block, it can still accept the key's calls/],
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
