// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";
import { Button, Field, Input, MessageBar, Spinner, Table, TableBody, TableCell, TableHeader, TableHeaderCell, TableRow, Text, Textarea, Title2 } from "@fluentui/react-components";
import { Add20Regular, Delete20Regular, Edit20Regular } from "@fluentui/react-icons";
import { createAgentPolicy, createSandbox, deleteAgentPolicy, deleteSandbox, getAgentPolicy, getSandbox, listAgentPolicies, listSandboxes, updateAgentPolicy, updateSandbox, type AgentPolicyResource, type PolicyCondition, type SandboxResource } from "../../api/adminPolicy";

type Resource = AgentPolicyResource | SandboxResource;
type ResourceKind = "policy" | "sandbox";

export const policyTemplate = {
  harnesses: { allowed: ["hermes"] }, skills: { allowed: [] },
  modelAccess: { copilot: { allowed: false }, sharedModels: { allowed: [] } },
  agentIdentity: { allowed: false, required: false, credentialServices: [] }, entraID: { enabled: false },
  sandboxSelection: { allowMultiple: false }, security: { readOnlyRootFilesystem: true },
  network: { blockedCIDRs: [], blockedFQDNs: [], blockedPorts: [], allowedInternalCIDRs: [], ingress: { mode: "authenticated", requireMTLS: false } },
};
export const sandboxTemplate = {
  type: "kata",
  resources: { agent: { requests: { cpu: "500m", memory: "1Gi", "ephemeral-storage": "2Gi" }, limits: { cpu: "500m", memory: "1Gi", "ephemeral-storage": "2Gi" } }, helpers: { requests: { cpu: "100m", memory: "128Mi", "ephemeral-storage": "512Mi" }, limits: { cpu: "100m", memory: "128Mi", "ephemeral-storage": "512Mi" } } },
  storage: { home: { storageClassName: "managed-csi-premium", size: "10Gi", accessMode: "ReadWriteOncePod" }, scratch: { sizeLimit: "2Gi" } },
};
const title = (kind: ResourceKind) => kind === "policy" ? "Agent policies" : "Sandboxes";
const singular = (kind: ResourceKind) => kind === "policy" ? "AgentPolicy" : "Sandbox";
const route = (kind: ResourceKind) => kind === "policy" ? "agent-policies" : "sandboxes";
const key = (kind: ResourceKind) => ["admin", "policy", kind] as const;
const list = (kind: ResourceKind): Promise<Resource[]> => kind === "policy" ? listAgentPolicies() : listSandboxes();
const get = (kind: ResourceKind, name: string): Promise<Resource> => kind === "policy" ? getAgentPolicy(name) : getSandbox(name);
function version(resource: Resource) {
  const { uid, resourceVersion } = resource.metadata;
  if (!uid || !resourceVersion) throw new Error("Policy resource version is unavailable; reload before saving");
  return { uid, resourceVersion };
}
const remove = (kind: ResourceKind, resource: Resource) => kind === "policy" ? deleteAgentPolicy(resource.metadata.name, version(resource)) : deleteSandbox(resource.metadata.name, version(resource));

export function resourcePayload(kind: ResourceKind, name: string, spec: Record<string, unknown>) {
  return { apiVersion: "cetirun.microsoft.com/v1alpha1", kind: singular(kind), metadata: { name }, spec };
}

function acceptance(conditions?: PolicyCondition[]) {
  const accepted = conditions?.find((condition) => condition.type === "Accepted" || condition.type === "Ready");
  return accepted ? `${accepted.type}: ${accepted.status}` : "Pending";
}

export function PolicyResourceListPage({ kind }: { kind: ResourceKind }) {
  const navigate = useNavigate(); const queryClient = useQueryClient(); const [confirming, setConfirming] = useState<Resource>();
  const query = useQuery<Resource[], Error>({ queryKey: key(kind), queryFn: () => list(kind) });
  const deletion = useMutation({ mutationFn: (resource: Resource) => remove(kind, resource), onSuccess: () => { queryClient.invalidateQueries({ queryKey: key(kind) }); setConfirming(undefined); } });
  return <div>
    <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 16 }}><Title2>{title(kind)}</Title2><Button appearance="primary" icon={<Add20Regular />} onClick={() => navigate(`/admin/${route(kind)}/new`)}>New {singular(kind)}</Button></div>
    <Text block style={{ marginBottom: 16 }}>{kind === "policy" ? "Policies define the approved Agent capabilities, model access, budgets and network controls." : "Sandboxes define approved runtime, resource limits and private storage profiles."}</Text>
    {query.isLoading && <Spinner label={`Loading ${title(kind).toLowerCase()}...`} />}
    {query.error && <MessageBar intent="error">{query.error.message}</MessageBar>}
    {deletion.error && <MessageBar intent="error">{deletion.error.message}</MessageBar>}
    {query.data && <Table aria-label={title(kind)}><TableHeader><TableRow><TableHeaderCell>Name</TableHeaderCell><TableHeaderCell>{kind === "policy" ? "Allowed harnesses" : "Runtime"}</TableHeaderCell><TableHeaderCell>Status</TableHeaderCell><TableHeaderCell>Actions</TableHeaderCell></TableRow></TableHeader><TableBody>
      {query.data.length === 0 && <TableRow><TableCell colSpan={4}>No {title(kind).toLowerCase()} yet.</TableCell></TableRow>}
      {query.data.map((resource) => <TableRow key={resource.metadata.name}><TableCell>{resource.metadata.name}</TableCell><TableCell>{kind === "policy" ? ((resource.spec.harnesses as { allowed?: string[] } | undefined)?.allowed || []).join(", ") || "—" : String(resource.spec.type || "kata")}</TableCell><TableCell>{acceptance(resource.status?.conditions)}</TableCell><TableCell><Button appearance="subtle" size="small" icon={<Edit20Regular />} aria-label={`Edit ${resource.metadata.name}`} onClick={() => navigate(`/admin/${route(kind)}/${resource.metadata.name}`)} /><Button appearance="subtle" size="small" icon={<Delete20Regular />} aria-label={`Delete ${resource.metadata.name}`} onClick={() => setConfirming(resource)} /></TableCell></TableRow>)}
    </TableBody></Table>}
    {confirming && <MessageBar intent="warning" style={{ marginTop: 16 }}>Delete {singular(kind)} <strong>{confirming.metadata.name}</strong>? Assigned Agents may be denied until a replacement is configured. <Button size="small" appearance="primary" disabled={deletion.isPending} onClick={() => deletion.mutate(confirming)}>Delete</Button><Button size="small" appearance="secondary" onClick={() => setConfirming(undefined)}>Cancel</Button></MessageBar>}
  </div>;
}

