// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
  Badge,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  Select,
  Spinner,
  Text,
  tokens,
} from "@fluentui/react-components";
import { Add20Regular, ArrowSync20Regular, Delete20Regular, Edit20Regular, Warning20Regular } from "@fluentui/react-icons";
import { useMemo, useState } from "react";
import { TOOL_CATALOG_VERSION, ToolAuthorizationError, type Connection, type ConnectionProvider, type ToolConstraints, type ToolGrant } from "../api/toolAuthorization";
import type { Instance } from "../types/instance";
import { useToolConnections, useToolGrants } from "../hooks/useToolAuthorization";

type ToolDefinition = { id: string; label: string; write?: boolean };

const toolsByProvider: Record<ConnectionProvider, ToolDefinition[]> = {
  azure: [
    { id: "azure_subscriptions_list", label: "List subscriptions" },
    { id: "azure_resource_groups_list", label: "List resource groups" },
    { id: "azure_resources_search", label: "Search resources" },
    { id: "azure_resource_show", label: "View resource details" },
  ],
  microsoft: [
    { id: "mail_messages_search", label: "Search mail" },
    { id: "mail_messages_get", label: "Read mail" },
    { id: "mail_send", label: "Send mail", write: true },
    { id: "calendar_events_list", label: "List calendar events" },
    { id: "calendar_events_create", label: "Create calendar events", write: true },
  ],
  github: [
    { id: "github_repositories_get", label: "Read repository metadata" },
    { id: "github_contents_get", label: "Read repository contents" },
    { id: "github_issues_list", label: "List issues" },
    { id: "github_pull_requests_list", label: "List pull requests" },
    { id: "github_issues_create", label: "Create issues", write: true },
    { id: "github_comments_create", label: "Create comments", write: true },
    { id: "github_pull_requests_create", label: "Create pull requests", write: true },
  ],
  google: [
    { id: "mail_messages_search", label: "Search mail" },
    { id: "mail_messages_get", label: "Read mail" },
    { id: "mail_send", label: "Send mail", write: true },
    { id: "calendar_events_list", label: "List calendar events" },
    { id: "calendar_events_create", label: "Create calendar events", write: true },
  ],
};

const providerLabels: Record<ConnectionProvider, string> = {
  azure: "Azure",
  microsoft: "Microsoft 365",
  github: "GitHub",
  google: "Google",
};

const emptyConstraints: ToolConstraints = {
  subscriptionIds: [], repositoryIds: [], baseBranches: [], calendarIds: [], recipientAddresses: [], recipientDomains: [], allowInvites: false,
};

function splitValues(value: string) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function joinValues(value: string[]) {
  return value.join(", ");
}

function isWriteTool(tool: string) {
  return Object.values(toolsByProvider).flat().some((definition) => definition.id === tool && definition.write);
}

