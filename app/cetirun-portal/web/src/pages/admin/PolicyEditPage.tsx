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
import {
  emptyRateLimitForm,
  MAX_MODEL_LIMITS,
  rateLimitFormFrom,
  rateLimitsFromForm,
  TPM_HINT,
  type RateLimitForm,
  type RateLimitRow,
} from "../../utils/rateLimits";

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
  limits: RateLimitForm;
}

const emptyForm: FormState = {
  name: "", uid: "", resourceVersion: "", openclaw: true, hermes: false, skills: "", copilot: false,
  sharedModels: "", budgetEnabled: false, defaultMonthlyUSD: "", maxMonthlyUSD: "", limits: emptyRateLimitForm,
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
    limits: rateLimitFormFrom(policy.modelAccess?.rateLimits),
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
    const limits = rateLimitsFromForm(form.limits);
    if (limits.error) {
      setSubmitError(limits.error);
      return;
    }
    if (limits.limits) request.modelAccess!.rateLimits = limits.limits;
    if (isCreate) createMutation.mutate(request); else updateMutation.mutate(request);
  };
  const setLimits = (limits: Partial<RateLimitForm>) => setForm({ ...form, limits: { ...form.limits, ...limits } });
  const setRow = (index: number, row: Partial<RateLimitRow>) =>
    setLimits({ perModel: form.limits.perModel.map((current, i) => (i === index ? { ...current, ...row } : current)) });
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
      <Field label="Allowed skills" hint="Enter one skill identifier per line; commas are not separators. Leave empty to allow no Team or Agent skills."><Textarea rows={4} value={form.skills} onChange={(_, data) => setForm({ ...form, skills: data.value })} placeholder={"skill-a\nnpm:@scope/skill-b\nskill-c@1.2.0"} /></Field>
      <Divider />
      <Text weight="semibold">AI Gateway</Text>
      <Switch checked={form.copilot} label="Allow GitHub Copilot" onChange={(_, data) => {
        const copilot = data.checked;
        setForm({ ...form, copilot, budgetEnabled: copilot || form.sharedModels.trim() !== "" ? form.budgetEnabled : false });
      }} />
      <Field label="Allowed shared models" hint="Enter one exact Gateway catalog model name per line; commas are not separators."><Textarea rows={4} value={form.sharedModels} onChange={(_, data) => {
        const sharedModels = data.value;
        setForm({ ...form, sharedModels, budgetEnabled: form.copilot || sharedModels.trim() !== "" ? form.budgetEnabled : false });
      }} placeholder={"gpt-4.1\ngpt-4o-mini"} /></Field>
      <Switch checked={form.budgetEnabled} label="Set monthly Gateway budget" onChange={(_, data) => setForm({ ...form, budgetEnabled: data.checked })} />
      {form.budgetEnabled && <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
        <Field label="Default monthly USD" required><Input value={form.defaultMonthlyUSD} onChange={(_, data) => setForm({ ...form, defaultMonthlyUSD: data.value })} placeholder="e.g. 20" /></Field>
        <Field label="Maximum monthly USD" required><Input value={form.maxMonthlyUSD} onChange={(_, data) => setForm({ ...form, maxMonthlyUSD: data.value })} placeholder="e.g. 50" /></Field>
      </div>}
      <Divider />
      <Text weight="semibold">AI Gateway limits per Agent</Text>
      <Text size={200}>Every Agent of a Team that uses this policy gets these limits on its own Gateway key; Agents do not share them. Leave a field empty for no limit. A change applies to running Agents without a restart.</Text>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 12 }}>
        <Field label="Max parallel requests" hint="At least 1."><Input inputMode="numeric" value={form.limits.maxParallelRequests} onChange={(_, data) => setLimits({ maxParallelRequests: data.value })} placeholder="No limit" /></Field>
        <Field label="Requests per minute" hint="At least 1."><Input inputMode="numeric" value={form.limits.rpm} onChange={(_, data) => setLimits({ rpm: data.value })} placeholder="No limit" /></Field>
        <Field label="Tokens per minute" hint="At least 4,096."><Input inputMode="numeric" value={form.limits.tpm} onChange={(_, data) => setLimits({ tpm: data.value })} placeholder="No limit" /></Field>
      </div>
      <Text size={200}>{TPM_HINT}</Text>
      <Text size={200}>Total limits count every request of the key, including GitHub Copilot models. A per-model limit applies to the exact model name the Agent requests; for an auto router that is the router's name, not the model it picks.</Text>
      {form.limits.perModel.map((row, index) => (
        <div key={index} style={{ display: "grid", gridTemplateColumns: "2fr 1fr 1fr auto", gap: 8, alignItems: "center" }}>
          <Input aria-label={`Model name ${index + 1}`} value={row.model} onChange={(_, data) => setRow(index, { model: data.value })} placeholder="Gateway model name" />
          <Input aria-label={`Requests per minute for model ${index + 1}`} inputMode="numeric" value={row.rpm} onChange={(_, data) => setRow(index, { rpm: data.value })} placeholder="RPM" />
          <Input aria-label={`Tokens per minute for model ${index + 1}`} inputMode="numeric" value={row.tpm} onChange={(_, data) => setRow(index, { tpm: data.value })} placeholder="TPM" />
          <Button type="button" appearance="subtle" onClick={() => setLimits({ perModel: form.limits.perModel.filter((_, i) => i !== index) })}>Remove</Button>
        </div>
      ))}
      <div>
        <Button type="button" disabled={form.limits.perModel.length >= MAX_MODEL_LIMITS} onClick={() => setLimits({ perModel: [...form.limits.perModel, { model: "", rpm: "", tpm: "" }] })}>Add per-model limit</Button>
      </div>
      {submitError && <MessageBar intent="error">{submitError}</MessageBar>}
      <div style={{ display: "flex", gap: 8 }}>
        <Button appearance="primary" type="submit" disabled={pending}>{pending ? "Saving..." : isCreate ? "Create" : "Save"}</Button>
        <Button appearance="secondary" type="button" onClick={() => navigate("/admin/policies")}>Cancel</Button>
      </div>
    </form>
  );
}
