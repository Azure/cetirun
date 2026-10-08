// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, fireEvent } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { OverviewSection } from "./OverviewSection";
import type { Instance } from "../types/instance";

const baseInstance: Instance = {
  uid: "test-agent-uid",
  resourceVersion: "7",
  createdAt: "2026-09-30T00:00:00Z",
  instanceName: "testuser",
  agent: { type: "openclaw" },
  displayName: "Test User",
  alias: "testuser",
  team: "aks",
  phase: "Ready",
  accessUrl: "https://testuser.example.com/",
  gatewayToken: "tok-abc123",
  upgradeAvailable: false,
  teamsEnabled: false,
};

const mockUpdate = vi.hoisted(() => vi.fn());

vi.mock("../hooks/useInstance", () => ({
  useDeleteInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useUpdateInstanceByName: () => ({ mutate: mockUpdate, isPending: false, isError: false, reset: vi.fn() }),
  useUpgradeInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useInstanceStatusByNameQuery: () => ({ data: undefined, isLoading: false }),
}));

vi.mock("../hooks/useFeatures", () => ({
  useFeatures: () => ({ data: { tailscaleEnabled: false } }),
}));

vi.mock("../api/instance", () => ({
  downloadTeamsApp: vi.fn(),
}));

vi.mock("./CopilotAuthBlock", () => ({
  CopilotAuthBlock: () => <div>Direct Copilot Auth</div>,
}));

vi.mock("./AIGatewayBlock", () => ({
  AIGatewayBlock: () => <div>AI Gateway Models</div>,
}));

