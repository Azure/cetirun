// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";
import {
  Badge,
  Button,
  MessageBar,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
  Title2,
} from "@fluentui/react-components";
import { ArrowLeft20Regular, ArrowRight20Regular } from "@fluentui/react-icons";
import {
  decideGovernanceAction,
  getGovernanceAction,
  listGovernanceActions,
  type GovernanceAction,
} from "../../api/adminGovernance";

const actionsKey = ["admin", "governance", "actions"] as const;

function phase(action: GovernanceAction): string {
  return action.status?.phase || (action.spec.approval ? action.spec.approval.decision : "Proposed");
}

function canDecide(action: GovernanceAction): boolean {
  const current = phase(action);
  return !action.spec.approval && (current === "Proposed" || current === "AwaitingApproval");
}

function phaseColor(current: string): "success" | "warning" | "danger" | "informative" | "important" | "subtle" {
  if (current === "Succeeded" || current === "Approved") return "success";
  if (current === "Denied" || current === "Failed" || current === "Expired") return "danger";
  if (current === "Executing") return "informative";
  return "warning";
}

function formatTime(value?: string): string {
  if (!value) return "—";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

export function GovernanceActionsPage() {
  const navigate = useNavigate();
  const { data, isLoading, error } = useQuery<GovernanceAction[], Error>({
    queryKey: actionsKey,
    queryFn: listGovernanceActions,
    refetchInterval: 15_000,
  });
  return (
    <div>
      <Title2 style={{ marginBottom: 8 }}>Governance actions</Title2>
      <Text block style={{ marginBottom: 16 }}>
        CetiAdmin can propose only narrow operational actions. A platform administrator reviews and approves or denies each proposal here.
      </Text>
      {isLoading && <Spinner label="Loading governance actions..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
      {data && (
        <Table aria-label="Governance actions">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Action</TableHeaderCell>
              <TableHeaderCell>Agent</TableHeaderCell>
              <TableHeaderCell>Requester</TableHeaderCell>
              <TableHeaderCell>Phase</TableHeaderCell>
              <TableHeaderCell>Expires</TableHeaderCell>
              <TableHeaderCell>Details</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.length === 0 && <TableRow><TableCell colSpan={6}>No governance actions.</TableCell></TableRow>}
            {data.map((action) => (
              <TableRow key={action.metadata.name}>
                <TableCell>{action.spec.operation}</TableCell>
                <TableCell>{action.spec.targetRef.name}</TableCell>
                <TableCell>{action.spec.requester.subject}</TableCell>
                <TableCell><Badge appearance="filled" color={phaseColor(phase(action))}>{phase(action)}</Badge></TableCell>
                <TableCell>{formatTime(action.spec.expiresAt)}</TableCell>
                <TableCell><Button size="small" appearance="subtle" icon={<ArrowRight20Regular />} onClick={() => navigate(`/admin/governance/actions/${action.metadata.name}`)}>Review</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

export function GovernanceActionDetailPage() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: action, isLoading, error } = useQuery<GovernanceAction, Error>({
    queryKey: [...actionsKey, name],
    queryFn: () => getGovernanceAction(name!),
    enabled: !!name,
    refetchInterval: 8_000,
  });
  const decision = useMutation({
    mutationFn: (value: "Approved" | "Denied") => decideGovernanceAction(name!, value),
    onSuccess: (updated) => {
      queryClient.setQueryData([...actionsKey, name], updated);
      queryClient.invalidateQueries({ queryKey: actionsKey });
    },
  });
  if (isLoading) return <Spinner label="Loading governance action..." />;
  if (error || !action) return <MessageBar intent="error">Failed to load: {error?.message || "Not found"}</MessageBar>;
  const currentPhase = phase(action);
  return (
    <div style={{ maxWidth: 900, display: "flex", flexDirection: "column", gap: 16 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <Button appearance="subtle" icon={<ArrowLeft20Regular />} onClick={() => navigate("/admin/governance/actions")}>Actions</Button>
        <Title2>Governance action: {action.metadata.name}</Title2>
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "180px 1fr", gap: "8px 16px" }}>
        <Text weight="semibold">Operation</Text><Text>{action.spec.operation}</Text>
        <Text weight="semibold">Target</Text><Text>{action.spec.targetRef.name} (generation {action.spec.targetRef.generation})</Text>
        <Text weight="semibold">Requester</Text><Text>{action.spec.requester.kind}: {action.spec.requester.subject}</Text>
        <Text weight="semibold">Requested</Text><Text>{formatTime(action.spec.requestedAt)}</Text>
        <Text weight="semibold">Expires</Text><Text>{formatTime(action.spec.expiresAt)}</Text>
        <Text weight="semibold">Phase</Text><Text><Badge appearance="filled" color={phaseColor(currentPhase)}>{currentPhase}</Badge></Text>
        <Text weight="semibold">Reason</Text><Text>{action.spec.reason}</Text>
        {action.status?.resultSummary && <><Text weight="semibold">Result</Text><Text>{action.status.resultSummary}</Text></>}
        {action.spec.approval && <>
          <Text weight="semibold">Decision</Text><Text>{action.spec.approval.decision} by {action.spec.approval.approver.objectID} at {formatTime(action.spec.approval.approvedAt)}</Text>
        </>}
      </div>
      <Text weight="semibold">Expected governance projections</Text>
      <pre style={{ margin: 0, padding: 12, overflow: "auto", borderRadius: 4, background: "var(--colorNeutralBackground2)" }}>{JSON.stringify(action.spec.expectedProjections, null, 2)}</pre>
      {decision.error && <MessageBar intent="error">{decision.error.message}</MessageBar>}
      {canDecide(action) && (
        <MessageBar intent="warning">
          Confirm that the target, reason and expected revisions are still appropriate before deciding. {" "}
          <Button appearance="primary" size="small" disabled={decision.isPending} onClick={() => decision.mutate("Approved")}>Approve</Button>
          <Button appearance="secondary" size="small" disabled={decision.isPending} onClick={() => decision.mutate("Denied")}>Deny</Button>
        </MessageBar>
      )}
    </div>
  );
}
