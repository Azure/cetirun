// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AIGatewayBlock } from "../AIGatewayBlock";
import type { Instance } from "../../types/instance";

const api = vi.hoisted(() => ({
  startCopilotLogin: vi.fn(),
  pollCopilotLogin: vi.fn(),
  cancelCopilotLogin: vi.fn(),
  getAIGatewayCatalog: vi.fn(),
}));
vi.mock("../../api/aiGateway", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/aiGateway")>()),
  ...api,
}));

const mockUpdate = vi.fn();
vi.mock("../../hooks/useInstance", () => ({
  useUpdateInstanceByName: () => ({ mutate: mockUpdate, isPending: false, error: null }),
}));

function instance(overrides: Partial<NonNullable<Instance["aiGateway"]>> = {}): Instance {
  return {
    instanceName: "alice-gw",
    uid: "agent-uid",
    resourceVersion: "42",
    agent: { type: "openclaw" },
    alias: "alice",
    displayName: "Alice",
    team: "platform",
    phase: "Ready",
    createdAt: "2026-10-01T00:00:00Z",
    upgradeAvailable: false,
    aiGateway: {
      routed: true,
      phase: "Ready",
      sharedModels: true,
      githubCopilot: true,
      models: ["gpt-6", "copilot/gpt-5.5"],
      defaultModel: "gpt-6",
      copilot: { phase: "SignInRequired", models: [] },
      ...overrides,
    },
  };
}

function renderBlock(value: Instance) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AIGatewayBlock instance={value} />
    </QueryClientProvider>,
  );
}

describe("AIGatewayBlock", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    Object.values(api).forEach((fn) => fn.mockReset());
    // Nothing to resume unless a test says otherwise.
    api.pollCopilotLogin.mockResolvedValue({ state: "none" });
    mockUpdate.mockReset();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders nothing for an agent outside the Gateway", () => {
    const { container } = renderBlock({ ...instance(), aiGateway: undefined });
    expect(container).toBeEmptyDOMElement();
  });

  it("lists the models, marks the default and saves a new default", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderBlock(instance());
    const models = within(screen.getByLabelText("Agent models"));
    expect(models.getByText("gpt-6 (default)")).toBeInTheDocument();
    expect(models.getByText("copilot/gpt-5.5")).toBeInTheDocument();
    expect(screen.getByText("Sign-in required")).toBeInTheDocument();
    await user.selectOptions(screen.getByRole("combobox", { name: "Default model" }), "copilot/gpt-5.5");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(mockUpdate).toHaveBeenCalledWith({ uid: "agent-uid", resourceVersion: "42", model: "copilot/gpt-5.5" });
  });

  it("explains an agent without models", () => {
    renderBlock(instance({ models: [], defaultModel: undefined }));
    expect(screen.getByText("Sign in to GitHub Copilot to add your Copilot models.")).toBeInTheDocument();
  });

  it("runs the GitHub sign-in until it succeeds", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    api.startCopilotLogin.mockResolvedValue({
      state: "pending", userCode: "ABCD-1234", verificationUri: "https://github.com/login/device", interval: 5,
    });
    api.pollCopilotLogin
      .mockResolvedValueOnce({ state: "none" })
      .mockResolvedValueOnce({ state: "pending", userCode: "ABCD-1234", verificationUri: "https://github.com/login/device", interval: 5 })
      .mockResolvedValueOnce({ state: "succeeded", message: "Signed in to GitHub Copilot: 2 models." });
    renderBlock(instance());
    await user.click(screen.getByRole("button", { name: "Sign in with GitHub" }));
    expect(api.startCopilotLogin).toHaveBeenCalledWith("alice-gw", "agent-uid");
    expect(await screen.findByText("ABCD-1234")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    // One poll on mount to resume, then one per interval.
    expect(api.pollCopilotLogin).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(await screen.findByText("Signed in to GitHub Copilot: 2 models.")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(api.pollCopilotLogin).toHaveBeenCalledTimes(3);
  });

  it("cancels a pending sign-in", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    api.startCopilotLogin.mockResolvedValue({
      state: "pending", userCode: "WXYZ-9876", verificationUri: "https://github.com/login/device", interval: 5,
    });
    api.cancelCopilotLogin.mockResolvedValue(undefined);
    renderBlock(instance());
    await user.click(screen.getByRole("button", { name: "Sign in with GitHub" }));
    await screen.findByText("WXYZ-9876");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(api.cancelCopilotLogin).toHaveBeenCalledWith("alice-gw", "agent-uid");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    // Only the poll on mount; none after the cancel.
    expect(api.pollCopilotLogin).toHaveBeenCalledTimes(1);
  });

  it("reports a failed start", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    api.startCopilotLogin.mockRejectedValue(new Error("The agent's GitHub Copilot proxy is not set up yet. Try again in a minute."));
    renderBlock(instance());
    await user.click(screen.getByRole("button", { name: "Sign in with GitHub" }));
    expect(await screen.findByText(/not set up yet/)).toBeInTheDocument();
  });

  it("shows models left out for lack of a price", () => {
    renderBlock(instance({ copilot: { phase: "Ready", models: ["copilot/gpt-5.5"], unpriced: ["mystery-model"] } }));
    expect(screen.getByText(/no price for them: mystery-model/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in again" })).toBeInTheDocument();
  });

  it("resumes a sign-in still pending after a reload", async () => {
    api.pollCopilotLogin.mockResolvedValue({
      state: "pending", userCode: "KLMN-4321", verificationUri: "https://github.com/login/device", interval: 5,
    });
    renderBlock(instance());
    expect(await screen.findByText("KLMN-4321")).toBeInTheDocument();
    expect(api.startCopilotLogin).not.toHaveBeenCalled();
  });

  it("stops polling when the Portal refuses the sign-in", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { PortalError } = await vi.importActual<typeof import("../../api/aiGateway")>("../../api/aiGateway");
    api.startCopilotLogin.mockResolvedValue({
      state: "pending", userCode: "ABCD-1234", verificationUri: "https://github.com/login/device", interval: 5,
    });
    renderBlock(instance());
    await act(async () => {
      await Promise.resolve();
    });
    api.pollCopilotLogin.mockRejectedValue(new PortalError("GitHub Copilot is not enabled for this agent", 409));
    await user.click(screen.getByRole("button", { name: "Sign in with GitHub" }));
    await screen.findByText("ABCD-1234");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    // The poll on mount, then a single refused poll.
    expect(api.pollCopilotLogin).toHaveBeenCalledTimes(2);
    expect(screen.getByText("GitHub Copilot is not enabled for this agent")).toBeInTheDocument();
  });
});
