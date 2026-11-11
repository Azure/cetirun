// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TeamListPage } from "./TeamListPage";
import * as teams from "../../api/adminTeam";
import { AdminConfigurationError } from "../../api/adminError";

vi.mock("../../api/adminTeam");

function compatibleTeam(): teams.TeamDTO {
  const checkedAt = new Date(Date.now() - 1_000);
  return {
    name: "research", uid: "team-uid", generation: 4, resourceVersion: "7", agentPolicyRef: "standard", sandboxRefs: ["kata"], defaultSandboxRef: "kata",
    compatibility: {
      state: "Compatible", team: { name: "research", uid: "team-uid", generation: 4 }, policy: { name: "standard", uid: "policy-uid", generation: 2 },
      sandboxes: [{ sandbox: { name: "kata", uid: "sandbox-uid", generation: 3 }, state: "Compatible" }], checkedAt: checkedAt.toISOString(), validUntil: new Date(checkedAt.getTime() + 30_000).toISOString(),
    },
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><TeamListPage /></MemoryRouter></QueryClientProvider>);
}

describe("TeamListPage compatibility", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(teams.listTeams).mockResolvedValue([compatibleTeam()]);
  });

  it("shows and expands the saved compatibility report", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("research")).toBeInTheDocument();
    expect(screen.getByText("Compatible")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show compatibility for research" }));
    const report = screen.getByLabelText("Compatibility report for research");
    expect(report).toHaveTextContent("Compatibility: Compatible");
    expect(report).toHaveTextContent("Policy: standard");
    expect(report).toHaveTextContent("Sandbox: kata");
  });

  it("does not show an expired report as compatible", async () => {
    const compatible = compatibleTeam();
    vi.mocked(teams.listTeams).mockResolvedValue([{ ...compatible, compatibility: { ...compatible.compatibility!, validUntil: "2000-01-01T00:00:00Z" } }]);
    renderPage();
    expect(await screen.findByText("Unknown")).toBeInTheDocument();
  });

  it("keeps a deletion error in its dialog and reloads the Team before another confirmation", async () => {
    const user = userEvent.setup();
    vi.mocked(teams.deleteTeam).mockRejectedValueOnce(new Error("Configuration changed; reload it before saving or deleting"));
    const latest = { ...compatibleTeam(), resourceVersion: "8", generation: 5, role: "Updated role" };
    vi.mocked(teams.getTeam).mockResolvedValue(latest);
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Delete research" }));
    await user.click(screen.getByRole("button", { name: "Delete", exact: true }));
    expect(await within(screen.getByRole("dialog")).findByText(/Configuration changed/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete", exact: true })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Reload latest Team" }));
    await waitFor(() => expect(screen.getByText(/Role: Updated role/)).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Delete", exact: true }));
    await waitFor(() => expect(teams.deleteTeam).toHaveBeenLastCalledWith(latest));
  });

  it("closes a conflicted deletion dialog when the Team was removed before reload", async () => {
    const user = userEvent.setup();
    vi.mocked(teams.deleteTeam).mockRejectedValueOnce(new Error("Configuration changed"));
    vi.mocked(teams.getTeam).mockRejectedValueOnce(new AdminConfigurationError("Team research not found", "team_not_found"));
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Delete research" }));
    await user.click(screen.getByRole("button", { name: "Delete", exact: true }));
    await screen.findByText("Configuration changed");
    await user.click(screen.getByRole("button", { name: "Reload latest Team" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { hidden: true })).not.toBeInTheDocument());
  });
});
