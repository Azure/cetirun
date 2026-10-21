// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Badge, Button, Spinner, Text, tokens } from "@fluentui/react-components";
import type { Instance } from "../types/instance";
import { useAzureToolAuthorization } from "../hooks/useToolAuthorization";

/** Remount local state when a name is reused for a different immutable Agent. */
export function ToolAuthorizationBlock({ instance }: { instance: Instance }) {
  return <AzureConnection key={instance.uid} instance={instance} />;
}

function AzureConnection({ instance }: { instance: Instance }) {
  const authorization = useAzureToolAuthorization(instance.instanceName, instance.uid, true);
  const connections = (authorization.connections.data ?? []).filter((connection) => (
    connection.mode === "agent" && connection.provider === "azure" && connection.identity?.instanceUid === instance.uid
  ));
  const bindings = (authorization.bindings.data?.bindings ?? []).filter((binding) => (
    binding.enabled && binding.instanceUid === instance.uid
  ));
  const binding = bindings.find((item) => connections.some((connection) => connection.id === item.connectionId)) ?? bindings[0];
  const connection = binding ? connections.find((item) => item.id === binding.connectionId) : connections[0];
  const loading = authorization.connections.isLoading || authorization.bindings.isLoading;
  const loadError = authorization.connections.error ?? authorization.bindings.error;
  const ready = instance.agentIdentity?.enabled === true && instance.agentIdentity.phase === "Ready";
  const readOnly = authorization.bindings.data?.readOnly === true;
  const connected = !!binding;
  const busy = authorization.connect.isPending || authorization.disconnect.isPending;
  const canConnect = ready && !loading && !loadError && !readOnly && !connected && !busy;
  const canDisconnect = !!binding && !authorization.bindings.isLoading && !authorization.bindings.isError && !busy;
  const error = authorization.connect.error ?? authorization.disconnect.error ?? loadError;

  return <section aria-label="Azure Agent connection" style={{ marginTop: 12, padding: 12, borderRadius: tokens.borderRadiusMedium, border: `1px solid ${tokens.colorNeutralStroke1}` }}>
    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 }}>
      <Text weight="semibold">Azure Agent identity</Text>
      {loading ? <Spinner size="tiny" label="Loading Azure connection..." /> : <Badge appearance="filled" color={connected ? "success" : "warning"}>{connected ? "Connected" : "Not connected"}</Badge>}
    </div>
    <Text size={200} block style={{ marginTop: 8 }}>Azure RBAC controls this Agent identity&apos;s resource access. Connected means CetiRun configured this Agent&apos;s Azure binding; it does not verify Azure permissions or runtime access.</Text>
    {connection?.identity && <div style={{ marginTop: 8 }}>
      <Text size={200} block>Agent principal ID: {connection.identity.principalId}</Text>
      <Text size={200} block>Client ID: {connection.identity.clientId}; tenant ID: {connection.identity.tenantId}</Text>
      <Text size={200} block>Resource owners assign Azure RBAC roles directly to this principal at the required scope. For read access, assign the Reader role at the required subscription or resource group scope.</Text>
    </div>}
    {!ready && <Text size={200} block style={{ marginTop: 8 }}>Agent Identity is {instance.agentIdentity?.enabled === true ? instance.agentIdentity.phase?.toLowerCase() || "provisioning" : "not enabled"}. Connect is available when its identity is ready.</Text>}
    {readOnly && <Text size={200} block style={{ marginTop: 8 }}>This runtime no longer supports creating Azure connections. Existing connections can still be disconnected.</Text>}
    {error && <div style={{ marginTop: 8 }}><Text role="alert" block>{error.message}</Text>{loadError && <Button appearance="subtle" onClick={() => { void authorization.refresh(); }}>Retry</Button>}</div>}
    {!loading && !loadError && !connected && <Text size={200} block style={{ marginTop: 8 }}>No Azure connection is configured for this Agent.</Text>}
    <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
      <Button appearance="primary" disabled={!canConnect} onClick={() => {
        authorization.disconnect.reset();
        authorization.connect.mutate();
      }}>
        {authorization.connect.isPending ? <Spinner size="tiny" /> : "Connect"}
      </Button>
      {binding && <Button appearance="secondary" disabled={!canDisconnect} onClick={() => {
        authorization.connect.reset();
        authorization.disconnect.mutate({ id: binding.id, revision: binding.revision });
      }}>
        {authorization.disconnect.isPending ? <Spinner size="tiny" /> : "Disconnect"}
      </Button>}
    </div>
  </section>;
}
