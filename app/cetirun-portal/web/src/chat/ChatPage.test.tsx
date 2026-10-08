// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import "fake-indexeddb/auto";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Instance } from "../types/instance";
import ChatPage from "./ChatPage";
import * as store from "./store";
import { conversationKey } from "./types";
import { ChatError, sendChat } from "./api";

const identity = vi.hoisted(() => ({
  scope: "",
  checking: false,
  invalidate: vi.fn(),
}));
vi.mock("./ChatAccount", () => ({
  useChatAccount: () => ({ ...identity, verify: async () => identity.scope }),
}));
vi.mock("./runtime", async (original) => ({
  ...(await original<typeof import("./runtime")>()),
  browserSupport: () => undefined,
  subscribe: () => () => {},
  notify: vi.fn(),
  withAgentLock: async (
    _scope: string,
    _uid: string,
    work: () => Promise<unknown>,
  ) => work(),
}));
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  describeChat: vi.fn().mockResolvedValue({
    eligible: true,
    available: true,
    reason: "ready",
    agentUid: "synthetic-uid",
    bindingRevision: "binding",
    limits: {},
  }),
  chatModels: vi.fn().mockResolvedValue(["example-agent"]),
  sendChat: vi.fn(),
}));

const instance: Instance = {
  uid: "synthetic-uid",
  resourceVersion: "1",
  instanceName: "example",
  displayName: "Research workspace",
  agent: { type: "custom", port: 8080 },
  alias: "alice",
  team: "example-team",
  phase: "Ready",
  upgradeAvailable: false,
  teamsEnabled: false,
  createdAt: "2026-10-05T00:00:00Z",
};
const input = () =>
  screen.getByRole("textbox", { name: "Message Research workspace" });
async function open(id?: string) {
  const view = render(
    <MemoryRouter
      initialEntries={[
        `/instances/example/chat${id ? `?conversation=${id}` : ""}`,
      ]}
    >
      <ChatPage instance={instance} />
    </MemoryRouter>,
  );
  await waitFor(() =>
    expect(screen.getByRole("combobox", { name: "Chat model" })).toHaveValue(
      "example-agent",
    ),
  );
  await waitFor(() => expect(input()).not.toBeDisabled());
  return view;
}

beforeEach(() => {
  identity.scope = `test-${crypto.randomUUID()}`;
  identity.checking = false;
  identity.invalidate.mockReset();
  vi.mocked(sendChat)
    .mockReset()
    .mockImplementation(
      async (_target, _model, _messages, _id, signal, onText) => {
        signal.throwIfAborted();
        await onText("Synthetic reply");
        return "stop";
      },
    );
});
afterEach(() => vi.restoreAllMocks());

describe("Custom Chat interactions", () => {
  it("flushes a draft when creating a conversation before the debounce fires", async () => {
    await store.checkStorage(identity.scope);
    const original = await store.createConversation(
      identity.scope,
      instance.uid,
      instance.instanceName,
    );
    await open(original.id);
    fireEvent.change(input(), {
      target: { value: "Keep this immediate draft" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "New chat", exact: true }),
    );
    await waitFor(async () =>
      expect(
        (await store.getConversation(conversationKey(original)))?.draft,
      ).toBe("Keep this immediate draft"),
    );
    await waitFor(() => expect(input()).toHaveValue(""));
    expect(sendChat).not.toHaveBeenCalled();
  });

  it("flushes a draft when selecting a different conversation", async () => {
    await store.checkStorage(identity.scope);
    const first = await store.createConversation(
      identity.scope,
      instance.uid,
      instance.instanceName,
    );
    const second = await store.createConversation(
      identity.scope,
      instance.uid,
      instance.instanceName,
    );
    await store.updateConversation(second, { title: "Other conversation" });
    await open(first.id);
    fireEvent.change(input(), { target: { value: "Unsent text" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Other conversation", exact: true }),
    );
    await waitFor(() => expect(input()).toHaveValue(""));
    expect((await store.getConversation(conversationKey(first)))?.draft).toBe(
      "Unsent text",
    );
    expect(sendChat).not.toHaveBeenCalled();
  });

  it("sends one request per click and supplies completed history to the next turn", async () => {
    await open();
    fireEvent.change(input(), { target: { value: "First question" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Send message", exact: true }),
    );
    await screen.findByText("Synthetic reply");
    await waitFor(() => expect(input()).not.toBeDisabled());
    fireEvent.change(input(), { target: { value: "Second question" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Send message", exact: true }),
    );
    await waitFor(() => expect(sendChat).toHaveBeenCalledTimes(2));
    expect(vi.mocked(sendChat).mock.calls[1][2]).toEqual([
      { role: "user", content: "First question" },
      { role: "assistant", content: "Synthetic reply" },
      { role: "user", content: "Second question" },
    ]);
    await waitFor(() => expect(input()).not.toBeDisabled());
  });

  it("does not offer sending when the initial storage transaction fails", async () => {
    const original = store.forAgent;
    vi.spyOn(store, "forAgent").mockImplementation((scope, uid) => ({
      ...original(scope, uid),
      checkStorage: async () => {
        throw new Error("storage unavailable");
      },
    }));
    render(
      <MemoryRouter>
        <ChatPage instance={instance} />
      </MemoryRouter>,
    );
    await screen.findByText("storage unavailable");
    expect(
      screen.getByRole("button", { name: "Send message", exact: true }),
    ).toBeDisabled();
    expect(sendChat).not.toHaveBeenCalled();
  });
  it("invalidates the local view when authentication expires during a send", async () => {
    vi.mocked(sendChat).mockRejectedValueOnce(
      new ChatError("Sign in again", "not_authenticated", true, 401),
    );
    await open();
    fireEvent.change(input(), { target: { value: "Private question" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Send message", exact: true }),
    );
    await waitFor(() => expect(identity.invalidate).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(input()).not.toBeDisabled());
    expect(sendChat).toHaveBeenCalledTimes(1);
  });
});
