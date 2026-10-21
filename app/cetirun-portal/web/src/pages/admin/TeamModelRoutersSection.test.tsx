// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { TeamEditPage } from "./TeamEditPage";
import { modelRouterProblem, toModelRouterRequests } from "./TeamModelRoutersSection";
import * as teams from "../../api/adminTeam";
import * as policies from "../../api/adminPolicy";
import * as sandboxes from "../../api/adminSandbox";
import * as gateway from "../../api/aiGateway";

vi.mock("../../api/adminTeam");
vi.mock("../../api/adminPolicy");
vi.mock("../../api/adminSandbox");
vi.mock("../../api/aiGateway");

const team: teams.TeamDTO = {
  name: "research", uid: "team-uid", resourceVersion: "7", agentPolicyRef: "standard", sandboxRefs: ["small"], defaultSandboxRef: "small",
  modelRouters: [{ name: "auto", classifier: "heuristic", simple: "small-model", medium: "medium-model", complex: "large-model" }],
  modelRouterStatus: [{ name: "auto", ready: false, message: "router auto: tier medium names medium-model, which is not a shared model of the AI Gateway" }],
};

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(teams.getTeam).mockResolvedValue(team);
  vi.mocked(teams.updateTeam).mockResolvedValue(team);
  vi.mocked(policies.listPolicies).mockResolvedValue([{ name: "standard", uid: "policy-uid", resourceVersion: "1", harnesses: { allowed: ["hermes"] } }]);
  vi.mocked(sandboxes.listSandboxes).mockResolvedValue([{ name: "small", deleting: false } as sandboxes.SandboxDTO]);
  vi.mocked(gateway.getAIGatewayCatalog).mockResolvedValue({ known: true, sharedModels: [{ name: "small-model" }, { name: "large-model" }], githubCopilot: true, agentTypes: ["hermes"] });
});

function renderEditor() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><MemoryRouter initialEntries={["/admin/teams/research/edit"]}><Routes>
    <Route path="/admin/teams/:name/edit" element={<TeamEditPage />} />
    <Route path="/admin/teams" element={<div>Team list</div>} />
  </Routes></MemoryRouter></QueryClientProvider>);
}

it("shows a router's status and saves added routers with the Team", async () => {
  const user = userEvent.setup(); renderEditor();
  expect(await screen.findByText(/tier medium names medium-model/)).toBeInTheDocument();
  await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Add smart-routing model" }));
  const added = screen.getByRole("group", { name: "Smart-routing model 2" });
  await user.type(within(added).getByRole("textbox", { name: "Model name" }), "auto-copilot");
  await user.selectOptions(within(added).getByRole("combobox", { name: "Classifier" }), "llm");
  await user.selectOptions(within(added).getByRole("combobox", { name: "Classifier model" }), "small-model");
  await user.type(within(added).getByRole("combobox", { name: "Simple tier" }), "copilot/fast-model");
  await user.type(within(added).getByRole("combobox", { name: "Medium tier" }), "small-model");
  await user.type(within(added).getByRole("combobox", { name: "Complex tier" }), "large-model");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(teams.updateTeam).toHaveBeenCalledOnce());
  expect(vi.mocked(teams.updateTeam).mock.calls[0][1].modelRouters).toEqual([
    { name: "auto", classifier: "heuristic", simple: "small-model", medium: "medium-model", complex: "large-model" },
    { name: "auto-copilot", classifier: "llm", classifierModel: "small-model", simple: "copilot/fast-model", medium: "small-model", complex: "large-model" },
  ]);
});

it("does not save an incomplete router", async () => {
  const user = userEvent.setup(); renderEditor();
  await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "Add smart-routing model" }));
  await user.type(within(screen.getByRole("group", { name: "Smart-routing model 2" })).getByRole("textbox", { name: "Model name" }), "auto-2");
  // The browser stops a form with an empty required tier; submit past that to check the page's own check.
  fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);
  expect(await screen.findByText(/auto-2: the simple tier needs a model name/)).toBeInTheDocument();
  expect(teams.updateTeam).not.toHaveBeenCalled();
});

it("removes a router", async () => {
  const user = userEvent.setup(); renderEditor();
  await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
  await user.click(within(screen.getByRole("group", { name: "Smart-routing model 1" })).getByRole("button", { name: "Remove" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(teams.updateTeam).toHaveBeenCalledOnce());
  expect(vi.mocked(teams.updateTeam).mock.calls[0][1].modelRouters).toBeUndefined();
});

it("checks routers the way the Team API does", () => {
  const router = { name: "auto", simple: "a", medium: "b", complex: "c" };
  expect(modelRouterProblem([router])).toBeNull();
  expect(modelRouterProblem([{ ...router, name: "CetiRun-x" }])).toMatch(/reserved/);
  expect(modelRouterProblem([{ ...router, name: "copilot/auto" }])).toMatch(/without spaces or slashes/);
  expect(modelRouterProblem([router, router])).toMatch(/defined twice/);
  expect(modelRouterProblem([{ ...router, classifier: "llm" }])).toMatch(/needs a shared model/);
  expect(modelRouterProblem([{ ...router, classifier: "llm", classifierModel: "copilot/x" }])).toMatch(/needs a shared model/);
  expect(modelRouterProblem(Array.from({ length: 11 }, (_, i) => ({ ...router, name: `auto-${i}` })))).toMatch(/at most 10/);
  expect(toModelRouterRequests([{ ...router, name: " auto ", classifierModel: "x", reasoning: " " }])).toEqual([
    { name: "auto", classifier: "heuristic", simple: "a", medium: "b", complex: "c" },
  ]);
});
