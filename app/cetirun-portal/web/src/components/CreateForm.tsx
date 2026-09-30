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
  custom: "Custom",
};

export function CreateForm({ onCreated }: { onCreated?: () => void }) {
  const [instanceName, setInstanceName] = useState("");
  const [agentType, setAgentType] = useState<AgentType>("hermes");
  const [image, setImage] = useState("");
  const [username, setUsername] = useState("vibe");
  const [port, setPort] = useState("");
  const [validationError, setValidationError] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [team, setTeam] = useState("");
  const [gatewayToken, setGatewayToken] = useState("");
  const [tailscaleAuthKey, setTailscaleAuthKey] = useState("");
  const [model, setModel] = useState("");
  const [teamsEnabled, setTeamsEnabled] = useState(false);
  const [agentIdentityEnabled, setAgentIdentityEnabled] = useState(false);
  const createMutation = useCreateInstance({ onSuccess: onCreated });
  const { data: features, isLoading: featuresLoading } = useFeatures();
  const effectiveProvider = useEffectiveTeamProvider(team);
  const custom = agentType === "custom";
  const usesGitHubCopilot = !custom && effectiveProvider === GITHUB_COPILOT_PROVIDER;

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

  useEffect(() => {
    if (!features?.agentIdentityEnabled || agentType === "custom") setAgentIdentityEnabled(false);
  }, [agentType, features?.agentIdentityEnabled]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const httpPort = port.trim() === "" ? undefined : Number(port);
    if (custom && (!image.trim() || !/^[a-z_][a-z0-9_-]{0,31}$/.test(username) || username === "root")) {
      setValidationError("Custom requires an image and an existing non-root image username.");
      return;
    }
    if (custom && httpPort !== undefined && (!Number.isInteger(httpPort) || httpPort < 1 || httpPort > 65535)) {
      setValidationError("HTTP port must be an integer between 1 and 65535, or left blank for a headless Agent.");
      return;
    }
    setValidationError("");
    createMutation.mutate({
      instanceName: instanceName || undefined,
      agent: custom
        ? { type: agentType, image: image.trim(), username, ...(httpPort === undefined ? {} : { port: httpPort }) }
        : { type: agentType },
      displayName: displayName || undefined,
      team: team || undefined,
      gatewayToken: !custom ? gatewayToken || undefined : undefined,
      tailscaleAuthKey: !custom ? tailscaleAuthKey || undefined : undefined,
      model: usesGitHubCopilot ? model.trim() || undefined : undefined,
      teamsEnabled: !custom ? teamsEnabled || undefined : undefined,
      agentIdentityEnabled: !custom ? agentIdentityEnabled || undefined : undefined,
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
          pattern={"[a-z](?:[a-z0-9\\-]*[a-z0-9])?"}
        />
      </Field>

      <Field label="Agent Type">
        <Select
          value={allowedAgentTypes.includes(agentType) ? agentType : ""}
          disabled={allowedAgentTypes.length <= 1}
          onChange={(event) => {
            setAgentType(event.target.value as AgentType);
            setModel("");
            setGatewayToken("");
            setTailscaleAuthKey("");
            setTeamsEnabled(false);
            setAgentIdentityEnabled(false);
            setValidationError("");
          }}
        >
          {allowedAgentTypes.map((type) => (
            <option key={type} value={type}>
              {AGENT_TYPE_LABELS[type]}
            </option>
          ))}
        </Select>
      </Field>

      {custom && (
        <>
          <MessageBar intent="info">
            Model setup and login remain with the image. Registry credentials and image changes after creation are managed separately.
          </MessageBar>
          <Field label="Agent Image" required hint="Use an image supported by the configured runtime. The image keeps its own entrypoint.">
            <Input value={image} onChange={(_, data) => setImage(data.value)} required maxLength={2048} placeholder="registry.example.com/team/agent@sha256:…" />
          </Field>
          <Field label="Agent Username" required hint="An existing non-root user in the image; HOME is /home/<username>. This cannot change after creation.">
            <Input value={username} onChange={(_, data) => setUsername(data.value)} required maxLength={32} pattern={"[a-z_][a-z0-9_\\-]{0,31}"} />
          </Field>
          <Field label="HTTP Port (optional)" hint="Leave blank for headless operation. Platform Terminal and Desktop do not need this port.">
            <Input type="number" min={1} max={65535} step={1} value={port} onChange={(_, data) => setPort(data.value)} placeholder="e.g. 8080" />
          </Field>
        </>
      )}

      <Field label="Display Name (optional)">
        <Input
          value={displayName}
          onChange={(_, data) => setDisplayName(data.value)}
          placeholder="Defaults to your profile name"
        />
      </Field>

      <TeamPicker value={team} onChange={setTeam} />

      {usesGitHubCopilot && <CopilotModelPicker value={model} onChange={setModel} />}

      {!custom && <Field label="Gateway Token (optional)">
        <Input
          type="password"
          autoComplete="off"
          value={gatewayToken}
          onChange={(_, data) => setGatewayToken(data.value)}
          placeholder="Leave empty to auto-generate"
        />
      </Field>}

      {!custom && features?.tailscaleEnabled && (
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

      {!custom && <Switch
        checked={teamsEnabled}
        onChange={(_, data) => setTeamsEnabled(!!data.checked)}
        label="Enable Microsoft Teams Integration"
      />}

      {features?.agentIdentityEnabled && agentType !== "custom" && (
        <Switch
          checked={agentIdentityEnabled}
          onChange={(_, data) => setAgentIdentityEnabled(!!data.checked)}
          label="Enable Microsoft Entra Agent Identity"
        />
      )}

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

      {validationError && <MessageBar intent="error">{validationError}</MessageBar>}
      {createMutation.error && (
        <MessageBar intent="error">
          {(createMutation.error as Error).message}
        </MessageBar>
      )}
    </form>
  );
}
