// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TeamEditPage } from "./TeamEditPage";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

describe("TeamEditPage errors", () => {
  it.each(["create", "update"])("shows API validation details after %s and preserves the form for retry", async (operation) => {
    const message = "Team configuration was rejected by the cluster. spec.agentPolicyRef: Required value; spec.defaultSandboxRef: Required value; spec.sandboxRefs: Required value. The cluster requires Agent Policy/Sandbox settings that this form cannot configure. Ask a platform administrator to configure the Team and verify that the Portal and installed CRDs are compatible.";
    const fetchMock = vi.fn();
    if (operation === "update") {
      fetchMock.mockResolvedValueOnce(jsonResponse(200, { name: "example-team", role: "Engineer" }));
    }
    fetchMock.mockResolvedValueOnce(jsonResponse(422, { error: { code: "invalid_team", message } }));
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { name: "example-team" }));
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[operation === "create" ? "/admin/teams/new" : "/admin/teams/example-team"]}>
          <Routes>
            <Route path="/admin/teams/:name" element={<TeamEditPage />} />
            <Route path="/admin/teams" element={<div>Team list</div>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const name = await screen.findByRole("textbox", { name: "Name" });
    if (operation === "create") fireEvent.change(name, { target: { value: "example-team" } });
    const role = screen.getByRole("textbox", { name: "Role" });
    fireEvent.change(role, { target: { value: "Platform Engineer" } });
    const button = screen.getByRole("button", { name: operation === "create" ? "Create" : "Save" });
    fireEvent.click(button);

    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(name).toHaveValue("example-team");
    expect(role).toHaveValue("Platform Engineer");
    expect(screen.queryByText("Team list")).not.toBeInTheDocument();
    await waitFor(() => expect(button).toBeEnabled());
    expect(fetchMock).toHaveBeenLastCalledWith(
      operation === "create" ? "/api/v1/admin/teams" : "/api/v1/admin/teams/example-team",
      expect.objectContaining({ method: operation === "create" ? "POST" : "PUT" }),
    );

    fireEvent.click(button);
    expect(await screen.findByText("Team list")).toBeInTheDocument();
    expect(screen.queryByText(message)).not.toBeInTheDocument();
  });
});
