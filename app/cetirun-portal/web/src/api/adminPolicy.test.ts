// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createAgentPolicy, createSandbox, listAgentPolicies, updateInstancePolicy, updateSandbox } from "./adminPolicy";

function response(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("admin policy API", () => {
  const fetchMock = vi.fn();
  beforeEach(() => { fetchMock.mockReset(); vi.stubGlobal("fetch", fetchMock); });

  it("lists policy resources", async () => {
    fetchMock.mockResolvedValue(response(200, [{ metadata: { name: "normal" }, spec: { harnesses: { allowed: ["hermes"] } } }]));
    await expect(listAgentPolicies()).resolves.toMatchObject([{ metadata: { name: "normal" } }]);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/admin/agent-policies");
  });

  it("uses an optimistic Agent identity for administrator policy changes", async () => {
    fetchMock.mockResolvedValue(response(200, { metadata: { name: "alice" } }));
    await updateInstancePolicy("alice work", { uid: "agent-uid", resourceVersion: "7", team: "platform", sandboxRef: "restricted", policyClass: "admin", monthlyBudgetUSD: "25" });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/admin/instances/alice%20work/policy");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "PATCH", body: JSON.stringify({ uid: "agent-uid", resourceVersion: "7", team: "platform", sandboxRef: "restricted", policyClass: "admin", monthlyBudgetUSD: "25" }) });
  });

  it("sends complete resource payloads to the policy and Sandbox routes", async () => {
    const policy = { apiVersion: "cetirun.microsoft.com/v1alpha1", kind: "AgentPolicy" as const, metadata: { name: "normal" }, spec: { harnesses: { allowed: ["hermes"] } } };
    const sandbox = { apiVersion: "cetirun.microsoft.com/v1alpha1", kind: "Sandbox" as const, metadata: { name: "standard" }, spec: { type: "kata" } };
    fetchMock.mockResolvedValue(response(201, policy));
    await createAgentPolicy(policy);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/admin/agent-policies");
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: "POST", body: JSON.stringify(policy) });

    fetchMock.mockResolvedValue(response(201, sandbox));
    await createSandbox(sandbox);
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/admin/sandboxes");
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: "POST", body: JSON.stringify(sandbox) });

    fetchMock.mockResolvedValue(response(200, sandbox));
    await updateSandbox("standard", { uid: "sandbox-uid", resourceVersion: "9" }, { type: "gvisor" });
    expect(fetchMock.mock.calls[2][0]).toBe("/api/v1/admin/sandboxes/standard");
    expect(fetchMock.mock.calls[2][1]).toMatchObject({ method: "PUT", body: JSON.stringify({ uid: "sandbox-uid", resourceVersion: "9", spec: { type: "gvisor" } }) });
  });
});
