// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Text, Spinner, tokens } from "@fluentui/react-components";
import type { StatusResponse } from "../types/instance";
import { ChartUnavailableNotice } from "./ChartUnavailableNotice";
import { StatusLabel, type StatusTone } from "./StatusLabel";

function conditionTone(status: string): StatusTone {
  if (status === "True") return "success";
  if (status === "False") return "danger";
  return "informative";
}

function podPhaseTone(phase: string): StatusTone {
  if (phase === "Running") return "success";
  if (phase === "Pending") return "warning";
  if (phase === "Failed" || phase === "Unknown") return "danger";
  return "informative";
}

export function StatusTab({
  data,
  isLoading,
  showChartNotice = true,
}: {
  data: StatusResponse | undefined;
  isLoading: boolean;
  /** Show the chart download warning. Pages that show it elsewhere turn it off. */
  showChartNotice?: boolean;
}) {
  if (isLoading) {
    return <Spinner size="small" label="Loading status..." style={{ justifyContent: "flex-start" }} />;
  }
  if (!data) {
    return <Text>No status data available.</Text>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 32 }}>
      {showChartNotice && <ChartUnavailableNotice conditions={data.conditions} />}
      {/* Conditions */}
      <section>
        <h3 className="portal-section-title" style={{ marginBottom: 8 }}>Conditions</h3>
        {data.conditions.length === 0 ? (
          <span className="portal-note">No conditions reported yet.</span>
        ) : (
          <div className="portal-table-scroll">
            <table className="portal-table">
              <thead>
                <tr>
                  <th>Type</th>
                  <th>Status</th>
                  <th>Reason</th>
                  <th>Message</th>
                </tr>
              </thead>
              <tbody>
                {data.conditions.map((c) => (
                  <tr key={c.type}>
                    <td><Text weight="semibold" size={200}>{c.type}</Text></td>
                    <td><StatusLabel tone={conditionTone(c.status)} size="small">{c.status}</StatusLabel></td>
                    <td><Text size={200}>{c.reason}</Text></td>
                    <td><Text size={200}>{c.message}</Text></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Pods */}
      <section>
        <h3 className="portal-section-title" style={{ marginBottom: 8 }}>Pods</h3>
        {data.pods.length === 0 ? (
          <span className="portal-note">No pods found.</span>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            {data.pods.map((pod) => (
              <div key={pod.name} className="portal-tile portal-tile-head" style={{ columnGap: 20 }}>
                <span className="portal-mono">{pod.name}</span>
                <StatusLabel tone={podPhaseTone(pod.phase)} size="small">{pod.phase}</StatusLabel>
                <StatusLabel tone={pod.ready ? "success" : "danger"} size="small">{pod.ready ? "Ready" : "Not Ready"}</StatusLabel>
                {pod.restartCount > 0 && (
                  <Text size={200} style={{ color: tokens.colorStatusWarningForeground1 }}>
                    Restarts: {pod.restartCount}
                  </Text>
                )}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
