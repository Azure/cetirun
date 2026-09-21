// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useMemo, useState } from "react";
import { Button, Input, Field, Switch, MessageBar, Select } from "@fluentui/react-components";
import { useCreateInstance } from "../hooks/useInstance";
import { useFeatures } from "../hooks/useFeatures";
import { GITHUB_COPILOT_PROVIDER, useEffectiveTeamProvider } from "../hooks/useTeams";
import type { AgentType } from "../types/instance";
import { TeamPicker } from "./TeamPicker";
import { CopilotModelPicker } from "./CopilotModelPicker";

const DEFAULT_AGENT_TYPES: AgentType[] = ["hermes", "openclaw"];
const AGENT_TYPE_LABELS: Record<AgentType, string> = {
  openclaw: "OpenClaw",
  hermes: "Hermes",
};

export function CreateForm({ onCreated }: { onCreated?: () => void }) {
  const [instanceName, setInstanceName] = useState("");
  const [agentType, setAgentType] = useState<AgentType>("hermes");
  const [displayName, setDisplayName] = useState("");
  const [team, setTeam] = useState("");
  const [gatewayToken, setGatewayToken] = useState("");
  const [tailscaleAuthKey, setTailscaleAuthKey] = useState("");
  const [model, setModel] = useState("");
  const [teamsEnabled, setTeamsEnabled] = useState(false);
  const createMutation = useCreateInstance({ onSuccess: onCreated });
  const { data: features, isLoading: featuresLoading } = useFeatures();
  const effectiveProvider = useEffectiveTeamProvider(team);
  const usesGitHubCopilot = effectiveProvider === GITHUB_COPILOT_PROVIDER;

  const allowedAgentTypes = useMemo(
    () => features?.allowedAgentTypes ?? DEFAULT_AGENT_TYPES,
    [features?.allowedAgentTypes],
  );

  useEffect(() => {
    if (allowedAgentTypes.length > 0 && !allowedAgentTypes.includes(agentType)) {
      setAgentType(allowedAgentTypes[0]);
    }
  }, [agentType, allowedAgentTypes]);

  useEffect(() => {
    if (!usesGitHubCopilot) setModel("");
  }, [usesGitHubCopilot]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    createMutation.mutate({
      instanceName: instanceName || undefined,
      agentType,
      displayName: displayName || undefined,
      team: team || undefined,
      gatewayToken: gatewayToken || undefined,
      tailscaleAuthKey: tailscaleAuthKey || undefined,
      model: usesGitHubCopilot ? model.trim() || undefined : undefined,
      teamsEnabled: teamsEnabled || undefined,
    });
  };

  return (
    <form
      onSubmit={handleSubmit}
      style={{ display: "flex", flexDirection: "column", gap: 16, marginTop: 16 }}
    >
      <Field
        label="Agent Name (optional)"
        hint={
          instanceName
            ? `CR name will be: <your-alias>-${instanceName} (e.g. alice-${instanceName})`
            : "Leave blank to use your alias as the agent name. Lowercase letters, digits, hyphens only."
        }
      >
        <Input
          value={instanceName}
          onChange={(_, data) => setInstanceName(data.value)}
          placeholder="e.g. my-project (leave blank for default)"
          pattern="[a-z][a-z0-9-]*"
        />
      </Field>

      <Field label="Agent Type">
        <Select
          value={allowedAgentTypes.includes(agentType) ? agentType : ""}
          disabled={allowedAgentTypes.length <= 1}
          onChange={(event) => setAgentType(event.target.value as AgentType)}
        >
          {allowedAgentTypes.map((type) => (
            <option key={type} value={type}>
              {AGENT_TYPE_LABELS[type]}
            </option>
          ))}
        </Select>
      </Field>

      <Field label="Display Name (optional)">
        <Input
          value={displayName}
          onChange={(_, data) => setDisplayName(data.value)}
          placeholder="Defaults to your profile name"
        />
      </Field>

      <TeamPicker value={team} onChange={setTeam} />

      {usesGitHubCopilot && <CopilotModelPicker value={model} onChange={setModel} />}

      <Field label="Gateway Token (optional)">
        <Input
          type="password"
          autoComplete="off"
          value={gatewayToken}
          onChange={(_, data) => setGatewayToken(data.value)}
          placeholder="Leave empty to auto-generate"
        />
      </Field>

      {features?.tailscaleEnabled && (
        <Field label="Tailscale Auth Key (optional)">
          <Input
            type="password"
            autoComplete="off"
            value={tailscaleAuthKey}
            onChange={(_, data) => setTailscaleAuthKey(data.value)}
            placeholder="Leave empty to skip Tailscale"
          />
        </Field>
      )}

      <Switch
        checked={teamsEnabled}
        onChange={(_, data) => setTeamsEnabled(!!data.checked)}
        label="Enable Microsoft Teams Integration"
      />

      <Button
        type="submit"
        appearance="primary"
        disabled={
          createMutation.isPending ||
          featuresLoading ||
          allowedAgentTypes.length === 0
        }
      >
        {createMutation.isPending ? "Creating..." : "Create Agent"}
      </Button>

      {createMutation.error && (
        <MessageBar intent="error">
          {(createMutation.error as Error).message}
        </MessageBar>
      )}
    </form>
  );
}
