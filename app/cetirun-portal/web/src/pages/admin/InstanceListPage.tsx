// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
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
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  Select,
} from "@fluentui/react-components";
import { listAllInstances, type AdminInstanceSummary } from "../../api/adminInstance";
import { updateInstancePolicy } from "../../api/adminPolicy";
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
  const [editing, setEditing] = useState<AdminInstanceSummary>();
  const [team, setTeam] = useState(""); const [sandboxRef, setSandboxRef] = useState(""); const [policyClass, setPolicyClass] = useState<"normal" | "admin">("normal"); const [monthlyBudgetUSD, setMonthlyBudgetUSD] = useState("");
  const { data, isLoading, error } = useQuery<AdminInstanceSummary[], Error>({
    queryKey: ["admin", "instances"],
    queryFn: listAllInstances,
    refetchInterval: 30_000,
  });
  const patch = useMutation({ mutationFn: () => {
    if (!editing) return Promise.resolve();
    return updateInstancePolicy(editing.name, { uid: editing.uid, resourceVersion: editing.resourceVersion, team: team.trim(), sandboxRef: sandboxRef.trim(), policyClass, monthlyBudgetUSD: monthlyBudgetUSD.trim() });
  }, onSuccess: () => { queryClient.invalidateQueries({ queryKey: ["admin", "instances"] }); setEditing(undefined); } });
  const openPolicy = (instance: AdminInstanceSummary) => { setEditing(instance); setTeam(instance.team || ""); setSandboxRef(instance.sandboxRef || ""); setPolicyClass(instance.policyClass || "normal"); setMonthlyBudgetUSD(instance.monthlyBudgetUSD || ""); patch.reset(); };

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
              <TableHeaderCell>Policy</TableHeaderCell>
              <TableHeaderCell>Age</TableHeaderCell>
              <TableHeaderCell>Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.length === 0 && (
              <TableRow>
                <TableCell colSpan={9}>
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
                <TableCell><Badge appearance="outline" color={ins.policy?.phase === "Applied" ? "success" : ins.policy?.phase === "Denied" ? "danger" : "warning"}>{ins.policy?.phase || "Pending"}</Badge></TableCell>
                <TableCell>{formatAge(ins.createdAt)}</TableCell>
                <TableCell><Button size="small" appearance="secondary" disabled={!ins.suspended} title={ins.suspended ? "Change the policy assignment" : "Suspend the Agent before changing policy"} onClick={() => openPolicy(ins)}>Policy</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <Dialog open={!!editing} onOpenChange={(_, data) => { if (!data.open) setEditing(undefined); }}>
        <DialogSurface><DialogBody><DialogTitle>Agent policy: {editing?.name}</DialogTitle><DialogContent style={{ display: "flex", flexDirection: "column", gap: 12 }}>
          <Text size={200}>Policy changes are applied only while the Agent is suspended. Changing Team, Sandbox, or policy class generates the explicit administrator reauthorization required by the platform.</Text>
          <Field label="Team" required><Input value={team} onChange={(_, data) => setTeam(data.value)} /></Field>
          <Field label="Sandbox" required><Input value={sandboxRef} onChange={(_, data) => setSandboxRef(data.value)} /></Field>
          <Field label="Policy class"><Select value={policyClass} onChange={(_, data) => setPolicyClass(data.value as "normal" | "admin")}><option value="normal">normal</option><option value="admin">admin</option></Select></Field>
          <Field label="Monthly Gateway budget (USD)" hint="Leave empty to use the AgentPolicy default."><Input value={monthlyBudgetUSD} onChange={(_, data) => setMonthlyBudgetUSD(data.value)} /></Field>
          {patch.error && <MessageBar intent="error">{patch.error.message}</MessageBar>}
        </DialogContent><DialogActions><Button appearance="secondary" onClick={() => setEditing(undefined)}>Cancel</Button><Button appearance="primary" disabled={patch.isPending} onClick={() => patch.mutate()}>Save policy</Button></DialogActions></DialogBody></DialogSurface>
      </Dialog>
    </div>
  );
}