function GrantEditor({ connection, grant, onCancel, onSave, saving }: {
  connection: Connection;
  grant?: ToolGrant;
  onCancel: () => void;
  onSave: (input: { tools: string[]; constraints: ToolConstraints; expiresAt: string }) => void;
  saving: boolean;
}) {
  const availableTools = useMemo(() => {
    const catalog = toolsByProvider[connection.provider];
    return connection.capabilities.length ? catalog.filter((tool) => connection.capabilities.includes(tool.id)) : catalog;
  }, [connection]);
  const [tools, setTools] = useState<string[]>(grant?.tools ?? availableTools.filter((tool) => !tool.write).map((tool) => tool.id));
  const [constraints, setConstraints] = useState<ToolConstraints>(grant?.constraints ?? emptyConstraints);
  const [days, setDays] = useState(() => {
    if (!grant) return "30";
    return String(Math.max(1, Math.min(90, Math.ceil((new Date(grant.expiresAt).getTime() - Date.now()) / 86_400_000))));
  });
  const [error, setError] = useState("");

  const toggleTool = (id: string, checked: boolean) => {
    setTools((current) => checked ? [...current, id] : current.filter((tool) => tool !== id));
  };
  const setList = (key: Exclude<keyof ToolConstraints, "allowInvites">, value: string) => {
    setConstraints((current) => ({ ...current, [key]: splitValues(value) }));
  };
  const submit = () => {
    const lifetime = Number(days);
    if (!Number.isInteger(lifetime) || lifetime < 1 || lifetime > 90) {
      setError("Grant validity must be a whole number from 1 to 90 days.");
      return;
    }
    if (!tools.length) {
      setError("Select at least one tool.");
      return;
    }
    onSave({ tools, constraints, expiresAt: new Date(Date.now() + lifetime * 86_400_000).toISOString() });
  };
  const writes = tools.filter(isWriteTool);

  return <Dialog open onOpenChange={(_, data) => { if (!data.open) onCancel(); }}>
    <DialogSurface>
      <DialogBody>
        <DialogTitle>{grant ? "Edit tool authorization" : "Authorize this agent"}</DialogTitle>
        <DialogContent>
          <Text size={200} block>Connection: {providerLabels[connection.provider]} — {connection.accountDisplay}</Text>
          <Field label="Read-only tools" style={{ marginTop: 12 }}>
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {availableTools.filter((tool) => !tool.write).map((tool) => <Checkbox key={tool.id} checked={tools.includes(tool.id)} label={tool.label} onChange={(_, data) => toggleTool(tool.id, !!data.checked)} />)}
            </div>
          </Field>
          <Field label="Write tools — explicit permission" hint="The agent can perform enabled writes automatically within these limits." style={{ marginTop: 12 }}>
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {availableTools.filter((tool) => tool.write).map((tool) => <Checkbox key={tool.id} checked={tools.includes(tool.id)} label={tool.label} onChange={(_, data) => toggleTool(tool.id, !!data.checked)} />)}
              {!availableTools.some((tool) => tool.write) && <Text size={200}>This connection has no available write capabilities.</Text>}
            </div>
          </Field>
          <Field label="Azure subscription IDs" hint="Comma-separated; Azure access is read-only." style={{ marginTop: 12 }}><Input value={joinValues(constraints.subscriptionIds)} onChange={(_, data) => setList("subscriptionIds", data.value)} /></Field>
          <Field label="GitHub repository IDs" hint="Comma-separated stable repository IDs." style={{ marginTop: 12 }}><Input value={joinValues(constraints.repositoryIds)} onChange={(_, data) => setList("repositoryIds", data.value)} /></Field>
          <Field label="Pull request base branches" style={{ marginTop: 12 }}><Input value={joinValues(constraints.baseBranches)} onChange={(_, data) => setList("baseBranches", data.value)} /></Field>
          <Field label="Calendar IDs" style={{ marginTop: 12 }}><Input value={joinValues(constraints.calendarIds)} onChange={(_, data) => setList("calendarIds", data.value)} /></Field>
          <Field label="Allowed recipient addresses" style={{ marginTop: 12 }}><Input value={joinValues(constraints.recipientAddresses)} onChange={(_, data) => setList("recipientAddresses", data.value)} /></Field>
          <Field label="Allowed recipient domains" hint="Exact domains only, for example example.com." style={{ marginTop: 12 }}><Input value={joinValues(constraints.recipientDomains)} onChange={(_, data) => setList("recipientDomains", data.value)} /></Field>
          <Checkbox checked={constraints.allowInvites} label="Allow calendar invitations within the recipient limits" onChange={(_, data) => setConstraints((current) => ({ ...current, allowInvites: !!data.checked }))} style={{ marginTop: 12 }} />
          <Field label="Grant validity (days)" hint="Default: 30 days. Maximum: 90 days." style={{ marginTop: 12 }}><Input type="number" min={1} max={90} value={days} onChange={(_, data) => setDays(data.value)} /></Field>
          <div style={{ marginTop: 12, padding: 10, background: tokens.colorNeutralBackground1Hover, borderRadius: tokens.borderRadiusMedium }}>
            <Text weight="semibold" block>Authorization summary</Text>
            <Text size={200} block>{tools.length} tool{tools.length === 1 ? "" : "s"}; {writes.length ? `writes: ${writes.join(", ")}` : "read-only"}; expires in {days || "?"} days.</Text>
            <Text size={200} block>Limits: {constraints.subscriptionIds.length} subscriptions, {constraints.repositoryIds.length} repositories, {constraints.calendarIds.length} calendars, {constraints.recipientAddresses.length + constraints.recipientDomains.length} recipient rules.</Text>
          </div>
          {error && <Text size={200} style={{ color: tokens.colorStatusDangerForeground1 }}>{error}</Text>}
        </DialogContent>
        <DialogActions><Button appearance="secondary" onClick={onCancel}>Cancel</Button><Button appearance="primary" disabled={saving} onClick={submit}>{saving ? <Spinner size="tiny" /> : grant ? "Save authorization" : "Authorize agent"}</Button></DialogActions>
      </DialogBody>
    </DialogSurface>
  </Dialog>;
}

