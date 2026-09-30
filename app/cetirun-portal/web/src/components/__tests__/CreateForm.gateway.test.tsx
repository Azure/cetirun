// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { CreateForm } from "../CreateForm";

const mockFeatures = vi.hoisted(() => ({
  data: { tailscaleEnabled: false, aiGatewayEnabled: true, allowedAgentTypes: ["openclaw", "hermes"] },
}));
vi.mock("../../hooks/useFeatures", () => ({
  useFeatures: () => ({ data: mockFeatures.data, isLoading: false }),
}));

const mockMutate = vi.fn();
vi.mock("../../hooks/useInstance", () => ({
  useCreateInstance: () => ({ mutate: mockMutate, isPending: false }),
}));

vi.mock("../../hooks/useTeams", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../hooks/useTeams")>();
  return {
    ...actual,
    useTeams: () => ({ data: [{ name: "platform", role: "", provider: "" }], isLoading: false }),
    useEffectiveTeamProvider: () => "github_copilot",
  };
});

const mockCatalog = vi.hoisted(() => ({
  data: { known: true, sharedModels: [{ name: "gpt-6" }, { name: "gpt-6-mini" }] } as {
    known: boolean;
    sharedModels: { name: string }[];
  },
}));
vi.mock("../../hooks/useAIGateway", () => ({
  useAIGatewayCatalog: (enabled: boolean) => ({ data: enabled ? mockCatalog.data : undefined, isLoading: false }),
}));

function renderForm() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <CreateForm />
    </QueryClientProvider>,
  );
}

async function chooseOpenClawAndTeam(user: ReturnType<typeof userEvent.setup>) {
  await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), "openclaw");
  await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
}

describe("CreateForm with the AI Gateway", () => {
  beforeEach(() => {
    mockMutate.mockClear();
    mockCatalog.data = { known: true, sharedModels: [{ name: "gpt-6" }, { name: "gpt-6-mini" }] };
  });

  it("requires a Team before an agent can be created", async () => {
    const user = userEvent.setup();
    renderForm();
    expect(screen.getByText("Team")).toBeInTheDocument();
    expect(screen.queryByText("Team (optional)")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeEnabled();
  });

  it("sends both model sources on by default for OpenClaw", async () => {
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    expect(screen.getByRole("switch", { name: "Use shared models" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Use GitHub Copilot" })).toBeChecked();
    expect(screen.getByText(/gpt-6, gpt-6-mini/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({
        agent: { type: "openclaw" },
        team: "platform",
        model: undefined,
        modelAccess: { sharedModels: true, githubCopilot: true },
      }),
    );
  });

  it("keeps GitHub Copilot on when shared models are turned off", async () => {
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    await user.click(screen.getByRole("switch", { name: "Use GitHub Copilot" }));
    expect(screen.getByRole("switch", { name: "Use GitHub Copilot" })).not.toBeChecked();
    await user.click(screen.getByRole("switch", { name: "Use shared models" }));
    const copilot = screen.getByRole("switch", { name: "Use GitHub Copilot" });
    expect(copilot).toBeChecked();
    expect(copilot).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({ modelAccess: { sharedModels: false, githubCopilot: true } }),
    );
  });

  it("requires GitHub Copilot when no shared model is available", async () => {
    mockCatalog.data = { known: true, sharedModels: [] };
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    const copilot = screen.getByRole("switch", { name: "Use GitHub Copilot" });
    expect(copilot).toBeChecked();
    expect(copilot).toBeDisabled();
    expect(screen.getByText("No shared models are available.")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: /Default model/ })).not.toBeInTheDocument();
  });

  it("sends Copilot off and a default shared model", async () => {
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    await user.click(screen.getByRole("switch", { name: "Use GitHub Copilot" }));
    await user.selectOptions(screen.getByRole("combobox", { name: /Default model/ }), "gpt-6-mini");
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({ model: "gpt-6-mini", modelAccess: { sharedModels: true, githubCopilot: false } }),
    );
  });

  it("offers no model switches for Hermes", async () => {
    const user = userEvent.setup();
    renderForm();
    await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), "hermes");
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    expect(screen.queryByRole("switch", { name: "Use shared models" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(expect.objectContaining({ agent: { type: "hermes" }, modelAccess: undefined }));
  });

  it("offers no model switches for Custom but still requires a Team", async () => {
    mockFeatures.data = { ...mockFeatures.data, allowedAgentTypes: ["openclaw", "hermes", "custom"] };
    try {
      const user = userEvent.setup();
      renderForm();
      await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), "custom");
      await user.type(screen.getByRole("textbox", { name: /Agent Image/ }), "registry.example.com/team/agent:1.0");
      expect(screen.queryByRole("switch", { name: "Use shared models" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
      await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
      await user.click(screen.getByRole("button", { name: "Create Agent" }));
      expect(mockMutate).toHaveBeenCalledWith(
        expect.objectContaining({
          agent: { type: "custom", image: "registry.example.com/team/agent:1.0", username: "vibe" },
          team: "platform",
          model: undefined,
          modelAccess: undefined,
        }),
      );
    } finally {
      mockFeatures.data = { ...mockFeatures.data, allowedAgentTypes: ["openclaw", "hermes"] };
    }
  });
});
