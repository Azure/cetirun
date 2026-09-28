// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AgentAuthorizationBlock } from "../AgentAuthorizationBlock";
import type { Instance } from "../../types/instance";

const mock = vi.hoisted(() => ({
  status: {
    data: { status: "disconnected" },
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  },
  start: { isError: false, isPending: false, error: null, mutateAsync: vi.fn() },
  end: { isError: false, isPending: false, error: null, mutate: vi.fn() },
}));

vi.mock("../../hooks/useAgentAuthorization", () => ({
  useAgentAuthorization: () => mock,
}));

const instance: Instance = {
  instanceName: "agent-a",
  agentType: "hermes",
  alias: "owner",
  displayName: "Agent A",
  team: "",
  phase: "Ready",
  createdAt: "2026-01-01T00:00:00Z",
  upgradeAvailable: false,
  agentIdentity: { enabled: true, phase: "Ready" },
};

function popupWindow() {
  return { closed: false, close: vi.fn(), location: { replace: vi.fn() } } as unknown as Window;
}

async function openAuthorizationPopup(popup = popupWindow()) {
  vi.spyOn(window, "open").mockReturnValue(popup);
  fireEvent.click(screen.getByRole("button", { name: "Connect Microsoft Entra" }));
  await act(async () => {});
  return popup;
}

describe("AgentAuthorizationBlock", () => {
  beforeEach(() => {
    vi.useRealTimers();
    mock.status.data = { status: "disconnected" };
    vi.restoreAllMocks();
    mock.status.refetch.mockReset();
    mock.start.mutateAsync.mockReset();
    mock.start.mutateAsync.mockResolvedValue({ authorizationUrl: "https://login.example.invalid/authorize" });
  });

  it("reports a failed callback and refreshes authorization status", async () => {
    render(<AgentAuthorizationBlock instance={instance} />);
    const popup = await openAuthorizationPopup();

    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        origin: window.location.origin,
        source: popup as unknown as MessageEventSource,
        data: { type: "cetirun-agent-authorization", status: "failed" },
      }));
    });

    expect(screen.getByText("Microsoft Entra connection could not be completed. Try again.")).toBeInTheDocument();
    expect(mock.status.refetch).toHaveBeenCalledOnce();
  });

  it("ignores callback messages from another origin", async () => {
    render(<AgentAuthorizationBlock instance={instance} />);
    const popup = await openAuthorizationPopup();

    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        origin: "https://attacker.example",
        source: popup as unknown as MessageEventSource,
        data: { type: "cetirun-agent-authorization", status: "failed" },
      }));
    });

    expect(screen.queryByText("Microsoft Entra connection could not be completed. Try again.")).not.toBeInTheDocument();
    expect(mock.status.refetch).not.toHaveBeenCalled();
  });

  it("explains when the popup account does not match the Portal owner", async () => {
    render(<AgentAuthorizationBlock instance={instance} />);
    const popup = await openAuthorizationPopup();

    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        origin: window.location.origin,
        source: popup as unknown as MessageEventSource,
        data: { type: "cetirun-agent-authorization", status: "owner_mismatch" },
      }));
    });

    expect(screen.getByText("Sign in with the same Microsoft Entra account used by the Portal, then try again.")).toBeInTheDocument();
    expect(mock.status.refetch).toHaveBeenCalledOnce();
  });

  it("reports a closed sign-in popup after allowing the completion message to arrive", async () => {
    vi.useFakeTimers();
    mock.status.data = { status: "connecting" };
    render(<AgentAuthorizationBlock instance={instance} />);
    const popup = await openAuthorizationPopup();
    expect(popup.location.replace).toHaveBeenCalledWith("https://login.example.invalid/authorize");

    Object.defineProperty(popup, "closed", { value: true });
    await act(async () => { vi.advanceTimersByTime(1_500); });

    expect(screen.getByText("Microsoft Entra sign-in window was closed before the connection completed. Try again.")).toBeInTheDocument();
    expect(screen.getByText("Not connected")).toBeInTheDocument();
    expect(screen.queryByText("Connecting")).not.toBeInTheDocument();
    expect(mock.status.refetch).toHaveBeenCalledOnce();
  });

  it("detects a popup closed before the authorization request returns", async () => {
    vi.useFakeTimers();
    const popup = popupWindow();
    let resolveStart!: (result: { authorizationUrl: string }) => void;
    mock.start.mutateAsync.mockImplementation(() => new Promise<{ authorizationUrl: string }>((resolve) => { resolveStart = resolve; }));
    render(<AgentAuthorizationBlock instance={instance} />);

    await openAuthorizationPopup(popup);
    Object.defineProperty(popup, "closed", { value: true });
    await act(async () => { vi.advanceTimersByTime(1_500); });
    await act(async () => { resolveStart({ authorizationUrl: "https://login.example.invalid/authorize" }); });

    expect(screen.getByText("Microsoft Entra sign-in window was closed before the connection completed. Try again.")).toBeInTheDocument();
    expect(popup.location.replace).not.toHaveBeenCalled();
    expect(mock.status.refetch).toHaveBeenCalledOnce();
  });

  it("accepts a completion message during the popup-close grace period", async () => {
    vi.useFakeTimers();
    render(<AgentAuthorizationBlock instance={instance} />);
    const popup = await openAuthorizationPopup();
    Object.defineProperty(popup, "closed", { value: true });
    await act(async () => { vi.advanceTimersByTime(500); });
    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        origin: window.location.origin,
        source: popup as unknown as MessageEventSource,
        data: { type: "cetirun-agent-authorization", status: "complete" },
      }));
      vi.advanceTimersByTime(1_000);
    });

    expect(screen.queryByText("Microsoft Entra sign-in window was closed before the connection completed. Try again.")).not.toBeInTheDocument();
    expect(mock.status.refetch).toHaveBeenCalledOnce();
  });

  it("cleans popup timers when the component unmounts", async () => {
    vi.useFakeTimers();
    const view = render(<AgentAuthorizationBlock instance={instance} />);
    const popup = await openAuthorizationPopup();

    view.unmount();
    Object.defineProperty(popup, "closed", { value: true });
    await act(async () => { vi.advanceTimersByTime(1_500); });

    expect(mock.status.refetch).not.toHaveBeenCalled();
  });

  it("replaces the old popup without allowing its callback to cancel the new popup watch", async () => {
    vi.useFakeTimers();
    const firstPopup = popupWindow();
    const secondPopup = popupWindow();
    vi.spyOn(window, "open").mockReturnValueOnce(firstPopup).mockReturnValueOnce(secondPopup);
    render(<AgentAuthorizationBlock instance={instance} />);

    fireEvent.click(screen.getByRole("button", { name: "Connect Microsoft Entra" }));
    await act(async () => {});
    fireEvent.click(screen.getByRole("button", { name: "Connect Microsoft Entra" }));
    await act(async () => {});
    expect(firstPopup.close).toHaveBeenCalledOnce();

    await act(async () => {
      window.dispatchEvent(new MessageEvent("message", {
        origin: window.location.origin,
        source: firstPopup as unknown as MessageEventSource,
        data: { type: "cetirun-agent-authorization", status: "failed" },
      }));
    });
    expect(mock.status.refetch).not.toHaveBeenCalled();

    Object.defineProperty(secondPopup, "closed", { value: true });
    await act(async () => { vi.advanceTimersByTime(1_500); });
    expect(mock.status.refetch).toHaveBeenCalledOnce();
    expect(screen.getByText("Microsoft Entra sign-in window was closed before the connection completed. Try again.")).toBeInTheDocument();
  });
});
