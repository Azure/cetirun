// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";
import {
  Button,
  Field,
  Input,
  MessageBar,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
  Textarea,
  Title2,
} from "@fluentui/react-components";
import { Add20Regular, Delete20Regular, Edit20Regular } from "@fluentui/react-icons";
import {
  createAgentPolicy,
  createSandbox,
  deleteAgentPolicy,
  deleteSandbox,
  getAgentPolicy,
  getSandbox,
  listAgentPolicies,
  listSandboxes,
  updateAgentPolicy,
  updateSandbox,
  type AgentPolicyResource,
  type GovernanceCondition,
  type SandboxResource,
} from "../../api/adminGovernance";

type Resource = AgentPolicyResource | SandboxResource;
type ResourceKind = "policy" | "sandbox";

const policyTemplate: Record<string, unknown> = {
  harnesses: { allowed: ["hermes"] },
  skills: { allowed: [] },
  modelAccess: { copilot: { allowed: false }, sharedModels: { allowed: [] } },
  agentIdentity: { allowed: false, required: false, credentialServices: [] },
  entraID: { enabled: false },
  sandboxSelection: { allowMultiple: false },
  security: { readOnlyRootFilesystem: true },
  network: {
    blockedCIDRs: [],
    blockedFQDNs: [],
    blockedPorts: [],
    allowedInternalCIDRs: [],
    ingress: { mode: "authenticated", requireMTLS: false },
  },
};

const sandboxTemplate: Record<string, unknown> = {
  type: "kata",
  resources: {
    agent: {
      requests: { cpu: "500m", memory: "1Gi", "ephemeral-storage": "2Gi" },
      limits: { cpu: "500m", memory: "1Gi", "ephemeral-storage": "2Gi" },
    },
    helpers: {
      requests: { cpu: "100m", memory: "128Mi", "ephemeral-storage": "512Mi" },
      limits: { cpu: "100m", memory: "128Mi", "ephemeral-storage": "512Mi" },
    },
  },
  storage: {
    home: { storageClassName: "managed-csi-premium", size: "10Gi", accessMode: "ReadWriteOnce" },
    scratch: { sizeLimit: "2Gi" },
  },
};

const titleFor = (kind: ResourceKind) => (kind === "policy" ? "Agent policies" : "Sandboxes");
const singularFor = (kind: ResourceKind) => (kind === "policy" ? "AgentPolicy" : "Sandbox");
const pathFor = (kind: ResourceKind) => (kind === "policy" ? "agent-policies" : "sandboxes");
const queryKeyFor = (kind: ResourceKind) => ["admin", "governance", kind] as const;

function acceptedStatus(conditions?: GovernanceCondition[]): string {
  const condition = conditions?.find((item) => item.type === "Accepted" || item.type === "Ready");
  return condition ? `${condition.type}: ${condition.status}` : "Pending";
}

function listResource(kind: ResourceKind): Promise<Resource[]> {
  return kind === "policy" ? listAgentPolicies() : listSandboxes();
}

function getResource(kind: ResourceKind, name: string): Promise<Resource> {
  return kind === "policy" ? getAgentPolicy(name) : getSandbox(name);
}

function deleteResource(kind: ResourceKind, name: string): Promise<void> {
  return kind === "policy" ? deleteAgentPolicy(name) : deleteSandbox(name);
}

function createResource(kind: ResourceKind, name: string, spec: Record<string, unknown>): Promise<Resource> {
  const resource = {
    apiVersion: "cetirun.microsoft.com/v1alpha1",
    kind: singularFor(kind),
    metadata: { name },
    spec,
  };
  return kind === "policy"
    ? createAgentPolicy(resource as AgentPolicyResource)
    : createSandbox(resource as SandboxResource);
}

function updateResource(kind: ResourceKind, name: string, spec: Record<string, unknown>): Promise<Resource> {
  return kind === "policy" ? updateAgentPolicy(name, spec) : updateSandbox(name, spec);
}

