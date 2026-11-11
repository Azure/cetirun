// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { deleteTeam, updateTeam, type TeamUpsertRequest } from "./adminTeam";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("admin Team API", () => {
  const fetchMock = vi.fn();
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  it("sends generation in a header while preserving the update and delete JSON shapes", async () => {
    const request: TeamUpsertRequest = {
      uid: "team-uid", resourceVersion: "42", generation: 7, agentPolicyRef: "standard", sandboxRefs: ["kata"], defaultSandboxRef: "kata",
    };
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { name: "research" }));
    await updateTeam("research", request);
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: "PUT", body: JSON.stringify({ ...request, generation: undefined }), headers: { "X-CetiRun-Team-Generation": "7" },
    });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).not.toHaveProperty("generation");

    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await deleteTeam({ name: "research", uid: "team-uid", resourceVersion: "42", generation: 7 });
    expect(fetchMock.mock.calls[1][1]).toMatchObject({
      method: "DELETE", body: JSON.stringify({ uid: "team-uid", resourceVersion: "42" }), headers: { "X-CetiRun-Team-Generation": "7" },
    });
  });
});
