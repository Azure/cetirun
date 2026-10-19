// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Instance } from "../types/instance";
import { RestartAgentButton } from "./RestartAgentButton";

const mutation = vi.hoisted(() => ({
  mutate: vi.fn(), reset: vi.fn(), isPending: false, isError: false,
  error: new Error("Connection interrupted"), data: undefined as Instance | undefined,
}));
vi.mock("../hooks/useInstance", () => ({ useRestartInstanceByName: () => mutation }));

const instance: Instance = {
  instanceName: "alice", alias: "alice", displayName: "Alice", team: "example-team",
  uid: "agent-uid", resourceVersion: "7", generation: 2, observedGeneration: 2,
  agent: { type: "openclaw" }, phase: "Ready", createdAt: "2026-01-01T00:00:00Z",
  upgradeAvailable: false, restartAvailable: true,
};

describe("RestartAgentButton", () => {
  beforeEach(() => {
    mutation.mutate.mockClear();
    mutation.reset.mockClear();
    mutation.isPending = false;
    mutation.isError = false;
    mutation.data = undefined;
    mutation.reset.mockImplementation(() => { mutation.isError = false; mutation.data = undefined; });
  });

  it("confirms a retained restart using the originally displayed Agent version", () => {
    const view = render(<RestartAgentButton instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));
    expect(screen.getByText(/HOME files and the existing Agent are retained/)).toBeInTheDocument();
    view.rerender(<RestartAgentButton instance={{ ...instance, uid: "replacement-uid", resourceVersion: "8" }} />);
    fireEvent.click(screen.getByRole("button", { name: "Restart", exact: true }));
    expect(mutation.mutate).toHaveBeenCalledWith({
      uid: "agent-uid", resourceVersion: "7", requestId: expect.stringMatching(/^[a-f0-9-]{36}$/),
    }, expect.anything());
  });

  it("reuses the identical request ID and version after an uncertain response", () => {
    const view = render(<RestartAgentButton instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Restart", exact: true }));
    const first = mutation.mutate.mock.calls[0][0];
    mutation.isError = true;
    view.rerender(<RestartAgentButton instance={{ ...instance, resourceVersion: "8" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Connection interrupted");
    fireEvent.click(screen.getByRole("button", { name: "Retry request" }));
    expect(mutation.mutate.mock.calls[1][0]).toEqual(first);
  });

  it("does not turn a persisted pending request into another restart after page reload", () => {
    render(<RestartAgentButton instance={{ ...instance, restartRequestId: "request-00000001" }} />);
    expect(screen.getByRole("button", { name: "Restart Agent" })).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent("waiting for the replacement Pod");
  });

  it("retires an uncertain request when polling confirms completion before a new restart", () => {
    const view = render(<RestartAgentButton instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Restart", exact: true }));
    const first = mutation.mutate.mock.calls[0][0];
    mutation.isError = true;
    view.rerender(<RestartAgentButton instance={{ ...instance, resourceVersion: "9", generation: 3,
      restartRequestId: first.requestId, observedRestartRequestId: first.requestId }} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Restart Agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Restart", exact: true }));
    const next = mutation.mutate.mock.calls[1][0];
    expect(next.resourceVersion).toBe("9");
    expect(next.requestId).not.toBe(first.requestId);
  });

  it("does not offer restart when the server reports it unavailable", () => {
    render(<RestartAgentButton instance={{ ...instance, phase: "Suspended", restartAvailable: false }} />);
    expect(screen.getByRole("button", { name: "Restart Agent" })).toBeDisabled();
  });
});
