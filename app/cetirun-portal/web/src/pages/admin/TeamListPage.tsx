// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Fragment, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Title2,
  Button,
  Spinner,
  MessageBar,
  Table,
  TableHeader,
  TableHeaderCell,
  TableRow,
  TableBody,
  TableCell,
  Dialog,
  DialogTrigger,
  DialogSurface,
  DialogTitle,
  DialogBody,
  DialogActions,
  DialogContent,
  Text,
} from "@fluentui/react-components";
import { Add20Regular, Delete20Regular, Edit20Regular } from "@fluentui/react-icons";
import { getTeam, listTeams, deleteTeam, type TeamDTO } from "../../api/adminTeam";
import { AdminConfigurationError } from "../../api/adminError";
import { CompatibilityBadge, CompatibilityReport } from "../../components/CompatibilityReport";

export function TeamListPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { data: teams, isLoading, error } = useQuery<TeamDTO[], Error>({
    queryKey: ["admin", "teams"],
    queryFn: listTeams,
    refetchInterval: 15_000,
  });
  const [pendingDelete, setPendingDelete] = useState<TeamDTO | null>(null);
  const [deleteError, setDeleteError] = useState<{ message: string; uid: string; generation: number } | null>(null);
  const [reloadingDelete, setReloadingDelete] = useState(false);
  const deleteEpoch = useRef(0);
  const [expanded, setExpanded] = useState<string | null>(null);

  const delMutation = useMutation({
    mutationFn: ({ team }: { team: TeamDTO; epoch: number }) => deleteTeam(team),
    onSuccess: (_, { team: deleted, epoch }) => {
      if (deleteEpoch.current !== epoch) return;
      qc.invalidateQueries({ queryKey: ["admin", "teams"] });
      setPendingDelete(current => current?.uid === deleted.uid && current.generation === deleted.generation ? null : current);
    },
    onError: (e: Error, { team: attempted, epoch }) => {
      if (deleteEpoch.current === epoch) setDeleteError({ message: e.message, uid: attempted.uid, generation: attempted.generation });
    },
  });
  const reloadDelete = async () => {
    if (!pendingDelete) return;
    const requested = pendingDelete;
    const name = requested.name;
    const epoch = deleteEpoch.current;
    setReloadingDelete(true);
    try {
      const current = await getTeam(name);
      if (deleteEpoch.current !== epoch) return;
      setPendingDelete(previous => previous?.name === name ? current : previous);
      setDeleteError(null);
      void qc.invalidateQueries({ queryKey: ["admin", "teams"] });
    } catch (e) {
      if (deleteEpoch.current !== epoch) return;
      if (e instanceof AdminConfigurationError && e.code === "team_not_found") {
        setPendingDelete(previous => previous?.name === name ? null : previous);
        setDeleteError(null);
        void qc.invalidateQueries({ queryKey: ["admin", "teams"] });
        return;
      }
      setDeleteError({ message: `Failed to reload Team: ${e instanceof Error ? e.message : "Try again."}`, uid: requested.uid, generation: requested.generation });
    } finally {
      if (deleteEpoch.current === epoch) setReloadingDelete(false);
    }
  };
  const activeDeleteError = deleteError && pendingDelete && deleteError.uid === pendingDelete.uid && deleteError.generation === pendingDelete.generation
    ? deleteError.message : null;

  return (
    <div>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          marginBottom: 16,
        }}
      >
        <Title2>Teams</Title2>
        <Button
          appearance="primary"
          icon={<Add20Regular />}
          onClick={() => navigate("/admin/teams/new")}
        >
          New Team
        </Button>
      </div>

      {isLoading && <Spinner label="Loading teams..." />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}

      {teams && (
        <Table aria-label="Teams">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Role</TableHeaderCell>
              <TableHeaderCell>Agent Policy</TableHeaderCell>
              <TableHeaderCell>Default Sandbox</TableHeaderCell>
              <TableHeaderCell>Compatibility</TableHeaderCell>
              <TableHeaderCell>Skills</TableHeaderCell>
              <TableHeaderCell style={{ width: 120 }}>Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {teams.length === 0 && (
              <TableRow>
                  <TableCell colSpan={7}>
                  <Text>No teams yet.</Text>
                </TableCell>
              </TableRow>
            )}
            {teams.map((t) => <Fragment key={t.name}>
              <TableRow>
                <TableCell>{t.name}</TableCell>
                <TableCell>{t.role || "—"}</TableCell>
                <TableCell>{t.agentPolicyRef || "—"}</TableCell>
                <TableCell>{t.defaultSandboxRef || "—"}</TableCell>
                <TableCell><CompatibilityBadge team={t} /></TableCell>
                <TableCell>{t.skills?.length ?? 0}</TableCell>
                <TableCell>
                  <Button size="small" appearance="subtle" onClick={() => setExpanded(expanded === t.name ? null : t.name)} aria-expanded={expanded === t.name} aria-label={`Show compatibility for ${t.name}`}>Details</Button>
                  <Button
                    size="small"
                    icon={<Edit20Regular />}
                    appearance="subtle"
                    disabled={t.deleting}
                    onClick={() => navigate(`/admin/teams/${encodeURIComponent(t.name)}/edit`)}
                    aria-label={`Edit ${t.name}`}
                  />
                  <Button
                    size="small"
                    icon={<Delete20Regular />}
                    appearance="subtle"
                    disabled={t.deleting}
                    onClick={() => {
                      setDeleteError(null);
                      deleteEpoch.current++;
                      setPendingDelete(t);
                    }}
                    aria-label={`Delete ${t.name}`}
                  />
                </TableCell>
              </TableRow>
              {expanded === t.name && <TableRow><TableCell colSpan={7}><CompatibilityReport team={t} /></TableCell></TableRow>}
            </Fragment>)}
          </TableBody>
        </Table>
      )}

      <Dialog
        open={pendingDelete !== null}
        onOpenChange={(_, data) => {
          if (!data.open && !delMutation.isPending) {
            deleteEpoch.current++;
            setPendingDelete(null);
            setDeleteError(null);
          }
        }}
      >
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Delete team?</DialogTitle>
            <DialogContent>
              Are you sure you want to delete team{" "}
              <strong>{pendingDelete?.name}</strong>? This cannot be undone.
              {pendingDelete && <Text block size={200} style={{ marginTop: 8 }}>
                Role: {pendingDelete.role || "—"}. Agent Policy: {pendingDelete.agentPolicyRef || "—"}. Sandboxes: {pendingDelete.sandboxRefs.join(", ") || "—"}.
              </Text>}
              {activeDeleteError && <MessageBar intent="error" style={{ marginTop: 12 }}>{activeDeleteError}</MessageBar>}
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button appearance="secondary">Cancel</Button>
              </DialogTrigger>
              <Button
                appearance="primary"
                disabled={delMutation.isPending || reloadingDelete || !!activeDeleteError || !!pendingDelete?.deleting}
                onClick={() => { if (pendingDelete) delMutation.mutate({ team: pendingDelete, epoch: deleteEpoch.current }); }}
              >
                Delete
              </Button>
              {activeDeleteError && <Button appearance="secondary" disabled={delMutation.isPending || reloadingDelete} onClick={() => void reloadDelete()}>
                {reloadingDelete ? "Reloading..." : "Reload latest Team"}
              </Button>}
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