describe("OverviewSection", () => {
  beforeEach(() => mockUpdate.mockClear());
  const renderInRouter = (ui: React.ReactElement) => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return render(ui, { wrapper: ({ children }) => (
      <QueryClientProvider client={queryClient}><MemoryRouter>{children}</MemoryRouter></QueryClientProvider>
    ) });
  };

  it.each(["", "   "])("uses the Agent name when Display Name is %j", (displayName) => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, displayName }} />);
    expect(screen.getByRole("heading", { name: "testuser" })).toBeInTheDocument();
    expect(screen.queryByText(/'s OpenClaw/)).toBeNull();
  });

  it("uses Display Name directly without a possessive suffix", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, displayName: "Research Assistant" }} />);
    expect(screen.getByRole("heading", { name: "Research Assistant" })).toBeInTheDocument();
    expect(screen.queryByText(/Research Assistant's/)).toBeNull();
  });

  it("does not show a configuration-saved notice while a generation is pending", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, generation: 2, observedGeneration: 1 }} />);
    expect(screen.getByRole("heading", { name: "Test User" })).toBeInTheDocument();
    expect(screen.queryByText(/Configuration saved|waiting for the runtime/i)).toBeNull();
  });

  it("clears a Custom HTTP port without changing the image or image user", () => {
    const instance = { ...baseInstance, agent: { type: "custom" as const, image: "registry.example.com/assistant:1", username: "assistant", port: 8080 }, gatewayToken: undefined };
    renderInRouter(<OverviewSection instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    expect(screen.getByLabelText("Agent Image")).not.toHaveAttribute("readonly");
    expect(screen.getByLabelText("Agent Username")).toHaveAttribute("readonly");
    fireEvent.change(screen.getByLabelText("HTTP Port (optional)"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Display Name"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", displayName: "", agent: { port: null } }, expect.anything());
  });

  it.each(["custom", "hermes", "openclaw"] as const)("updates only the %s image with the displayed instance version", (type) => {
    const instance = { ...baseInstance, agent: { type, image: "registry.example.com/assistant:1", username: "vibe", port: 8080 } };
    renderInRouter(<OverviewSection instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    expect(screen.getByLabelText("Agent Image")).not.toHaveAttribute("readonly");
    expect(screen.getByLabelText("Agent Username")).toHaveAttribute("readonly");
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: " registry.example.com/assistant:2 " } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { image: "registry.example.com/assistant:2" } }, expect.anything());
  });

  it.each(["custom", "hermes", "openclaw"] as const)("does not clear a pinned %s image", (type) => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type, image: "registry.example.com/assistant:1" } }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: "   " } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).not.toHaveBeenCalled();
    expect(screen.getByText("A pinned Agent image cannot be cleared.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: "registry.example.com/assistant:2" } });
    expect(screen.queryByText("A pinned Agent image cannot be cleared.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { image: "registry.example.com/assistant:2" } }, expect.anything());
  });

  it("keeps the version that was opened for editing when fresh data arrives", () => {
    const rendered = renderInRouter(<OverviewSection instance={baseInstance} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.change(screen.getByLabelText("Display Name"), { target: { value: "My edit" } });
    rendered.rerender(<OverviewSection instance={{ ...baseInstance, displayName: "Concurrent edit", resourceVersion: "8" }} />);
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", displayName: "My edit" }, expect.anything());
  });

  it("shows Web UI configuration and preserves it during an unrelated edit", () => {
    const instance = { ...baseInstance, agent: { type: "custom" as const, image: "registry.example.com/editor:1", port: 8080, webUI: true }, gatewayToken: undefined };
    renderInRouter(<OverviewSection instance={instance} />);
    expect(screen.getByText("Web UI port: 8080")).toBeInTheDocument();
    expect(screen.queryByText(/Headless/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    expect(screen.getByRole("spinbutton", { name: "Web UI Port" })).toHaveValue(8080);
    expect(screen.getByRole("switch", { name: "Serve Web UI" })).toBeChecked();
    fireEvent.change(screen.getByLabelText("Display Name"), { target: { value: "Editor" } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", displayName: "Editor" }, expect.anything());
  });

  it("uses the same editable port for Web UI and preserves explicit false", () => {
    const instance = { ...baseInstance, agent: { type: "custom" as const, port: 8080, webUI: true }, gatewayToken: undefined };
    renderInRouter(<OverviewSection instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.change(screen.getByRole("spinbutton", { name: "Web UI Port" }), { target: { value: "9090" } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenLastCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { port: 9090 } }, expect.anything());
    fireEvent.click(screen.getByRole("switch", { name: "Serve Web UI" }));
    expect(screen.getByLabelText("HTTP Port (optional)")).toHaveValue(9090);
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenLastCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { port: 9090, webUI: false } }, expect.anything());
  });

  it("requires a port before enabling Web UI on a headless Custom", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type: "custom" }, gatewayToken: undefined }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.click(screen.getByRole("switch", { name: "Serve Web UI" }));
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).not.toHaveBeenCalled();
    expect(screen.getByText("A port is required when Web UI is enabled.")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("spinbutton", { name: "Web UI Port" }), { target: { value: "8080" } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { port: 8080, webUI: true } }, expect.anything());
  });

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
    expect(screen.getByRole("heading", { name: "Test User" }).parentElement).toHaveStyle({ flexWrap: "wrap" });
    expect(screen.getByRole("button", { name: "Edit Agent" }).parentElement).toHaveStyle({ flexWrap: "wrap" });
  });

  it("shows gateway token row", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.getByText(/Gateway Token/i)).toBeInTheDocument();
  });

  it("does not offer native integration edits to Custom", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type: "custom" }, gatewayToken: undefined }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    expect(screen.queryByLabelText("Gateway Token")).toBeNull();
    expect(screen.queryByLabelText("Tailscale Auth Key")).toBeNull();
    expect(screen.queryByLabelText("Enable Microsoft Teams Integration")).toBeNull();
    expect(screen.queryByText(/will restart the gateway/)).toBeNull();
  });

  it("shows the Hermes API and dashboard key instead of the OpenClaw token", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type: "hermes" }, apiServerKey: "hermes-key" }} />);
    expect(screen.getByText(/Hermes API \/ Dashboard Key/i)).toBeInTheDocument();
  });

  it("does not show terminal/desktop action cards", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.queryByText("Terminal")).toBeNull();
    expect(screen.queryByText("Desktop")).toBeNull();
  });

  it("hides direct Copilot Auth when the AI Gateway owns Copilot sign-in", () => {
    const instance = {
      ...baseInstance,
      cetirunAuthKey: "instance-key",
      aiGateway: {
        routed: true,
        phase: "Ready" as const,
        sharedModels: false,
        githubCopilot: true,
        models: [],
        copilot: { phase: "SignInRequired" as const, models: [], unpriced: [] },
      },
    };
    renderInRouter(<OverviewSection instance={instance} />);
    expect(screen.queryByText("Direct Copilot Auth")).toBeNull();
    expect(screen.getByText("AI Gateway Models")).toBeInTheDocument();
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
