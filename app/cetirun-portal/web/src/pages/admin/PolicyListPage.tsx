// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useState } from "react";
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
  Title2,
} from "@fluentui/react-components";
import { Add20Regular, Delete20Regular, Edit20Regular } from "@fluentui/react-icons";
import { deletePolicy, listPolicies, type AgentPolicyDTO } from "../../api/adminPolicy";

function modelAccessLabel(policy: AgentPolicyDTO): string {
  const labels: string[] = [];
  if (policy.modelAccess?.copilot?.allowed) labels.push("Copilot");
  const shared = policy.modelAccess?.sharedModels?.allowed?.length ?? 0;
  if (shared > 0) labels.push(`${shared} shared`);
  return labels.join(", ") || "—";
}

export function PolicyListPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: policies, isLoading, error } = useQuery<AgentPolicyDTO[], Error>({
    queryKey: ["admin", "policies"],
    queryFn: listPolicies,
    refetchInterval: 30_000,
  });
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
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
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 16 }}>
        <Title2>Agent policies</Title2>
        <Button appearance="primary" icon={<Add20Regular />} onClick={() => navigate("/admin/policies/new")}>
          New policy
        </Button>
      </div>
      <Text block style={{ marginBottom: 16 }}>
        Policies declare which harnesses, skills, and AI Gateway model access an Agent may use. Assign a Policy and its allowed Sandboxes in the Team editor.
      </Text>
      {isLoading && <Spinner label="Loading policies..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
      {deleteError && <MessageBar intent="error">{deleteError}</MessageBar>}
      {policies && (
        <Table aria-label="Agent policies">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Harnesses</TableHeaderCell>
              <TableHeaderCell>Skills</TableHeaderCell>
              <TableHeaderCell>Model access</TableHeaderCell>
              <TableHeaderCell style={{ width: 120 }}>Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {policies.length === 0 && (
              <TableRow><TableCell colSpan={5}>No policies yet.</TableCell></TableRow>
            )}
            {policies.map((policy) => (
              <TableRow key={policy.name}>
                <TableCell>{policy.name}</TableCell>
                <TableCell>{policy.harnesses.allowed.join(", ")}</TableCell>
                <TableCell>{policy.skills?.allowed?.length ?? 0}</TableCell>
                <TableCell>{modelAccessLabel(policy)}</TableCell>
                <TableCell>
                  <Button size="small" appearance="subtle" icon={<Edit20Regular />} aria-label={`Edit ${policy.name}`} disabled={policy.deleting} onClick={() => navigate(`/admin/policies/${policy.name}/edit`)} />
                  <Button size="small" appearance="subtle" icon={<Delete20Regular />} aria-label={`Delete ${policy.name}`} disabled={policy.deleting} onClick={() => { setDeleteError(null); setPendingDelete(policy.name); }} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <Dialog open={pendingDelete !== null} onOpenChange={(_, data) => { if (!data.open) setPendingDelete(null); }}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Delete policy?</DialogTitle>
            <DialogContent>Deleting <strong>{pendingDelete}</strong> can deny Agents assigned to Teams that use it.</DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement><Button appearance="secondary">Cancel</Button></DialogTrigger>
              <Button appearance="primary" disabled={deleteMutation.isPending} onClick={() => {
                const policy = policies?.find((item) => item.name === pendingDelete);
                if (policy) deleteMutation.mutate(policy);
              }}>Delete</Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