export function ToolAuthorizationBlock({ instance }: { instance: Instance }) {
  const connectionsApi = useToolConnections(true);
  const grantsApi = useToolGrants(instance.instanceName, true);
  const [provider, setProvider] = useState<ConnectionProvider>("azure");
  const [grantConnectionID, setGrantConnectionID] = useState("");
  const [editing, setEditing] = useState<{ connection: Connection; grant?: ToolGrant }>();
  const [message, setMessage] = useState("");

  const beginConnection = (execute: (onSuccess: (result: { authorizationUrl: string }) => void, onError: () => void) => void) => {
    const popup = window.open("about:blank", "cetirun-tool-connection", "popup,width=620,height=720");
    if (!popup) {
      setMessage("Allow popups for this Portal to connect an account.");
      return;
    }
    execute((result) => {
      popup.location.replace(result.authorizationUrl);
      setMessage("Complete the provider sign-in in the new window, then refresh connections.");
    }, () => {
      popup.close();
      setMessage("The account connection could not be started. Try again.");
    });
  };

  const connections = connectionsApi.connections.data ?? [];
  const connectedConnections = connections.filter((connection) => connection.status === "connected");
  const grants = grantsApi.grants.data ?? [];
  const connectionForGrant = (grant: ToolGrant) => connections.find((connection) => connection.id === grant.connectionId);
  const error = connectionsApi.connections.error ?? grantsApi.grants.error ?? connectionsApi.start.error ?? connectionsApi.reconnect.error ?? connectionsApi.remove.error ?? grantsApi.create.error ?? grantsApi.update.error ?? grantsApi.remove.error;
  const unsupportedRuntime = grantsApi.grants.error instanceof ToolAuthorizationError &&
    ["unsupported_agent_type", "unsupported_sandbox"].includes(grantsApi.grants.error.code ?? "");

  if (unsupportedRuntime) return null;

  return <section aria-label="Agent tool access" style={{ marginTop: 12, padding: 12, borderRadius: tokens.borderRadiusMedium, border: `1px solid ${tokens.colorNeutralStroke1}` }}>
    <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}><Text weight="semibold">Connections & agent tool access</Text><Badge appearance="filled" color="informative">Catalog {TOOL_CATALOG_VERSION}</Badge></div>
    <Text size={200} block style={{ marginTop: 6, color: tokens.colorNeutralForeground3 }}>Connected accounts belong to you. An account becomes available to this agent only after you create an explicit, time-limited grant. Provider credentials never enter this browser.</Text>
    <div style={{ display: "flex", gap: 8, alignItems: "end", flexWrap: "wrap", marginTop: 12 }}>
      <Field label="Add account"><Select value={provider} onChange={(_, data) => setProvider(data.value as ConnectionProvider)}>{(Object.keys(providerLabels) as ConnectionProvider[]).map((item) => <option key={item} value={item}>{providerLabels[item]}</option>)}</Select></Field>
      <Button appearance="primary" icon={<Add20Regular />} disabled={connectionsApi.start.isPending} onClick={() => beginConnection((onSuccess, onError) => connectionsApi.start.mutate(provider, { onSuccess, onError }))}>{connectionsApi.start.isPending ? "Starting..." : "Connect account"}</Button>
      <Button appearance="secondary" icon={<ArrowSync20Regular />} onClick={() => { void connectionsApi.connections.refetch(); void grantsApi.grants.refetch(); }}>Refresh</Button>
    </div>
    {message && <Text size={200} block style={{ marginTop: 8 }}>{message}</Text>}
    {(connectionsApi.connections.isLoading || grantsApi.grants.isLoading) && <Spinner size="tiny" label="Loading tool access..." style={{ marginTop: 8 }} />}
    {error && <div style={{ display: "flex", gap: 8, alignItems: "center", marginTop: 8, color: tokens.colorStatusDangerForeground1 }}><Warning20Regular /><Text size={200}>{error.message}</Text></div>}
    <div style={{ marginTop: 12, display: "flex", flexDirection: "column", gap: 8 }}>
      {connections.map((connection) => <div key={connection.id} style={{ padding: 8, borderRadius: tokens.borderRadiusSmall, background: tokens.colorNeutralBackground1Hover }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}><Text weight="semibold">{providerLabels[connection.provider]} — {connection.accountDisplay}</Text><Badge appearance="filled" color={connection.status === "connected" ? "success" : "warning"}>{connection.status.replace("_", " ")}</Badge></div>
        <Text size={200} block>Capabilities: {connection.capabilities.length ? connection.capabilities.join(", ") : "Provider determines available tools after connection."}</Text>
        <div style={{ display: "flex", gap: 8, marginTop: 6 }}><Button size="small" appearance="secondary" disabled={connectionsApi.reconnect.isPending} onClick={() => beginConnection((onSuccess, onError) => connectionsApi.reconnect.mutate(connection.id, { onSuccess, onError }))}>Reconnect</Button><Button size="small" appearance="secondary" icon={<Delete20Regular />} onClick={() => connectionsApi.remove.mutate(connection.id)}>Disconnect</Button></div>
      </div>)}
      {!connectionsApi.connections.isLoading && !connections.length && <Text size={200}>No connected accounts yet.</Text>}
    </div>
    <div style={{ display: "flex", justifyContent: "space-between", gap: 8, alignItems: "end", flexWrap: "wrap", marginTop: 16 }}><Text weight="semibold">This agent&apos;s grants</Text><Field label="Account to authorize"><Select value={grantConnectionID} onChange={(_, data) => setGrantConnectionID(data.value)}><option value="">Select a connected account</option>{connectedConnections.map((connection) => <option key={connection.id} value={connection.id}>{providerLabels[connection.provider]} — {connection.accountDisplay}</option>)}</Select></Field><Button appearance="primary" disabled={!grantConnectionID} onClick={() => { const connection = connectedConnections.find((item) => item.id === grantConnectionID); if (connection) setEditing({ connection }); }}>Authorize account</Button></div>
    <Text size={200} block style={{ marginTop: 4, color: tokens.colorNeutralForeground3 }}>Read tools are selected by default. Enable each write tool deliberately; the agent can use enabled tools automatically within the listed limits.</Text>
    <div style={{ marginTop: 8, display: "flex", flexDirection: "column", gap: 8 }}>
      {grants.map((grant) => { const connection = connectionForGrant(grant); return <div key={grant.id} style={{ padding: 8, borderRadius: tokens.borderRadiusSmall, background: tokens.colorNeutralBackground1Hover }}>
        <Text weight="semibold" block>{connection ? `${providerLabels[connection.provider]} — ${connection.accountDisplay}` : "Disconnected account"}</Text>
        <Text size={200} block>{grant.tools.join(", ") || "No tools"}; expires {new Date(grant.expiresAt).toLocaleString()}.</Text>
        <Text size={200} block>Limits: {grant.constraints.subscriptionIds.length} subscriptions, {grant.constraints.repositoryIds.length} repositories, {grant.constraints.calendarIds.length} calendars, {grant.constraints.recipientAddresses.length + grant.constraints.recipientDomains.length} recipient rules{grant.constraints.allowInvites ? "; calendar invitations allowed" : ""}.</Text>
        <div style={{ display: "flex", gap: 8, marginTop: 6 }}><Button size="small" appearance="secondary" icon={<Edit20Regular />} disabled={!connection} onClick={() => connection && setEditing({ connection, grant })}>Edit</Button><Button size="small" appearance="secondary" icon={<Delete20Regular />} onClick={() => grantsApi.remove.mutate({ id: grant.id, revision: grant.revision })}>Revoke</Button></div>
      </div>; })}
      {!grantsApi.grants.isLoading && !grants.length && <Text size={200}>No account has been authorized for this agent.</Text>}
    </div>
    {editing && <GrantEditor connection={editing.connection} grant={editing.grant} saving={grantsApi.create.isPending || grantsApi.update.isPending} onCancel={() => setEditing(undefined)} onSave={(input) => {
      if (editing.grant) grantsApi.update.mutate({ id: editing.grant.id, input: { ...input, connectionId: editing.connection.id, revision: editing.grant.revision } }, { onSuccess: () => setEditing(undefined) });
      else grantsApi.create.mutate({ ...input, connectionId: editing.connection.id }, { onSuccess: () => setEditing(undefined) });
    }} />}
  </section>;
}
