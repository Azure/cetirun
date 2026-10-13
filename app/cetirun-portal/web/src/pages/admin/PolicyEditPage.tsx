// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button, Divider, Field, Input, MessageBar, Spinner, Switch, Text, Textarea, Title2 } from "@fluentui/react-components";
import {
  createPolicy,
  getPolicy,
  updatePolicy,
  type AgentPolicyDTO,
  type AgentPolicyUpsertRequest,
} from "../../api/adminPolicy";

interface FormState {
  name: string;
  uid: string;
  resourceVersion: string;
  openclaw: boolean;
  hermes: boolean;
  skills: string;
  copilot: boolean;
  sharedModels: string;
  budgetEnabled: boolean;
  defaultMonthlyUSD: string;
  maxMonthlyUSD: string;
}

const emptyForm: FormState = {
  name: "", uid: "", resourceVersion: "", openclaw: true, hermes: false, skills: "", copilot: false,
  sharedModels: "", budgetEnabled: false, defaultMonthlyUSD: "", maxMonthlyUSD: "",
};

function fromDTO(policy: AgentPolicyDTO): FormState {
  return {
    name: policy.name,
    uid: policy.uid,
    resourceVersion: policy.resourceVersion,
    openclaw: policy.harnesses.allowed.includes("openclaw"),
    hermes: policy.harnesses.allowed.includes("hermes"),
    skills: (policy.skills?.allowed || []).join("\n"),
    copilot: !!policy.modelAccess?.copilot?.allowed,
    sharedModels: (policy.modelAccess?.sharedModels?.allowed || []).join("\n"),
    budgetEnabled: !!policy.modelAccess?.gatewayBudget,
    defaultMonthlyUSD: policy.modelAccess?.gatewayBudget?.defaultMonthlyUSD || "",
    maxMonthlyUSD: policy.modelAccess?.gatewayBudget?.maxMonthlyUSD || "",
  };
}

function toRequest(form: FormState): AgentPolicyUpsertRequest {
  const lines = (value: string) => value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const allowed = [] as ("openclaw" | "hermes")[];
  if (form.openclaw) allowed.push("openclaw");
  if (form.hermes) allowed.push("hermes");
  const request: AgentPolicyUpsertRequest = {
    name: form.name.trim(),
    uid: form.uid || undefined,
    resourceVersion: form.resourceVersion || undefined,
    harnesses: { allowed },
    skills: { allowed: lines(form.skills) },
    modelAccess: {
      copilot: { allowed: form.copilot },
      sharedModels: { allowed: lines(form.sharedModels) },
    },
  };
  if (form.budgetEnabled) {
    request.modelAccess!.gatewayBudget = {
      defaultMonthlyUSD: form.defaultMonthlyUSD.trim(),
      maxMonthlyUSD: form.maxMonthlyUSD.trim(),
    };
  }
  return request;
}

