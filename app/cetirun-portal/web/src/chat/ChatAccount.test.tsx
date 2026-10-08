// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fetchMe } from "../api/me";
import { ChatAccountProvider, useChatAccount } from "./ChatAccount";
import { flight } from "./runtime";

vi.mock("../api/me", () => ({ fetchMe: vi.fn() }));
const alice = {
  alias: "alice",
  displayName: "Alice",
  email: "alice@example.com",
  isAdmin: false,
  chatStorageScope: "scope-alice",
};
function View() {
  const account = useChatAccount();
  return (
    <>
      <span>
        {account?.checking ? "checking" : (account?.scope ?? "no scope")}
      </span>
      <button onClick={() => account?.invalidate("scope-alice")}>
        Reject old request
      </button>
    </>
  );
}
async function open() {
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={cache}>
      <ChatAccountProvider>
        <View />
      </ChatAccountProvider>
    </QueryClientProvider>,
  );
  await screen.findByText("scope-alice");
  return cache;
}
beforeEach(() => vi.mocked(fetchMe).mockReset().mockResolvedValue(alice));
afterEach(() => vi.restoreAllMocks());

describe("Chat account boundaries", () => {
  it("ignores an old request's rejection while a new identity is being verified or has arrived", async () => {
    await open();
    let resolve!: (me: typeof alice) => void;
    vi.mocked(fetchMe).mockReturnValueOnce(
      new Promise((done) => {
        resolve = done;
      }),
    );
    fireEvent.focus(window);
    fireEvent.click(screen.getByRole("button", { name: "Reject old request" }));
    expect(screen.getByText("checking")).toBeInTheDocument();
    await act(async () => resolve({ ...alice, chatStorageScope: "scope-bob" }));
    await screen.findByText("scope-bob");
    fireEvent.click(screen.getByRole("button", { name: "Reject old request" }));
    expect(screen.getByText("scope-bob")).toBeInTheDocument();
  });
  it("hides content during focus verification without cancelling the same account", async () => {
    await open();
    const work = flight("scope-alice");
    let resolve!: (me: typeof alice) => void;
    vi.mocked(fetchMe).mockReturnValueOnce(
      new Promise((done) => {
        resolve = done;
      }),
    );
    fireEvent.focus(window);
    expect(screen.getByText("checking")).toBeInTheDocument();
    expect(work.controller.signal.aborted).toBe(false);
    await act(async () => resolve(alice));
    await screen.findByText("scope-alice");
    expect(work.controller.signal.aborted).toBe(false);
    work.release();
  });
  it("cancels the old account and drops private query caches after an account change", async () => {
    const cache = await open();
    cache.setQueryData(["private-instance"], "synthetic private content");
    const work = flight("scope-alice");
    vi.mocked(fetchMe).mockResolvedValueOnce({
      ...alice,
      alias: "bob",
      chatStorageScope: "scope-bob",
    });
    fireEvent.focus(window);
    await screen.findByText("scope-bob");
    expect(work.controller.signal.aborted).toBe(true);
    expect(cache.getQueryData(["private-instance"])).toBeUndefined();
    work.release();
  });
  it("hides before a page is frozen in back-forward cache", async () => {
    await open();
    const work = flight("scope-alice");
    act(() =>
      window.dispatchEvent(
        new PageTransitionEvent("pagehide", { persisted: true }),
      ),
    );
    expect(screen.getByText("checking")).toBeInTheDocument();
    expect(work.controller.signal.aborted).toBe(true);
    fireEvent(window, new PageTransitionEvent("pageshow", { persisted: true }));
    await screen.findByText("scope-alice");
    work.release();
  });
  it("does not disrupt ordinary Portal identity when Chat has no configured realm", async () => {
    const cache = await open();
    vi.mocked(fetchMe).mockResolvedValueOnce({
      ...alice,
      chatStorageScope: undefined,
    });
    fireEvent.focus(window);
    await screen.findByText("no scope");
    await waitFor(() =>
      expect(cache.getQueryData(["me"])).toMatchObject({ alias: "alice" }),
    );
  });
});
