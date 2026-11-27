// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, fireEvent, within } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { OverviewSection } from "./OverviewSection";
import type { Instance, SetInstanceSuspendedRequest } from "../types/instance";

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
};

const mockUpdate = vi.hoisted(() => vi.fn());
const approvedImage = "registry.example.com/assistant@sha256:" + "b".repeat(64);
const mockCatalog = vi.hoisted(() => ({
  data: { customImages: [] as string[] | null }, isFetching: false, isError: false,
}));
vi.mock("../hooks/useAIGateway", () => ({ useAIGatewayCatalog: () => mockCatalog }));
const mockFeatures = vi.hoisted(() => ({ data: { tailscaleEnabled: false, agentIdentityEnabled: false, agentToolsEnabled: false } }));
const mockSuspension = vi.hoisted(() => ({
  mutate: vi.fn(), reset: vi.fn(), isPending: false, isError: false, error: new Error("Connection interrupted"),
  variables: undefined as SetInstanceSuspendedRequest | undefined,
}));
const mockRestart = vi.hoisted(() => ({
  mutate: vi.fn(), reset: vi.fn(), isPending: false, isError: false, error: null, data: undefined, variables: undefined,
}));

vi.mock("../hooks/useInstance", () => ({
  useDeleteInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useUpdateInstanceByName: () => ({ mutate: mockUpdate, isPending: false, isError: false, reset: vi.fn() }),
  useUpgradeInstanceByName: () => ({ mutate: vi.fn(), isPending: false, isError: false }),
  useInstanceStatusByNameQuery: () => ({ data: undefined, isLoading: false }),
  useSetInstanceSuspendedByName: () => mockSuspension,
  useRestartInstanceByName: () => mockRestart,
}));

vi.mock("../hooks/useFeatures", () => ({
  useFeatures: () => ({ data: mockFeatures.data }),
}));

vi.mock("./ToolAuthorizationBlock", () => ({
  ToolAuthorizationBlock: () => <div>Azure connection management</div>,
}));

