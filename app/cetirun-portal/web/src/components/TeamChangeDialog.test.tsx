// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TeamChangeDialog } from "./TeamChangeDialog";
import { PortalError } from "../api/fetch";

const api = vi.hoisted(() => ({ getTeamChangeByName: vi.fn(), requestTeamChangeByName: vi.fn() }));
vi.mock("../api/instance", async (importOriginal) => ({
  ...await importOriginal<typeof import("../api/instance")>(),
  ...api,
}));
vi.mock("../hooks/useTeams", () => ({ useTeams: () => ({ data: [{ name: "restricted", role: "" }], isLoading: false }) }));
vi.mock("../hooks/useCreateOptions", () => ({
  useCreateOptions: (team: string, enabled: boolean) => ({
    data: enabled ? {
      team, allowedSandboxes: ["restricted-sandbox"], defaultSandbox: "restricted-sandbox",
      allowedAgentTypes: ["hermes"], aiGatewayEnabled: true,
      gateway: { known: true, sharedModels: [{ name: "gpt-6-mini" }], githubCopilot: true, agentTypes: ["hermes"] },
      agentIdentity: { allowed: false, required: false }, modelRouters: [],
    } : undefined,
    isFetching: false,
  }),
}));

const operation = {
  uid: "agent-uid", resourceVersion: "7", team: "source", sandbox: "source-sandbox", suspended: false, pending: false,
};

function renderDialog() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}><TeamChangeDialog instanceName="agent-a" open onOpenChange={() => {}} /></QueryClientProvider>);
}

describe("TeamChangeDialog", () => {
  it("allows a new change after the accepted request is cleared into completed status", async () => {
    api.getTeamChangeByName.mockResolvedValue(operation);
    api.requestTeamChangeByName.mockImplementationOnce(async (_name, _request, requestID) => {
      const completed = {
        ...operation, resourceVersion: "15", team: "restricted",
        status: { requestID, phase: "Completed" },
      };
      api.getTeamChangeByName.mockResolvedValue(completed);
      return completed;
    });
    const user = userEvent.setup();
    renderDialog();
    const select = await screen.findByRole("combobox", { name: "Target Team" });
    await user.selectOptions(select, "restricted");
    await user.click(screen.getByRole("button", { name: "Change Team" }));
    await screen.findByText("Team change completed.");
    await waitFor(() => expect(select).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Change Team" }));
    await waitFor(() => expect(api.requestTeamChangeByName).toHaveBeenCalledTimes(2));
    const [first, second] = api.requestTeamChangeByName.mock.calls;
    expect(second[2]).not.toBe(first[2]);
    expect(second[1].resourceVersion).toBe("15");
  });

  it("refreshes a rejected stale version before issuing a new request", async () => {
    api.getTeamChangeByName.mockResolvedValue(operation);
    api.requestTeamChangeByName.mockImplementationOnce(async () => {
      api.getTeamChangeByName.mockResolvedValue({ ...operation, resourceVersion: "9" });
      throw new PortalError("Agent changed", 409, "instance_changed");
    });
    const user = userEvent.setup();
    renderDialog();
    const select = await screen.findByRole("combobox", { name: "Target Team" });
    await user.selectOptions(select, "restricted");
    await user.click(screen.getByRole("button", { name: "Change Team" }));
    await screen.findByText(/Review the current selection/);
    await waitFor(() => expect(screen.getByRole("button", { name: "Change Team" })).toBeEnabled());
    expect(select).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Change Team" }));
    await waitFor(() => expect(api.requestTeamChangeByName).toHaveBeenCalledTimes(2));
    const [first, second] = api.requestTeamChangeByName.mock.calls;
    expect(second[2]).not.toBe(first[2]);
    expect(second[1].resourceVersion).toBe("9");
  });

  it("retries a lost response with the same immutable request", async () => {
    api.getTeamChangeByName.mockResolvedValue(operation);
    api.requestTeamChangeByName.mockRejectedValueOnce(new Error("response lost"));
    const user = userEvent.setup();
    renderDialog();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Target Team" }), "restricted");
    await user.click(screen.getByRole("button", { name: "Change Team" }));
    await screen.findByText(/response lost/);
    const first = api.requestTeamChangeByName.mock.calls[0];
    expect(first[1]).toEqual({
      uid: "agent-uid", resourceVersion: "7", targetTeam: "restricted", targetSandbox: "restricted-sandbox",
      model: undefined, modelAccess: { sharedModels: true, githubCopilot: true },
    });
    await user.click(screen.getByRole("button", { name: "Retry request" }));
    await waitFor(() => expect(api.requestTeamChangeByName).toHaveBeenCalledTimes(2));
    expect(api.requestTeamChangeByName.mock.calls[1]).toEqual(first);
  });

  it("sends selected target model sources and accurately labels an empty model", async () => {
    api.getTeamChangeByName.mockResolvedValue(operation);
    api.requestTeamChangeByName.mockResolvedValue({ ...operation, pending: true });
    const user = userEvent.setup();
    renderDialog();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Target Team" }), "restricted");
    expect(screen.getByRole("option", { name: "Keep current model" })).toBeInTheDocument();
    await user.click(screen.getByRole("switch", { name: "Use GitHub Copilot" }));
    await user.selectOptions(screen.getByRole("combobox", { name: "Model (optional)" }), "gpt-6-mini");
    await user.click(screen.getByRole("button", { name: "Change Team" }));
    await waitFor(() => expect(api.requestTeamChangeByName).toHaveBeenCalledTimes(1));
    expect(api.requestTeamChangeByName.mock.calls[0][1]).toEqual(expect.objectContaining({
      model: "gpt-6-mini", modelAccess: { sharedModels: true, githubCopilot: false },
    }));
  });
});
