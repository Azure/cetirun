// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { updateTeam, type TeamUpsertRequest } from "./adminTeam";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("admin Team API", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  it("sends the Team revision observed by the editor with an update", async () => {
    const request: TeamUpsertRequest = {
      role: "Platform Engineering",
      membership: [{ kind: "User", tenantID: "11111111-1111-1111-1111-111111111111", objectID: "22222222-2222-2222-2222-222222222222" }],
      normalAgentPolicyRef: "normal",
      sandboxRefs: ["standard"],
      defaultSandboxRef: "standard",
    };
    fetchMock.mockResolvedValue(jsonResponse(200, { name: "platform", uid: "team-uid", resourceVersion: "42" }));

    await updateTeam("platform/team", request, { uid: "team-uid", resourceVersion: "41" });

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/admin/teams/platform%2Fteam", expect.objectContaining({
      method: "PUT",
      body: JSON.stringify({ ...request, uid: "team-uid", resourceVersion: "41" }),
    }));
  });
});