describe("OverviewSection", () => {
  beforeEach(() => {
    mockUpdate.mockClear();
    mockCatalog.data.customImages = [approvedImage];
    mockCatalog.isFetching = false;
    mockCatalog.isError = false;
    mockFeatures.data = { tailscaleEnabled: false, agentIdentityEnabled: false, agentToolsEnabled: false };
    mockSuspension.mutate.mockClear();
    mockSuspension.isError = false;
    mockSuspension.variables = undefined;
    mockSuspension.mutate.mockImplementation((request: SetInstanceSuspendedRequest) => { mockSuspension.variables = request; });
    mockSuspension.reset.mockImplementation(() => {
      mockSuspension.isError = false;
      mockSuspension.variables = undefined;
    });
  });
  const renderInRouter = (ui: React.ReactElement) => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return render(ui, { wrapper: ({ children }) => (
      <QueryClientProvider client={queryClient}><MemoryRouter>{children}</MemoryRouter></QueryClientProvider>
    ) });
  };

  it("shows connection management only when Agent Tools is enabled", () => {
    mockFeatures.data = { tailscaleEnabled: false, agentIdentityEnabled: true, agentToolsEnabled: true };
    const view = renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.getByText("Azure connection management")).toBeInTheDocument();
    expect(screen.queryByText("Legacy Entra authorization")).not.toBeInTheDocument();

    mockFeatures.data = { tailscaleEnabled: false, agentIdentityEnabled: true, agentToolsEnabled: false };
    view.rerender(<OverviewSection instance={baseInstance} />);
    expect(screen.queryByText("Legacy Entra authorization")).not.toBeInTheDocument();
    expect(screen.queryByText("Azure connection management")).not.toBeInTheDocument();
  });

  it("shows the identity cause on both tabs instead of a provisioning spinner", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, phase: "Provisioning", generation: 2,
      governance: { category: "identity", reason: "OwnerBindingMissing", message: "trusted Agent Identity owner binding is missing or invalid", condition: "AgentIdentityReady", observedGeneration: 2 },
    }} />);
    expect(screen.getByText("Identity owner binding required")).toBeInTheDocument();
    expect(screen.queryByText("Provisioning your agent...")).not.toBeInTheDocument();
    expect(screen.getByText("Provisioning")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Status" }));
    expect(screen.getByText("Identity owner binding required")).toBeInTheDocument();
  });

  it("adds owner lifecycle controls without replacing connection controls or claiming shutdown", () => {
    mockFeatures.data = { tailscaleEnabled: false, agentIdentityEnabled: true, agentToolsEnabled: true };
    renderInRouter(<OverviewSection instance={{ ...baseInstance, suspended: true, resumeAvailable: true, phase: "Suspended" }} />);
    expect(screen.getByRole("button", { name: "Resume Agent" })).toBeEnabled();
    expect(screen.getByText("Azure connection management")).toBeInTheDocument();
    expect(screen.getByText(/does not confirm Pod termination/)).toBeInTheDocument();
    expect(screen.queryByText(/Contact your administrator to restore access/)).not.toBeInTheDocument();
  });

  it("keeps only the lifecycle controls that admission allows for CetiAdmin", () => {
    const cetiAdmin: Instance = {
      ...baseInstance, instanceName: "cetiadmin", displayName: "", team: "cetiadmin", agent: { type: "hermes" },
      gatewayToken: undefined, apiServerKey: "hermes-key", upgradeAvailable: true, cetiAdmin: true,
      suspended: false, suspendAvailable: true, restartAvailable: true,
    };
    const view = renderInRouter(<OverviewSection instance={cetiAdmin} />);
    expect(screen.getByRole("button", { name: "Restart Agent" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Suspend Agent" })).toBeInTheDocument();
    for (const name of ["Edit Agent", "Delete Agent", "Upgrade to Latest"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    const notice = screen.getByLabelText("CetiAdmin");
    expect(notice).toHaveTextContent("The platform manages CetiAdmin.");
    expect(notice).toHaveTextContent("cetiadmin.model.default");
    expect(notice).toHaveTextContent("sign in as cetirun with the Hermes API / Webapp Key");

    view.rerender(<OverviewSection instance={{ ...cetiAdmin, cetiAdmin: undefined }} />);
    for (const name of ["Edit Agent", "Delete Agent", "Upgrade to Latest"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.queryByLabelText("CetiAdmin")).not.toBeInTheDocument();
  });

  const unreadyHermes: Instance = {
    ...baseInstance, agent: { type: "hermes" }, phase: "Provisioning", generation: 2, observedGeneration: 2,
    suspended: false, suspendAvailable: true, resumeAvailable: false, restartAvailable: false,
    conditions: [{ type: "Ready", status: "False", reason: "PodNotReady", message: "StatefulSet is not ready", observedGeneration: 0 }],
  };

  it("shows the reported readiness cause and keeps recovery on the existing confirmed suspension path", () => {
    const view = renderInRouter(<OverviewSection instance={unreadyHermes} />);
    expect(screen.getByText("Agent is not ready")).toBeInTheDocument();
    expect(screen.getByText("Reported reason: PodNotReady: StatefulSet is not ready")).toBeInTheDocument();
    expect(screen.queryByText("Provisioning your agent...")).not.toBeInTheDocument();
    expect(screen.queryByText(/WebappNotServing/)).not.toBeInTheDocument();
    expect(screen.getByText(/Wait for the controller to report Suspended/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restart Agent" })).not.toBeInTheDocument();
    expect(mockSuspension.mutate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "View Status" }));
    expect(screen.getByRole("tab", { name: "Status" })).toHaveAttribute("aria-selected", "true");
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }));
    fireEvent.click(screen.getByRole("button", { name: "Suspend Agent" }));
    expect(mockSuspension.mutate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Suspend", exact: true }));
    expect(mockSuspension.mutate).toHaveBeenLastCalledWith({ uid: baseInstance.uid, resourceVersion: "7", generation: 2, suspended: true }, expect.anything());

    view.rerender(<OverviewSection instance={{ ...unreadyHermes, resourceVersion: "8", generation: 3,
      phase: "Suspended", suspended: true, suspendAvailable: false, resumeAvailable: true }} />);
    expect(screen.queryByText("Agent is not ready")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Resume Agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Resume", exact: true }));
    expect(mockSuspension.mutate).toHaveBeenLastCalledWith({ uid: baseInstance.uid, resourceVersion: "8", generation: 3, suspended: false }, expect.anything());

    view.rerender(<OverviewSection instance={{ ...unreadyHermes, resourceVersion: "9", generation: 4 }} />);
    expect(screen.getByText("Agent is not ready")).toBeInTheDocument();
    expect(screen.getByText(/An accepted resume does not mean the Agent is Ready/)).toBeInTheDocument();
    view.rerender(<OverviewSection instance={{ ...unreadyHermes, phase: "Ready", restartAvailable: true }} />);
    expect(screen.queryByText("Agent is not ready")).not.toBeInTheDocument();
    expect(screen.queryByText(/Wait for the controller to report Suspended/)).not.toBeInTheDocument();
  });

  it("shows configuration failures without presenting a restart as their remedy", () => {
    renderInRouter(<OverviewSection instance={{ ...unreadyHermes, phase: "Failed", conditions: [
      { type: "Ready", status: "False", reason: "AgentConfigInvalid", message: "spec.agent.image is not qualified for the selected harness profile", observedGeneration: 2 },
    ] }} />);
    expect(screen.getByText(/Reported reason: AgentConfigInvalid/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View Status" })).toBeEnabled();
    expect(screen.queryByText(/Wait for the controller to report Suspended/)).not.toBeInTheDocument();
  });

  it("does not suggest suspension when the backend cannot verify retained HOME", () => {
    renderInRouter(<OverviewSection instance={{ ...unreadyHermes, suspendAvailable: false,
      suspensionUnavailableReason: "Retained suspension requires persistent HOME storage" }} />);
    expect(screen.getByText(/Reported reason: PodNotReady/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Suspend Agent" })).toBeDisabled();
    expect(screen.getByText(/Retained suspension requires persistent HOME/)).toBeInTheDocument();
    expect(screen.queryByText(/Wait for the controller to report Suspended/)).not.toBeInTheDocument();
  });

  it("does not suggest another recovery while a restart is pending", () => {
    renderInRouter(<OverviewSection instance={{ ...unreadyHermes, restartRequestId: "request-00000001" }} />);
    expect(screen.getByText(/Reported reason: PodNotReady/)).toBeInTheDocument();
    expect(screen.queryByText(/Wait for the controller to report Suspended/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart Agent" })).toBeDisabled();
  });

  it.each([undefined, [{ type: "Ready", status: "False", reason: "PreviousFailure", message: "Old configuration", observedGeneration: 1 }]])(
    "keeps the provisioning indicator without a current reported failure", (conditions) => {
      renderInRouter(<OverviewSection instance={{ ...unreadyHermes, conditions }} />);
      expect(screen.queryByText("Agent is not ready")).not.toBeInTheDocument();
      expect(screen.getByText("Provisioning your agent...")).toBeInTheDocument();
    },
  );

  it("keeps governance failures ahead of workload recovery advice", () => {
    renderInRouter(<OverviewSection instance={{ ...unreadyHermes,
      governance: { category: "policy", reason: "PolicyDenied", message: "Image is not approved", condition: "PolicyApplied", observedGeneration: 2 },
    }} />);
    expect(screen.getByText("Blocked by policy")).toBeInTheDocument();
    expect(screen.queryByText("Agent is not ready")).not.toBeInTheDocument();
    expect(screen.queryByText(/Wait for the controller to report Suspended/)).not.toBeInTheDocument();
  });

  it("retains an uncertain lifecycle request while the owner checks the Status tab", () => {
    const current = { ...baseInstance, suspended: false, suspendAvailable: true, restartAvailable: false };
    const view = renderInRouter(<OverviewSection instance={current} />);
    fireEvent.click(screen.getByRole("button", { name: "Suspend Agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Suspend", exact: true }));
    const sent = mockSuspension.mutate.mock.calls[0][0];
    mockSuspension.isError = true;
    view.rerender(<OverviewSection instance={{ ...current, resourceVersion: "8" }} />);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(screen.getByRole("tab", { name: "Status" }));
    fireEvent.click(screen.getByRole("tab", { name: "Overview" }));
    fireEvent.click(screen.getByRole("button", { name: "Review suspend request" }));
    fireEvent.click(screen.getByRole("button", { name: "Retry request" }));
    expect(mockSuspension.mutate.mock.calls[1][0]).toEqual(sent);
    expect(sent.resourceVersion).toBe("7");
  });

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
    const image = type === "custom" ? approvedImage : "registry.example.com/assistant:2";
    const instance = { ...baseInstance, agent: { type, image: "registry.example.com/assistant:1", username: "vibe", port: 8080 } };
    renderInRouter(<OverviewSection instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    expect(screen.getByLabelText("Agent Image")).not.toHaveAttribute("readonly");
    expect(screen.getByLabelText("Agent Username")).toHaveAttribute("readonly");
    if (type === "custom") {
      expect(screen.getByLabelText("Agent Image")).toHaveAttribute("list", "edit-approved-custom-images");
      expect(document.querySelector("#edit-approved-custom-images option")).toHaveValue(approvedImage);
    }
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: ` ${image} ` } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { image } }, expect.anything());
  });

  it.each(["custom", "hermes", "openclaw"] as const)("does not clear a pinned %s image", (type) => {
    const image = type === "custom" ? approvedImage : "registry.example.com/assistant:2";
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type, image: "registry.example.com/assistant:1" } }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: "   " } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).not.toHaveBeenCalled();
    expect(screen.getByText("A pinned Agent image cannot be cleared.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: image } });
    expect(screen.queryByText("A pinned Agent image cannot be cleared.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", agent: { image } }, expect.anything());
  });

  it.each(["registry.example.com/assistant:latest", "registry.example.com/assistant@sha256:" + "c".repeat(64)])("rejects an unapproved Custom image update: %s", (image) => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type: "custom", image: approvedImage } }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: image } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).not.toHaveBeenCalled();
    expect(screen.getByText(/Tags and unapproved images cannot run/)).toBeInTheDocument();
  });

  it.each(["unknown", "loading", "error", "empty"])("keeps unrelated edits available when Custom approvals are %s", (state) => {
    mockCatalog.data.customImages = state === "unknown" ? null : state === "empty" ? [] : [approvedImage];
    mockCatalog.isFetching = state === "loading";
    mockCatalog.isError = state === "error";
    const image = "registry.example.com/assistant@sha256:" + "a".repeat(64);
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type: "custom", image } }} />);
    fireEvent.click(screen.getByRole("button", { name: "Edit Agent" }));
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: approvedImage } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).not.toHaveBeenCalled();
    expect(screen.getByText(state === "empty" ? /Tags and unapproved images cannot run/ : /Custom image approvals are not available/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Agent Image"), { target: { value: image } });
    fireEvent.change(screen.getByLabelText("Display Name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save", exact: true }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "test-agent-uid", resourceVersion: "7", displayName: "Renamed" }, expect.anything());
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
    expect(screen.queryByText(/will restart the gateway/)).toBeNull();
  });

  it("shows the Hermes API and Webapp key instead of the OpenClaw token", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, agent: { type: "hermes" }, apiServerKey: "hermes-key" }} />);
    expect(screen.getByText(/Hermes API \/ Webapp Key/i)).toBeInTheDocument();
  });

  it("does not show terminal/desktop action cards", () => {
    renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.queryByText("Terminal")).toBeNull();
    expect(screen.queryByText("Desktop")).toBeNull();
  });

  it("shows Suspended warning when phase is Suspended", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, phase: "Suspended" }} />);
    expect(screen.getByText(/The controller reports Suspended/i)).toBeInTheDocument();
  });

  it("lists the Team's skills with their publication state", () => {
    renderInRouter(<OverviewSection instance={{ ...baseInstance, teamSkills: [
      { name: "pdf-tools", description: "Reads and fills PDF forms.", version: "bbbbbbbbbbbb", state: "published" },
      { name: "web-search", description: "Searches the web.", version: "", repositoryVersion: "cccccccccccc", state: "pending" },
      { name: "old-skill", description: "", version: "dddddddddddd", state: "retained" },
      { name: "unknown-skill", description: "", version: "", state: "missing" },
    ] }} />);
    const block = within(screen.getByRole("region", { name: "Team skills" }));
    const items = block.getAllByRole("listitem");
    expect(items.map((item) => within(item).getByText(/^(Published|Pending|Retained|Missing)$/).textContent))
      .toEqual(["Published", "Pending", "Retained", "Missing"]);
    expect(within(items[0]).getByText("pdf-tools")).toBeInTheDocument();
    expect(within(items[0]).getByText("Reads and fills PDF forms.")).toBeInTheDocument();
    expect(within(items[0]).getByText("Version bbbbbbb")).toBeInTheDocument();
    expect(within(items[0]).queryByText(/^Repository version/)).toBeNull();
    expect(within(items[1]).getByText(/next start waits for it/)).toBeInTheDocument();
    expect(within(items[1]).getByText("Repository version ccccccc")).toBeInTheDocument();
    expect(within(items[1]).queryByText(/^Version/)).toBeNull();
    expect(within(items[2]).getByText(/keeps the last published version/)).toBeInTheDocument();
    expect(within(items[3]).getByText(/does not have it/)).toBeInTheDocument();
    expect(within(items[3]).queryByText(/^Version/)).toBeNull();
  });

  it("tells a repository change that the Team's release lacks, and a failed publication, from what the Team published", () => {
    const teamSkills: Instance["teamSkills"] = [
      { name: "pdf-tools", description: "Reads and fills PDF forms.", version: "aaaaaaaaaaaa", repositoryVersion: "bbbbbbbbbbbb", state: "updating" },
    ];
    const block = () => within(screen.getByRole("region", { name: "Team skills" }));
    const view = renderInRouter(<OverviewSection instance={{ ...baseInstance, teamSkills }} />);
    const [item] = block().getAllByRole("listitem");
    expect(within(item).getByText("Update pending")).toBeInTheDocument();
    expect(within(item).getByText("Version aaaaaaa")).toBeInTheDocument();
    expect(within(item).getByText("Repository version bbbbbbb")).toBeInTheDocument();
    expect(within(item).getByText(/does not have the change yet, so the Agent gets the version shown/)).toBeInTheDocument();
    expect(block().queryByText(/could not publish/)).toBeNull();

    view.rerender(<OverviewSection instance={{ ...baseInstance, teamSkills, teamSkillsPublicationFailed: true }} />);
    expect(block().getByText(/could not publish the Team's current skills/)).toBeInTheDocument();
  });

  it("shows no Team skills block without Team skills", () => {
    const view = renderInRouter(<OverviewSection instance={baseInstance} />);
    expect(screen.queryByRole("region", { name: "Team skills" })).toBeNull();
    view.rerender(<OverviewSection instance={{ ...baseInstance, teamSkills: [] }} />);
    expect(screen.queryByRole("region", { name: "Team skills" })).toBeNull();
  });

  it("asks for a restart while the Agent runs an earlier release of the Team's skills", () => {
    const teamSkills: Instance["teamSkills"] = [
      { name: "pdf-tools", description: "Reads and fills PDF forms.", version: "bbbbbbbbbbbb", state: "published" },
    ];
    const block = () => within(screen.getByRole("region", { name: "Team skills" }));
    const view = renderInRouter(<OverviewSection instance={{ ...baseInstance, teamSkills, restartAvailable: true }} />);
    expect(block().queryByText(/changed after this Agent started/)).toBeNull();
    expect(block().queryByRole("button", { name: "Restart Agent" })).toBeNull();

    view.rerender(<OverviewSection instance={{ ...baseInstance, teamSkills, restartAvailable: true, teamSkillsRestartNeeded: true }} />);
    expect(block().getByText(/changed after this Agent started/)).toBeInTheDocument();
    // The block's button asks for the same confirmation as the Agent's own.
    fireEvent.click(block().getByRole("button", { name: "Restart Agent" }));
    expect(within(screen.getByRole("dialog")).getByText(/Restart this Agent using its current configuration/)).toBeInTheDocument();

    // Without a restart that the owner may request, the block only explains.
    view.rerender(<OverviewSection instance={{ ...baseInstance, teamSkills, restartAvailable: false, teamSkillsRestartNeeded: true }} />);
    expect(block().getByText(/changed after this Agent started/)).toBeInTheDocument();
    expect(block().queryByRole("button", { name: "Restart Agent" })).toBeNull();
  });

});
