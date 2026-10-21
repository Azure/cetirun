// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Connection, ConnectionBinding } from "../../api/toolAuthorization";
import type { Instance } from "../../types/instance";
import { ToolAuthorizationBlock } from "../ToolAuthorizationBlock";

const api = vi.hoisted(() => ({
  listConnections: vi.fn(), listConnectionBindings: vi.fn(), createToolIdentity: vi.fn(), createConnectionBinding: vi.fn(), deleteConnectionBinding: vi.fn(),
}));
vi.mock("../../api/toolAuthorization", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../api/toolAuthorization")>()), ...api }));

const instance: Instance = {
  instanceName: "agent-a", uid: "agent-uid", resourceVersion: "1", agent: { type: "hermes" }, alias: "owner", displayName: "Agent A", team: "platform", phase: "Ready", createdAt: "2026-10-10T00:00:00Z", upgradeAvailable: false,
  agentIdentity: { enabled: true, phase: "Ready" },
};
const azure: Connection = {
  id: "azure-connection", provider: "azure", accountDisplay: "Agent A", services: ["azure_arm"], revision: 1, status: "connected", createdAt: "2026-10-10T00:00:00Z", mode: "agent",
  identity: { instanceUid: instance.uid, principalId: "principal", clientId: "client", tenantId: "tenant" },
};
const binding: ConnectionBinding = { id: "binding-1", instanceUid: instance.uid, connectionId: azure.id, services: ["azure_arm"], enabled: true, revision: 7 };

function renderUI(value = instance, client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })) {
  return render(<QueryClientProvider client={client}><ToolAuthorizationBlock instance={value} /></QueryClientProvider>);
}

