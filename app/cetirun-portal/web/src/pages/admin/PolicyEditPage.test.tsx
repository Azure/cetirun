// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PolicyEditPage } from "./PolicyEditPage";
import * as policyAPI from "../../api/adminPolicy";

vi.mock("../../api/adminPolicy", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../api/adminPolicy")>();
  return {
    ...original,
    createPolicy: vi.fn(),
    getPolicy: vi.fn(),
    updatePolicy: vi.fn(),
  };
});

describe("PolicyEditPage", () => {
  beforeEach(() => {
    vi.mocked(policyAPI.createPolicy).mockReset();
    vi.mocked(policyAPI.getPolicy).mockReset();
    vi.mocked(policyAPI.updatePolicy).mockReset();
  });

  it("shows one-per-line examples for list fields", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={["/admin/policies/create"]}>
          <Routes>
            <Route path="/admin/policies/create" element={<PolicyEditPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(screen.getByRole("textbox", { name: "Allowed skills" })).toHaveAttribute(
      "placeholder",
      "skill-a\nnpm:@scope/skill-b\nskill-c@1.2.0",
    );
    expect(screen.getByRole("textbox", { name: "Allowed shared models" })).toHaveAttribute(
      "placeholder",
      "gpt-4.1\ngpt-4o-mini",
    );
    expect(screen.getAllByText(/commas are not separators/i)).toHaveLength(2);
  });

  it("edits a policy named new instead of entering create mode", async () => {
    vi.mocked(policyAPI.getPolicy).mockResolvedValue({
      name: "new",
      uid: "policy-uid",
      resourceVersion: "42",
      harnesses: { allowed: ["openclaw"] },
      skills: { allowed: [] },
      modelAccess: {
        copilot: { allowed: false },
        sharedModels: { allowed: [] },
      },
    });
    vi.mocked(policyAPI.updatePolicy).mockResolvedValue({
      name: "new",
      uid: "policy-uid",
      resourceVersion: "43",
      harnesses: { allowed: ["openclaw"] },
    });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={["/admin/policies/new/edit"]}>
          <Routes>
            <Route path="/admin/policies/:name/edit" element={<PolicyEditPage />} />
            <Route path="/admin/policies" element={<div>Policy list</div>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Agent policy: new")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Name" })).toBeDisabled();
    fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);

    await waitFor(() => expect(policyAPI.updatePolicy).toHaveBeenCalledWith("new", expect.objectContaining({
      name: "new",
      uid: "policy-uid",
      resourceVersion: "42",
    })));
    expect(policyAPI.createPolicy).not.toHaveBeenCalled();
  });

  function renderEdit(name: string) {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[`/admin/policies/${name}/edit`]}>
          <Routes>
            <Route path="/admin/policies/:name/edit" element={<PolicyEditPage />} />
            <Route path="/admin/policies" element={<div>Policy list</div>} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  const limitedPolicy: policyAPI.AgentPolicyDTO = {
    name: "limited",
    uid: "policy-uid",
    resourceVersion: "7",
    harnesses: { allowed: ["openclaw"] },
    modelAccess: {
      sharedModels: { allowed: ["example-model"] },
      gatewayBudget: { defaultMonthlyUSD: "10", maxMonthlyUSD: "20" },
      rateLimits: { maxParallelRequests: 4, tpm: 200000, perModel: { "example-model": { rpm: 10 } } },
    },
  };

  it("keeps the loaded rate limits when saving another change", async () => {
    vi.mocked(policyAPI.getPolicy).mockResolvedValue(limitedPolicy);
    vi.mocked(policyAPI.updatePolicy).mockResolvedValue(limitedPolicy);
    renderEdit("limited");
    expect(await screen.findByRole("textbox", { name: "Max parallel requests" })).toHaveValue("4");
    expect(screen.getByRole("textbox", { name: "Requests per minute" })).toHaveValue("");
    expect(screen.getByRole("textbox", { name: "Model name 1" })).toHaveValue("example-model");
    expect(screen.getByText(/rejects long conversations/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("switch", { name: "Hermes" }));
    fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);
    await waitFor(() => expect(policyAPI.updatePolicy).toHaveBeenCalled());
    expect(vi.mocked(policyAPI.updatePolicy).mock.calls[0][1].modelAccess?.rateLimits).toEqual(limitedPolicy.modelAccess!.rateLimits);
  });

  it("sends new limits, removes cleared ones and never sends zero", async () => {
    vi.mocked(policyAPI.getPolicy).mockResolvedValue(limitedPolicy);
    vi.mocked(policyAPI.updatePolicy).mockResolvedValue(limitedPolicy);
    renderEdit("limited");
    const parallel = await screen.findByRole("textbox", { name: "Max parallel requests" });
    fireEvent.change(parallel, { target: { value: "" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Requests per minute" }), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    fireEvent.click(screen.getByRole("button", { name: "Add per-model limit" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Model name 1" }), { target: { value: "auto" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Tokens per minute for model 1" }), { target: { value: "8192" } });
    fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);
    await waitFor(() => expect(policyAPI.updatePolicy).toHaveBeenCalled());
    expect(vi.mocked(policyAPI.updatePolicy).mock.calls[0][1].modelAccess?.rateLimits).toEqual({
      rpm: 60, tpm: 200000, perModel: { auto: { tpm: 8192 } },
    });
  });

  it("rejects a limit below the minimum before saving", async () => {
    vi.mocked(policyAPI.getPolicy).mockResolvedValue(limitedPolicy);
    renderEdit("limited");
    fireEvent.change(await screen.findByRole("textbox", { name: "Tokens per minute" }), { target: { value: "1000" } });
    fireEvent.submit(screen.getByRole("button", { name: "Save" }).closest("form")!);
    expect(await screen.findByText(/Tokens per minute must be at least 4,096/)).toBeInTheDocument();
    expect(policyAPI.updatePolicy).not.toHaveBeenCalled();
  });
});
