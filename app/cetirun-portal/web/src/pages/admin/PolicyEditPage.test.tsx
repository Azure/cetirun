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
});
