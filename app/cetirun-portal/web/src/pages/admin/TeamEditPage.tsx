// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useState, type FormEvent } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  Title2,
  Button,
  Spinner,
  MessageBar,
  Field,
  Input,
  Textarea,
  Text,
  Divider,
  tokens,
} from "@fluentui/react-components";
import {
  createTeam,
  getTeam,
  updateTeam,
  type TeamDTO,
  type TeamSkillStorage,
  type TeamUpsertRequest,
} from "../../api/adminTeam";

interface FormState {
  name: string;
  role: string;
  description: string; // newline-separated
  defaultProvider: string;
  skills: string; // newline-separated
  storage: TeamSkillStorage;
  storageEnabled: boolean;
  membership: string;
  normalAgentPolicyRef: string;
  adminAgentPolicyRef: string;
  sandboxRefs: string;
  defaultSandboxRef: string;
}

const emptyStorage: TeamSkillStorage = {
  resourceGroup: "",
  storageAccount: "",
  containerName: "",
  storageClassName: "",
  storageSize: "",
  mountPath: "",
};

function fromDTO(t: TeamDTO): FormState {
  return {
    name: t.name,
    role: t.role || "",
    description: (t.description || []).join("\n"),
    defaultProvider: t.defaultProvider || "",
    skills: (t.skills || []).join("\n"),
    storage: t.skillStorage || { ...emptyStorage },
    storageEnabled: !!t.skillStorage,
    membership: JSON.stringify(t.membership || [], null, 2),
    normalAgentPolicyRef: t.normalAgentPolicyRef || "",
    adminAgentPolicyRef: t.adminAgentPolicyRef || "",
    sandboxRefs: (t.sandboxRefs || []).join("\n"),
    defaultSandboxRef: t.defaultSandboxRef || "",
  };
}

function toRequest(s: FormState): TeamUpsertRequest {
  const splitLines = (text: string) =>
    text
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line.length > 0);

  let membership: TeamUpsertRequest["membership"];
  try { membership = JSON.parse(s.membership); } catch { throw new Error("Membership must be a JSON array of verified subjects."); }
  if (!Array.isArray(membership)) throw new Error("Membership must be a JSON array of verified subjects.");
  const req: TeamUpsertRequest = {
    name: s.name.trim(),
    role: s.role.trim() || undefined,
    description: splitLines(s.description),
    defaultProvider: s.defaultProvider.trim() || undefined,
    skills: splitLines(s.skills),
    membership,
    normalAgentPolicyRef: s.normalAgentPolicyRef.trim(),
    adminAgentPolicyRef: s.adminAgentPolicyRef.trim() || undefined,
    sandboxRefs: splitLines(s.sandboxRefs),
    defaultSandboxRef: s.defaultSandboxRef.trim(),
  };
  if (s.storageEnabled) {
    req.skillStorage = {
      resourceGroup: s.storage.resourceGroup.trim(),
      storageAccount: s.storage.storageAccount.trim(),
      containerName: s.storage.containerName.trim(),
      storageClassName: s.storage.storageClassName?.trim() || undefined,
      storageSize: s.storage.storageSize?.trim() || undefined,
      mountPath: s.storage.mountPath?.trim() || undefined,
    };
  }
  if (req.description?.length === 0) delete req.description;
  if (req.skills?.length === 0) delete req.skills;
  return req;
}

