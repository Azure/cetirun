// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { agentTypeLabel } from "./instance";

describe("agentTypeLabel", () => {
  it.each([
    ["hermes", "Hermes"],
    ["openclaw", "OpenClaw"],
    ["custom", "Custom"],
    ["future-agent", "future-agent"],
    ["", "—"],
    [undefined, "—"],
  ])("labels %s without guessing an absent type", (type, label) => {
    expect(agentTypeLabel(type)).toBe(label);
  });
});
