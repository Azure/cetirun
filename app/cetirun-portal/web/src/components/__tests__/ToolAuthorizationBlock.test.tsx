// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ToolAuthorizationError, type Connection, type ToolGrant } from "../../api/toolAuthorization";
import type { Instance } from "../../types/instance";
import { ToolAuthorizationBlock } from "../ToolAuthorizationBlock";

const api = vi.hoisted(() => ({
  listConnections: vi.fn(),
  listToolGrants: vi.fn(),
  startConnection: vi.fn(),
  reconnectConnection: vi.fn(),
  deleteConnection: vi.fn(),
  createToolGrant: vi.fn(),
  updateToolGrant: vi.fn(),
  deleteToolGrant: vi.fn(),
}));

vi.mock("../../api/toolAuthorization", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/toolAuthorization")>()),
  ...api,
}));

const instance: Instance = {
  instanceName: "agent-a",
  uid: "agent-uid",
  resourceVersion: "1",
  agent: { type: "hermes" },
  alias: "owner",
  displayName: "Agent A",
  team: "platform",
  phase: "Ready",
  createdAt: "2026-10-10T00:00:00Z",
  upgradeAvailable: false,
};

const githubConnection: Connection = {
  id: "connection-github",
  provider: "github",
  accountDisplay: "octocat",
  capabilities: ["github_contents_get", "github_issues_create"],
  revision: 3,
  status: "connected",
  createdAt: "2026-10-10T00:00:00Z",
};

function renderBlock() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><ToolAuthorizationBlock instance={instance} /></QueryClientProvider>);
}

describe("ToolAuthorizationBlock", () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset());
    api.listConnections.mockResolvedValue([]);
    api.listToolGrants.mockResolvedValue([]);
    api.startConnection.mockResolvedValue({ authorizationUrl: "https://login.example.com", connectionId: "new-connection", expiresAt: "2026-10-10T00:10:00Z" });
    api.reconnectConnection.mockResolvedValue({ authorizationUrl: "https://login.example.com", connectionId: "connection-github", expiresAt: "2026-10-10T00:10:00Z" });
    api.deleteConnection.mockResolvedValue(undefined);
    api.createToolGrant.mockResolvedValue({});
    api.updateToolGrant.mockResolvedValue({});
    api.deleteToolGrant.mockResolvedValue(undefined);
  });

  it("offers only the fixed account providers", () => {
    renderBlock();

    expect(within(screen.getByRole("combobox", { name: "Add account" })).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Azure", "Microsoft 365", "GitHub", "Google",
    ]);
  });

  it("creates a scoped grant with read tools by default and writes enabled explicitly", async () => {
    api.listConnections.mockResolvedValue([githubConnection]);
    const user = userEvent.setup();
    renderBlock();

    await screen.findByRole("option", { name: "GitHub — octocat" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Account to authorize" }), githubConnection.id);
    await user.click(screen.getByRole("button", { name: "Authorize account" }));

    const readTool = screen.getByRole("checkbox", { name: /Read repository contents/ });
    const writeTool = screen.getByRole("checkbox", { name: /Create issues/ });
    expect(readTool).toBeChecked();
    expect(writeTool).not.toBeChecked();
    expect(screen.getByLabelText("Grant validity (days)")).toHaveValue(30);

    await user.click(writeTool);
    fireEvent.change(screen.getByLabelText("GitHub repository IDs"), { target: { value: "123, 456" } });
    fireEvent.change(screen.getByLabelText("Pull request base branches"), { target: { value: "main, release" } });
    await user.click(screen.getByRole("button", { name: "Authorize agent" }));

    await waitFor(() => expect(api.createToolGrant).toHaveBeenCalledTimes(1));
    expect(api.createToolGrant).toHaveBeenCalledWith("agent-a", expect.objectContaining({
      connectionId: "connection-github",
      tools: ["github_contents_get", "github_issues_create"],
      constraints: {
        subscriptionIds: [],
        repositoryIds: ["123", "456"],
        baseBranches: ["main", "release"],
        calendarIds: [],
        recipientAddresses: [],
        recipientDomains: [],
        allowInvites: false,
      },
    }));
    const grantInput = api.createToolGrant.mock.calls[0][1] as { expiresAt: string };
    expect(Date.parse(grantInput.expiresAt) - Date.now()).toBeGreaterThan(29 * 86_400_000);
    expect(Date.parse(grantInput.expiresAt) - Date.now()).toBeLessThan(31 * 86_400_000);
  });

  it("updates scoped grants and revokes them with their current revision", async () => {
    const grant: ToolGrant = {
      id: "grant-1",
      instanceUid: "agent-uid",
      connectionId: githubConnection.id,
      tools: ["github_contents_get"],
      constraints: {
        subscriptionIds: [],
        repositoryIds: ["123"],
        baseBranches: ["main"],
        calendarIds: [],
        recipientAddresses: [],
        recipientDomains: ["example.com"],
        allowInvites: false,
      },
      expiresAt: "2026-11-09T00:00:00Z",
      revision: 7,
    };
    api.listConnections.mockResolvedValue([githubConnection]);
    api.listToolGrants.mockResolvedValue([grant]);
    const user = userEvent.setup();
    renderBlock();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Allowed recipient domains"), { target: { value: "example.com, contoso.com" } });
    await user.click(screen.getByRole("button", { name: "Save authorization" }));

    await waitFor(() => expect(api.updateToolGrant).toHaveBeenCalledTimes(1));
    expect(api.updateToolGrant).toHaveBeenCalledWith("agent-a", "grant-1", expect.objectContaining({
      connectionId: "connection-github",
      revision: 7,
      tools: ["github_contents_get"],
      constraints: expect.objectContaining({
        repositoryIds: ["123"],
        baseBranches: ["main"],
        recipientDomains: ["example.com", "contoso.com"],
      }),
    }));

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(api.deleteToolGrant).toHaveBeenCalledWith("agent-a", "grant-1", 7));
  });

  it("shows loading while access is checked and reports owner-gated errors", async () => {
    let rejectConnections!: (error: Error) => void;
    api.listConnections.mockImplementationOnce(() => new Promise<Connection[]>((_, reject) => { rejectConnections = reject; }));
    renderBlock();

    expect(screen.getByLabelText("Loading tool access...")).toBeInTheDocument();
    rejectConnections(new Error("Only the agent owner can manage tool access."));

    expect(await screen.findByText("Only the agent owner can manage tool access.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading tool access...")).toBeNull();
  });

  it("hides access controls when the runtime does not support MCP", async () => {
    api.listToolGrants.mockRejectedValue(new ToolAuthorizationError("Agent Tools MCP is not available to Sandbox Agents", "unsupported_sandbox"));
    renderBlock();

    await waitFor(() => expect(screen.queryByText("Connections & agent tool access")).toBeNull());
  });

  it("shows ordinary grant errors", async () => {
    api.listToolGrants.mockRejectedValue(new Error("The grants service is unavailable."));
    renderBlock();

    expect(await screen.findByText("The grants service is unavailable.")).toBeInTheDocument();
  });
});
