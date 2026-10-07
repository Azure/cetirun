// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createAgentPolicy,
  decideGovernanceAction,
  listSandboxes,
  updateSandbox,
  type AgentPolicyResource,
} from "./adminGovernance";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("admin governance API", () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  it("uses the cluster-scoped AgentPolicy CRUD endpoints", async () => {
    const policy: AgentPolicyResource = {
      apiVersion: "cetirun.microsoft.com/v1alpha1",
      kind: "AgentPolicy",
      metadata: { name: "normal" },
      spec: { harnesses: { allowed: ["hermes"] } },
    };
    fetchMock.mockResolvedValue(jsonResponse(201, policy));
    await expect(createAgentPolicy(policy)).resolves.toEqual(policy);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/admin/agent-policies", expect.objectContaining({
      method: "POST", body: JSON.stringify(policy),
    }));
  });

  it("lists sandboxes and wraps an update in the API's spec envelope", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, []));
    await expect(listSandboxes()).resolves.toEqual([]);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/admin/sandboxes");

    fetchMock.mockResolvedValueOnce(jsonResponse(200, { metadata: { name: "kata" }, spec: { type: "kata" } }));
    await updateSandbox("kata test", { type: "kata" });
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/admin/sandboxes/kata%20test", expect.objectContaining({
      method: "PUT", body: JSON.stringify({ spec: { type: "kata" } }),
    }));
  });

  it("submits an explicit action decision and surfaces API errors", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { metadata: { name: "restart-a" }, spec: {} }));
    await decideGovernanceAction("restart/a", "Approved");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/admin/governance/actions/restart%2Fa/approval", expect.objectContaining({
      method: "POST", body: JSON.stringify({ decision: "Approved" }),
    }));

    fetchMock.mockResolvedValueOnce(jsonResponse(409, { error: { message: "Action has expired" } }));
    await expect(decideGovernanceAction("restart-a", "Denied")).rejects.toThrow("Action has expired");
  });
});