describe("ToolAuthorizationBlock", () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    api.listConnections.mockResolvedValue([azure]);
    api.listConnectionBindings.mockResolvedValue({ bindings: [], readOnly: false });
    api.createToolIdentity.mockResolvedValue(azure);
    api.createConnectionBinding.mockResolvedValue(binding);
    api.deleteConnectionBinding.mockResolvedValue(undefined);
  });

  it("connects the current Agent's Azure identity with Azure Resource Manager only", async () => {
    const user = userEvent.setup();
    renderUI();
    await screen.findByText("No Azure connection is configured for this Agent.");
    await user.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.createToolIdentity).toHaveBeenCalledWith("agent-a"));
    expect(api.createConnectionBinding).toHaveBeenCalledWith("agent-a", "azure-connection");
    expect(screen.getByText("Agent principal ID: principal")).toBeInTheDocument();
    expect(screen.getByText(/does not verify Azure permissions/)).toBeInTheDocument();
  });

  it("disconnects only the current Agent binding with its revision", async () => {
    api.listConnectionBindings.mockResolvedValue({ bindings: [binding], readOnly: false });
    const user = userEvent.setup();
    renderUI();
    await user.click(await screen.findByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(api.deleteConnectionBinding).toHaveBeenCalledWith("agent-a", "binding-1", 7));
    expect(screen.queryByRole("button", { name: /remove account|disable|reauthorize/i })).not.toBeInTheDocument();
  });

  it("clears a failed Connect error after a conflict is resolved by Disconnect", async () => {
    api.listConnectionBindings
      .mockResolvedValueOnce({ bindings: [], readOnly: false })
      .mockResolvedValue({ bindings: [binding], readOnly: false });
    api.createConnectionBinding.mockRejectedValue(new Error("Binding changed; reload before updating"));
    const user = userEvent.setup();
    renderUI();
    await user.click(await screen.findByRole("button", { name: "Connect" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Binding changed; reload before updating");
    await user.click(await screen.findByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(api.deleteConnectionBinding).toHaveBeenCalledWith("agent-a", "binding-1", 7));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows partial Connect failure honestly and retries through the deterministic identity endpoint", async () => {
    let identityCreated = false;
    let connected = false;
    let bindingAttempts = 0;
    api.listConnections.mockImplementation(async () => identityCreated ? [azure] : []);
    api.listConnectionBindings.mockImplementation(async () => ({ bindings: connected ? [binding] : [], readOnly: false }));
    api.createToolIdentity.mockImplementation(async () => { identityCreated = true; return azure; });
    api.createConnectionBinding.mockImplementation(async () => {
      if (bindingAttempts++ === 0) {
        throw new Error("Binding changed; reload before updating");
      }
      connected = true;
      return binding;
    });
    const user = userEvent.setup();
    renderUI();
    await user.click(await screen.findByRole("button", { name: "Connect" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Binding changed; reload before updating");
    expect(screen.getByText("Not connected")).toBeInTheDocument();
    expect(screen.getByText("Agent principal ID: principal")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.createToolIdentity).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByText("Connected")).toBeInTheDocument());
    expect(screen.getAllByText("Azure Agent identity")).toHaveLength(1);
  });

  it("refreshes a revision conflict and retries Disconnect with the current revision", async () => {
    let currentRevision = 7;
    api.listConnectionBindings.mockImplementation(async () => ({ bindings: [{ ...binding, revision: currentRevision }], readOnly: false }));
    api.deleteConnectionBinding.mockImplementation(async (_name: string, _id: string, revision: number) => {
      if (revision === 7) {
        currentRevision = 8;
        throw new Error("Binding revision changed");
      }
    });
    const user = userEvent.setup();
    renderUI();
    await user.click(await screen.findByRole("button", { name: "Disconnect" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Binding revision changed");
    await user.click(screen.getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(api.deleteConnectionBinding).toHaveBeenLastCalledWith("agent-a", "binding-1", 8));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not expose another Agent's Azure identity", async () => {
    api.listConnections.mockResolvedValue([{ ...azure, identity: { ...azure.identity!, instanceUid: "other-agent" } }]);
    renderUI();
    expect(await screen.findByText("No Azure connection is configured for this Agent.")).toBeInTheDocument();
    expect(screen.queryByText("Agent principal ID: principal")).not.toBeInTheDocument();
  });

  it("fails closed while a connection load failed and retries", async () => {
    api.listConnections.mockRejectedValue(new Error("Unavailable"));
    renderUI();
    expect(await screen.findByRole("alert")).toHaveTextContent("Unavailable");
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(api.listConnections).toHaveBeenCalledTimes(2);
  });

  it("blocks create until identity readiness, and allows cleanup on a read-only runtime", async () => {
    const view = renderUI({ ...instance, agentIdentity: { enabled: true, phase: "Provisioning" } });
    await screen.findByText(/Connect is available when its identity is ready/);
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();

    view.unmount();
    api.listConnectionBindings.mockResolvedValue({ bindings: [binding], readOnly: true });
    renderUI();
    expect(await screen.findByText(/no longer supports creating Azure connections/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeEnabled();
  });

  it("does not offer Connect for a stale ready phase after identity was disabled", async () => {
    renderUI({ ...instance, agentIdentity: { enabled: false, phase: "Ready" } });
    await screen.findByText(/Connect is available when its identity is ready/);
    expect(screen.getByRole("button", { name: "Connect" })).toBeDisabled();
  });

  it("does not reuse cache after the same Agent name receives a new UID", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
    const view = renderUI(instance, client);
    await screen.findByText("Agent principal ID: principal");
    api.listConnections.mockResolvedValue([]);
    api.listConnectionBindings.mockResolvedValue({ bindings: [], readOnly: false });
    view.rerender(<QueryClientProvider client={client}><ToolAuthorizationBlock instance={{ ...instance, uid: "replacement-uid" }} /></QueryClientProvider>);
    expect(await screen.findByText("No Azure connection is configured for this Agent.")).toBeInTheDocument();
    expect(screen.queryByText("Agent principal ID: principal")).not.toBeInTheDocument();
  });
});
