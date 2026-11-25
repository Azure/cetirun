// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InstanceListPage } from "./InstanceListPage";
import * as adminInstances from "../../api/adminInstance";

vi.mock("../../api/adminInstance");

describe("admin InstanceListPage", () => {
  beforeEach(() => vi.resetAllMocks());

  it("shows platform support as the cause in the status cell", async () => {
    vi.mocked(adminInstances.listAllInstances).mockResolvedValue([
      { name: "research", alias: "alice", uid: "1", agent: { type: "openclaw" }, ready: false, phase: "Pending", createdAt: "2026-01-01T00:00:00Z",
        governance: { category: "platform", reason: "UnsupportedControl", message: "A requested Sandbox control is not qualified.", condition: "SandboxReady", observedGeneration: 2 } },
    ]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><InstanceListPage /></MemoryRouter></QueryClientProvider>);
    const cell = (await screen.findByText("Platform support required")).closest("td")!;
    expect(within(cell).getByText("Pending")).toBeInTheDocument();
    expect(within(cell).getByText("Platform support required")).toBeInTheDocument();
    expect(within(cell).getByText(/UnsupportedControl:/)).toBeInTheDocument();
  });

  it("filters locally and links the Agent name to its detail page", async () => {
    vi.mocked(adminInstances.listAllInstances).mockResolvedValue([
      { name: "research", alias: "alice", team: "science", uid: "1", agent: { type: "openclaw" }, ready: true, phase: "Ready", createdAt: "2026-01-01T00:00:00Z" },
      { name: "support", alias: "bob", team: "operations", uid: "2", agent: { type: "custom" }, ready: false, phase: "Failed", createdAt: "2026-01-01T00:00:00Z" },
    ]);
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><InstanceListPage /></MemoryRouter></QueryClientProvider>);
    const link = await screen.findByRole("link", { name: "research" });
    expect(link).toHaveAttribute("href", "/admin/instances/research");
    await user.type(screen.getByRole("textbox", { name: "Owner" }), "bob");
    expect(screen.queryByRole("link", { name: "research" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "support" })).toBeInTheDocument();
  });

  it("shows each key's spend against its budget and why the AI Gateway refuses it", async () => {
    vi.mocked(adminInstances.listAllInstances).mockResolvedValue([
      { name: "research", alias: "alice", uid: "1", agent: { type: "openclaw" }, ready: true, phase: "Ready", createdAt: "2026-01-01T00:00:00Z",
        aiGatewayBudget: { budgetUSD: "50", spendUSD: "12.4", duration: "1mo", state: "active" } },
      { name: "support", alias: "bob", uid: "2", agent: { type: "hermes" }, ready: true, phase: "Ready", createdAt: "2026-01-01T00:00:00Z",
        aiGatewayBudget: { budgetUSD: "10", spendUSD: "10.02", duration: "1mo", state: "budgetReached" } },
      { name: "draft", alias: "carol", uid: "3", agent: { type: "custom" }, ready: false, phase: "Pending", createdAt: "2026-01-01T00:00:00Z" },
    ]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><InstanceListPage /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByRole("columnheader", { name: "Spent (USD)" })).toBeInTheDocument();
    const row = (name: string) => screen.getByRole("link", { name }).closest("tr")!;
    expect(within(row("research")).getByText("12.40 of 50.00")).toBeInTheDocument();
    expect(within(row("research")).queryByText("Budget reached")).not.toBeInTheDocument();
    expect(within(row("support")).getByText("10.02 of 10.00")).toBeInTheDocument();
    expect(within(row("support")).getByText("Budget reached")).toHaveClass("status-label");
    // Before the operator has read a key, the cell has no numbers.
    expect(within(row("draft")).queryByText(/ of /)).not.toBeInTheDocument();
  });
});
