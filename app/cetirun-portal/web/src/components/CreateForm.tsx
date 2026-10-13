// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useMemo, useState } from "react";
import { Button, Input, Field, Switch, MessageBar, Select } from "@fluentui/react-components";
import { useCreateInstance } from "../hooks/useInstance";
import { useFeatures } from "../hooks/useFeatures";
import { useAIGatewayCatalog } from "../hooks/useAIGateway";
import { GITHUB_COPILOT_PROVIDER, useEffectiveTeamProvider, useTeams } from "../hooks/useTeams";
import type { AgentType } from "../types/instance";
import { TeamPicker } from "./TeamPicker";
import { CopilotModelPicker } from "./CopilotModelPicker";
import { CreateInstanceError } from "../api/instance";

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
  const [webUI, setWebUI] = useState(false);
  const [validationError, setValidationError] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [team, setTeam] = useState("");
  const [gatewayToken, setGatewayToken] = useState("");
  const [tailscaleAuthKey, setTailscaleAuthKey] = useState("");
  const [model, setModel] = useState("");
  const [teamsEnabled, setTeamsEnabled] = useState(false);
  const [agentIdentityEnabled, setAgentIdentityEnabled] = useState(false);
  const [privateRegistry, setPrivateRegistry] = useState(false);
  const [registryUsername, setRegistryUsername] = useState("");
  const [registryPassword, setRegistryPassword] = useState("");
  const [requestId] = useState(() => crypto.randomUUID());
  const clearRegistryCredentials = () => {
    setRegistryUsername("");
    setRegistryPassword("");
  };
  const [useSharedModels, setUseSharedModels] = useState(true);
  const [useCopilot, setUseCopilot] = useState(false);
  const [gatewayModel, setGatewayModel] = useState("");
  const createMutation = useCreateInstance({ onSuccess: onCreated, onSettled: clearRegistryCredentials });
  const { data: features, isLoading: featuresLoading } = useFeatures();
  const { data: teams = [] } = useTeams();
  const selectedTeam = teams.find((candidate) => candidate.name === team);
  const effectiveProvider = useEffectiveTeamProvider(team);
  const custom = agentType === "custom";
  const builtIn = !custom;
  const gatewayOn = !!features?.aiGatewayEnabled;
  // Built-in Agents always route platform model traffic through the Gateway.
  // Custom Agents can explicitly request its sources, but their approved
  // runtime decides whether that request is consumed.
  const gatewayModels = gatewayOn && (agentType === "openclaw" || agentType === "hermes" || custom);
  const builtInGatewayModels = gatewayModels && builtIn;
  const usesGitHubCopilot = builtIn && effectiveProvider === GITHUB_COPILOT_PROVIDER && !gatewayModels;
  const catalogQuery = useAIGatewayCatalog(gatewayModels);
  const catalog = catalogQuery.data;
  const catalogKnown = !!catalog?.known;
  // Only the agent types the deployment's agent chart routes through the
  // Gateway can take its models; null while the operator has not read that
  // chart. A catalog without the list comes from an earlier operator, which
  // routes only OpenClaw.
  const routedAgentTypes = catalog?.agentTypes === undefined ? ["openclaw"] : catalog.agentTypes;
  const agentTypesUnknown = builtInGatewayModels && catalogKnown && routedAgentTypes === null;
  const chartUnsupported =
    builtInGatewayModels && catalogKnown && routedAgentTypes !== null && !routedAgentTypes.includes(agentType);
  const sharedModels = useMemo(() => {
    if (!catalog?.known) return [];
    if (custom) return catalog.sharedModels;
    if (!selectedTeam) return [];
    const allowed = new Set(selectedTeam.policy.sharedModels);
    return catalog.sharedModels.filter((model) => allowed.has(model.name));
  }, [catalog, custom, selectedTeam]);
  const sharedAvailable = sharedModels.length > 0;
  const copilotOffered = custom
    ? catalogKnown && !!catalog?.githubCopilot
    : catalogKnown && !!catalog?.githubCopilot && !!selectedTeam?.policy.githubCopilot;
  const harnessAllowed = custom || !selectedTeam || selectedTeam.policy.harnesses.includes(agentType);
  // An OpenClaw or Hermes agent needs a model to use: shared models while any are
  // available, or GitHub Copilot while the deployment offers it. Without
  // GitHub Copilot, shared models stay on; with no shared model on,
  // GitHub Copilot does.
  const sharedForced = builtIn && !copilotOffered && sharedAvailable;
  const effectiveShared = sharedForced || useSharedModels;
  const copilotForced = builtIn && copilotOffered && (!effectiveShared || !sharedAvailable);
  const effectiveCopilot = copilotOffered && (copilotForced || useCopilot);
  const noModelSource = builtInGatewayModels && catalogKnown && !!selectedTeam && !sharedAvailable && !copilotOffered;

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
    if (!effectiveShared || !sharedModels.some((m) => m.name === gatewayModel)) setGatewayModel("");
  }, [effectiveShared, sharedModels, gatewayModel]);

  useEffect(() => {
    if (!features?.agentIdentityEnabled || agentType === "custom") setAgentIdentityEnabled(false);
  }, [agentType, features?.agentIdentityEnabled]);

  useEffect(() => {
    // Unlike built-in Agents, a Custom runtime must receive an explicit
    // Gateway-source request. Do not carry built-in defaults across a switch.
    if (custom) {
      setUseSharedModels(false);
      setUseCopilot(false);
      setGatewayModel("");
    }
  }, [custom]);

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
    if (custom && webUI && httpPort === undefined) {
      setValidationError("A port is required when Web UI is enabled.");
      return;
    }
    setValidationError("");
    createMutation.mutate({
      instanceName: instanceName || undefined,
      agent: custom
        ? { type: agentType, image: image.trim(), username, port: httpPort, webUI: webUI || undefined }
        : { type: agentType, image: image.trim() || undefined },
      registryAuth: privateRegistry ? { username: registryUsername, password: registryPassword } : undefined,
      requestId: privateRegistry ? requestId : undefined,
      displayName: displayName || undefined,
      team: team || undefined,
      gatewayToken: !custom ? gatewayToken || undefined : undefined,
      tailscaleAuthKey: !custom ? tailscaleAuthKey || undefined : undefined,
      model: builtInGatewayModels ? gatewayModel || undefined : usesGitHubCopilot ? model.trim() || undefined : undefined,
      teamsEnabled: !custom ? teamsEnabled || undefined : undefined,
      agentIdentityEnabled: !custom ? agentIdentityEnabled || undefined : undefined,
      modelAccess: builtInGatewayModels
        ? { sharedModels: effectiveShared, githubCopilot: effectiveCopilot }
        : custom && (effectiveShared || effectiveCopilot)
          ? { sharedModels: effectiveShared, githubCopilot: effectiveCopilot }
          : undefined,
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
            setWebUI(false);
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
        <MessageBar intent="info">
          Model setup and login remain with the image. Image updates retain HOME and require a compatible image.
        </MessageBar>
      )}
      <Field
        label={custom || privateRegistry ? "Agent Image" : "Agent Image (optional)"}
        hint={custom
          ? "Use an image supported by the configured runtime. The image keeps its own entrypoint."
          : "Leave empty for the platform default. Overrides must be qualified for the selected agent type."}
        required={custom || privateRegistry}
      >
        <Input value={image} onChange={(_, data) => setImage(data.value)} maxLength={2048} placeholder="registry.example.com/team/agent:version" required={custom || privateRegistry} />
      </Field>
      {custom && (
        <>
          <Field label="Agent Username" required hint="An existing non-root user in the image; HOME is /home/<username>. This cannot change after creation.">
            <Input value={username} onChange={(_, data) => setUsername(data.value)} required maxLength={32} pattern={"[a-z_][a-z0-9_\\-]{0,31}"} />
          </Field>
          <Switch checked={webUI} label="Serve Web UI" onChange={(_, data) => setWebUI(data.checked)} />
          <Field label={webUI ? "Web UI Port" : "HTTP Port (optional)"} required={webUI}
            hint={webUI ? "Serve a browser UI through platform authentication instead of the Chat API. The image must support /ui/<instance>/ and /healthz." : "Leave blank for headless operation. Platform Terminal and Desktop do not need this port."}>
            <Input type="number" min={1} max={65535} step={1} value={port} required={webUI} onChange={(_, data) => setPort(data.value)} placeholder="e.g. 8080" />
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

      <Switch checked={privateRegistry} label="Use private registry credentials" onChange={(_, data) => {
        setPrivateRegistry(data.checked);
        clearRegistryCredentials();
      }} />
      {privateRegistry && (
        <>
          <Field label="Registry username" required>
            <Input autoComplete="off" value={registryUsername} onChange={(_, data) => setRegistryUsername(data.value)} required />
          </Field>
          <Field label="Registry password or token" required hint="Write-only. The registry address is taken from the image; no Secret setup is needed.">
            <Input type="password" autoComplete="new-password" value={registryPassword} onChange={(_, data) => setRegistryPassword(data.value)} required />
          </Field>
        </>
      )}

      <TeamPicker value={team} onChange={setTeam} required={builtIn} />

      {usesGitHubCopilot && <CopilotModelPicker value={model} onChange={setModel} />}

      {chartUnsupported && (
        <MessageBar intent="error">
          {`This deployment's agent chart does not route ${AGENT_TYPE_LABELS[agentType]} agents through the AI Gateway. Ask an administrator to upgrade the agent chart.`}
        </MessageBar>
      )}

      {agentTypesUnknown && (
        <MessageBar intent="warning">
          The operator has not read its agent chart yet. Try again in a minute.
        </MessageBar>
      )}

      {selectedTeam && !harnessAllowed && (
        <MessageBar intent="error">
          {`The selected Team's AgentPolicy does not allow ${AGENT_TYPE_LABELS[agentType]} agents.`}
        </MessageBar>
      )}

      {gatewayModels && !chartUnsupported && !agentTypesUnknown && harnessAllowed && (
        <>
          {custom && (
            <MessageBar intent="info">
              Request platform AI Gateway model access only when this approved Custom image is configured to use it. This does not use a Team policy or Sandbox.
            </MessageBar>
          )}
          <Field
            hint={
              sharedAvailable
                ? custom
                  ? `Available platform models: ${sharedModels.map((m) => m.name).join(", ")}`
                  : `Models allowed by this Team: ${sharedModels.map((m) => m.name).join(", ")}`
                : catalogQuery.isLoading
                  ? "Loading the shared models..."
                  : catalogKnown
                    ? custom
                      ? "No shared models are currently available."
                      : selectedTeam
                      ? "This Team has no currently available shared models."
                      : "Choose a Team to see its allowed models."
                    : undefined
            }
          >
            <Switch
              checked={effectiveShared}
              disabled={sharedForced || !catalogKnown || !sharedAvailable}
              onChange={(_, data) => setUseSharedModels(!!data.checked)}
              label={custom ? "Request shared models" : "Use shared models"}
            />
          </Field>
          <Field
            hint={
              !copilotOffered
                ? catalogKnown
                  ? selectedTeam?.policy.githubCopilot
                    ? "GitHub Copilot is turned off for this deployment."
                    : custom
                      ? "GitHub Copilot is turned off for this deployment."
                      : "This Team's AgentPolicy does not allow GitHub Copilot."
                  : undefined
                : copilotForced
                  ? "Required when shared models are off or none are available. Sign in with GitHub once the agent is created."
                  : "Uses your own GitHub Copilot subscription. Sign in with GitHub once the agent is created."
            }
          >
            <Switch
              checked={effectiveCopilot}
              disabled={!catalogKnown || !copilotOffered || copilotForced}
              onChange={(_, data) => setUseCopilot(!!data.checked)}
              label={custom ? "Request GitHub Copilot" : "Use GitHub Copilot"}
            />
          </Field>
          {builtIn && effectiveShared && sharedAvailable && (
            <Field label="Default model (optional)" hint="GitHub Copilot models can be chosen after sign-in.">
              <Select value={gatewayModel} onChange={(event) => setGatewayModel(event.target.value)}>
                <option value="">(automatic)</option>
                {sharedModels.map((m) => (
                  <option key={m.name} value={m.name}>
                    {m.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          {!catalogKnown && !catalogQuery.isLoading && (
            <MessageBar intent="warning">
              The AI Gateway has not published its models yet. Try again in a minute.
            </MessageBar>
          )}
          {noModelSource && (
            <MessageBar intent="error">
              This Team's AgentPolicy grants no currently available model source. Ask an administrator
              to update the policy or Gateway catalog.
            </MessageBar>
          )}
        </>
      )}

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

      <p style={{ margin: 0, fontSize: 12 }}>
        When Memory assets are installed, memory and bounded learning are enabled automatically.
        Qualified entrypoints may use up to four extra model requests per day for synthetic method
        checks. Conversation-level memory restrictions still apply; Team Skills and tool permissions
        are unchanged.
      </p>

      <Button
        type="submit"
        appearance="primary"
        disabled={
          createMutation.isPending ||
          featuresLoading ||
          allowedAgentTypes.length === 0 ||
          (builtIn && !team) ||
          (builtInGatewayModels && (!catalogKnown || noModelSource || chartUnsupported || agentTypesUnknown || !harnessAllowed))
        }
      >
        {createMutation.isPending ? "Creating..." : "Create Agent"}
      </Button>

      {validationError && <MessageBar intent="error">{validationError}</MessageBar>}
      {createMutation.error && (
        <MessageBar intent="error">
          {(createMutation.error as Error).message}
          {createMutation.error instanceof CreateInstanceError && createMutation.error.instanceName && (
            <a href={`/instances/${encodeURIComponent(createMutation.error.instanceName)}/overview`}>Manage the existing agent</a>
          )}
        </MessageBar>
      )}
    </form>
  );
}
