// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InstanceListPage } from "./InstanceListPage";

const mockInstances = vi.hoisted(() => ({ data: [] as unknown[] }));
const mockFeatures = vi.hoisted(() => ({ data: { maxInstancesPerUser: 3 } }));
vi.mock("../hooks/useInstance", () => ({
  useInstancesQuery: () => ({ data: mockInstances.data }),
}));
vi.mock("../hooks/useFeatures", () => ({ useFeatures: () => ({ data: mockFeatures.data }) }));
vi.mock("../components/CreateForm", () => ({ CreateForm: () => null }));

const cetiAdmin = { instanceName: "cetiadmin", displayName: "", agent: { type: "hermes" }, phase: "Ready", cetiAdmin: true };
const research = { instanceName: "research", displayName: "Research", agent: { type: "openclaw" }, phase: "Ready" };

describe("InstanceListPage", () => {
  beforeEach(() => {
    mockInstances.data = [];
    mockFeatures.data = { maxInstancesPerUser: 3 };
  });

  it("shows the policy cause alongside Pending while preserving ordinary provisioning", () => {
    mockInstances.data = [
      { instanceName: "research", displayName: "Research", agent: { type: "openclaw" }, phase: "Pending", generation: 2,
        governance: { category: "policy", condition: "PolicyResolved", reason: "PolicyDenied", message: "policy requires Agent Identity", observedGeneration: 2 } },
      { instanceName: "support", displayName: "Support", agent: { type: "hermes" }, phase: "Provisioning" },
    ];
    render(<MemoryRouter><InstanceListPage /></MemoryRouter>);
    const blocked = screen.getByText("research").closest("[role=group]")!;
    expect(within(blocked as HTMLElement).getByText("Pending")).toBeInTheDocument();
    expect(within(blocked as HTMLElement).getByText("Blocked by policy")).toBeInTheDocument();
    expect(screen.getByText("PolicyDenied: policy requires Agent Identity")).toBeInTheDocument();
    expect(screen.getByText("Provisioning")).toBeInTheDocument();
    expect(screen.getAllByLabelText("Governance status")).toHaveLength(1);
  });

  it("does not count CetiAdmin toward the limit of its owner", () => {
    mockFeatures.data = { maxInstancesPerUser: 1 };
    mockInstances.data = [cetiAdmin];
    const view = render(<MemoryRouter><InstanceListPage /></MemoryRouter>);
    expect(screen.getByText("0 of 1 agents")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New Agent" })).toBeEnabled();
    const row = within(screen.getByRole("group", { name: "cetiadmin" }));
    expect(row.getByText("CetiAdmin. The platform manages it. It does not count toward your limit of agents.")).toBeInTheDocument();

    // An Agent of the owner counts.
    mockInstances.data = [cetiAdmin, research];
    view.rerender(<MemoryRouter><InstanceListPage /></MemoryRouter>);
    expect(screen.getByText("1 of 1 agents")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New Agent" })).toBeDisabled();
    expect(within(screen.getByRole("group", { name: "research" })).queryByText(/CetiAdmin/)).not.toBeInTheDocument();

    // Without a limit, the list counts every Agent, and the note names no limit.
    mockFeatures.data = { maxInstancesPerUser: 0 };
    view.rerender(<MemoryRouter><InstanceListPage /></MemoryRouter>);
    expect(screen.getByText("2 agents")).toBeInTheDocument();
    expect(screen.getByText("CetiAdmin. The platform manages it.")).toBeInTheDocument();
  });
});
