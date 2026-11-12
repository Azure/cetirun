// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HermesWebappButton } from "./HermesWebappButton";
import { signInToHermes } from "../api/hermes";

vi.mock("../api/hermes", () => ({ signInToHermes: vi.fn() }));
const instance = { uid: "agent-uid", instanceName: "alice-hermes" };
const target = "https://portal.example.com/ui/alice-hermes/";
const assign = vi.fn();

describe("HermesWebappButton", () => {
  beforeEach(() => {
    vi.mocked(signInToHermes).mockReset();
    assign.mockReset();
    vi.stubGlobal("location", { origin: "https://portal.example.com", assign });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("opens the UI only after sign-in succeeds and ignores repeated clicks", async () => {
    let finish!: (url: string) => void;
    vi.mocked(signInToHermes).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    render(<HermesWebappButton instance={instance} disabled={false} />);
    const button = screen.getByRole("button", { name: "Agent UI" });
    fireEvent.click(button);
    fireEvent.click(button);
    expect(signInToHermes).toHaveBeenCalledTimes(1);
    expect(signInToHermes).toHaveBeenCalledWith(instance, expect.any(AbortSignal));
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(assign).not.toHaveBeenCalled();
    await act(async () => finish(target));
    expect(assign).toHaveBeenCalledWith(target);
  });

  it("shows a failed sign-in and allows an explicit retry", async () => {
    vi.mocked(signInToHermes).mockRejectedValueOnce(new Error("Hermes rejected the sign-in key."))
      .mockResolvedValueOnce(target);
    render(<HermesWebappButton instance={instance} disabled={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Agent UI" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Hermes rejected the sign-in key.");
    expect(assign).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Agent UI" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith(target));
    expect(signInToHermes).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("does not start while the Agent UI is unavailable", () => {
    render(<HermesWebappButton instance={instance} disabled />);
    fireEvent.click(screen.getByRole("button", { name: "Agent UI" }));
    expect(signInToHermes).not.toHaveBeenCalled();
  });

  it("cancels a request when the owner leaves the page", async () => {
    let finish!: (url: string) => void;
    vi.mocked(signInToHermes).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const { unmount } = render(<HermesWebappButton instance={instance} disabled={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Agent UI" }));
    const signal = vi.mocked(signInToHermes).mock.calls[0][1];
    unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => finish(target));
    expect(assign).not.toHaveBeenCalled();
  });

  it.each(["replacement", "not-ready"])("ignores a stale completion after %s", async (change) => {
    let finish!: (url: string) => void;
    vi.mocked(signInToHermes).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const { rerender } = render(<HermesWebappButton instance={instance} disabled={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Agent UI" }));
    const signal = vi.mocked(signInToHermes).mock.calls[0][1];
    rerender(<HermesWebappButton instance={change === "replacement" ? { ...instance, uid: "new-uid" } : instance} disabled={change === "not-ready"} />);
    expect(signal.aborted).toBe(true);
    await act(async () => finish(target));
    expect(assign).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("reports a bounded wait expiring", async () => {
    vi.mocked(signInToHermes).mockRejectedValue(new DOMException("Timeout", "TimeoutError"));
    render(<HermesWebappButton instance={instance} disabled={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Agent UI" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Hermes sign-in timed out");
    expect(screen.getByRole("button", { name: "Agent UI" })).toBeEnabled();
    expect(assign).not.toHaveBeenCalled();
  });
});
