// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PolicyListPage } from "./PolicyListPage";
import * as policyAPI from "../../api/adminPolicy";

vi.mock("../../api/adminPolicy");

describe("PolicyListPage", () => {
  beforeEach(() => vi.resetAllMocks());

  it("disables actions while a policy is deleting", async () => {
    vi.mocked(policyAPI.listPolicies).mockResolvedValue([{
      name: "standard", uid: "policy-uid", resourceVersion: "1", harnesses: { allowed: ["openclaw"] },
      generation: 3, observedGeneration: 2, deleting: true,
    }]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><MemoryRouter><PolicyListPage /></MemoryRouter></QueryClientProvider>);
    expect(await screen.findByText("standard")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit standard" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete standard" })).toBeDisabled();
  });
});
