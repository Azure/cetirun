// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { AgentPhase } from "./Lifecycle";

describe("AgentPhase", () => {
  it.each([
    ["Ready", "ready"],
    ["Provisioning", "busy"],
    ["Pending", "waiting"],
    ["Suspended", "suspended"],
    ["Failed", "failed"],
    ["Deleting", "deleting"],
    ["Unknown", "unknown"],
  ])("writes %s and draws it as %s", (phase, state) => {
    const { container } = render(<AgentPhase phase={phase} />);
    expect(screen.getByText(phase)).toBeInTheDocument();
    const glyph = container.querySelector("svg")!;
    expect(glyph).toHaveClass(`lifecycle--${state}`);
    expect(glyph).toHaveAttribute("aria-hidden", "true");
  });

  it("draws the state a caller gives instead of the one the phase implies", () => {
    const { container } = render(<AgentPhase phase="Ready" state="waiting" />);
    expect(container.querySelector("svg")).toHaveClass("lifecycle--waiting");
  });
});
