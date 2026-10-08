// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
  Button,
  Text,
} from "@fluentui/react-components";
import { listAllInstances, updateInstanceLifecycle, type AdminInstanceSummary } from "../../api/adminInstance";
import { agentTypeLabel } from "../../utils/instance";

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
	const queryClient = useQueryClient();
  const { data, isLoading, error } = useQuery<AdminInstanceSummary[], Error>({
    queryKey: ["admin", "instances"],
    queryFn: listAllInstances,
    refetchInterval: 30_000,
  });
	const lifecycle = useMutation({
		mutationFn: ({ instance, suspended }: { instance: AdminInstanceSummary; suspended: boolean }) => updateInstanceLifecycle(instance, suspended),
		onSuccess: () => queryClient.invalidateQueries({ queryKey: ["admin", "instances"] }),
	});

  return (
    <div>
      <Title2 style={{ marginBottom: 16 }}>All agents</Title2>
      {isLoading && <Spinner label="Loading agents..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
	  {lifecycle.error && <MessageBar intent="error">{lifecycle.error.message}</MessageBar>}
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
				<TableCell>
				  <Button
					size="small"
					appearance={ins.suspended ? "primary" : "secondary"}
					disabled={lifecycle.isPending && lifecycle.variables?.instance.name === ins.name}
					onClick={() => lifecycle.mutate({ instance: ins, suspended: !ins.suspended })}
				  >
					{ins.suspended ? "Resume" : "Suspend"}
				  </Button>
				</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
