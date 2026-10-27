// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InstanceDetailPage } from "./InstanceDetailPage";
import * as adminInstances from "../../api/adminInstance";
import { PortalError } from "../../api/fetch";

vi.mock("../../api/adminInstance");
vi.mock("../../hooks/useFeatures", () => ({
  useFeatures: () => ({ data: { allowedAgentTypes: ["openclaw", "hermes", "custom"] } }),
}));

const agent: adminInstances.AdminInstanceDetail = {
  name: "example",
  alias: "owner",
  displayName: "Example Agent",
  email: "owner@example.com",
  team: "research",
  uid: "agent-uid",
  resourceVersion: "7",
  agent: { type: "custom", image: "registry.example.com/agent:1", username: "agent", port: 8080, webUI: true },
  phase: "Ready",
  ready: true,
  createdAt: "2026-01-01T00:00:00Z",
  generation: 4,
  observedGeneration: 4,
  suspended: false,
  teamsEnabled: false,
  tailscaleEnabled: false,
  registryAuthConfigured: true,
  conditions: [],
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={["/admin/instances/example"]}><Routes>
    <Route path="/admin/instances/:name" element={<InstanceDetailPage />} />
    <Route path="/admin/instances" element={<div>Agent list</div>} />
  </Routes></MemoryRouter></QueryClientProvider>);
  return { ...view, client };
}

