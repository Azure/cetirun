// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Badge, Button, Checkbox, Dialog, DialogActions, DialogBody, DialogContent, DialogSurface, DialogTitle, Field, Select, Spinner, Text, tokens } from "@fluentui/react-components";
import { useState } from "react";
import { reconnectConnection, startConnection, type Connection, type ConnectionBinding, type ConnectionProvider } from "../api/toolAuthorization";
import type { Instance } from "../types/instance";
import { useToolConnections, useConnectionBindings, useToolIdentity } from "../hooks/useToolAuthorization";
import { useConnectionSignIn } from "../hooks/useConnectionSignIn";

export const providerLabels: Record<ConnectionProvider, string> = { azure: "Azure", microsoft: "Microsoft 365", github: "GitHub", google: "Google" };
export const providerDisabledReasons: Partial<Record<ConnectionProvider, string>> = { microsoft: "Not tested yet" };
export const serviceLabels: Record<string, string> = { azure_arm: "Azure Resource Manager", github: "GitHub REST API", microsoft_graph: "Microsoft Graph", google_gmail: "Gmail", google_calendar: "Google Calendar" };

function ServiceSelection({ connection, services, setServices }: { connection: Connection; services: string[]; setServices: (services: string[]) => void }) {
  return <>
    <Text block>{providerLabels[connection.provider]} — {connection.accountDisplay}</Text>
    <Text size={200} block>The agent uses this identity&apos;s provider permissions. Manage resource permissions with the provider.</Text>
    {connection.services.map((id) => <Checkbox key={id} label={serviceLabels[id] ?? id} checked={services.includes(id)} onChange={(_, data) => setServices(data.checked ? [...services, id] : services.filter((s) => s !== id))} />)}
  </>;
}

function ConnectWizard({ instance, connections, onClose }: { instance: Instance; connections: Connection[]; onClose: () => void }) {
  const bindingsApi = useConnectionBindings(instance.instanceName, instance.uid, true);
  const identity = useToolIdentity(instance.instanceName, instance.uid);
  const signin = useConnectionSignIn(instance.uid);
  const [connection, setConnection] = useState<Connection>();
  const [services, setServices] = useState<string[]>([]);
  const [provider, setProvider] = useState<ConnectionProvider>("github");
  const [error, setError] = useState("");
  const choose = (c: Connection) => { setError(""); setConnection(c); setServices(c.services); };
  const busy = bindingsApi.create.isPending || identity.isPending;
  const active = new Set(bindingsApi.bindings.data?.bindings.filter((b) => b.enabled).map((b) => b.connectionId));
  const personal = connections.filter((c) => c.mode === "delegated" && c.status !== "connecting" && (!c.canonicalConnectionId || c.canonicalConnectionId === c.id) && !active.has(c.id));
  const azure = connections.find((c) => c.mode === "agent" && c.provider === "azure" && c.identity?.instanceUid === instance.uid);
  const close = () => { if (!busy) { signin.cancel(); onClose(); } };
  return <Dialog open onOpenChange={(_, d) => { if (!d.open) close(); }}><DialogSurface><DialogBody>
    <DialogTitle>Connect to this agent</DialogTitle>
    <DialogContent>
      {!connection ? <>
        <Text block weight="semibold">Agent identity</Text>
        <Button disabled={busy || signin.pending || !!(azure && active.has(azure.id))} onClick={() => {
          if (azure) choose(azure);
          else identity.mutate({ provider: "azure" }, { onSuccess: choose, onError: (e) => setError(e.message) });
        }}>Azure Agent identity</Button>
        <Text block weight="semibold" style={{ marginTop: 16 }}>Existing personal accounts</Text>
        {personal.map((c) => <div key={c.id} style={{ marginTop: 8 }}><Button disabled={!!providerDisabledReasons[c.provider] || busy || signin.pending || (c.status !== "connected" && c.status !== "interaction_required")} onClick={() => c.status === "connected" ? choose(c) : signin.begin(() => reconnectConnection(c.id), choose)}>{providerLabels[c.provider]} — {c.accountDisplay}{providerDisabledReasons[c.provider] ? ` (${providerDisabledReasons[c.provider]})` : c.status === "interaction_required" ? " (Sign-in required)" : ""}</Button></div>)}
        {!personal.length && <Text block size={200}>No available personal accounts.</Text>}
        <Field label="Sign in to another account" style={{ marginTop: 16 }}><Select value={provider} onChange={(_, d) => setProvider(d.value as ConnectionProvider)}>{(Object.keys(providerLabels) as ConnectionProvider[]).map((id) => <option key={id} value={id} disabled={!!providerDisabledReasons[id]}>{providerLabels[id]}{providerDisabledReasons[id] ? ` (${providerDisabledReasons[id]})` : ""}</option>)}</Select></Field>
        <Button disabled={!!providerDisabledReasons[provider] || busy || signin.pending} onClick={() => signin.begin(() => startConnection(provider), choose)}>Sign in</Button>
      </> : <ServiceSelection connection={connection} services={services} setServices={setServices} />}
      {signin.pending && <Spinner size="tiny" label="Waiting for sign-in..." />}
      {signin.message && <Text role="status" block>{signin.message}</Text>}
      {error && <Text role="alert" block>{error}</Text>}
    </DialogContent>
    <DialogActions><Button disabled={busy} onClick={close}>Cancel</Button>{connection && <>
      <Button disabled={busy} onClick={() => { setError(""); setConnection(undefined); }}>Back</Button>
      <Button appearance="primary" disabled={busy || !services.length || active.has(connection.id)} onClick={() => { setError(""); bindingsApi.create.mutate({ connectionId: connection.id, services, enabled: true }, { onSuccess: onClose, onError: (e) => setError(e.message) }); }}>Connect</Button>
    </>}</DialogActions>
  </DialogBody></DialogSurface></Dialog>;
}

