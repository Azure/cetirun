// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  cancelCopilotLogin,
  fetchCopilotAuthStatus,
  fetchCopilotModels,
  pollCopilotLoginStatus,
  startCopilotLogin,
} from "./copilotAuth";

function success(body: unknown = {}): Response {
  return {
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
  } as Response;
}

describe("Copilot helper authentication", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    global.fetch = vi.fn().mockResolvedValue(success());
  });

  it.each([
    ["status", () => fetchCopilotAuthStatus("/ui/example", "instance-key")],
    ["models", () => fetchCopilotModels("/ui/example", "instance-key")],
    ["login start", () => startCopilotLogin("/ui/example", "instance-key")],
    [
      "login status",
      () => pollCopilotLoginStatus("/ui/example", "instance-key", "session-id"),
    ],
    [
      "login cancellation",
      () => cancelCopilotLogin("/ui/example", "instance-key", "session-id"),
    ],
  ])("uses the dedicated ingress transport header for %s", async (_name, request) => {
    await request();

    expect(fetch).toHaveBeenCalledTimes(1);
    const init = vi.mocked(fetch).mock.calls[0][1];
    const headers = new Headers(init?.headers);
    expect(headers.get("X-CetiRun-Auth-Key")).toBe("instance-key");
    expect(headers.has("Authorization")).toBe(false);
  });
});
