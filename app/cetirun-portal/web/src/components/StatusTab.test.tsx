// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusTab } from "./StatusTab";
import type { StatusResponse } from "../types/instance";

const message = "Agent chart 1.0.0 could not be downloaded from the registry (network: check that the registry is reachable); the deployed release is unchanged and the operator will retry";

function status(reason: string, conditionStatus: "True" | "False" = "False"): StatusResponse {
  return { conditions: [{ type: "AgentConfigReady", status: conditionStatus, reason, message }], events: [], nsEvents: [], pods: [] };
}

describe("StatusTab", () => {
  it("warns the owner when the Agent chart cannot be downloaded", () => {
    render(<StatusTab data={status("AgentChartUnavailable")} isLoading={false} />);
    const notice = screen.getByTestId("chart-unavailable-notice");
    expect(notice).toHaveTextContent("Agent chart unavailable");
    expect(notice).toHaveTextContent("network: check that the registry is reachable");
  });

  it.each([
    ["another reason", status("AgentConfigInvalid")],
    ["a true condition", status("AgentChartUnavailable", "True")],
  ])("shows no warning for %s", (_, data) => {
    render(<StatusTab data={data} isLoading={false} />);
    expect(screen.queryByTestId("chart-unavailable-notice")).not.toBeInTheDocument();
  });

  it("can leave the warning to the page", () => {
    render(<StatusTab data={status("AgentChartUnavailable")} isLoading={false} showChartNotice={false} />);
    expect(screen.queryByTestId("chart-unavailable-notice")).not.toBeInTheDocument();
  });
});
