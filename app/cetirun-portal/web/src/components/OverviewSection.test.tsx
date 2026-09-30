// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, fireEvent } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { OverviewSection } from "./OverviewSection";
import type { Instance } from "../types/instance";

const baseInstance: Instance = {
  instanceName: "testuser",
  agentType: "openclaw",
  displayName: "Test User",
  alias: "testuser",
  team: "aks",
  phase: "Ready",
  accessUrl: "https://testuser.example.com/",
  gatewayToken: "tok-abc123",
  upgradeAvailable: false,
  teamsEnabled: false,
};

vi.mock("../hooks/useInstance", () => ({
  useDeleteInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useUpdateInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false, reset: vi.fn() }),
  useUpgradeInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useInstanceStatusByNameQuery: () => ({ data: undefined, isLoading: false }),
}));

vi.mock("../hooks/useFeatures", () => ({
  useFeatures: () => ({ data: { tailscaleEnabled: false } }),
}));

vi.mock("../api/instance", () => ({
  downloadTeamsApp: vi.fn(),
}));

describe("OverviewSection", () => {
  const renderInRouter = (ui: React.ReactElement) => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>{ui}</MemoryRouter>
      </QueryClientProvider>,
    );
  };

  it("shows alias and team", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.getByText(/testuser/)).toBeInTheDocument();
    expect(screen.getByText(/aks/i)).toBeInTheDocument();
  });

  it("uses Agent terminology for management actions", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.getByRole("button", { name: "Edit Agent" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete Agent" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit Instance" })).toBeNull();
  });

  it("allows headings and actions to wrap in narrow panels", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, upgradeAvailable: true }} />);
    expect(screen.getByText("Test User's OpenClaw").parentElement).toHaveStyle({ flexWrap: "wrap" });
    expect(screen.getByRole("button", { name: "Edit Agent" }).parentElement).toHaveStyle({ flexWrap: "wrap" });
  });

  it("shows gateway token row", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.getByText(/Gateway Token/i)).toBeInTheDocument();
  });

  it("does not offer native integration edits to Custom", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agentType: "custom", gatewayToken: undefined }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    expect(screen.queryByLabelText("Gateway Token")).toBeNull();
    expect(screen.queryByLabelText("Tailscale Auth Key")).toBeNull();
    expect(screen.queryByLabelText("Enable Microsoft Teams Integration")).toBeNull();
    expect(screen.queryByText(/will restart the gateway/)).toBeNull();
  });

  it("shows the Hermes API and dashboard key instead of the OpenClaw token", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agentType: "hermes", apiServerKey: "hermes-key" }} />);
    expect(screen.getByText(/Hermes API \/ Dashboard Key/i)).toBeInTheDocument();
  });

  it("does not show terminal/desktop action cards", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.queryByText("Terminal")).toBeNull();
    expect(screen.queryByText("Desktop")).toBeNull();
  });

  it("shows Suspended warning when phase is Suspended", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, phase: "Suspended" }} />);
    expect(screen.getByText(/Contact your administrator/i)).toBeInTheDocument();
  });

  it("ignores the removed Teams version gate", () => {
    const instance = {
      ...baseInstance,
      teamsEnabled: true,
      teamsState: "Ready" as const,
      teamsUpgradeRequired: true,
    };
    renderInRouter(<OverviewSection instance={instance} />);
    expect(screen.getByRole("button", { name: "Download Teams App" })).toBeInTheDocument();
    expect(screen.queryByText(/Your chart version does not support Teams integration/i)).toBeNull();
  });
});