export function TeamEditPage() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const isCreate = !name || name === "new";

  const [form, setForm] = useState<FormState>({
    name: "",
    role: "",
    description: "",
    defaultProvider: "",
    skills: "",
    storage: { ...emptyStorage },
    storageEnabled: false,
    membership: "[]",
    normalAgentPolicyRef: "",
    adminAgentPolicyRef: "",
    sandboxRefs: "",
    defaultSandboxRef: "",
  });
  const [submitError, setSubmitError] = useState<string | null>(null);

  const { data: existing, isLoading, error } = useQuery<TeamDTO, Error>({
    queryKey: ["admin", "team", name],
    queryFn: () => getTeam(name!),
    enabled: !isCreate,
  });

  useEffect(() => {
    if (existing) setForm(fromDTO(existing));
  }, [existing]);

  const onSuccess = () => {
    qc.invalidateQueries({ queryKey: ["admin", "teams"] });
    if (name) qc.invalidateQueries({ queryKey: ["admin", "team", name] });
    navigate("/admin/teams");
  };

  const createMut = useMutation({
    mutationFn: (req: TeamUpsertRequest) => createTeam(req),
    onSuccess,
    onError: (e: Error) => setSubmitError(e.message),
  });
  const updateMut = useMutation({
    mutationFn: (req: TeamUpsertRequest) => {
      if (!existing?.uid || !existing.resourceVersion) {
        return Promise.reject(new Error("Team version is unavailable. Reload before saving."));
      }
      return updateTeam(name!, req, { uid: existing.uid, resourceVersion: existing.resourceVersion });
    },
    onSuccess,
    onError: (e: Error) => setSubmitError(e.message),
  });

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setSubmitError(null);
    let req: TeamUpsertRequest;
    try { req = toRequest(form); } catch (err) { setSubmitError(err instanceof Error ? err.message : "Invalid Team policy configuration"); return; }
    if (isCreate) {
      if (!/^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/.test(req.name || "")) {
        setSubmitError(
          "Name must be a DNS-1123 label: lowercase, start with a letter, max 63 chars."
        );
        return;
      }
      createMut.mutate(req);
    } else {
      updateMut.mutate(req);
    }
  };

  if (!isCreate && isLoading) return <Spinner label="Loading team..." />;
  if (!isCreate && error) {
    return <MessageBar intent="error">Failed to load: {error.message}</MessageBar>;
  }

  const pending = createMut.isPending || updateMut.isPending;

  return (
    <form onSubmit={onSubmit} style={{ maxWidth: 720, display: "flex", flexDirection: "column", gap: 16 }}>
      <Title2>{isCreate ? "New team" : `Team: ${name}`}</Title2>

      <Field label="Name" required>
        <Input
          value={form.name}
          disabled={!isCreate}
          onChange={(_, data) => setForm({ ...form, name: data.value })}
          placeholder="e.g. aks"
        />
      </Field>

      <Field label="Role" hint="A short label describing the team's role.">
        <Input
          value={form.role}
          onChange={(_, data) => setForm({ ...form, role: data.value })}
          placeholder="e.g. AKS Engineer"
        />
      </Field>

      <Field
        label="Description"
        hint="One bullet per line. Used as additional context for team members."
      >
        <Textarea
          rows={4}
          value={form.description}
          onChange={(_, data) => setForm({ ...form, description: data.value })}
        />
      </Field>

      <Divider />
      <Text size={400} weight="semibold">Defaults</Text>

      <Field label="Default provider">
        <Input
          value={form.defaultProvider}
          onChange={(_, data) => setForm({ ...form, defaultProvider: data.value })}
          placeholder="e.g. github_copilot"
        />
      </Field>

      <Divider />
      <Text size={400} weight="semibold">Policy assignment</Text>
      <Field label="Verified membership" required hint='JSON subjects, e.g. [{"kind":"User","tenantID":"...","objectID":"..."}]'>
        <Textarea rows={6} value={form.membership} onChange={(_, data) => setForm({ ...form, membership: data.value })} />
      </Field>
      <Field label="Normal AgentPolicy" required><Input value={form.normalAgentPolicyRef} onChange={(_, data) => setForm({ ...form, normalAgentPolicyRef: data.value })} /></Field>
      <Field label="Admin AgentPolicy"><Input value={form.adminAgentPolicyRef} onChange={(_, data) => setForm({ ...form, adminAgentPolicyRef: data.value })} /></Field>
      <Field label="Allowed Sandboxes" required hint="One Sandbox name per line"><Textarea rows={3} value={form.sandboxRefs} onChange={(_, data) => setForm({ ...form, sandboxRefs: data.value })} /></Field>
      <Field label="Default Sandbox" required><Input value={form.defaultSandboxRef} onChange={(_, data) => setForm({ ...form, defaultSandboxRef: data.value })} /></Field>
      <Divider />
      <Field label="Skills" hint="One skill identifier per line.">
        <Textarea
          rows={4}
          value={form.skills}
          onChange={(_, data) => setForm({ ...form, skills: data.value })}
          placeholder={"@anthropic/skill-foo\n@scope/skill-bar"}
        />
      </Field>

      <Divider />
      <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
        <Text size={400} weight="semibold">Skill storage</Text>
        <label style={{ display: "flex", alignItems: "center", gap: 6 }}>
          <input
            type="checkbox"
            checked={form.storageEnabled}
            onChange={(e) => setForm({ ...form, storageEnabled: e.target.checked })}
          />
          <Text size={300}>Enable shared skill storage</Text>
        </label>
      </div>

      {form.storageEnabled && (
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "1fr 1fr",
            gap: 12,
            padding: 12,
            border: `1px solid ${tokens.colorNeutralStroke2}`,
            borderRadius: 4,
          }}
        >
          <Field label="Resource group" required>
            <Input
              value={form.storage.resourceGroup}
              onChange={(_, data) =>
                setForm({ ...form, storage: { ...form.storage, resourceGroup: data.value } })
              }
            />
          </Field>
          <Field label="Storage account" required>
            <Input
              value={form.storage.storageAccount}
              onChange={(_, data) =>
                setForm({ ...form, storage: { ...form.storage, storageAccount: data.value } })
              }
            />
          </Field>
          <Field label="Container name" required>
            <Input
              value={form.storage.containerName}
              onChange={(_, data) =>
                setForm({ ...form, storage: { ...form.storage, containerName: data.value } })
              }
            />
          </Field>
          <Field label="Storage class">
            <Input
              value={form.storage.storageClassName || ""}
              onChange={(_, data) =>
                setForm({ ...form, storage: { ...form.storage, storageClassName: data.value } })
              }
            />
          </Field>
          <Field label="Storage size">
            <Input
              value={form.storage.storageSize || ""}
              onChange={(_, data) =>
                setForm({ ...form, storage: { ...form.storage, storageSize: data.value } })
              }
              placeholder="e.g. 10Gi"
            />
          </Field>
          <Field label="Mount path">
            <Input
              value={form.storage.mountPath || ""}
              onChange={(_, data) =>
                setForm({ ...form, storage: { ...form.storage, mountPath: data.value } })
              }
              placeholder="e.g. /home/vibe/.aks/skills (default)"
            />
          </Field>
        </div>
      )}

      <div style={{ display: "flex", flexDirection: "column", gap: 8, marginTop: 8 }}>
        {submitError && <MessageBar intent="error">{submitError}</MessageBar>}
        <div style={{ display: "flex", gap: 8 }}>
          <Button appearance="primary" type="submit" disabled={pending}>
            {pending ? "Saving..." : isCreate ? "Create" : "Save"}
          </Button>
          <Button
            appearance="secondary"
            type="button"
            onClick={() => navigate("/admin/teams")}
          >
            Cancel
          </Button>
        </div>
      </div>
    </form>
  );
}
