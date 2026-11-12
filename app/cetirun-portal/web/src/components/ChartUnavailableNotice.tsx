// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { MessageBar, MessageBarBody, MessageBarTitle } from "@fluentui/react-components";
import type { ConditionStatus } from "../types/instance";

/**
 * A non-blocking warning when the Operator cannot download the Agent's chart
 * again. The Agent keeps running its deployed release; the condition message
 * tells the kind of failure (for example, an expired chart-pull credential).
 */
export function ChartUnavailableNotice({ conditions }: { conditions: ConditionStatus[] | undefined }) {
  const condition = conditions?.find(
    (c) => c.type === "AgentConfigReady" && c.status === "False" && c.reason === "AgentChartUnavailable",
  );
  if (!condition) return null;
  return (
    <MessageBar intent="warning" data-testid="chart-unavailable-notice">
      <MessageBarBody>
        <MessageBarTitle>Agent chart unavailable</MessageBarTitle>
        {condition.message}
      </MessageBarBody>
    </MessageBar>
  );
}
