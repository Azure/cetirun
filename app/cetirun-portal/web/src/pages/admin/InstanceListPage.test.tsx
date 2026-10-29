// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InstanceListPage } from "./InstanceListPage";
import * as adminInstances from "../../api/adminInstance";

vi.mock("../../api/adminInstance");

describe("admin InstanceListPage", () => {
  beforeEach(() => vi.resetAllMocks());

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
});
