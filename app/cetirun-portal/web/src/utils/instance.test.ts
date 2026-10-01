// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { agentTypeLabel, agentUIUrl, desktopUrl, terminalUrl } from "./instance";
import type { Instance } from "../types/instance";

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

describe("Custom platform access", () => {
  const instance: Pick<Instance, "agent" | "instanceName" | "accessUrl" | "conditions"> = {
    agent: { type: "custom" },
    instanceName: "alice",
    accessUrl: "https://gateway.example.com/alice/term/",
    conditions: [
      { type: "IngressReady", status: "True", reason: "", message: "" },
      { type: "DesktopConfigured", status: "False", reason: "", message: "" },
      { type: "TerminalConfigured", status: "True", reason: "", message: "" },
    ],
  };

  it("enables only the configured terminal and does not invent a native UI", () => {
    expect(terminalUrl(instance)).toBe("https://gateway.example.com/alice/term");
    expect(desktopUrl(instance)).toBeUndefined();
    expect(agentUIUrl(instance)).toBeUndefined();
  });

  it("keeps an unready or unconfigured Custom terminal disabled", () => {
    expect(terminalUrl({ ...instance, conditions: [] })).toBeUndefined();
    expect(terminalUrl({
      ...instance,
      conditions: instance.conditions?.map((condition) => condition.type === "TerminalConfigured"
        ? { ...condition, status: "False" as const } : condition),
    })).toBeUndefined();
  });

  it("does not expose the terminal after ingress removal", () => {
    expect(terminalUrl({
      ...instance,
      conditions: instance.conditions?.filter((condition) => condition.type !== "IngressReady"),
    })).toBeUndefined();
  });
});