describe("InstanceDetailPage", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(adminInstances.getAdminInstance).mockResolvedValue(agent);
    vi.mocked(adminInstances.getAdminInstanceStatus).mockResolvedValue({ uid: agent.uid, conditions: [], events: [], nsEvents: [], pods: [] });
    vi.spyOn(document.body, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 1024, 768));
    vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) { return this.parentElement; });
  });

  it("sends only changed editable fields with the version opened for review", async () => {
    vi.mocked(adminInstances.updateAdminInstance).mockResolvedValue({ ...agent, displayName: "Renamed" });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const displayName = screen.getByRole("textbox", { name: "Display name" });
    await user.clear(displayName);
    await user.type(displayName, "Renamed");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(adminInstances.updateAdminInstance).toHaveBeenCalledWith("example", {
      uid: "agent-uid", resourceVersion: "7", displayName: "Renamed",
    }));
  });

  it.each(["hermes", "openclaw"] as const)("shows the %s port as read-only and omits it when saving", async (type) => {
    const builtIn = { ...agent, agent: { ...agent.agent, type, webUI: false } };
    vi.mocked(adminInstances.getAdminInstance).mockResolvedValue(builtIn);
    vi.mocked(adminInstances.updateAdminInstance).mockResolvedValue({ ...builtIn, displayName: "Renamed" });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const dialog = within(screen.getByRole("dialog"));
    expect(dialog.queryByRole("spinbutton")).not.toBeInTheDocument();
    expect(dialog.getByText("HTTP port: 8080 (managed by the platform runtime).")).toBeInTheDocument();
    expect(dialog.queryByRole("checkbox", { name: "Serve Web UI" })).not.toBeInTheDocument();
    const displayName = dialog.getByRole("textbox", { name: "Display name" });
    await user.clear(displayName);
    await user.type(displayName, "Renamed");
    await user.click(dialog.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(adminInstances.updateAdminInstance).toHaveBeenCalledWith("example", {
      uid: "agent-uid", resourceVersion: "7", displayName: "Renamed",
    }));
  });

  it("requires the complete name before deletion and keeps the detail page while deletion begins", async () => {
    vi.mocked(adminInstances.deleteAdminInstance).mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "Delete", exact: true });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByRole("textbox", { name: "Type example to confirm" }), "example");
    await user.click(confirm);
    await waitFor(() => expect(adminInstances.deleteAdminInstance).toHaveBeenCalledWith("example", { uid: "agent-uid", resourceVersion: "7" }));
    expect(screen.getByText(/Deletion has been requested/)).toBeInTheDocument();
    expect(screen.queryByText("Agent list")).not.toBeInTheDocument();
  });

  it("deletes only the object version confirmed when the dialog opened", async () => {
    vi.mocked(adminInstances.deleteAdminInstance).mockResolvedValue(undefined);
    const user = userEvent.setup();
    const { client } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    client.setQueryData(["admin", "instance", "example"], { ...agent, uid: "replacement-uid", resourceVersion: "1" });
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: "Type example to confirm" }), "example");
    await user.click(within(dialog).getByRole("button", { name: "Delete", exact: true }));
    await waitFor(() => expect(adminInstances.deleteAdminInstance).toHaveBeenCalledWith("example", { uid: "agent-uid", resourceVersion: "7" }));
  });

  it("returns to the list if a deleted name is reused by a different Agent", async () => {
    vi.mocked(adminInstances.deleteAdminInstance).mockResolvedValue(undefined);
    const user = userEvent.setup();
    const { client } = renderPage();
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: "Type example to confirm" }), "example");
    await user.click(within(dialog).getByRole("button", { name: "Delete", exact: true }));
    await waitFor(() => expect(screen.getByText(/Deletion has been requested/)).toBeInTheDocument());
    client.setQueryData(["admin", "instance", "example"], { ...agent, uid: "replacement-uid", resourceVersion: "1", phase: "Ready" });
    expect(await screen.findByText("Agent list")).toBeInTheDocument();
  });

  it("does not trim unchanged display and model values while saving another field", async () => {
    const spaced = { ...agent, displayName: "  Original  ", model: "  original-model  " };
    vi.mocked(adminInstances.getAdminInstance).mockResolvedValue(spaced);
    vi.mocked(adminInstances.updateAdminInstance).mockResolvedValue({ ...spaced, agent: { ...spaced.agent, port: 8081 } });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const port = screen.getByRole("spinbutton", { name: "Web UI port" });
    await user.clear(port);
    await user.type(port, "8081");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(adminInstances.updateAdminInstance).toHaveBeenCalledWith("example", {
      uid: "agent-uid", resourceVersion: "7", agent: { port: 8081 },
    }));
  });

  it("keeps only changed draft fields when a same-UID conflict is reloaded", async () => {
    const refreshed = { ...agent, resourceVersion: "8", agent: { ...agent.agent, port: 9090 }, model: "server-model" };
    vi.mocked(adminInstances.getAdminInstance).mockResolvedValueOnce(agent).mockResolvedValueOnce(refreshed);
    vi.mocked(adminInstances.updateAdminInstance)
      .mockRejectedValueOnce(new PortalError("Reload and review", 409, "instance_changed"))
      .mockResolvedValueOnce({ ...refreshed, displayName: "Renamed" });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const displayName = screen.getByRole("textbox", { name: "Display name" });
    await user.clear(displayName);
    await user.type(displayName, "Renamed");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/This Agent changed since/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Reload and review" }));
    await waitFor(() => expect(screen.getByRole("textbox", { name: "Display name" })).toHaveValue("Renamed"));
    expect(screen.getByRole("spinbutton", { name: "Web UI port" })).toHaveValue(9090);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(adminInstances.updateAdminInstance).toHaveBeenLastCalledWith("example", {
      uid: "agent-uid", resourceVersion: "8", displayName: "Renamed",
    }));
  });

  it("discards a draft when conflict review finds a same-name replacement", async () => {
    const replacement = { ...agent, uid: "replacement-uid", resourceVersion: "1" };
    vi.mocked(adminInstances.getAdminInstance).mockResolvedValueOnce(agent).mockResolvedValueOnce(replacement);
    vi.mocked(adminInstances.updateAdminInstance).mockRejectedValueOnce(new PortalError("Reload and review", 409, "instance_changed"));
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.clear(screen.getByRole("textbox", { name: "Display name" }));
    await user.type(screen.getByRole("textbox", { name: "Display name" }), "Draft");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await user.click(await screen.findByRole("button", { name: "Reload and review" }));
    expect(await screen.findByText(/was replaced with a new object/)).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Display name" })).not.toBeInTheDocument();
  });

  it("shows partial diagnostics failures without hiding available status", async () => {
    vi.mocked(adminInstances.getAdminInstanceStatus).mockResolvedValue({
      uid: agent.uid, conditions: [], events: [], nsEvents: [], pods: [], errors: [{ section: "pods", message: "Namespace access is unavailable" }],
    });
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Status" }));
    expect(await screen.findByText("pods: Namespace access is unavailable")).toBeInTheDocument();
    expect(screen.getByText("No pods found.")).toBeInTheDocument();
  });
});