export function PolicyResourceEditPage({ kind }: { kind: ResourceKind }) {
  const { name } = useParams<{ name: string }>(); const navigate = useNavigate(); const queryClient = useQueryClient(); const creating = !name || name === "new";
  const [resourceName, setResourceName] = useState(""); const [specJSON, setSpecJSON] = useState(() => JSON.stringify(kind === "policy" ? policyTemplate : sandboxTemplate, null, 2)); const [error, setError] = useState<string>();
  const query = useQuery<Resource, Error>({ queryKey: [...key(kind), name], queryFn: () => get(kind, name!), enabled: !creating });
  useEffect(() => { if (query.data) { setResourceName(query.data.metadata.name); setSpecJSON(JSON.stringify(query.data.spec, null, 2)); } }, [query.data]);
  const save = useMutation<Resource, Error, { itemName: string; spec: Record<string, unknown> }>({ mutationFn: ({ itemName, spec }) => {
    if (creating) { const resource = resourcePayload(kind, itemName, spec); return (kind === "policy" ? createAgentPolicy(resource as AgentPolicyResource) : createSandbox(resource as SandboxResource)) as Promise<Resource>; }
    const current = query.data;
    if (!current) throw new Error("Policy resource is unavailable; reload before saving");
    return (kind === "policy" ? updateAgentPolicy(name!, version(current), spec) : updateSandbox(name!, version(current), spec)) as Promise<Resource>;
  }, onSuccess: () => { queryClient.invalidateQueries({ queryKey: key(kind) }); navigate(`/admin/${route(kind)}`); }, onError: (cause: Error) => setError(cause.message) });
  const submit = (event: FormEvent) => { event.preventDefault(); setError(undefined); if (!/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/.test(resourceName.trim())) { setError("Name must be a DNS-1123 label: lowercase, start with a letter, max 63 characters."); return; } try { const spec: unknown = JSON.parse(specJSON); if (!spec || Array.isArray(spec) || typeof spec !== "object") throw new Error(); save.mutate({ itemName: resourceName.trim(), spec: spec as Record<string, unknown> }); } catch { setError("Specification must be a valid JSON object."); } };
  if (!creating && query.isLoading) return <Spinner label={`Loading ${singular(kind)}...`} />;
  if (!creating && query.error) return <MessageBar intent="error">{query.error.message}</MessageBar>;
  return <form onSubmit={submit} style={{ maxWidth: 820, display: "flex", flexDirection: "column", gap: 16 }}><Title2>{creating ? `New ${singular(kind)}` : `${singular(kind)}: ${name}`}</Title2><Text>{kind === "policy" ? "Edit the complete AgentPolicy specification. Start restrictive and explicitly add approved capabilities." : "Edit the complete Sandbox specification. Resource requests and limits need CPU, memory and ephemeral storage."}</Text><Field label="Name" required><Input value={resourceName} disabled={!creating} onChange={(_, data) => setResourceName(data.value)} /></Field><Field label="Specification" required hint="Only the CRD spec is editable; the platform owns metadata and status."><Textarea aria-label={`${singular(kind)} specification`} rows={24} value={specJSON} onChange={(_, data) => setSpecJSON(data.value)} style={{ fontFamily: "monospace" }} /></Field>{error && <MessageBar intent="error">{error}</MessageBar>}<div style={{ display: "flex", gap: 8 }}><Button appearance="primary" type="submit" disabled={save.isPending}>{save.isPending ? "Saving..." : creating ? "Create" : "Save"}</Button><Button appearance="secondary" type="button" onClick={() => navigate(`/admin/${route(kind)}`)}>Cancel</Button></div></form>;
}
