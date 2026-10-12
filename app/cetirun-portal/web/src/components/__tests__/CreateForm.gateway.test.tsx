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

const mockTeams = vi.hoisted(() => ({
  data: [{
    name: "platform",
    role: "",
    provider: "",
    policy: {
      name: "standard",
      harnesses: ["openclaw", "hermes"],
      sharedModels: ["gpt-6", "gpt-6-mini"],
      githubCopilot: true,
    },
  }],
}));

vi.mock("../../hooks/useTeams", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../hooks/useTeams")>();
  return {
    ...actual,
    useTeams: () => ({ data: mockTeams.data, isLoading: false }),
    useEffectiveTeamProvider: () => "github_copilot",
  };
});

const mockCatalog = vi.hoisted(() => ({
  data: {
    known: true,
    sharedModels: [{ name: "gpt-6" }, { name: "gpt-6-mini" }],
    githubCopilot: true,
    agentTypes: ["openclaw", "hermes"],
  } as {
    known: boolean;
    sharedModels: { name: string }[];
    githubCopilot: boolean;
    agentTypes?: string[] | null;
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
    mockCatalog.data = {
      known: true,
      sharedModels: [{ name: "gpt-6" }, { name: "gpt-6-mini" }],
      githubCopilot: true,
      agentTypes: ["openclaw", "hermes"],
    };
    mockTeams.data[0].policy = {
      name: "standard",
      harnesses: ["openclaw", "hermes"],
      sharedModels: ["gpt-6", "gpt-6-mini"],
      githubCopilot: true,
    };
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

  it("prefers Team shared models and keeps Copilot off by default", async () => {
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    expect(screen.getByRole("switch", { name: "Use shared models" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "Use GitHub Copilot" })).not.toBeChecked();
    expect(screen.getByText(/gpt-6, gpt-6-mini/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({
        agent: { type: "openclaw" },
        team: "platform",
        model: undefined,
        modelAccess: { sharedModels: true, githubCopilot: false },
      }),
    );
  });

  it("keeps GitHub Copilot on when shared models are turned off", async () => {
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
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
    mockCatalog.data = { known: true, sharedModels: [], githubCopilot: true };
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    const copilot = screen.getByRole("switch", { name: "Use GitHub Copilot" });
    expect(copilot).toBeChecked();
    expect(copilot).toBeDisabled();
    expect(screen.getByText("This Team has no currently available shared models.")).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: /Default model/ })).not.toBeInTheDocument();
  });

  it("keeps shared models on when the deployment offers no GitHub Copilot", async () => {
    mockCatalog.data = { known: true, sharedModels: [{ name: "gpt-6" }], githubCopilot: false };
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    const copilot = screen.getByRole("switch", { name: "Use GitHub Copilot" });
    expect(copilot).not.toBeChecked();
    expect(copilot).toBeDisabled();
    expect(screen.getByText("GitHub Copilot is turned off for this deployment.")).toBeInTheDocument();
    const shared = screen.getByRole("switch", { name: "Use shared models" });
    expect(shared).toBeChecked();
    expect(shared).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({ modelAccess: { sharedModels: true, githubCopilot: false } }),
    );
  });

  it("refuses an OpenClaw agent when no model source is available", async () => {
    mockCatalog.data = { known: true, sharedModels: [], githubCopilot: false };
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    expect(screen.getByText(/grants no currently available model source/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
  });

  it("waits for the published model sources before creating an OpenClaw agent", async () => {
    mockCatalog.data = { known: false, sharedModels: [], githubCopilot: false };
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    expect(screen.getByText(/has not published its models yet/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
  });

  it("sends Copilot off and a default shared model", async () => {
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    await user.selectOptions(screen.getByRole("combobox", { name: /Default model/ }), "gpt-6-mini");
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({ model: "gpt-6-mini", modelAccess: { sharedModels: true, githubCopilot: false } }),
    );
  });

  it("offers Hermes the same model sources as OpenClaw", async () => {
    const user = userEvent.setup();
    renderForm();
    await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), "hermes");
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    expect(screen.getByRole("switch", { name: "Use shared models" })).toBeChecked();
    await user.selectOptions(screen.getByRole("combobox", { name: /Default model/ }), "gpt-6-mini");
    await user.click(screen.getByRole("button", { name: "Create Agent" }));
    expect(mockMutate).toHaveBeenCalledWith(
      expect.objectContaining({
        agent: { type: "hermes" },
        model: "gpt-6-mini",
        modelAccess: { sharedModels: true, githubCopilot: false },
      }),
    );
  });

  it("limits model choices to the selected Team's AgentPolicy", async () => {
    mockTeams.data[0].policy.sharedModels = ["gpt-6"];
    mockTeams.data[0].policy.githubCopilot = false;
    const user = userEvent.setup();
    renderForm();
    await chooseOpenClawAndTeam(user);
    expect(screen.getByText(/Models allowed by this Team: gpt-6/)).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "gpt-6-mini" })).not.toBeInTheDocument();
    const copilot = screen.getByRole("switch", { name: "Use GitHub Copilot" });
    expect(copilot).not.toBeChecked();
    expect(copilot).toBeDisabled();
    expect(screen.getByText(/AgentPolicy does not allow GitHub Copilot/)).toBeInTheDocument();
  });

  it("prevents a harness that the selected Team's AgentPolicy denies", async () => {
    mockTeams.data[0].policy.harnesses = ["openclaw"];
    const user = userEvent.setup();
    renderForm();
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    expect(screen.getByText(/does not allow Hermes agents/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
  });

  it("refuses Hermes while the deployment's agent chart routes only OpenClaw", async () => {
    mockCatalog.data = { ...mockCatalog.data, agentTypes: ["openclaw"] };
    const user = userEvent.setup();
    renderForm();
    await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), "hermes");
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    expect(screen.getByText(/does not route Hermes agents through the AI Gateway/)).toBeInTheDocument();
    expect(screen.getByText(/upgrade the agent chart/)).toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "Use shared models" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
    await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), "openclaw");
    expect(screen.queryByText(/does not route/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeEnabled();
  });

  it("routes only OpenClaw with an earlier operator's catalog", async () => {
    mockCatalog.data = { known: true, sharedModels: [{ name: "gpt-6" }], githubCopilot: true };
    const user = userEvent.setup();
    renderForm();
    await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
    expect(screen.getByText(/does not route Hermes agents through the AI Gateway/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
  });

  it("waits until the operator has read its agent chart", async () => {
    mockCatalog.data = { ...mockCatalog.data, agentTypes: null };
    const user = userEvent.setup();
    renderForm();
    for (const type of ["hermes", "openclaw"]) {
      await user.selectOptions(screen.getByRole("combobox", { name: /Agent Type/ }), type);
      await user.selectOptions(screen.getByRole("combobox", { name: /Team/ }), "platform");
      expect(screen.getByText(/has not read its agent chart yet/)).toBeInTheDocument();
      expect(screen.queryByText(/does not route/)).not.toBeInTheDocument();
      expect(screen.queryByRole("switch", { name: "Use shared models" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Create Agent" })).toBeDisabled();
    }
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
