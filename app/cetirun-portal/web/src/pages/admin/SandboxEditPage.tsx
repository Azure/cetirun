// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Divider, Field, Input, MessageBar, Select, Spinner, Text, Title2 } from "@fluentui/react-components";
import { createSandbox, getSandbox, updateSandbox, type SandboxSpec, type SandboxUpsertRequest } from "../../api/adminSandbox";

const emptyForm: SandboxUpsertRequest = {
  name: "", type: "kata",
  resources: {
    agent: { requests: { cpu: "500m", memory: "1Gi", "ephemeral-storage": "1Gi" }, limits: { cpu: "2", memory: "4Gi", "ephemeral-storage": "8Gi" } },
    helpers: { requests: { cpu: "100m", memory: "128Mi", "ephemeral-storage": "128Mi" }, limits: { cpu: "500m", memory: "512Mi", "ephemeral-storage": "1Gi" } },
  },
  storage: { home: { storageClassName: "", size: "10Gi", accessMode: "ReadWriteOncePod" }, scratch: { sizeLimit: "1Gi" } },
};

export function SandboxEditPage() {
  const { name } = useParams<{ name: string }>();
  const isCreate = !name;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [form, setForm] = useState<SandboxUpsertRequest>(emptyForm);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const { data: existing, isLoading, error } = useQuery({ queryKey: ["admin", "sandbox", name], queryFn: () => getSandbox(name!), enabled: !isCreate });
  useEffect(() => {
    if (existing) setForm({ name: existing.name, uid: existing.uid, resourceVersion: existing.resourceVersion, type: existing.type, resources: existing.resources, storage: existing.storage });
  }, [existing]);
  const success = () => {
    queryClient.invalidateQueries({ queryKey: ["admin", "sandboxes"] });
    if (name) queryClient.invalidateQueries({ queryKey: ["admin", "sandbox", name] });
    navigate("/admin/sandboxes");
  };
  const mutation = useMutation({ mutationFn: (request: SandboxUpsertRequest) => isCreate ? createSandbox(request) : updateSandbox(name!, request), onSuccess: success, onError: (error: Error) => setSubmitError(error.message) });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setSubmitError(null);
    if (mutation.isPending || existing?.deleting) return;
    if (isCreate && !/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/.test(form.name?.trim() || "")) {
      setSubmitError("Name must be a DNS-1123 label: lowercase, start with a letter, max 63 chars."); return;
    }
    if (!form.storage.home.storageClassName.trim()) { setSubmitError("Choose the StorageClass used for Agent HOME."); return; }
    mutation.mutate({ ...form, name: form.name?.trim(), storage: { ...form.storage, home: { ...form.storage.home, storageClassName: form.storage.home.storageClassName.trim() } } });
  };
  if (!isCreate && isLoading) return <Spinner label="Loading Sandbox..." />;
  if (!isCreate && error) return <MessageBar intent="error">Failed to load: {error.message}</MessageBar>;
  return (
    <form onSubmit={submit} style={{ maxWidth: 720, display: "flex", flexDirection: "column", gap: 16 }}>
      <Title2>{isCreate ? "New Sandbox" : `Sandbox: ${name}`}</Title2>
      <Text>A Sandbox defines reusable runtime, resource and storage settings. The selected runtime and StorageClass must already be available in the cluster.</Text>
      <Field label="Name" required><Input value={form.name || ""} disabled={!isCreate} onChange={(_, data) => setForm({ ...form, name: data.value })} placeholder="e.g. standard" required /></Field>
      <Field label="Runtime" required><Select value={form.type} onChange={event => setForm({ ...form, type: event.target.value as SandboxSpec["type"] })}>
        <option value="kata">Kata</option><option value="gvisor">gVisor</option>
      </Select></Field>
      {(["agent", "helpers"] as const).map(container => <div key={container} style={{ display: "flex", flexDirection: "column", gap: 12 }}>
        <Divider />
        <Text weight="semibold">{container === "agent" ? "Agent resources" : "Resources per helper container"}</Text>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
          {(["cpu", "memory", "ephemeral-storage"] as const).flatMap(resource => (["requests", "limits"] as const).map(bound => <Field key={`${resource}-${bound}`}
            label={`${container === "agent" ? "Agent" : "Helper"} ${resource} ${bound === "requests" ? "request" : "limit"}`} required>
            <Input value={form.resources[container][bound][resource]} required onChange={(_, data) => setForm({ ...form, resources: { ...form.resources, [container]: { ...form.resources[container], [bound]: { ...form.resources[container][bound], [resource]: data.value } } } })} />
          </Field>))}
        </div>
        <Text size={300}>Use Kubernetes quantities such as 500m for CPU and 1Gi for memory or storage. Requests must not exceed limits.</Text>
      </div>)}
      <Divider />
      <Text weight="semibold">Agent HOME</Text>
      <Field label="HOME StorageClass" required hint="The name of an existing StorageClass, for example managed-csi."><Input value={form.storage.home.storageClassName} required onChange={(_, data) => setForm({ ...form, storage: { ...form.storage, home: { ...form.storage.home, storageClassName: data.value } } })} /></Field>
      <Field label="HOME size" required><Input value={form.storage.home.size} required onChange={(_, data) => setForm({ ...form, storage: { ...form.storage, home: { ...form.storage.home, size: data.value } } })} /></Field>
      <Field label="HOME access mode" required><Select value={form.storage.home.accessMode} onChange={event => setForm({ ...form, storage: { ...form.storage, home: { ...form.storage.home, accessMode: event.target.value as SandboxSpec["storage"]["home"]["accessMode"] } } })}>
        <option value="ReadWriteOncePod">ReadWriteOncePod</option><option value="ReadWriteOnce">ReadWriteOnce</option>
      </Select></Field>
      <Field label="Scratch size limit" required><Input value={form.storage.scratch.sizeLimit} required onChange={(_, data) => setForm({ ...form, storage: { ...form.storage, scratch: { sizeLimit: data.value } } })} /></Field>
      {submitError && <MessageBar intent="error">{submitError}</MessageBar>}
      <div style={{ display: "flex", gap: 8 }}>
        <Button appearance="primary" type="submit" disabled={mutation.isPending || !!existing?.deleting}>{mutation.isPending ? "Saving..." : isCreate ? "Create" : "Save"}</Button>
        <Button type="button" onClick={() => navigate("/admin/sandboxes")}>Cancel</Button>
      </div>
    </form>
  );
}
