// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";
import type { TeamDTO } from "../api/adminTeam";
import { CompatibilityBadge, CompatibilityReport, compatibilityPresentation } from "./CompatibilityReport";

function team(checkedAt: string): TeamDTO {
  return {
    name: "research", uid: "team-uid", generation: 1, resourceVersion: "1", agentPolicyRef: "standard", sandboxRefs: ["kata"], defaultSandboxRef: "kata",
    compatibility: { state: "Compatible", team: { name: "research", uid: "team-uid", generation: 1 }, policy: { name: "standard", uid: "policy-uid", generation: 1 },
      sandboxes: [{ sandbox: { name: "kata", uid: "sandbox-uid", generation: 1 }, state: "Compatible" }], checkedAt, validUntil: "2026-01-01T00:00:06Z" },
  };
}

describe("CompatibilityBadge", () => {
  afterEach(() => vi.useRealTimers());

  it("uses the current time when a refreshed report arrives", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    const view = render(<CompatibilityBadge team={team("2026-01-01T00:00:00Z")} />);
    expect(screen.getByText("Compatible")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(2_000));
    view.rerender(<CompatibilityBadge team={team("2026-01-01T00:00:01Z")} />);
    expect(screen.getByText("Compatible")).toBeInTheDocument();
  });

  it("fails closed when the Team is deleting", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    expect(compatibilityPresentation({ ...team("2026-01-01T00:00:00Z"), deleting: true }).state).toBe("Unknown");
  });

  it("does not repeat Sandbox-scoped issues at the Team level", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    const report = team("2026-01-01T00:00:00Z");
    report.compatibility!.issues = [{ code: "sandbox_incompatible", message: "Sandbox control conflict", sandbox: { name: "kata", uid: "sandbox-uid", generation: 1 } }, { code: "team_incompatible", message: "Team control conflict" }];
    report.compatibility!.sandboxes![0].issues = [{ code: "sandbox_incompatible", message: "Sandbox control conflict" }];
    render(<MemoryRouter><CompatibilityReport team={report} sandboxName="kata" /></MemoryRouter>);
    expect(screen.getAllByText("Sandbox control conflict")).toHaveLength(1);
    expect(screen.getByText("Team control conflict")).toBeInTheDocument();
  });

  it("accepts a persisted report without an optional Sandbox list", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
    const report = team("2026-01-01T00:00:00Z");
    report.sandboxRefs = [];
    report.defaultSandboxRef = "";
    delete report.compatibility!.sandboxes;
    render(<CompatibilityBadge team={report} />);
    expect(screen.getByText("Compatible")).toBeInTheDocument();
  });
});
