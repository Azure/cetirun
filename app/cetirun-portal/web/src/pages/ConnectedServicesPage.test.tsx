// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Connection } from "../api/toolAuthorization";
import { ConnectedServicesPage } from "./ConnectedServicesPage";

const features = vi.hoisted(() => ({ data: { agentToolsEnabled: true } as { agentToolsEnabled: boolean } | undefined, isLoading: false }));
const api = vi.hoisted(() => ({ listConnections: vi.fn(), getConnectionUsage: vi.fn(), deleteConnection: vi.fn() }));

vi.mock("../hooks/useFeatures", () => ({ useFeatures: () => features }));
vi.mock("../api/toolAuthorization", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/toolAuthorization")>()),
  ...api,
}));

const personalGitHub: Connection = {
  id: "github-personal",
  provider: "github",
  accountDisplay: "octocat",
  services: ["github"],
  scopes: ["repo"],
  revision: 1,
  status: "connected",
  createdAt: "2026-10-26T00:00:00Z",
  mode: "delegated",
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><MemoryRouter><ConnectedServicesPage /></MemoryRouter></QueryClientProvider>);
}

describe("ConnectedServicesPage", () => {
  beforeEach(() => {
    vi.spyOn(document.body, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 1024, 768));
    vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) { return this.parentElement; });
    features.data = { agentToolsEnabled: true };
    features.isLoading = false;
    api.listConnections.mockReset().mockResolvedValue([personalGitHub]);
    api.getConnectionUsage.mockReset().mockResolvedValue({ connectionId: personalGitHub.id, agents: [], canRemove: true });
    api.deleteConnection.mockReset().mockResolvedValue(undefined);
  });
  afterEach(() => vi.restoreAllMocks());

  it("fails closed when service access is disabled", () => {
    features.data = { agentToolsEnabled: false };
    renderPage();
    expect(screen.getByText("403 Forbidden")).toBeInTheDocument();
    expect(api.listConnections).not.toHaveBeenCalled();
  });

  it("groups personal and GitHub App access while keeping legacy aliases visible", async () => {
    const appConnection: Connection = {
      ...personalGitHub,
      id: "github-app",
      mode: "installation",
      accountDisplay: "cetirun[bot]",
      github: { installationId: 7, accountLogin: "octo-org", appSlug: "cetirun", userLogin: "octocat" },
    };
    const legacyAlias: Connection = { ...personalGitHub, id: "legacy-github", canonicalConnectionId: personalGitHub.id, accountDisplay: "legacy-octocat" };
    api.listConnections.mockResolvedValue([personalGitHub, appConnection, legacyAlias]);
    api.getConnectionUsage.mockImplementation((id: string) => Promise.resolve({ connectionId: id, agents: id === appConnection.id ? [{ instanceUid: "agent-uid", instanceName: "agent-a", instanceNamespace: "default" }] : [], canRemove: id !== appConnection.id }));
    renderPage();

    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    expect(screen.getByText("Selected repositories")).toBeInTheDocument();
    expect(screen.getByText("GitHub actions run as cetirun[bot]")).toBeInTheDocument();
    expect(screen.getByText("legacy-octocat")).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "agent-a" })).toHaveAttribute("href", "/instances/agent-a/overview#service-access");
  });

  it("removes only an unused connection after confirmation and preserves provider permissions", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText("octocat");
    await user.click(screen.getByRole("button", { name: "Remove connection" }));
    expect(screen.getByText("Removing this connection does not remove the provider grant or its permissions.")).toBeInTheDocument();
    expect(api.deleteConnection).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Remove connection permanently" }));
    await waitFor(() => expect(api.deleteConnection).toHaveBeenCalledWith(personalGitHub.id));
  });

  it("blocks removal when usage cannot be checked", async () => {
    api.getConnectionUsage.mockRejectedValue(new Error("unavailable"));
    renderPage();
    await screen.findByText("Could not check which Agents use this connection.");
    expect(screen.getByRole("button", { name: "Remove connection" })).toBeDisabled();
  });

  it("announces the removal dialog, focuses Cancel, and restores keyboard focus", async () => {
    const user = userEvent.setup();
    renderPage();
    const trigger = await screen.findByRole("button", { name: "Remove connection" });
    await waitFor(() => expect(trigger).toBeEnabled());
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("alertdialog", { name: "Remove octocat from CetiRun?" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus());
    await user.keyboard("{Enter}");
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(api.deleteConnection).not.toHaveBeenCalled();
  });
});
