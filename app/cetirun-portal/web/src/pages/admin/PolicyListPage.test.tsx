// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PolicyListPage } from "./PolicyListPage";
import * as policyAPI from "../../api/adminPolicy";

vi.mock("../../api/adminPolicy");

describe("PolicyListPage", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    // Tabster needs layout to focus dialog buttons; jsdom does not calculate it.
    vi.spyOn(document.body, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 1024, 768));
    vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) {
      return this.parentElement;
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it("disables actions while a policy is deleting", async () => {
    vi.mocked(policyAPI.listPolicies).mockResolvedValue([{
      name: "standard", uid: "policy-uid", resourceVersion: "1", harnesses: { allowed: ["openclaw"] },
      generation: 3, observedGeneration: 2, deleting: true,
    }]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><PolicyListPage /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText("standard")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit standard" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete standard" })).toBeDisabled();
  });

  it.each([
    { change: "updated", uid: "policy-uid" },
    { change: "recreated", uid: "replacement-uid" },
  ])("keeps the confirmed version when a policy is $change during a refresh", async ({ uid }) => {
    const user = userEvent.setup();
    const selected: policyAPI.AgentPolicyDTO = {
      name: "standard", uid: "policy-uid", resourceVersion: "1", harnesses: { allowed: ["openclaw"] },
    };
    const refreshed: policyAPI.AgentPolicyDTO = {
      ...selected, uid, resourceVersion: "2", harnesses: { allowed: ["hermes"] },
    };
    vi.mocked(policyAPI.listPolicies).mockResolvedValueOnce([selected]).mockResolvedValue([refreshed]);
    const rejection = new Error("Policy changed; reload and try again.");
    vi.mocked(policyAPI.deletePolicy)
      .mockRejectedValueOnce(rejection)
      .mockRejectedValueOnce(rejection)
      .mockResolvedValue(undefined);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><PolicyListPage /></MemoryRouter></QueryClientProvider>);

    await user.click(await screen.findByRole("button", { name: "Delete standard" }));
    expect(within(await screen.findByRole("dialog")).getByRole("button", { name: "Cancel" })).toHaveFocus();
    await act(async () => { await client.refetchQueries({ queryKey: ["admin", "policies"] }); });
    expect(await screen.findByText("hermes")).toBeInTheDocument();
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete", exact: true }));
    expect(await screen.findByText("Policy changed; reload and try again.")).toBeInTheDocument();
    expect(policyAPI.deletePolicy).toHaveBeenLastCalledWith(selected);

    // Retrying the same confirmation must not silently adopt the newer version.
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete", exact: true }));
    await waitFor(() => {
      expect(policyAPI.deletePolicy).toHaveBeenCalledTimes(2);
      expect(client.isMutating()).toBe(0);
      expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete", exact: true })).toBeEnabled();
    });
    expect(policyAPI.deletePolicy).toHaveBeenLastCalledWith(selected);

    // A new selection and confirmation can delete the version now shown in the list.
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    // Wait for the exiting modal to unmount and release its focus trap before reopening.
    await waitFor(() => expect(screen.queryByRole("dialog", { hidden: true })).not.toBeInTheDocument());
    await user.click(await screen.findByRole("button", { name: "Delete standard" }));
    expect(within(await screen.findByRole("dialog")).getByRole("button", { name: "Cancel" })).toHaveFocus();
    expect(screen.queryByText("Policy changed; reload and try again.")).not.toBeInTheDocument();
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete", exact: true }));
    await waitFor(() => expect(policyAPI.deletePolicy).toHaveBeenCalledTimes(3));
    expect(policyAPI.deletePolicy).toHaveBeenLastCalledWith(refreshed);
    await waitFor(() => expect(screen.queryByRole("dialog", { hidden: true })).not.toBeInTheDocument());
    await waitFor(() => expect(client.isMutating()).toBe(0));
    client.clear();
  });
});
