// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { CreateForm } from "../CreateForm";

const mutate = vi.hoisted(() => vi.fn());
vi.mock("../../hooks/useFeatures", () => ({ useFeatures: () => ({ data: { tailscaleEnabled: false }, isLoading: false }) }));
vi.mock("../../hooks/useCreateOptions", () => ({
  useCreateOptions: (team: string) => ({
    data: { team, allowedAgentTypes: ["hermes"], aiGatewayEnabled: false,
      gateway: { known: false, sharedModels: [], githubCopilot: false, agentTypes: [], customImages: [] },
      agentIdentity: { allowed: false, required: false } },
    isFetching: false, refetch: vi.fn(),
  }),
}));
vi.mock("../../hooks/useInstance", () => ({ useCreateInstance: () => ({ mutate, isPending: false }) }));
vi.mock("../../hooks/useTeams", () => ({ useTeams: () => ({ data: [], isLoading: false }) }));

describe("CreateForm cancel", () => {
  it("offers no Cancel button on its own", () => {
    render(<CreateForm />);
    expect(screen.getByRole("button", { name: "Create Agent" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  it("cancels without creating an Agent", () => {
    const onCancel = vi.fn();
    render(<CreateForm onCancel={onCancel} />);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(mutate).not.toHaveBeenCalled();
  });
});
