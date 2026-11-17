// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { InstanceListPage } from "./InstanceListPage";
import * as adminInstances from "../../api/adminInstance";

vi.mock("../../api/adminInstance");

/** The row of the named Agent writes the phase and draws the state. */
function expectRow(name: string, phase: string, state: string) {
  const row = screen.getByRole("link", { name }).closest("tr")!;
  expect(within(row).getByText(phase)).toBeInTheDocument();
  expect(row.querySelector("svg.lifecycle")).toHaveClass(`lifecycle--${state}`);
}

describe("admin InstanceListPage state", () => {
  it("draws an Agent as ready only when the platform reports it ready", async () => {
    vi.mocked(adminInstances.listAllInstances).mockResolvedValue([
      { name: "settled", alias: "alice", uid: "1", agent: { type: "hermes" }, ready: true, phase: "Ready", createdAt: "2026-01-01T00:00:00Z" },
      { name: "stale", alias: "bob", uid: "2", agent: { type: "hermes" }, ready: false, phase: "Ready", createdAt: "2026-01-01T00:00:00Z" },
      { name: "starting", alias: "carol", uid: "3", agent: { type: "custom" }, ready: false, phase: "Provisioning", createdAt: "2026-01-01T00:00:00Z" },
      { name: "recovered", alias: "dave", uid: "4", agent: { type: "custom" }, ready: true, phase: "Provisioning", createdAt: "2026-01-01T00:00:00Z" },
      { name: "unreported", alias: "erin", uid: "5", agent: { type: "custom" }, ready: true, createdAt: "2026-01-01T00:00:00Z" },
    ]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><InstanceListPage /></MemoryRouter></QueryClientProvider>);
    await screen.findByRole("link", { name: "settled" });
    expectRow("settled", "Ready", "ready");
    expectRow("stale", "Ready", "waiting");
    expectRow("starting", "Provisioning", "busy");
    expectRow("recovered", "Provisioning", "ready");
    expectRow("unreported", "Unknown", "ready");
  });
});
