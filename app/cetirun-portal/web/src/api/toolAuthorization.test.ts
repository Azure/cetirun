// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it, vi } from "vitest";
import { createConnectionBinding, createToolIdentity, deleteConnectionBinding, listConnectionBindings, ToolAuthorizationError } from "./toolAuthorization";

describe("Azure connection API", () => {
  it("creates the independent Azure identity and its fixed ARM binding", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`{"id":"azure-connection"}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    await createToolIdentity("agent a");
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent%20a/tool-identities", expect.objectContaining({
      method: "POST", body: JSON.stringify({ provider: "azure" }),
    }));

    global.fetch = vi.fn().mockResolvedValue(new Response(`{"id":"binding"}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    await createConnectionBinding("agent a", "azure-connection");
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent%20a/connection-bindings", expect.objectContaining({
      method: "POST", body: JSON.stringify({ connectionId: "azure-connection", services: ["azure_arm"], enabled: true }),
    }));
  });

  it("reads the runtime create restriction and disconnects with the binding revision", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response("[]", { status: 200, headers: { "Content-Type": "application/json", "X-CetiRun-Connection-Bindings-Read-Only": "true" } }));
    await expect(listConnectionBindings("agent-a")).resolves.toEqual({ bindings: [], readOnly: true });

    global.fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204, headers: { "Content-Type": "application/json" } }));
    await deleteConnectionBinding("agent-a", "binding-1", 7);
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent-a/connection-bindings/binding-1", expect.objectContaining({
      method: "DELETE", headers: expect.objectContaining({ "If-Match": "7" }),
    }));
  });

  it("retains Portal error codes for an identity that is not ready", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`{"error":{"code":"identity_not_ready","message":"Agent identity is not ready"}}`, { status: 409, headers: { "Content-Type": "application/json" } }));
    await expect(createToolIdentity("agent-a")).rejects.toMatchObject({
      name: "ToolAuthorizationError", code: "identity_not_ready", message: "Agent identity is not ready",
    } satisfies Partial<ToolAuthorizationError>);
  });
});