function EditServices({ instance, connection, binding, onClose }: { instance: Instance; connection: Connection; binding: ConnectionBinding; onClose: () => void }) {
  const api = useConnectionBindings(instance.instanceName, instance.uid, true);
  const [services, setServices] = useState(binding.services);
  return <Dialog open onOpenChange={(_, d) => { if (!d.open && !api.update.isPending) onClose(); }}><DialogSurface><DialogBody>
    <DialogTitle>Edit services</DialogTitle><DialogContent><ServiceSelection connection={connection} services={services} setServices={setServices} />{api.update.error && <Text role="alert">{api.update.error.message}</Text>}</DialogContent>
    <DialogActions><Button disabled={api.update.isPending} onClick={onClose}>Cancel</Button><Button appearance="primary" disabled={api.update.isPending || !services.length} onClick={() => api.update.mutate({ id: binding.id, input: { connectionId: connection.id, services, enabled: true, revision: binding.revision } }, { onSuccess: onClose })}>Save services</Button></DialogActions>
  </DialogBody></DialogSurface></Dialog>;
}

// Remount local workflow state on immutable Agent UID changes.
export function ToolAuthorizationBlock({ instance }: { instance: Instance }) { return <AgentConnections key={instance.uid} instance={instance} />; }
function AgentConnections({ instance }: { instance: Instance }) {
  const connectionsApi = useToolConnections(true);
  const bindingsApi = useConnectionBindings(instance.instanceName, instance.uid, true);
  const signin = useConnectionSignIn(instance.uid);
  const [connecting, setConnecting] = useState(false);
  const [editing, setEditing] = useState<{ connection: Connection; binding: ConnectionBinding }>();
  const connections = (connectionsApi.connections.data ?? []).filter((c) => c.mode === "delegated" || c.identity?.instanceUid === instance.uid);
  const bindings = (bindingsApi.bindings.data?.bindings ?? []).filter((b) => b.enabled && b.instanceUid === instance.uid);
  const loading = connectionsApi.connections.isLoading || bindingsApi.bindings.isLoading;
  const error = connectionsApi.connections.error ?? bindingsApi.bindings.error ?? bindingsApi.remove.error;
  const canManage = !loading && !connectionsApi.connections.isError && !bindingsApi.bindings.isError && bindingsApi.bindings.data?.readOnly === false;
  return <section aria-label="Agent connections" style={{ marginTop: 12, padding: 12, borderRadius: tokens.borderRadiusMedium, border: `1px solid ${tokens.colorNeutralStroke1}` }}>
    <div style={{ display: "flex", justifyContent: "space-between", gap: 8 }}><Text weight="semibold">Connections for this agent</Text><Button disabled={!canManage} onClick={() => setConnecting(true)}>Connect</Button></div>
    <Text size={200} block>Azure Agent identities use Azure RBAC. Personal accounts use their OAuth permissions. Disconnect removes only this agent&apos;s access.</Text>
    {loading && <Spinner size="tiny" label="Loading connections..." />}
    {error && <div><Text role="alert" block>{error.message}</Text><Button onClick={() => { bindingsApi.remove.reset(); void connectionsApi.connections.refetch(); void bindingsApi.bindings.refetch(); }}>Retry</Button></div>}
    {signin.message && <Text role="status" block>{signin.message}</Text>}
    {bindings.map((binding) => {
      const connection = connections.find((c) => c.id === binding.connectionId);
      return <div key={binding.id} style={{ padding: 8, marginTop: 8, background: tokens.colorNeutralBackground1Hover }}>
        <Text weight="semibold">{connection ? `${providerLabels[connection.provider]} — ${connection.accountDisplay}` : "Unavailable connection"}</Text>{" "}<Badge>{connection?.status === "connected" ? "Configured" : connection?.status === "interaction_required" ? "Sign-in required" : "Unavailable"}</Badge>
        <Text size={200} block>Services: {binding.services.map((id) => serviceLabels[id] ?? id).join(", ")}</Text>
        {connection && providerDisabledReasons[connection.provider] && <Text size={200} block>{providerLabels[connection.provider]}: {providerDisabledReasons[connection.provider]}</Text>}
        {connection?.mode === "agent" && connection.identity && <><Text size={200} block>Agent principal ID: {connection.identity.principalId}</Text><Text size={200} block>Client ID: {connection.identity.clientId}; tenant ID: {connection.identity.tenantId}</Text><Text size={200} block>Resource owners assign Azure RBAC directly to this principal. A configured connection does not verify Azure access.</Text></>}
        {connection?.mode === "delegated" && <Text size={200} block>Executor: {connection.accountDisplay}</Text>}
        <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
          {connection && connection.services.length > 1 && <Button disabled={!!providerDisabledReasons[connection.provider] || !canManage || connection.status !== "connected"} onClick={() => setEditing({ connection, binding })}>Edit services</Button>}
          {connection?.mode === "delegated" && connection.status === "interaction_required" && <Button disabled={!!providerDisabledReasons[connection.provider] || !canManage || signin.pending} onClick={() => signin.begin(() => reconnectConnection(connection.id), () => {})}>Reauthorize</Button>}
          <Button disabled={bindingsApi.bindings.isError || bindingsApi.remove.isPending} onClick={() => { signin.cancel(); bindingsApi.remove.reset(); bindingsApi.remove.mutate({ id: binding.id, revision: binding.revision }); }}>Disconnect</Button>
        </div>
      </div>;
    })}
    {!bindings.length && !loading && !error && <Text size={200} block>No connections for this agent.</Text>}
    {connecting && <ConnectWizard instance={instance} connections={connections} onClose={() => setConnecting(false)} />}
    {editing && bindings.some((b) => b.id === editing.binding.id) && <EditServices instance={instance} connection={editing.connection} binding={bindings.find((b) => b.id === editing.binding.id)!} onClose={() => setEditing(undefined)} />}
  </section>;
}
