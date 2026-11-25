// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMemo, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  Spinner,
  MessageBar,
  Table,
  TableHeader,
  TableHeaderCell,
  TableRow,
  TableBody,
  TableCell,
  Text,
  Input,
  Field,
  Select,
} from "@fluentui/react-components";
import { listAllInstances, type AdminInstanceSummary } from "../../api/adminInstance";
import { agentTypeLabel } from "../../utils/instance";
import { budgetAmounts, keyStateText } from "../../utils/budget";
import { GovernanceNotice } from "../../components/GovernanceNotice";
import { AgentPhase, lifecycleState, type LifecycleState } from "../../components/Lifecycle";
import { StatusLabel } from "../../components/StatusLabel";
import type { AIGatewayBudget } from "../../types/instance";

/** The glyph is green only when the Agent is ready, whatever its phase says. */
function listedState(instance: AdminInstanceSummary): LifecycleState {
  if (instance.ready) return "ready";
  const state = lifecycleState(instance.phase);
  return state === "ready" ? "waiting" : state;
}

/** The spend of the key against its budget, and why the AI Gateway refuses the key. */
function BudgetCell({ budget }: { budget?: AIGatewayBudget }) {
  if (!budget) return <>—</>;
  const state = keyStateText(budget.state);
  return (
    <div style={{ display: "flex", flexDirection: "column", alignItems: "flex-start" }}>
      <span>{budgetAmounts(budget)}</span>
      {state && <StatusLabel tone={state.tone} size="small">{state.label}</StatusLabel>}
    </div>
  );
}

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
  const location = useLocation();
  const [nameFilter, setNameFilter] = useState("");
  const [ownerFilter, setOwnerFilter] = useState("");
  const [teamFilter, setTeamFilter] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [phaseFilter, setPhaseFilter] = useState("");
  const { data, isLoading, error } = useQuery<AdminInstanceSummary[], Error>({
    queryKey: ["admin", "instances"],
    queryFn: listAllInstances,
    refetchInterval: 30_000,
  });
  const agents = useMemo(() => (data || []).filter((instance) => {
    const name = nameFilter.trim().toLowerCase();
    const owner = ownerFilter.trim().toLowerCase();
    const team = teamFilter.trim().toLowerCase();
    return (!name || instance.name.toLowerCase().includes(name)) &&
      (!owner || instance.alias.toLowerCase().includes(owner) || instance.email?.toLowerCase().includes(owner)) &&
      (!team || instance.team?.toLowerCase().includes(team)) &&
      (!typeFilter || instance.agent.type === typeFilter) &&
      (!phaseFilter || instance.phase === phaseFilter);
  }), [data, nameFilter, ownerFilter, teamFilter, typeFilter, phaseFilter]);
  const types = useMemo(() => [...new Set((data || []).map((instance) => instance.agent.type))].sort(), [data]);
  const phases = useMemo(() => [...new Set((data || []).map((instance) => instance.phase).filter(Boolean))].sort(), [data]);

  return (
    <div>
      <div className="portal-page-head">
        <h1 className="portal-page-title portal-page-title--section">All agents</h1>
      </div>
      {location.state?.deletedAgent && <MessageBar intent="success">Agent {location.state.deletedAgent} has been deleted.</MessageBar>}
      {location.state?.missingAgent && <MessageBar intent="info">Agent {location.state.missingAgent} no longer exists.</MessageBar>}
      {isLoading && <Spinner label="Loading agents..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
      {data && (
        <>
        <div className="portal-filters">
          <Field label="Name"><Input value={nameFilter} onChange={(_, value) => setNameFilter(value.value)} /></Field>
          <Field label="Owner"><Input value={ownerFilter} onChange={(_, value) => setOwnerFilter(value.value)} /></Field>
          <Field label="Team"><Input value={teamFilter} onChange={(_, value) => setTeamFilter(value.value)} /></Field>
          <Field label="Type"><Select aria-label="Type" value={typeFilter} onChange={(event) => setTypeFilter(event.target.value)}><option value="">All types</option>{types.map((type) => <option key={type} value={type}>{agentTypeLabel(type)}</option>)}</Select></Field>
          <Field label="Phase"><Select aria-label="Phase" value={phaseFilter} onChange={(event) => setPhaseFilter(event.target.value)}><option value="">All phases</option>{phases.map((phase) => <option key={phase} value={phase}>{phase}</option>)}</Select></Field>
        </div>
        <div className="portal-table-scroll"><Table aria-label="All agents" style={{ minWidth: 1080 }}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell style={{ width: 100 }}>Alias</TableHeaderCell>
              <TableHeaderCell>Display name</TableHeaderCell>
              <TableHeaderCell style={{ width: 120 }}>Team</TableHeaderCell>
              <TableHeaderCell style={{ width: 96 }}>Agent</TableHeaderCell>
              <TableHeaderCell style={{ width: 190 }}>Status</TableHeaderCell>
              <TableHeaderCell style={{ width: 140 }}>Spent (USD)</TableHeaderCell>
              <TableHeaderCell style={{ width: 56 }}>Age</TableHeaderCell>
              <TableHeaderCell style={{ width: 104 }}>Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {agents.length === 0 && (
              <TableRow>
                <TableCell colSpan={9}>
                  <Text>{data.length === 0 ? "No agents." : "No agents match the filters."}</Text>
                </TableCell>
              </TableRow>
            )}
            {agents.map((ins) => (
              <TableRow key={ins.name}>
                <TableCell><Link to={`/admin/instances/${encodeURIComponent(ins.name)}`}>{ins.name}</Link></TableCell>
                <TableCell>{ins.alias}</TableCell>
                <TableCell>{ins.displayName || "—"}</TableCell>
                <TableCell>{ins.team || "—"}</TableCell>
                <TableCell>{agentTypeLabel(ins.agent.type)}</TableCell>
                <TableCell>
                  <AgentPhase phase={ins.phase || "Unknown"} state={listedState(ins)} />
                  <GovernanceNotice instance={ins} compact />
                </TableCell>
                <TableCell><BudgetCell budget={ins.aiGatewayBudget} /></TableCell>
                <TableCell>{formatAge(ins.createdAt)}</TableCell>
                <TableCell><Link to={`/admin/instances/${encodeURIComponent(ins.name)}`}>View details</Link></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table></div>
        </>
      )}
    </div>
  );
}
