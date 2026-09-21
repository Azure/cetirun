// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CreateForm } from "../CreateForm";

const mockFeatures = vi.hoisted(() => ({
  data: { tailscaleEnabled: true, allowedAgentTypes: ["openclaw", "hermes"] },
}));

vi.mock("../../hooks/useFeatures", () => ({
  useFeatures: () => ({ data: mockFeatures.data, isLoading: false }),
}));

const mockMutate = vi.fn();
vi.mock("../../hooks/useInstance", () => ({
  useCreateInstance: () => ({
    mutate: mockMutate,
    isPending: false,
  }),
}));

const mockTeams = vi.hoisted(() => ({
  data: [
    { name: "platform", role: "", provider: "github_copilot" },
    { name: "security", role: "", provider: "anthropic" },
  ],
}));

vi.mock("../../hooks/useTeams", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../hooks/useTeams")>();
  return {
    ...actual,
    useTeams: () => ({ data: mockTeams.data, isLoading: false }),
    useEffectiveTeamProvider: (teamName: string) =>
      mockTeams.data.find((team) => team.name === teamName)?.provider || "github_copilot",
  };
});

function renderWithQuery(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe("CreateForm", () => {
  beforeEach(() => {
    mockMutate.mockClear();
    mockFeatures.data = {
      tailscaleEnabled: true,
      allowedAgentTypes: ["openclaw", "hermes"],
    };
  });

  it("renders form fields", () => {
    renderWithQuery(<CreateForm />);

    expect(screen.getByText("Display Name (optional)")).toBeInTheDocument();
    expect(screen.getByText("Team (optional)")).toBeInTheDocument();
    expect(screen.getByText("Gateway Token (optional)")).toBeInTheDocument();
    expect(screen.getByText("Tailscale Auth Key (optional)")).toBeInTheDocument();
    expect(screen.getByText("Create Agent")).toBeInTheDocument();
  });

  it("submits without team (optional)", async () => {
    const user = userEvent.setup();
    renderWithQuery(<CreateForm />);

    await user.type(
      screen.getByPlaceholderText("e.g. my-project (leave blank for default)"),
      "my-instance",
    );
    await user.click(screen.getByText("Create Agent"));

    expect(mockMutate).toHaveBeenCalledWith({
      instanceName: "my-instance",
      agentType: "openclaw",
      displayName: undefined,
      team: undefined,
      gatewayToken: undefined,
      tailscaleAuthKey: undefined,
      teamsEnabled: undefined,
    });
  });

  it("submits with selected team", async () => {
    const user = userEvent.setup();
    renderWithQuery(<CreateForm />);

    await user.type(
      screen.getByPlaceholderText("e.g. my-project (leave blank for default)"),
      "my-instance",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: /Team/ }),
      "platform",
    );
    await user.click(screen.getByText("Create Agent"));

    expect(mockMutate).toHaveBeenCalledWith({
      instanceName: "my-instance",
      agentType: "openclaw",
      displayName: undefined,
      team: "platform",
      gatewayToken: undefined,
      tailscaleAuthKey: undefined,
      teamsEnabled: undefined,
    });
  });

  it("submits with all optional fields filled in", async () => {
    const user = userEvent.setup();
    renderWithQuery(<CreateForm />);

    await user.type(
      screen.getByPlaceholderText("e.g. my-project (leave blank for default)"),
      "my-instance",
    );
    await user.type(
      screen.getByPlaceholderText("Defaults to your profile name"),
      "Custom Name",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: /Team/ }),
      "security",
    );
    await user.type(
      screen.getByPlaceholderText("Leave empty to auto-generate"),
      "my-gw-token",
    );
    await user.type(
      screen.getByPlaceholderText("Leave empty to skip Tailscale"),
      "tskey-auth-xxx",
    );

    await user.click(screen.getByText("Create Agent"));

    expect(mockMutate).toHaveBeenCalledWith({
      instanceName: "my-instance",
      agentType: "openclaw",
      displayName: "Custom Name",
      team: "security",
      gatewayToken: "my-gw-token",
      tailscaleAuthKey: "tskey-auth-xxx",
      teamsEnabled: undefined,
    });
  });

  it("submits with selected agent type", async () => {
    const user = userEvent.setup();
    renderWithQuery(<CreateForm />);

    await user.selectOptions(
      screen.getByRole("combobox", { name: /Agent Type/ }),
      "hermes",
    );
    await user.click(screen.getByText("Create Agent"));

    expect(mockMutate).toHaveBeenCalledWith({
      instanceName: undefined,
      agentType: "hermes",
      displayName: undefined,
      team: undefined,
      gatewayToken: undefined,
      tailscaleAuthKey: undefined,
      teamsEnabled: undefined,
    });
  });

  it("uses the only allowed agent type", async () => {
    const user = userEvent.setup();
    mockFeatures.data = { tailscaleEnabled: true, allowedAgentTypes: ["hermes"] };
    renderWithQuery(<CreateForm />);

    const agentTypeSelect = screen.getByRole("combobox", { name: /Agent Type/ });
    await waitFor(() => expect(agentTypeSelect).toHaveValue("hermes"));
    expect(agentTypeSelect).toBeDisabled();
    await user.click(screen.getByText("Create Agent"));

    expect(mockMutate).toHaveBeenCalledWith({
      instanceName: undefined,
      agentType: "hermes",
      displayName: undefined,
      team: undefined,
      gatewayToken: undefined,
      tailscaleAuthKey: undefined,
      teamsEnabled: undefined,
    });
  });

  it("submit button is enabled even without team selected", () => {
    renderWithQuery(<CreateForm />);
    expect(screen.getByText("Create Agent")).not.toBeDisabled();
  });

  it("only shows Copilot model selection for the effective Copilot provider", async () => {
    const user = userEvent.setup();
    renderWithQuery(<CreateForm />);

    expect(screen.getByText("GitHub Copilot model")).toBeInTheDocument();
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "security");
    await waitFor(() => expect(screen.queryByText("GitHub Copilot model")).not.toBeInTheDocument());

    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    await waitFor(() => expect(screen.getByText("GitHub Copilot model")).toBeInTheDocument());
  });
});
