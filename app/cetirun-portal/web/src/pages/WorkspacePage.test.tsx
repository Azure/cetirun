import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { WorkspacePage } from "./WorkspacePage";
import type { Instance } from "../types/instance";

const mockInstance: Instance = {
  instanceName: "testuser",
  agentType: "openclaw",
  displayName: "Test User",
  alias: "testuser",
  team: "aks",
  phase: "Ready",
  accessUrl: "https://example.com/ui/testuser/",
  gatewayToken: "tok-abc",
  upgradeAvailable: false,
  teamsEnabled: false,
};

vi.mock("../hooks/useInstance", () => ({
  useInstanceByNameQuery: () => ({ data: mockInstance, isLoading: false }),
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

function renderAt(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/instances/:instanceName/:section" element={<WorkspacePage />} />
          <Route path="/instances/:instanceName" element={<WorkspacePage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

describe("WorkspacePage", () => {
  it("shows instance name in sidebar", () => {
    renderAt("/instances/testuser/overview");
    expect(screen.getByText("testuser")).toBeInTheDocument();
  });

  it("shows all nav items", () => {
    renderAt("/instances/testuser/overview");
    expect(screen.getAllByText("Overview").length).toBeGreaterThan(0);
    expect(screen.getByText("Agent UI")).toBeInTheDocument();
    expect(screen.getByText("Terminal")).toBeInTheDocument();
    expect(screen.getByText("Desktop")).toBeInTheDocument();
  });

  it("renders overview section by default", () => {
    renderAt("/instances/testuser/overview");
    expect(screen.getByText(/Test User/)).toBeInTheDocument();
  });

  it("redirects /instances/:instanceName to /instances/:instanceName/overview", () => {
    renderAt("/instances/testuser");
    expect(screen.getByText(/Test User/)).toBeInTheDocument();
  });

  it("redirects unknown section to overview", () => {
    renderAt("/instances/testuser/unknown-section");
    expect(screen.getByText(/Test User/)).toBeInTheDocument();
  });

  it("links to standalone instance services when the instance is ready", () => {
    renderAt("/instances/testuser/overview");
    expect(screen.getByRole("link", { name: "Agent UI" })).toHaveAttribute("href", mockInstance.accessUrl);
    expect(screen.getByRole("link", { name: "Terminal" })).toHaveAttribute("href", "https://example.com/testuser/term");
    expect(screen.getByRole("link", { name: "Desktop" })).toHaveAttribute("href", "https://example.com/testuser/desktop");
  });

  it("links Hermes instances to their native dashboard", () => {
    mockInstance.agentType = "hermes";
    renderAt("/instances/testuser/overview");
    expect(screen.getByRole("link", { name: "Agent UI" })).toHaveAttribute("href", "https://example.com/ui/testuser/");
    mockInstance.agentType = "openclaw";
  });

  it("disables standalone instance services until the instance is ready", () => {
    mockInstance.phase = "Provisioning";
    renderAt("/instances/testuser/overview");
    expect(screen.getByText("Agent UI")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Terminal")).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Desktop")).toHaveAttribute("aria-disabled", "true");
    mockInstance.phase = "Ready";
  });
});