export function PolicyEditPage() {
  const { name } = useParams<{ name: string }>();
  const isCreate = !name;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [form, setForm] = useState<FormState>(emptyForm);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const { data: existing, isLoading, error } = useQuery<AgentPolicyDTO, Error>({
    queryKey: ["admin", "policy", name], queryFn: () => getPolicy(name!), enabled: !isCreate,
  });
  useEffect(() => { if (existing) setForm(fromDTO(existing)); }, [existing]);
  const success = () => {
    queryClient.invalidateQueries({ queryKey: ["admin", "policies"] });
    if (name) queryClient.invalidateQueries({ queryKey: ["admin", "policy", name] });
    navigate("/admin/policies");
  };
  const createMutation = useMutation({ mutationFn: createPolicy, onSuccess: success, onError: (error: Error) => setSubmitError(error.message) });
  const updateMutation = useMutation({ mutationFn: (request: AgentPolicyUpsertRequest) => updatePolicy(name!, request), onSuccess: success, onError: (error: Error) => setSubmitError(error.message) });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setSubmitError(null);
    const request = toRequest(form);
    if (request.harnesses.allowed.length === 0) {
      setSubmitError("Choose at least one harness.");
      return;
    }
    const hasModelEntitlement = !!request.modelAccess?.copilot?.allowed ||
      (request.modelAccess?.sharedModels?.allowed?.length || 0) > 0;
    if (hasModelEntitlement && !request.modelAccess?.gatewayBudget) {
      setSubmitError("Model access requires a monthly Gateway budget.");
      return;
    }
    if (!hasModelEntitlement && request.modelAccess?.gatewayBudget) {
      setSubmitError("A Gateway budget requires Copilot or a shared model.");
      return;
    }
    if (isCreate && !/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/.test(request.name || "")) {
      setSubmitError("Name must be a DNS-1123 label: lowercase, start with a letter, max 63 chars.");
      return;
    }
    if (isCreate) createMutation.mutate(request); else updateMutation.mutate(request);
  };
  if (!isCreate && isLoading) return <Spinner label="Loading policy..." />;
  if (!isCreate && error) return <MessageBar intent="error">Failed to load: {error.message}</MessageBar>;
  const pending = createMutation.isPending || updateMutation.isPending;
  return (
    <form onSubmit={submit} style={{ maxWidth: 720, display: "flex", flexDirection: "column", gap: 16 }}>
      <Title2>{isCreate ? "New agent policy" : `Agent policy: ${name}`}</Title2>
      <Text>This page manages the common Policy fields exposed by Portal. Gateway and Sandbox remain responsible for executing the fields they own.</Text>
      <Field label="Name" required><Input value={form.name} disabled={!isCreate} onChange={(_, data) => setForm({ ...form, name: data.value })} placeholder="e.g. standard" /></Field>
      <Divider />
      <Text weight="semibold">Allowed harnesses</Text>
      <Switch checked={form.openclaw} label="OpenClaw" onChange={(_, data) => setForm({ ...form, openclaw: data.checked })} />
      <Switch checked={form.hermes} label="Hermes" onChange={(_, data) => setForm({ ...form, hermes: data.checked })} />
      <Field label="Allowed skills" hint="One skill identifier per line. Leave empty to allow no Team or Agent skills."><Textarea rows={4} value={form.skills} onChange={(_, data) => setForm({ ...form, skills: data.value })} /></Field>
      <Divider />
      <Text weight="semibold">AI Gateway</Text>
      <Switch checked={form.copilot} label="Allow GitHub Copilot" onChange={(_, data) => {
        const copilot = data.checked;
        setForm({ ...form, copilot, budgetEnabled: copilot || form.sharedModels.trim() !== "" ? form.budgetEnabled : false });
      }} />
      <Field label="Allowed shared models" hint="One exact Gateway catalog model name per line."><Textarea rows={4} value={form.sharedModels} onChange={(_, data) => {
        const sharedModels = data.value;
        setForm({ ...form, sharedModels, budgetEnabled: form.copilot || sharedModels.trim() !== "" ? form.budgetEnabled : false });
      }} placeholder="e.g. gpt-4.1" /></Field>
      <Switch checked={form.budgetEnabled} label="Set monthly Gateway budget" onChange={(_, data) => setForm({ ...form, budgetEnabled: data.checked })} />
      {form.budgetEnabled && <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <Field label="Default monthly USD" required><Input value={form.defaultMonthlyUSD} onChange={(_, data) => setForm({ ...form, defaultMonthlyUSD: data.value })} placeholder="e.g. 20" /></Field>
        <Field label="Maximum monthly USD" required><Input value={form.maxMonthlyUSD} onChange={(_, data) => setForm({ ...form, maxMonthlyUSD: data.value })} placeholder="e.g. 50" /></Field>
      </div>}
      {submitError && <MessageBar intent="error">{submitError}</MessageBar>}
      <div style={{ display: "flex", gap: 8 }}>
        <Button appearance="primary" type="submit" disabled={pending}>{pending ? "Saving..." : isCreate ? "Create" : "Save"}</Button>
        <Button appearance="secondary" type="button" onClick={() => navigate("/admin/policies")}>Cancel</Button>
      </div>
    </form>
  );
}
