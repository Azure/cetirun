// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Fragment, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  MessageBar,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
} from "@fluentui/react-components";
import { Add20Regular, Delete20Regular, Edit20Regular } from "@fluentui/react-icons";
import { deletePolicy, listPolicies, type AgentPolicyDTO } from "../../api/adminPolicy";
import { listTeams, type TeamDTO } from "../../api/adminTeam";
import { describeRateLimits } from "../../utils/rateLimits";
import { CompatibilityReferences } from "../../components/CompatibilityReport";
import { StatusLabel, type StatusTone } from "../../components/StatusLabel";

function modelAccessLabel(policy: AgentPolicyDTO): string {
  const labels: string[] = [];
  if (policy.modelAccess?.copilot?.allowed) labels.push("Copilot");
  const shared = policy.modelAccess?.sharedModels?.allowed?.length ?? 0;
  if (shared > 0) labels.push(`${shared} shared`);
  if (describeRateLimits(policy.modelAccess?.rateLimits).length > 0) labels.push("rate limits");
  return labels.join(", ") || "—";
}

function acceptedLabel(policy: AgentPolicyDTO): string {
  const condition = acceptedCondition(policy);
  if (!condition) return "Pending";
  return condition.status === "True" ? "Accepted" : condition.status === "False" ? "Rejected" : "Unknown";
}

const CONFIGURATION_TONES: Record<string, StatusTone> = { Accepted: "success", Rejected: "danger", Unknown: "warning", Pending: "subtle", Deleting: "informative" };

function ConfigurationStatus({ policy }: { policy: AgentPolicyDTO }) {
  const label = policy.deleting ? "Deleting" : acceptedLabel(policy);
  return <StatusLabel tone={CONFIGURATION_TONES[label]}>{label}</StatusLabel>;
}

function acceptedCondition(policy: AgentPolicyDTO) {
  return policy.conditions?.find((candidate) => candidate.type === "Accepted" && candidate.observedGeneration === policy.generation);
}

function conditionDetails(condition: { status: string; reason?: string; message?: string } | undefined) {
  if (!condition || condition.status === "True" || (!condition.reason && !condition.message)) return undefined;
  return [condition.reason, condition.message].filter(Boolean).join(": ");
}

export function PolicyListPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: policies, isLoading, error } = useQuery<AgentPolicyDTO[], Error>({
    queryKey: ["admin", "policies"],
    queryFn: listPolicies,
    refetchInterval: 15_000,
  });
  const teamsQuery = useQuery<TeamDTO[], Error>({ queryKey: ["admin", "teams"], queryFn: listTeams, refetchInterval: 15_000 });
  const [pendingDelete, setPendingDelete] = useState<AgentPolicyDTO | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const deleteMutation = useMutation({
    mutationFn: (policy: AgentPolicyDTO) => deletePolicy(policy),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["admin", "policies"] });
      setPendingDelete(null);
    },
    onError: (error: Error) => setDeleteError(error.message),
  });

  return (
    <div>
      <div className="portal-page-head">
        <h1 className="portal-page-title portal-page-title--section">Agent policies</h1>
        <Button appearance="primary" icon={<Add20Regular />} onClick={() => navigate("/admin/policies/new")}>
          New policy
        </Button>
      </div>
      <p className="portal-page-lede">
        Policies declare which harnesses, skills, and AI Gateway model access an Agent may use. Assign a Policy and its allowed Sandboxes in the Team editor.
      </p>
      {isLoading && <Spinner label="Loading policies..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
      {deleteError && <MessageBar intent="error">{deleteError}</MessageBar>}
      {policies && (
        <div className="portal-table-scroll"><Table aria-label="Agent policies">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Harnesses</TableHeaderCell>
              <TableHeaderCell>Skills</TableHeaderCell>
              <TableHeaderCell>Model access</TableHeaderCell>
              <TableHeaderCell>Configuration</TableHeaderCell>
              <TableHeaderCell className="portal-col-actions">Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {policies.length === 0 && (
              <TableRow><TableCell colSpan={6}>No policies yet.</TableCell></TableRow>
            )}
            {policies.map((policy) => <Fragment key={policy.name}>
              <TableRow>
                <TableCell>{policy.name}</TableCell>
                <TableCell>{policy.harnesses.allowed.join(", ")}</TableCell>
                <TableCell>{policy.skills?.allowed?.length ?? 0}</TableCell>
                <TableCell>{modelAccessLabel(policy)}</TableCell>
                <TableCell><ConfigurationStatus policy={policy} /></TableCell>
                <TableCell><div className="portal-row-actions">
                  <Button size="small" appearance="subtle" onClick={() => setExpanded(expanded === policy.name ? null : policy.name)} aria-expanded={expanded === policy.name} aria-label={`Show Teams using ${policy.name}`}>Teams</Button>
                  <Button size="small" appearance="subtle" icon={<Edit20Regular />} aria-label={`Edit ${policy.name}`} disabled={policy.deleting} onClick={() => navigate(`/admin/policies/${policy.name}/edit`)} />
                  <Button size="small" appearance="subtle" icon={<Delete20Regular />} aria-label={`Delete ${policy.name}`} disabled={policy.deleting} onClick={() => { setDeleteError(null); setPendingDelete(policy); }} />
                </div></TableCell>
              </TableRow>
              {expanded === policy.name && <TableRow className="portal-row-detail"><TableCell colSpan={6}>
                {conditionDetails(acceptedCondition(policy)) && <Text block size={200} style={{ marginBottom: 8 }}>
                  Configuration: {conditionDetails(acceptedCondition(policy))}
                </Text>}
                {teamsQuery.isLoading && <Spinner size="tiny" label="Loading referencing Teams..." />}
                {teamsQuery.error && <MessageBar intent="error">Failed to load Teams: {teamsQuery.error.message}</MessageBar>}
                {teamsQuery.data && <CompatibilityReferences teams={teamsQuery.data.filter((team) => team.agentPolicyRef === policy.name)} dependency={{ kind: "policy", name: policy.name, uid: policy.uid, generation: policy.generation ?? 0, deleting: policy.deleting }} />}
              </TableCell></TableRow>}
            </Fragment>)}
          </TableBody>
        </Table></div>
      )}
      <Dialog open={pendingDelete !== null} onOpenChange={(_, data) => { if (!data.open) setPendingDelete(null); }}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Delete policy?</DialogTitle>
            <DialogContent>Deleting <strong>{pendingDelete?.name}</strong> can deny Agents assigned to Teams that use it.</DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement><Button appearance="secondary">Cancel</Button></DialogTrigger>
              <Button className="portal-danger" appearance="primary" disabled={deleteMutation.isPending || !pendingDelete} onClick={() => {
                if (pendingDelete) deleteMutation.mutate(pendingDelete);
              }}>Delete</Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
