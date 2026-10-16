// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { type Connection, type ToolGrant } from "../../api/toolAuthorization";
import type { Instance } from "../../types/instance";
import { ToolAuthorizationBlock } from "../ToolAuthorizationBlock";

const api = vi.hoisted(() => ({
  listConnections: vi.fn(),
  listToolGrants: vi.fn(),
  startConnection: vi.fn(),
  reconnectConnection: vi.fn(),
  deleteConnection: vi.fn(),
  createToolIdentity: vi.fn(),
  createToolGrant: vi.fn(),
  updateToolGrant: vi.fn(),
  deleteToolGrant: vi.fn(),
  verifyToolGrant: vi.fn(),
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
    api.listToolGrants.mockResolvedValue({ grants: [], readOnly: false });
    api.startConnection.mockResolvedValue({ authorizationUrl: "https://login.example.com", connectionId: "new-connection", expiresAt: "2026-10-10T00:10:00Z" });
    api.reconnectConnection.mockResolvedValue({ authorizationUrl: "https://login.example.com", connectionId: "connection-github", expiresAt: "2026-10-10T00:10:00Z" });
    api.deleteConnection.mockResolvedValue(undefined);
    api.createToolIdentity.mockResolvedValue({});
    api.createToolGrant.mockResolvedValue({});
    api.updateToolGrant.mockResolvedValue({});
    api.deleteToolGrant.mockResolvedValue(undefined);
    api.verifyToolGrant.mockResolvedValue({});
  });

  it("defaults to an Azure Agent identity and makes personal accounts explicit", async () => {
    const user = userEvent.setup();
    renderBlock();

    expect(screen.getByRole("combobox", { name: "Authorization mode" })).toHaveValue("agent");
    expect(within(screen.getByRole("combobox", { name: "Authorization mode" })).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Agent identity (Azure)", "Personal delegated account",
    ]);
    expect(screen.getByRole("button", { name: "Set up Azure Agent identity" })).toBeInTheDocument();
    await user.selectOptions(screen.getByRole("combobox", { name: "Authorization mode" }), "delegated");
    expect(within(screen.getByRole("combobox", { name: "Add account" })).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "Azure", "Microsoft 365", "GitHub", "Google",
    ]);
  });

  it("creates a scoped grant with read tools by default and writes enabled explicitly", async () => {
    api.listConnections.mockResolvedValue([githubConnection]);
    const user = userEvent.setup();
    renderBlock();

    await user.selectOptions(screen.getByRole("combobox", { name: "Authorization mode" }), "delegated");
    await screen.findByRole("option", { name: "GitHub — octocat" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Account to authorize" }), githubConnection.id);
    await user.click(screen.getByRole("button", { name: "Authorize account" }));

    const readTool = screen.getByRole("checkbox", { name: /Read repository contents/ });
    const writeTool = screen.getByRole("checkbox", { name: /Create issues/ });
    expect(readTool).toBeChecked();
    expect(writeTool).not.toBeChecked();
    expect(screen.getByLabelText("Grant validity (days)")).toHaveValue(30);
    expect(screen.getByText(/GitHub calls run as the connected personal account/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Azure subscription IDs")).toBeNull();
    expect(screen.queryByLabelText("Calendar IDs")).toBeNull();
    expect(screen.queryByLabelText("Allowed recipient addresses")).toBeNull();

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
        resourceGroupNames: [],
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
    api.listToolGrants.mockResolvedValue({ grants: [grant], readOnly: false });
    const user = userEvent.setup();
    renderBlock();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Pull request base branches"), { target: { value: "main, release" } });
    await user.click(screen.getByRole("button", { name: "Save authorization" }));

    await waitFor(() => expect(api.updateToolGrant).toHaveBeenCalledTimes(1));
    expect(api.updateToolGrant).toHaveBeenCalledWith("agent-a", "grant-1", expect.objectContaining({
      connectionId: "connection-github",
      revision: 7,
      tools: ["github_contents_get"],
      constraints: expect.objectContaining({
        resourceGroupNames: [],
        repositoryIds: ["123"],
        baseBranches: ["main", "release"],
      }),
    }));

    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(api.deleteToolGrant).toHaveBeenCalledWith("agent-a", "grant-1", 7));
  });

  it("requires exact resource group names for opt-in Azure writes", async () => {
    const azureIdentity: Connection = {
      id: "identity-azure-writes",
      provider: "azure",
      accountDisplay: "Agent A",
      capabilities: ["azure_subscriptions_list", "azure_resource_groups_list", "azure_resources_search", "azure_resource_show", "azure_resource_groups_create", "azure_resource_group_tags_update"],
      revision: 1,
      status: "ready",
      createdAt: "2026-10-10T00:00:00Z",
      mode: "agent",
      identity: { instanceUid: "agent-uid", tenantId: "tenant-1", principalId: "principal-1", clientId: "client-1" },
    };
    api.listConnections.mockResolvedValue([azureIdentity]);
    const user = userEvent.setup();
    renderBlock();

    await screen.findByRole("option", { name: "Azure — Agent A" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Agent identity to authorize" }), azureIdentity.id);
    await user.click(screen.getByRole("button", { name: "Create pending grant" }));

    const createGroups = screen.getByRole("checkbox", { name: "Create resource groups" });
    const mergeTags = screen.getByRole("checkbox", { name: "Merge resource group tags" });
    expect(createGroups).not.toBeChecked();
    expect(mergeTags).not.toBeChecked();
    expect(screen.queryByLabelText("Writable resource group names")).toBeNull();

    await user.click(mergeTags);
    expect(screen.getByLabelText("Writable resource group names")).toBeRequired();
    await user.click(screen.getByRole("button", { name: "Authorize agent" }));
    expect(screen.getByText("Writable resource group names are required when Azure write tools are selected.")).toBeInTheDocument();
    expect(api.createToolGrant).not.toHaveBeenCalled();

    for (const name of [".audit-rg", "audit..rg", "audit-rg."]) {
      fireEvent.change(screen.getByLabelText("Writable resource group names"), { target: { value: name } });
      await user.click(screen.getByRole("button", { name: "Authorize agent" }));
      expect(screen.getByText("Writable resource group names must start with a letter or number, use only Azure resource group name characters, contain no '..', and not end with '.'.")).toBeInTheDocument();
      expect(api.createToolGrant).not.toHaveBeenCalled();
    }

    fireEvent.change(screen.getByLabelText("Writable resource group names"), { target: { value: "audit-rg" } });
    await user.click(screen.getByRole("button", { name: "Authorize agent" }));

    await waitFor(() => expect(api.createToolGrant).toHaveBeenCalledWith("agent-a", expect.objectContaining({
      connectionId: azureIdentity.id,
      tools: ["azure_subscriptions_list", "azure_resource_groups_list", "azure_resources_search", "azure_resource_show", "azure_resource_group_tags_update"],
      constraints: expect.objectContaining({ resourceGroupNames: ["audit-rg"] }),
    })));
  });

  it("does not offer Azure write tools to delegated connections", async () => {
    const delegatedAzure: Connection = {
      id: "connection-azure",
      provider: "azure",
      accountDisplay: "owner@example.com",
      capabilities: ["azure_subscriptions_list", "azure_resource_groups_list", "azure_resources_search", "azure_resource_show", "azure_resource_groups_create", "azure_resource_group_tags_update"],
      revision: 1,
      status: "connected",
      createdAt: "2026-10-10T00:00:00Z",
      mode: "delegated",
    };
    api.listConnections.mockResolvedValue([delegatedAzure]);
    const user = userEvent.setup();
    renderBlock();

    await user.selectOptions(screen.getByRole("combobox", { name: "Authorization mode" }), "delegated");
    await screen.findByRole("option", { name: "Azure — owner@example.com" });
    await user.selectOptions(screen.getByRole("combobox", { name: "Account to authorize" }), delegatedAzure.id);
    await user.click(screen.getByRole("button", { name: "Authorize account" }));

    expect(screen.queryByRole("checkbox", { name: "Create resource groups" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "Merge resource group tags" })).toBeNull();
  });

  it("creates an Azure identity binding and renders pending RBAC evidence", async () => {
    const azureIdentity: Connection = {
      id: "identity-azure",
      provider: "azure",
      accountDisplay: "Agent A",
      capabilities: ["azure_subscriptions_list"],
      revision: 1,
      status: "ready",
      createdAt: "2026-10-10T00:00:00Z",
      mode: "agent",
      identity: { instanceUid: "agent-uid", tenantId: "tenant-1", principalId: "principal-1", clientId: "client-1" },
    };
    const grant: ToolGrant = {
      id: "grant-azure",
      instanceUid: "agent-uid",
      connectionId: azureIdentity.id,
      tools: ["azure_subscriptions_list"],
      constraints: { subscriptionIds: ["sub-1"], repositoryIds: [], baseBranches: [], calendarIds: [], recipientAddresses: [], recipientDomains: [], allowInvites: false },
      expiresAt: "2026-11-09T00:00:00Z",
      revision: 4,
      authorization: { state: "pending", source: "direct_rbac", reason: "Reader role not found" },
    };
    api.listConnections.mockResolvedValue([azureIdentity]);
    api.listToolGrants.mockResolvedValue({ grants: [grant], readOnly: false });
    const user = userEvent.setup();
    renderBlock();

    expect(await screen.findByText("Agent principal ID: principal-1")).toBeInTheDocument();
    expect(screen.getByText(/Resource owners grant Azure RBAC directly/)).toBeInTheDocument();
    expect(screen.getByText(/pending Azure RBAC verification/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Verify Azure RBAC" }));
    await waitFor(() => expect(api.verifyToolGrant).toHaveBeenCalledWith("agent-a", "grant-azure", 4));
  });

  it("makes legacy non-Azure Agent identity grants revoke-only", async () => {
    const legacyGitHubIdentity: Connection = {
      id: "identity-github-legacy",
      provider: "github",
      accountDisplay: "Legacy GitHub identity",
      capabilities: ["github_contents_get"],
      revision: 1,
      status: "ready",
      createdAt: "2026-10-10T00:00:00Z",
      mode: "agent",
      identity: { instanceUid: "agent-uid", tenantId: "tenant-1", principalId: "principal-1", clientId: "client-1" },
    };
    const grant: ToolGrant = {
      id: "grant-github-legacy",
      instanceUid: "agent-uid",
      connectionId: legacyGitHubIdentity.id,
      tools: ["github_contents_get"],
      constraints: { subscriptionIds: [], repositoryIds: ["123"], baseBranches: [], calendarIds: [], recipientAddresses: [], recipientDomains: [], allowInvites: false },
      expiresAt: "2026-11-09T00:00:00Z",
      revision: 5,
      authorization: { state: "pending", source: "direct_rbac" },
    };
    api.listConnections.mockResolvedValue([legacyGitHubIdentity]);
    api.listToolGrants.mockResolvedValue({ grants: [grant], readOnly: false });
    const user = userEvent.setup();
    renderBlock();

    expect(await screen.findByText(/legacy independent identity grant is no longer supported/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Verify Azure RBAC" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(api.deleteToolGrant).toHaveBeenCalledWith("agent-a", "grant-github-legacy", 5));
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

  it("shows historical grants only for revocation after a runtime migration", async () => {
    const grant: ToolGrant = {
      id: "grant-history",
      instanceUid: "agent-uid",
      connectionId: githubConnection.id,
      tools: ["github_contents_get"],
      constraints: { subscriptionIds: [], repositoryIds: [], baseBranches: [], calendarIds: [], recipientAddresses: [], recipientDomains: [], allowInvites: false },
      expiresAt: "2026-11-09T00:00:00Z",
      revision: 9,
    };
    api.listToolGrants.mockResolvedValue({ grants: [grant], readOnly: true });
    const user = userEvent.setup();
    renderBlock();

    expect(await screen.findByText("Historical agent tool grants")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Set up Azure Agent identity" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(api.deleteToolGrant).toHaveBeenCalledWith("agent-a", "grant-history", 9));
  });

  it("shows ordinary grant errors", async () => {
    api.listToolGrants.mockRejectedValue(new Error("The grants service is unavailable."));
    renderBlock();

    expect(await screen.findByText("The grants service is unavailable.")).toBeInTheDocument();
  });
});