export function GovernanceResourceListPage({ kind }: { kind: ResourceKind }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<string | null>(null);
  const { data, isLoading, error } = useQuery<Resource[], Error>({
    queryKey: queryKeyFor(kind),
    queryFn: () => listResource(kind),
  });
  const deletion = useMutation({
    mutationFn: (name: string) => deleteResource(kind, name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeyFor(kind) });
      setDeleting(null);
    },
  });

  return (
    <div>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 16 }}>
        <Title2>{titleFor(kind)}</Title2>
        <Button appearance="primary" icon={<Add20Regular />} onClick={() => navigate(`/admin/${pathFor(kind)}/new`)}>
          New {singularFor(kind)}
        </Button>
      </div>
      <Text block style={{ marginBottom: 16 }}>
        {kind === "policy"
          ? "Policies define approved runtimes, model access, budgets, identity and network controls."
          : "Sandboxes define the approved runtime, resource limits and private storage profile for agents."}
      </Text>
      {isLoading && <Spinner label={`Loading ${titleFor(kind).toLowerCase()}...`} />}
      {error && <MessageBar intent="error">Failed to load: {error.message}</MessageBar>}
      {deletion.error && <MessageBar intent="error">{deletion.error.message}</MessageBar>}
      {data && (
        <Table aria-label={titleFor(kind)}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>{kind === "policy" ? "Allowed harnesses" : "Runtime"}</TableHeaderCell>
              <TableHeaderCell>Status</TableHeaderCell>
              <TableHeaderCell style={{ width: 120 }}>Actions</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.length === 0 && <TableRow><TableCell colSpan={4}>No {titleFor(kind).toLowerCase()} yet.</TableCell></TableRow>}
            {data.map((resource) => (
              <TableRow key={resource.metadata.name}>
                <TableCell>{resource.metadata.name}</TableCell>
                <TableCell>
                  {kind === "policy"
                    ? ((resource.spec.harnesses as { allowed?: string[] } | undefined)?.allowed || []).join(", ") || "—"
                    : String(resource.spec.type || "kata")}
                </TableCell>
                <TableCell>{acceptedStatus(resource.status?.conditions)}</TableCell>
                <TableCell>
                  <Button appearance="subtle" size="small" icon={<Edit20Regular />} aria-label={`Edit ${resource.metadata.name}`} onClick={() => navigate(`/admin/${pathFor(kind)}/${resource.metadata.name}`)} />
                  <Button appearance="subtle" size="small" icon={<Delete20Regular />} aria-label={`Delete ${resource.metadata.name}`} disabled={deletion.isPending} onClick={() => setDeleting(resource.metadata.name)} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {deleting && (
        <MessageBar intent="warning" style={{ marginTop: 16 }}>
          Delete {singularFor(kind)} <strong>{deleting}</strong>? Existing governance bindings may reject reconciliation. {" "}
          <Button size="small" appearance="primary" disabled={deletion.isPending} onClick={() => deletion.mutate(deleting)}>Delete</Button>
          <Button size="small" appearance="secondary" onClick={() => setDeleting(null)}>Cancel</Button>
        </MessageBar>
      )}
    </div>
  );
}

export function GovernanceResourceEditPage({ kind }: { kind: ResourceKind }) {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const isCreate = !name || name === "new";
  const [resourceName, setResourceName] = useState("");
  const [specJSON, setSpecJSON] = useState(() => JSON.stringify(kind === "policy" ? policyTemplate : sandboxTemplate, null, 2));
  const [formError, setFormError] = useState<string | null>(null);
  const { data: existing, isLoading, error } = useQuery<Resource, Error>({
    queryKey: [...queryKeyFor(kind), name],
    queryFn: () => getResource(kind, name!),
    enabled: !isCreate,
  });
  useEffect(() => {
    if (existing) {
      setResourceName(existing.metadata.name);
      setSpecJSON(JSON.stringify(existing.spec, null, 2));
    }
  }, [existing]);
  const save = useMutation({
    mutationFn: ({ resourceName: itemName, spec }: { resourceName: string; spec: Record<string, unknown> }) =>
      isCreate ? createResource(kind, itemName, spec) : updateResource(kind, name!, spec),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeyFor(kind) });
      navigate(`/admin/${pathFor(kind)}`);
    },
    onError: (e: Error) => setFormError(e.message),
  });
  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setFormError(null);
    if (!/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/.test(resourceName.trim())) {
      setFormError("Name must be a DNS-1123 label: lowercase, start with a letter, max 63 characters.");
      return;
    }
    let spec: unknown;
    try { spec = JSON.parse(specJSON); } catch { setFormError("Specification must be valid JSON."); return; }
    if (!spec || Array.isArray(spec) || typeof spec !== "object") {
      setFormError("Specification must be a JSON object.");
      return;
    }
    save.mutate({ resourceName: resourceName.trim(), spec: spec as Record<string, unknown> });
  };
  if (!isCreate && isLoading) return <Spinner label={`Loading ${singularFor(kind)}...`} />;
  if (!isCreate && error) return <MessageBar intent="error">Failed to load: {error.message}</MessageBar>;
  return (
    <form onSubmit={onSubmit} style={{ maxWidth: 820, display: "flex", flexDirection: "column", gap: 16 }}>
      <Title2>{isCreate ? `New ${singularFor(kind)}` : `${singularFor(kind)}: ${name}`}</Title2>
      <Text>
        {kind === "policy"
          ? "Edit the complete AgentPolicy specification. The template starts restrictive; add only explicitly approved capabilities."
          : "Edit the complete Sandbox specification. Resource requests and limits must include CPU, memory and ephemeral storage."}
      </Text>
      <Field label="Name" required>
        <Input value={resourceName} disabled={!isCreate} onChange={(_, data) => setResourceName(data.value)} />
      </Field>
      <Field label="Specification" required hint="This is the CRD spec only; status and metadata are managed by the platform.">
        <Textarea aria-label={`${singularFor(kind)} specification`} rows={24} value={specJSON} onChange={(_, data) => setSpecJSON(data.value)} style={{ fontFamily: "monospace" }} />
      </Field>
      {formError && <MessageBar intent="error">{formError}</MessageBar>}
      <div style={{ display: "flex", gap: 8 }}>
        <Button appearance="primary" type="submit" disabled={save.isPending}>{save.isPending ? "Saving..." : isCreate ? "Create" : "Save"}</Button>
        <Button appearance="secondary" type="button" onClick={() => navigate(`/admin/${pathFor(kind)}`)}>Cancel</Button>
      </div>
    </form>
  );
}
