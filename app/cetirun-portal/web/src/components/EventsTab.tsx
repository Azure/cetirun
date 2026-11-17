// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Text, Spinner } from "@fluentui/react-components";
import type { EventItem, StatusResponse } from "../types/instance";
import { StatusLabel } from "./StatusLabel";

function EventList({ events, title }: { events: EventItem[]; title: string }) {
  return (
    <section>
      <h3 className="portal-section-title" style={{ marginBottom: 8 }}>{title}</h3>
      {events.length === 0 ? (
        <span className="portal-note">No events.</span>
      ) : (
        <div className="portal-table-scroll">
          <table className="portal-table">
            <thead>
              <tr>
                <th>Type</th>
                <th>Reason</th>
                <th>Message</th>
                <th>Source</th>
                <th>Count</th>
                <th>Last Seen</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e, i) => (
                <tr key={`${e.reason}-${e.lastSeen}-${i}`} className={e.type === "Warning" ? "is-warning" : undefined}>
                  <td><StatusLabel tone={e.type === "Warning" ? "warning" : "informative"} size="small">{e.type}</StatusLabel></td>
                  <td><Text weight="semibold" size={200}>{e.reason}</Text></td>
                  <td><Text size={200}>{e.message}</Text></td>
                  <td><Text size={200} className="portal-mono">{e.source}</Text></td>
                  <td><Text size={200}>{e.count}</Text></td>
                  <td>
                    <Text size={200} style={{ whiteSpace: "nowrap" }}>
                      {e.lastSeen ? new Date(e.lastSeen).toLocaleString() : "-"}
                    </Text>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

export function EventsTab({
  data,
  isLoading,
}: {
  data: StatusResponse | undefined;
  isLoading: boolean;
}) {
  if (isLoading) {
    return <Spinner size="small" label="Loading events..." style={{ justifyContent: "flex-start" }} />;
  }
  if (!data) {
    return <Text>No event data available.</Text>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 32 }}>
      <EventList events={data.events} title="Instance Events" />
      <EventList events={data.nsEvents} title="Namespace Events" />
    </div>
  );
}
