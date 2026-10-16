// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it, vi } from "vitest";
import { createToolGrant, createToolIdentity, deleteToolGrant, listToolGrants, startConnection, ToolAuthorizationError, updateToolGrant, verifyToolGrant } from "./toolAuthorization";

describe("tool authorization API", () => {
  it("uses canonical connection and grant paths without browser credentials", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`{"connectionId":"conn-1","authorizationUrl":"https://provider.example.invalid/authorize","expiresAt":"2026-10-11T00:00:00Z"}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    await startConnection("github");
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/connections/start", expect.objectContaining({ method: "POST", body: JSON.stringify({ provider: "github" }) }));

    global.fetch = vi.fn().mockResolvedValue(new Response(`{"id":"grant-1"}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    const input = { connectionId: "conn-1", tools: ["github_contents_get"], constraints: { subscriptionIds: [], repositoryIds: ["1"], baseBranches: [], calendarIds: [], recipientAddresses: [], recipientDomains: [], allowInvites: false }, expiresAt: "2026-10-11T00:00:00Z" };
    await createToolGrant("agent-a", input);
    const [, options] = vi.mocked(fetch).mock.calls[0];
    expect(options?.method).toBe("POST");
    expect(String(options?.body)).toContain('"connectionId":"conn-1"');
    expect(String(options?.body)).not.toMatch(/token|credential|secret/i);
  });

  it("uses PUT and includes revision for grant changes", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`{"id":"grant-1"}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    await updateToolGrant("agent-a", "grant-1", { connectionId: "conn-1", tools: ["github_contents_get"], constraints: { subscriptionIds: [], repositoryIds: [], baseBranches: [], calendarIds: [], recipientAddresses: [], recipientDomains: [], allowInvites: false }, expiresAt: "2026-10-11T00:00:00Z", revision: 7 });
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent-a/tool-grants/grant-1", expect.objectContaining({ method: "PUT", body: expect.stringContaining('"revision":7') }));

    global.fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204, headers: { "Content-Type": "application/json" } }));
    await deleteToolGrant("agent-a", "grant-1", 7);
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent-a/tool-grants/grant-1", expect.objectContaining({ method: "DELETE", headers: expect.objectContaining({ "If-Match": "7" }) }));
  });

  it("creates an Azure Agent binding and verifies with an optimistic revision", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`{"id":"agent-identity","mode":"agent"}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    await createToolIdentity("agent-a");
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent-a/tool-identities", expect.objectContaining({ method: "POST", body: JSON.stringify({ provider: "azure" }) }));

    global.fetch = vi.fn().mockResolvedValue(new Response(`{"id":"grant-1","authorization":{"state":"pending"}}`, { status: 200, headers: { "Content-Type": "application/json" } }));
    await verifyToolGrant("agent-a", "grant-1", 7);
    expect(fetch).toHaveBeenLastCalledWith("/api/v1/instances/agent-a/tool-grants/grant-1/verify", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "If-Match": "7" }) }));
  });

  it("retains Portal error codes for unsupported runtimes", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`{"error":{"code":"unsupported_sandbox","message":"Agent Tools MCP is not available to Sandbox Agents"}}`, { status: 400, headers: { "Content-Type": "application/json" } }));

    await expect(listToolGrants("agent-a")).rejects.toMatchObject({
      name: "Error",
      message: "Agent Tools MCP is not available to Sandbox Agents",
      code: "unsupported_sandbox",
    } satisfies Partial<ToolAuthorizationError>);
  });

  it("marks historical grants read-only when the runtime was migrated", async () => {
    global.fetch = vi.fn().mockResolvedValue(new Response(`[{"id":"grant-1"}]`, { status: 200, headers: { "Content-Type": "application/json", "X-CetiRun-Tool-Grants-Read-Only": "true" } }));
    await expect(listToolGrants("agent-a")).resolves.toMatchObject({ grants: [{ id: "grant-1" }], readOnly: true });
  });
});
