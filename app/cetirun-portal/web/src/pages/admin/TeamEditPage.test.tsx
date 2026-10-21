// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { TeamEditPage } from "./TeamEditPage";
import * as teams from "../../api/adminTeam";
import * as policies from "../../api/adminPolicy";
import * as sandboxes from "../../api/adminSandbox";

vi.mock("../../api/adminTeam");
vi.mock("../../api/adminPolicy");
vi.mock("../../api/adminSandbox");

const team: teams.TeamDTO = { name: "example", uid: "team-uid", resourceVersion: "7", agentPolicyRef: "standard", sandboxRefs: ["small", "large"], defaultSandboxRef: "large", role: "Engineer" };
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(teams.getTeam).mockResolvedValue(team);
  vi.mocked(teams.createTeam).mockResolvedValue(team);
  vi.mocked(teams.updateTeam).mockResolvedValue(team);
  vi.mocked(policies.listPolicies).mockResolvedValue([{ name: "standard", uid: "policy-uid", resourceVersion: "1", harnesses: { allowed: ["hermes"] } }]);
  vi.mocked(sandboxes.listSandboxes).mockResolvedValue(["small", "large"].map(name => ({ name, deleting: false }) as sandboxes.SandboxDTO));
});

function renderEditor(path = "/admin/teams/new") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><Routes>
    <Route path="/admin/teams/new" element={<TeamEditPage />} />
    <Route path="/admin/teams/:name/edit" element={<TeamEditPage />} />
    <Route path="/admin/teams" element={<div>Team list</div>} />
  </Routes></MemoryRouter></QueryClientProvider>);
}

it("creates a Team with its policy, allowed Sandboxes and selected default", async () => {
  const user = userEvent.setup(); renderEditor();
  await user.type(screen.getByRole("textbox", { name: "Name" }), "example");
  await user.selectOptions(await screen.findByRole("combobox", { name: "Agent Policy" }), "standard");
  await user.click(await screen.findByRole("checkbox", { name: "small" }));
  await user.click(screen.getByRole("checkbox", { name: "large" }));
  await user.selectOptions(screen.getByRole("combobox", { name: "Default Sandbox" }), "large");
  await user.click(screen.getByRole("button", { name: "Create" }));
  await waitFor(() => expect(teams.createTeam).toHaveBeenCalledWith(expect.objectContaining({ name: "example", agentPolicyRef: "standard", sandboxRefs: ["small", "large"], defaultSandboxRef: "large" })));
});

it("offers creation links and blocks submission when prerequisites are absent", async () => {
  vi.mocked(policies.listPolicies).mockResolvedValue([]); vi.mocked(sandboxes.listSandboxes).mockResolvedValue([]);
  renderEditor();
  expect(await screen.findByRole("link", { name: "Agent Policy" })).toHaveAttribute("href", "/admin/policies/new");
  expect(screen.getByRole("link", { name: "Sandbox" })).toHaveAttribute("href", "/admin/sandboxes/new");
  // Each notice reads as one sentence: its words and link sit in one
  // MessageBarBody, not in separate items of the MessageBar's row.
  for (const name of ["Agent Policy", "Sandbox"]) {
    const body = screen.getByRole("link", { name }).closest(".fui-MessageBarBody");
    expect(body).not.toBeNull();
    expect(body).toHaveTextContent(new RegExp(`^Create an? ${name} before creating this Team\\.$`));
  }
  expect(screen.getByRole("group", { name: /Allowed Sandboxes/ })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
  fireEvent.submit(screen.getByRole("button", { name: "Create" }).closest("form")!);
  expect(teams.createTeam).not.toHaveBeenCalled();
});

it("blocks failed lookups and offers a retry", async () => {
  vi.mocked(sandboxes.listSandboxes).mockRejectedValueOnce(new Error("Sandboxes unavailable"));
  const user = userEvent.setup(); renderEditor();
  expect(await screen.findByText("Sandboxes unavailable")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
  await user.click(screen.getByRole("button", { name: "Retry configuration" }));
  await waitFor(() => expect(screen.queryByText("Sandboxes unavailable")).not.toBeInTheDocument());
  expect(teams.createTeam).not.toHaveBeenCalled();
});

it("round-trips bindings and the reviewed version without retrying a stale save", async () => {
  vi.mocked(teams.getTeam).mockResolvedValue({ ...team, name: "new" });
  vi.mocked(teams.updateTeam).mockRejectedValue(new Error("Configuration changed; reload it before saving or deleting"));
  const user = userEvent.setup(); renderEditor("/admin/teams/new/edit");
  await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("new");
  expect(screen.getByRole("combobox", { name: "Default Sandbox" })).toHaveValue("large");
  await user.click(screen.getByRole("checkbox", { name: "large" }));
  expect(screen.getByRole("combobox", { name: "Default Sandbox" })).toHaveValue("small");
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText(/Configuration changed/)).toBeInTheDocument();
  expect(teams.updateTeam).toHaveBeenCalledExactlyOnceWith("new", expect.objectContaining({ uid: "team-uid", resourceVersion: "7", agentPolicyRef: "standard", sandboxRefs: ["small"], defaultSandboxRef: "small" }));
  expect(teams.createTeam).not.toHaveBeenCalled();
});

it.each(["create", "update"])("shows validation details after %s and preserves the form for retry", async operation => {
  const message = "Team configuration was rejected by the cluster. spec.skills[0]: Invalid value. Check the Team fields.";
  const mutation = operation === "create" ? vi.mocked(teams.createTeam) : vi.mocked(teams.updateTeam);
  mutation.mockRejectedValueOnce(new Error(message));
  const user = userEvent.setup();
  renderEditor(operation === "create" ? "/admin/teams/new" : "/admin/teams/example/edit");
  if (operation === "create") {
    await user.type(screen.getByRole("textbox", { name: "Name" }), "example");
    await user.selectOptions(await screen.findByRole("combobox", { name: "Agent Policy" }), "standard");
    await user.click(await screen.findByRole("checkbox", { name: "small" }));
  }
  const role = await screen.findByRole("textbox", { name: "Role" });
  await user.clear(role);
  await user.type(role, "Platform Engineer");
  const button = screen.getByRole("button", { name: operation === "create" ? "Create" : "Save" });
  await waitFor(() => expect(button).toBeEnabled());
  await user.click(button);
  expect(await screen.findByText(message)).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("example");
  expect(role).toHaveValue("Platform Engineer");
  expect(screen.queryByText("Team list")).not.toBeInTheDocument();
  await waitFor(() => expect(button).toBeEnabled());
  await user.click(button);
  expect(await screen.findByText("Team list")).toBeInTheDocument();
  expect(screen.queryByText(message)).not.toBeInTheDocument();
});
