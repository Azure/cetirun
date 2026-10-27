// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useQuery } from "@tanstack/react-query";
import {
  Title2,
  Spinner,
  MessageBar,
  Table,
  TableHeader,
  TableHeaderCell,
  TableRow,
  TableBody,
  TableCell,
  Badge,
  Text,
  Button,
} from "@fluentui/react-components";
import { listAllInstances, type AdminInstanceSummary } from "../../api/adminInstance";
import { agentTypeLabel } from "../../utils/instance";
import { TeamChangeDialog } from "../../components/TeamChangeDialog";
import { useState } from "react";

function formatAge(createdAt?: string): string {
  if (!createdAt) return "—";
  const created = new Date(createdAt).getTime();
  if (Number.isNaN(created)) return "—";
  const seconds = Math.max(0, Math.floor((Date.now() - created) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}

export function InstanceListPage() {
  const [teamChangeInstance, setTeamChangeInstance] = useState<AdminInstanceSummary | null>(null);
  const { data, isLoading, error } = useQuery<AdminInstanceSummary[], Error>({
    queryKey: ["admin", "instances"],
    queryFn: listAllInstances,
    refetchInterval: 30_000,
  });

  return (
    <div>
      <Title2 style={{ marginBottom: 16 }}>All agents</Title2>
      {isLoading && <Spinner label="Loading agents..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
      {data && (
        <Table aria-label="All agents">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Alias</TableHeaderCell>
              <TableHeaderCell>Display name</TableHeaderCell>
              <TableHeaderCell>Team</TableHeaderCell>
              <TableHeaderCell>Agent</TableHeaderCell>
              <TableHeaderCell>Phase</TableHeaderCell>
              <TableHeaderCell>Age</TableHeaderCell>
              <TableHeaderCell>Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.length === 0 && (
              <TableRow>
                <TableCell colSpan={8}>
                  <Text>No agents.</Text>
                </TableCell>
              </TableRow>
            )}
            {data.map((ins) => (
              <TableRow key={ins.name}>
                <TableCell>{ins.name}</TableCell>
                <TableCell>{ins.alias}</TableCell>
                <TableCell>{ins.displayName || "—"}</TableCell>
                <TableCell>{ins.team || "—"}</TableCell>
                <TableCell>
                  <Badge appearance="outline">
                    {agentTypeLabel(ins.agent.type)}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Badge appearance="filled" color={ins.ready ? "success" : "warning"}>
                    {ins.phase || "Unknown"}
                  </Badge>
                </TableCell>
                <TableCell>{formatAge(ins.createdAt)}</TableCell>
                <TableCell><Button appearance="subtle" size="small" onClick={() => setTeamChangeInstance(ins)}>{ins.teamChangePending ? "View Team Change" : "Change Team"}</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {teamChangeInstance && <TeamChangeDialog instanceName={teamChangeInstance.name} open onOpenChange={(open) => { if (!open) setTeamChangeInstance(null); }} />}
    </div>
  );
}
