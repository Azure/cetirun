// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Button, Dialog, DialogActions, DialogBody, DialogContent, DialogSurface, DialogTitle, Spinner, Text } from "@fluentui/react-components";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { deleteConnection, getConnectionUsage, reconnectConnection, type Connection } from "../api/toolAuthorization";
import { useToolConnections } from "../hooks/useToolAuthorization";
import { useConnectionSignIn } from "../hooks/useConnectionSignIn";
import { providerDisabledReasons, providerLabels } from "./ToolAuthorizationBlock";

function Account({ connection }: { connection: Connection }) {
  const client = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const signin = useConnectionSignIn(connection.id);
  const usage = useQuery({ queryKey: ["connection-usage", connection.id], queryFn: ({ signal }) => getConnectionUsage(connection.id, signal) });
  const remove = useMutation({ mutationFn: () => deleteConnection(connection.id), onSuccess: () => client.invalidateQueries({ queryKey: ["tool-connections"] }), onError: () => { setConfirm(false); void usage.refetch(); } });
  return <div style={{ borderBottom: "1px solid #ccc", padding: "12px 0" }}>
    <Text weight="semibold" block>{providerLabels[connection.provider]} — {connection.accountDisplay || "Unfinished sign-in"}</Text>
    {providerDisabledReasons[connection.provider] && <Text size={200} block>{providerLabels[connection.provider]}: {providerDisabledReasons[connection.provider]}</Text>}
    <Text size={200} block>{connection.status === "connected" ? "Authorized" : connection.status.replace(/_/g, " ")} · Added {new Date(connection.createdAt).toLocaleDateString()} · {connection.id.slice(-8)}</Text>
    <Text size={200} block>OAuth scopes reported by the provider: {connection.scopes?.join(", ") || "Not reported"}</Text>
    {usage.isLoading && <Spinner size="tiny" label="Checking account usage..." />}
    {usage.data && <Text size={200} block>{usage.data.agents.length ? `Used by: ${usage.data.agents.map((a) => a.instanceName || a.instanceUid).join(", ")}. Disconnect in each Agent before removing this account.` : "No agents use this account."}</Text>}
    {usage.error && <><Text role="alert" block>Could not check account usage.</Text><Button onClick={() => void usage.refetch()}>Retry</Button></>}
    {remove.error && <Text role="alert" block>{remove.error.message}</Text>}
    {signin.message && <Text role="status" block>{signin.message}</Text>}
    <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
      <Button disabled={!!providerDisabledReasons[connection.provider] || signin.pending || remove.isPending || connection.status === "connecting"} onClick={() => signin.begin(() => reconnectConnection(connection.id), () => {})}>Reauthorize</Button>
      <Button disabled={!usage.data?.canRemove || usage.isError || usage.isFetching || signin.pending || remove.isPending} onClick={() => setConfirm(true)}>Remove</Button>
    </div>
    {confirm && <div><Text block>Remove this account from CetiRun? Provider permissions remain unchanged.</Text><Button disabled={remove.isPending} onClick={() => setConfirm(false)}>Cancel</Button><Button disabled={remove.isPending || !usage.data?.canRemove} onClick={() => remove.mutate()}>Remove account</Button></div>}
  </div>;
}

export default function MyAccountsDialog({ onClose }: { onClose: () => void }) {
  const { connections } = useToolConnections(true);
  const accounts = (connections.data ?? []).filter((c) => c.mode === "delegated");
  return <Dialog open onOpenChange={(_, d) => { if (!d.open) onClose(); }}><DialogSurface><DialogBody>
    <DialogTitle>My accounts</DialogTitle><DialogContent>
      <Text block>Personal accounts you have authorized. Connect an account to an Agent from its Connections section.</Text>
      {connections.isLoading && <Spinner size="tiny" label="Loading accounts..." />}
      {connections.error && <><Text role="alert" block>{connections.error.message}</Text><Button onClick={() => void connections.refetch()}>Retry</Button></>}
      {accounts.map((connection) => <Account key={connection.id} connection={connection} />)}
      {!accounts.length && !connections.isLoading && !connections.error && <Text block>No personal accounts.</Text>}
    </DialogContent><DialogActions><Button onClick={onClose}>Close</Button></DialogActions>
  </DialogBody></DialogSurface></Dialog>;
}
