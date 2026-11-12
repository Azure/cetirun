// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { signInToHermes } from "./hermes";
import { SESSION_EXPIRED_MESSAGE } from "./fetch";
import type { Instance } from "../types/instance";

const origin = "https://portal.example.com";
const current: Instance = {
  uid: "agent-uid", resourceVersion: "7", instanceName: "alice-hermes",
  agent: { type: "hermes" }, alias: "alice", displayName: "Hermes", team: "",
  phase: "Ready", accessUrl: `${origin}/ui/alice-hermes/`,
  apiServerKey: "synthetic-current-hermes-key", upgradeAvailable: false, createdAt: "2026-01-01T00:00:00Z",
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("signInToHermes", () => {
  beforeEach(() => {
    vi.stubGlobal("location", { origin });
    vi.stubGlobal("fetch", vi.fn());
  });
  afterEach(() => vi.unstubAllGlobals());

  it("uses the fresh owner-scoped key and returns only the instance UI address", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ ...current, accessUrl: `${current.accessUrl}?token=discarded#fragment` }))
      .mockResolvedValueOnce(json({ ok: true, next: "https://elsewhere.example.com/" }));
    const signal = new AbortController().signal;
    const old = { ...current, apiServerKey: "synthetic-old-key" };
    await expect(signInToHermes(old, signal)).resolves.toBe(current.accessUrl);
    expect(fetch).toHaveBeenNthCalledWith(1, "/api/v1/instances/alice-hermes", expect.objectContaining({ signal }));
    const [url, options] = vi.mocked(fetch).mock.calls[1];
    expect(String(url)).toBe(`${origin}/ui/alice-hermes/auth/password-login`);
    expect(options).toMatchObject({
      method: "POST", mode: "same-origin", credentials: "same-origin", redirect: "manual", signal,
      headers: { "Content-Type": "application/json" },
    });
    expect(JSON.parse(options?.body as string)).toEqual({ provider: "basic", username: "cetirun", password: current.apiServerKey, next: "/" });
  });

  it("normalizes an instance address without a trailing slash", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ ...current, accessUrl: current.accessUrl!.slice(0, -1) }))
      .mockResolvedValueOnce(json({ ok: true }));
    await expect(signInToHermes(current, new AbortController().signal)).resolves.toBe(current.accessUrl);
  });

  it.each([
    { uid: "replacement-uid" }, { instanceName: "another-agent" }, { agent: { type: "openclaw" } },
    { phase: "Suspended" }, { suspended: true }, { apiServerKey: undefined }, { accessUrl: undefined },
  ])("does not submit a key after the current Agent changes: %j", async (change) => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ ...current, ...change }));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each([
    "https://elsewhere.example.com/ui/alice-hermes/",
    "http://portal.example.com/ui/alice-hermes/",
    "https://portal.example.com:8443/ui/alice-hermes/",
    `${origin}/ui/another-agent/`, `${origin}/ui/alice-hermes/extra`, `${origin}/api/`,
    "https://user:password@portal.example.com/ui/alice-hermes/",
    "javascript:alert(1)", "not-a-url",
  ])("never sends the key to an unexpected recipient: %s", async (accessUrl) => {
    vi.mocked(fetch).mockResolvedValueOnce(json({ ...current, accessUrl }));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each([401, 403, 404])("stops when the owner-scoped read returns %s", async (status) => {
    vi.mocked(fetch).mockResolvedValueOnce(json({}, status));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it.each([302, 307])("does not follow a login redirect (%s)", async (status) => {
    vi.mocked(fetch).mockResolvedValueOnce(json(current))
      .mockResolvedValueOnce(new Response(null, { status, headers: { Location: "https://login.example.com/" } }));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow(SESSION_EXPIRED_MESSAGE);
    expect(vi.mocked(fetch).mock.calls[1][1]?.redirect).toBe("manual");
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("recognizes the browser's opaque redirect response", async () => {
    const redirect = new Response(null);
    Object.defineProperty(redirect, "type", { value: "opaqueredirect" });
    vi.mocked(fetch).mockResolvedValueOnce(json(current)).mockResolvedValueOnce(redirect);
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow(SESSION_EXPIRED_MESSAGE);
  });

  it("recognizes a sign-in HTML page instead of treating it as success", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json(current))
      .mockResolvedValueOnce(new Response("<html>Sign in</html>", { headers: { "Content-Type": "text/html" } }));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow(SESSION_EXPIRED_MESSAGE);
  });

  it.each([
    [401, "Hermes rejected the sign-in key"], [403, "Access to the Hermes Webapp was denied"],
    [429, "Too many Hermes sign-in attempts"], [503, "Could not sign in"],
  ])("reports a Hermes %s without displaying the reply body", async (status, message) => {
    vi.mocked(fetch).mockResolvedValueOnce(json(current)).mockResolvedValueOnce(json({ detail: current.apiServerKey }, status as number));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow(message as string);
  });

  it.each([null, {}, { ok: false }, { ok: "true" }])("requires an affirmative JSON response: %j", async (body) => {
    vi.mocked(fetch).mockResolvedValueOnce(json(current)).mockResolvedValueOnce(json(body));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow("Could not sign in");
  });

  it("reports a malformed response", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json(current)).mockResolvedValueOnce(new Response("{"));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow("Could not sign in");
  });

  it("reports a network failure without assuming the key is wrong", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(json(current)).mockRejectedValueOnce(new TypeError("Network failure"));
    await expect(signInToHermes(current, new AbortController().signal)).rejects.toThrow("Could not reach the Hermes Webapp");
  });

  it("does not start an aborted request", async () => {
    const controller = new AbortController();
    controller.abort();
    await expect(signInToHermes(current, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(fetch).not.toHaveBeenCalled();
  });

  it("does not submit the key when cancelled during the owner read", async () => {
    const controller = new AbortController();
    vi.mocked(fetch).mockImplementationOnce(async () => { controller.abort(); return json(current); });
    await expect(signInToHermes(current, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("does not return a navigation target after cancellation", async () => {
    const controller = new AbortController();
    vi.mocked(fetch).mockResolvedValueOnce(json(current)).mockImplementationOnce(async () => {
      controller.abort();
      return json({ ok: true });
    });
    await expect(signInToHermes(current, controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
});
