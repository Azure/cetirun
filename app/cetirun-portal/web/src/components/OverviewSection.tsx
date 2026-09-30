// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
  Badge,
  Button,
  Text,
  Spinner,
  Dialog,
  DialogSurface,
  DialogTitle,
  DialogContent,
  DialogBody,
  DialogActions,
  Input,
  Field,
  Checkbox,
  Switch,
  TabList,
  Tab,
  type SelectTabData,
  tokens,
} from "@fluentui/react-components";
import {
  Eye20Regular,
  EyeOff20Regular,
  Info20Regular,
  Warning20Regular,
  Edit20Regular,
  Copy20Regular,
  Checkmark20Regular,
  ArrowSync20Regular,
  ArrowDownload20Regular,
} from "@fluentui/react-icons";
import { useState, useRef, useCallback, useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { useDeleteInstanceByName, useUpdateInstanceByName, useUpgradeInstanceByName, useInstanceStatusByNameQuery } from "../hooks/useInstance";
import { useFeatures } from "../hooks/useFeatures";
import { GITHUB_COPILOT_PROVIDER, useEffectiveTeamProvider } from "../hooks/useTeams";
import { downloadTeamsApp } from "../api/instance";
import type { Instance, InstanceVersion, UpdateInstanceRequest } from "../types/instance";
import { agentTypeLabel, agentUIUrl, phaseBadgeColor } from "../utils/instance";
import { StatusTab } from "./StatusTab";
import { CopilotAuthBlock } from "./CopilotAuthBlock";
import { CopilotModelPicker } from "./CopilotModelPicker";
import { AgentAuthorizationBlock } from "./AgentAuthorizationBlock";
import { RegistryAuthBlock } from "./RegistryAuthBlock";
import { AIGatewayBlock } from "./AIGatewayBlock";

const dialogListStyle: React.CSSProperties = {
  margin: "4px 0 0 0",
  paddingLeft: 18,
  fontSize: 12,
  lineHeight: 1.8,
};

const teamsStateBadge: Record<string, "success" | "warning" | "danger" | "informative"> = {
  Ready: "success",
  Provisioning: "warning",
  PendingAppRegistration: "informative",
  Failed: "danger",
  Deleting: "warning",
};

function TeamsStatusSection({
  instance,
  onDownload,
  onShowInstructions,
}: {
  instance: Instance;
  onDownload: () => void;
  onShowInstructions: () => void;
}) {
  const state = instance.teamsState;
  return (
    <div
      style={{
        marginTop: 8,
        marginBottom: 8,
        padding: "12px",
        borderRadius: tokens.borderRadiusMedium,
        border: `1px solid ${tokens.colorNeutralStroke1}`,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
        <Text weight="semibold">Microsoft Teams</Text>
        <Badge appearance="filled" color={state ? (teamsStateBadge[state] ?? "informative") : "informative"}>
          {state ? (state === "PendingAppRegistration" ? "Pending" : state) : "Unknown"}
        </Badge>
      </div>

      {!state && (
        <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <Spinner size="tiny" label="Teams integration status is still being updated..." />
          <Text size={200}>Teams has been enabled, but its current status is not available yet. Please wait a moment and refresh if needed.</Text>
        </div>
      )}
      {state === "Provisioning" && <Spinner size="tiny" label="Setting up Teams integration..." />}
      {state === "Deleting" && <Spinner size="tiny" label="Removing Teams integration..." />}
      {state === "PendingAppRegistration" && (
        <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
          <Info20Regular style={{ color: tokens.colorBrandForeground1, flexShrink: 0, marginTop: 2 }} />
          <Text size={200}>Teams app registration is being set up. This typically takes 2–3 business days.</Text>
        </div>
      )}
      {state === "Ready" && (
        <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <Button appearance="primary" icon={<ArrowDownload20Regular />} onClick={onDownload}>
            Download Teams App
          </Button>
          <Text size={200}>
            See{" "}
            <Button appearance="transparent" size="small" onClick={onShowInstructions} style={{ padding: 0, minWidth: 0, height: "auto", color: tokens.colorBrandForeground1 }}>
              how to upload the app to a Team
            </Button>.
          </Text>
        </div>
      )}
      {state === "Failed" && (
        <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
          <Warning20Regular style={{ color: tokens.colorStatusDangerForeground1, flexShrink: 0, marginTop: 2 }} />
          <Text size={200} style={{ color: tokens.colorStatusDangerForeground1 }}>
            {instance.teamsMessage || "Teams integration setup failed."}
          </Text>
        </div>
      )}
    </div>
  );
}

export function OverviewSection({ instance }: { instance: Instance }) {
  const navigate = useNavigate();
  const deleteMutation = useDeleteInstanceByName(instance.instanceName);
  const updateMutation = useUpdateInstanceByName(instance.instanceName);
  const upgradeMutation = useUpgradeInstanceByName(instance.instanceName);
  const { data: features } = useFeatures();
  const [selectedTab, setSelectedTab] = useState<string>("overview");
  const statusQuery = useInstanceStatusByNameQuery(instance.instanceName, selectedTab === "status", instance.uid);
  const [showAgentCredential, setShowAgentCredential] = useState(false);
  const [showTailscaleKey, setShowTailscaleKey] = useState(false);
  const [copiedAgentCredential, setCopiedAgentCredential] = useState(false);
  const [copiedTailscaleKey, setCopiedTailscaleKey] = useState(false);
  const copiedAgentCredentialTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const copiedTailscaleKeyTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [deleteVersion, setDeleteVersion] = useState<InstanceVersion>({ uid: instance.uid, resourceVersion: instance.resourceVersion });
  const [editOpen, setEditOpen] = useState(false);
  const [teamsDownloadOpen, setTeamsDownloadOpen] = useState(false);
  const [teamsCountdown, setTeamsCountdown] = useState(10);
  const teamsCountdownTimer = useRef<ReturnType<typeof setInterval>>(undefined);
  const [teamsInstructionsOpen, setTeamsInstructionsOpen] = useState(false);

  const [editDisplayName, setEditDisplayName] = useState("");
  const [editTeam, setEditTeam] = useState(instance.team);
  const [editGatewayToken, setEditGatewayToken] = useState("");
  const [editTailscaleAuthKey, setEditTailscaleAuthKey] = useState("");
  const [editModel, setEditModel] = useState(instance.model ?? "");
  const [disableTailscale, setDisableTailscale] = useState(false);
  const [editTeamsEnabled, setEditTeamsEnabled] = useState(false);
  const [editSnapshot, setEditSnapshot] = useState<Instance | null>(null);
  const [editImage, setEditImage] = useState("");
  const [editPort, setEditPort] = useState("");
  const [editWebUI, setEditWebUI] = useState(false);
  const [editValidationError, setEditValidationError] = useState("");
  const effectiveEditProvider = useEffectiveTeamProvider(editTeam);
  // An Agent routed through the AI Gateway picks its default model from its
  // Gateway models instead.
  const gatewayRouted = !!instance.aiGateway?.routed;
  const editUsesGitHubCopilot = effectiveEditProvider === GITHUB_COPILOT_PROVIDER && !gatewayRouted;

  useEffect(() => {
    return () => {
      clearTimeout(copiedAgentCredentialTimer.current);
      clearTimeout(copiedTailscaleKeyTimer.current);
      clearInterval(teamsCountdownTimer.current);
    };
  }, []);

  const openTeamsDownloadDialog = useCallback(() => {
    setTeamsCountdown(10);
    setTeamsDownloadOpen(true);
    clearInterval(teamsCountdownTimer.current);
    teamsCountdownTimer.current = setInterval(() => {
      setTeamsCountdown((prev) => {
        if (prev <= 1) { clearInterval(teamsCountdownTimer.current); return 0; }
        return prev - 1;
      });
    }, 1000);
  }, []);

  const closeTeamsDownloadDialog = useCallback(() => {
    setTeamsDownloadOpen(false);
    clearInterval(teamsCountdownTimer.current);
  }, []);

  const handleTeamsDownloadConfirm = useCallback(async () => {
    try {
      await downloadTeamsApp(instance.instanceName);
      closeTeamsDownloadDialog();
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to download the Teams app.";
      window.alert(message);
    }
  }, [closeTeamsDownloadDialog, instance.instanceName]);

  const agentCredential = instance.agent.type === "custom" ? undefined : instance.agent.type === "hermes" ? instance.apiServerKey : instance.gatewayToken;
  const agentCredentialLabel = instance.agent.type === "hermes" ? "Hermes API / Dashboard Key (user: cetirun)" : "Gateway Token";
  const nativeIntegrations = instance.agent.type !== "custom";
  const portLabel = instance.agent.webUI ? "Web UI port" : "HTTP port";
  const portValue = instance.agent.port ?? (nativeIntegrations ? "Platform default" : "Headless");
  let portHint = "Leave blank to remove the Agent HTTP endpoint. Terminal and Desktop are independent.";
  if (nativeIntegrations) {
    portHint = "Leave blank to restore the pinned runtime version's default.";
  } else if (editWebUI) {
    portHint = "Serve a browser UI through platform authentication instead of the Chat API. The image must support /ui/<instance>/ and /healthz.";
  }

  const handleCopyAgentCredential = useCallback(() => {
    navigator.clipboard.writeText(agentCredential ?? "").then(() => {
      setCopiedAgentCredential(true);
      clearTimeout(copiedAgentCredentialTimer.current);
      copiedAgentCredentialTimer.current = setTimeout(() => setCopiedAgentCredential(false), 2000);
    });
  }, [agentCredential]);

  const handleCopyTailscaleKey = useCallback(() => {
    navigator.clipboard.writeText(instance.tailscaleAuthKey ?? "").then(() => {
      setCopiedTailscaleKey(true);
      clearTimeout(copiedTailscaleKeyTimer.current);
      copiedTailscaleKeyTimer.current = setTimeout(() => setCopiedTailscaleKey(false), 2000);
    });
  }, [instance.tailscaleAuthKey]);

  const openEditDialog = () => {
    updateMutation.reset();
    setEditSnapshot(instance);
    setEditImage(instance.agent.image ?? "");
    setEditPort(instance.agent.port?.toString() ?? "");
    setEditWebUI(instance.agent.webUI ?? false);
    setEditValidationError("");
    setEditDisplayName(instance.displayName);
    setEditTeam(instance.team);
    setEditGatewayToken("");
    setEditTailscaleAuthKey("");
    setEditModel(instance.model ?? "");
    setDisableTailscale(false);
    setEditTeamsEnabled(instance.teamsEnabled ?? false);
    setEditOpen(true);
  };

  useEffect(() => {
    if (!editUsesGitHubCopilot) setEditModel("");
  }, [editUsesGitHubCopilot]);

  const handleEditSave = () => {
    const original = editSnapshot ?? instance;
    const req: UpdateInstanceRequest = { uid: original.uid, resourceVersion: original.resourceVersion };
    const agent: NonNullable<UpdateInstanceRequest["agent"]> = {};
    const port = editPort.trim() === "" ? null : Number(editPort);
    if (port !== null && (!Number.isInteger(port) || port < 1 || port > 65535)) {
      setEditValidationError("HTTP port must be an integer between 1 and 65535, or left blank.");
      return;
    }
    if (editWebUI && port === null) {
      setEditValidationError("A port is required when Web UI is enabled.");
      return;
    }
    setEditValidationError("");
    if (port !== (original.agent.port ?? null)) agent.port = port;
    if (!nativeIntegrations && editWebUI !== (original.agent.webUI ?? false)) agent.webUI = editWebUI;
    if (nativeIntegrations && editImage.trim() !== (original.agent.image ?? "")) agent.image = editImage.trim() || null;
    if (Object.keys(agent).length) req.agent = agent;
    if (editDisplayName.trim() !== original.displayName) req.displayName = editDisplayName.trim();
    if (nativeIntegrations && editUsesGitHubCopilot && editModel.trim() !== (original.model ?? "")) req.model = editModel.trim();
    if (nativeIntegrations) {
      if (editGatewayToken) req.gatewayToken = editGatewayToken;
      if (disableTailscale) { req.tailscaleAuthKey = ""; } else if (editTailscaleAuthKey) { req.tailscaleAuthKey = editTailscaleAuthKey; }
      if (editTeamsEnabled !== (original.teamsEnabled ?? false)) req.teamsEnabled = editTeamsEnabled;
    }
    if (Object.keys(req).length === 2) { setEditOpen(false); return; }
    updateMutation.mutate(req, { onSuccess: () => setEditOpen(false) });
  };

  const nativeAgentUIUrl = agentUIUrl(instance);

  return (
    <div style={{ padding: 24, overflowY: "auto", flex: 1, minWidth: 0 }}>
      <TabList
        size="medium"
        selectedValue={selectedTab}
        onTabSelect={(_, data: SelectTabData) => setSelectedTab(data.value as string)}
        style={{ marginBottom: 16 }}
      >
        <Tab value="overview">Overview</Tab>
        <Tab value="status">Status</Tab>
      </TabList>

      {selectedTab === "status" && (
        <StatusTab data={statusQuery.data} isLoading={statusQuery.isLoading} />
      )}

      {selectedTab === "overview" && (<>
      {/* Header */}
      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 12, marginBottom: 16 }}>
        <Text as="h1" size={600} weight="semibold" style={{ minWidth: 0, margin: 0, overflowWrap: "anywhere" }}>{instance.displayName.trim() || instance.instanceName}</Text>
        <Badge appearance="filled" color={phaseBadgeColor[instance.phase] ?? "informative"}>{instance.phase}</Badge>
      </div>
      <Text size={300} style={{ color: tokens.colorNeutralForeground3, marginBottom: 16, display: "block" }}>
        Agent: {instance.instanceName} | Owner: {instance.alias}{instance.team ? ` | Team: ${instance.team}` : ""}
      </Text>
      <div aria-label="Requested Agent configuration" style={{ display: "flex", flexDirection: "column", gap: 6, marginBottom: 16, overflowWrap: "anywhere" }}>
        <Text>Type: {agentTypeLabel(instance.agent.type)}</Text>
        <Text>Image: {instance.agent.image ?? "Platform default (not yet pinned)"}</Text>
        <Text>Username: {instance.agent.username ?? "vibe"}</Text>
        <Text>{portLabel}: {portValue}</Text>
      </div>
      {instance.phase === "Suspended" && (
        <div style={{ display: "flex", alignItems: "center", gap: 8, padding: 12, borderRadius: tokens.borderRadiusMedium, backgroundColor: tokens.colorPaletteYellowBackground2, marginBottom: 8 }}>
          <Warning20Regular style={{ color: tokens.colorPaletteYellowForeground2, flexShrink: 0 }} />
          <Text size={200}>This agent has been paused. Contact your administrator to restore access.</Text>
        </div>
      )}

      {instance.phase === "Provisioning" && <Spinner size="small" label="Provisioning your agent..." />}

      {agentCredential && (
        <div style={{ marginTop: 8, marginBottom: 8, display: "flex", alignItems: "center", gap: 8 }}>
          <Text weight="semibold">{agentCredentialLabel}: </Text>
          <Text style={{ fontFamily: "monospace", fontSize: 12, flex: 1 }}>
            {showAgentCredential ? agentCredential : "•".repeat(32)}
          </Text>
          <Button appearance="subtle" size="small" icon={showAgentCredential ? <EyeOff20Regular /> : <Eye20Regular />} onClick={() => setShowAgentCredential(!showAgentCredential)} aria-label={showAgentCredential ? "Hide credential" : "Show credential"} />
          <Button appearance="subtle" size="small" icon={copiedAgentCredential ? <Checkmark20Regular /> : <Copy20Regular />} onClick={handleCopyAgentCredential} aria-label="Copy credential" />
        </div>
      )}

      {instance.tailscaleAuthKey && features?.tailscaleEnabled && (
        <div style={{ marginTop: 8, marginBottom: 8, display: "flex", alignItems: "center", gap: 8 }}>
          <Text weight="semibold">Tailscale Auth Key: </Text>
          <Text style={{ fontFamily: "monospace", fontSize: 12, flex: 1 }}>
            {showTailscaleKey ? instance.tailscaleAuthKey : "•".repeat(32)}
          </Text>
          <Button appearance="subtle" size="small" icon={showTailscaleKey ? <EyeOff20Regular /> : <Eye20Regular />} onClick={() => setShowTailscaleKey(!showTailscaleKey)} aria-label={showTailscaleKey ? "Hide Tailscale auth key" : "Show Tailscale auth key"} />
          <Button appearance="subtle" size="small" icon={copiedTailscaleKey ? <Checkmark20Regular /> : <Copy20Regular />} onClick={handleCopyTailscaleKey} aria-label="Copy Tailscale auth key" />
        </div>
      )}

      {nativeIntegrations && nativeAgentUIUrl && instance.phase !== "Suspended" && (
        <CopilotAuthBlock instance={instance} />
      )}

      {gatewayRouted && instance.phase !== "Suspended" && <AIGatewayBlock instance={instance} />}

      {nativeIntegrations && features?.agentIdentityEnabled && <AgentAuthorizationBlock instance={instance} />}

      {instance.agent.registrySecretRef && <RegistryAuthBlock key={instance.uid || instance.instanceName} instance={instance} />}

      {instance.teamsEnabled && (
        <TeamsStatusSection instance={instance} onDownload={openTeamsDownloadDialog} onShowInstructions={() => setTeamsInstructionsOpen(true)} />
      )}

      {instance.phase !== "Deleting" && (
        <div style={{ display: "flex", flexWrap: "wrap", gap: 8, marginTop: 8 }}>
          {instance.upgradeAvailable && (
            <Button appearance="primary" icon={<ArrowSync20Regular />} onClick={() => upgradeMutation.mutate(instance)} disabled={upgradeMutation.isPending}>
              {upgradeMutation.isPending ? "Upgrading..." : "Upgrade to Latest"}
            </Button>
          )}
          <Button appearance="secondary" icon={<Edit20Regular />} onClick={openEditDialog} disabled={updateMutation.isPending}>Edit Agent</Button>
          <Button appearance="secondary" onClick={() => { setDeleteVersion({ uid: instance.uid, resourceVersion: instance.resourceVersion }); setConfirmOpen(true); }} disabled={deleteMutation.isPending}>
            {deleteMutation.isPending ? "Deleting..." : "Delete Agent"}
          </Button>
        </div>
      )}

      {upgradeMutation.isError && (
        <Text size={200} style={{ color: tokens.colorStatusDangerForeground1 }}>
          {upgradeMutation.error?.message || "Failed to upgrade agent"}
        </Text>
      )}
      {deleteMutation.isError && (
        <Text size={200} style={{ color: tokens.colorStatusDangerForeground1 }}>
          {deleteMutation.error?.message || "Failed to delete agent"}
        </Text>
      )}

      {/* Delete Dialog */}
      <Dialog open={confirmOpen} onOpenChange={(_, data) => setConfirmOpen(data.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Delete Agent</DialogTitle>
            <DialogContent>
              Are you sure you want to delete this agent? This action cannot be undone.
              {deleteMutation.isError && <Text block style={{ color: tokens.colorStatusDangerForeground1 }}>{deleteMutation.error?.message || "Failed to delete agent"}</Text>}
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" onClick={() => setConfirmOpen(false)} disabled={deleteMutation.isPending}>Cancel</Button>
              <Button appearance="primary" style={{ backgroundColor: tokens.colorStatusDangerBackground3 }} disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate(deleteVersion, { onSuccess: () => navigate("/") })}>Delete</Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      {/* Edit Dialog */}
      <Dialog open={editOpen} onOpenChange={(_, data) => setEditOpen(data.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Edit Agent</DialogTitle>
            <DialogContent>
              <div style={{ display: "flex", flexDirection: "column", gap: 16, paddingTop: 8 }}>
                <Field label="Display Name" hint="Leave blank to display the Agent name.">
                  <Input value={editDisplayName} onChange={(_, data) => setEditDisplayName(data.value)} />
                </Field>
                <Field label="Team" hint="Team reassignment is an administrator operation on a suspended Agent.">
                  <Input value={editTeam || "(none)"} readOnly />
                </Field>
                <Field label="Agent Type"><Input value={agentTypeLabel(instance.agent.type)} readOnly /></Field>
                <Field label="Agent Username" hint="The image user cannot change after creation.">
                  <Input value={instance.agent.username ?? "vibe"} readOnly />
                </Field>
                <Field label="Agent Image" hint={nativeIntegrations ? "Overrides require platform approval and Harness compatibility. A pinned image cannot be cleared." : "Custom image changes require separate HOME compatibility qualification and are not available here."}>
                  <Input value={editImage} readOnly={!nativeIntegrations} onChange={(_, data) => setEditImage(data.value)} maxLength={2048} />
                </Field>
                {!nativeIntegrations && <Switch checked={editWebUI} label="Serve Web UI" onChange={(_, data) => setEditWebUI(data.checked)} />}
                <Field label={editWebUI ? "Web UI Port" : "HTTP Port (optional)"} required={editWebUI} hint={portHint}>
                  <Input type="number" min={1} max={65535} step={1} value={editPort} onChange={(_, data) => setEditPort(data.value)} />
                </Field>
                {nativeIntegrations && editUsesGitHubCopilot && <CopilotModelPicker instance={instance} value={editModel} onChange={setEditModel} allowDefault />}
                {nativeIntegrations && <Field label="Gateway Token">
                  <Input type="password" autoComplete="off" value={editGatewayToken} onChange={(_, data) => setEditGatewayToken(data.value)} placeholder="Leave empty to keep unchanged" />
                </Field>}
                {nativeIntegrations && features?.tailscaleEnabled && (
                  <>
                    <Field label="Tailscale Auth Key">
                      <Input type="password" autoComplete="off" value={editTailscaleAuthKey} onChange={(_, data) => setEditTailscaleAuthKey(data.value)} placeholder="Leave empty to keep unchanged" disabled={disableTailscale} />
                    </Field>
                    {instance.tailscaleAuthKey && (
                      <Checkbox checked={disableTailscale} onChange={(_, data) => { setDisableTailscale(!!data.checked); if (data.checked) setEditTailscaleAuthKey(""); }} label="Disable Tailscale" />
                    )}
                  </>
                )}
                {nativeIntegrations && <Switch checked={editTeamsEnabled} onChange={(_, data) => setEditTeamsEnabled(!!data.checked)} label="Enable Microsoft Teams Integration" />}
                {nativeIntegrations && <div style={{ display: "flex", alignItems: "flex-start", gap: 8, padding: "10px 12px", borderRadius: tokens.borderRadiusMedium, backgroundColor: tokens.colorNeutralBackground1Hover }}>
                  <Info20Regular style={{ color: tokens.colorBrandForeground1, flexShrink: 0, marginTop: 2 }} />
                  <Text size={200}>
                    Updating the model, Gateway Token{features?.tailscaleEnabled ? " or Tailscale Auth Key" : ""} will restart the gateway. The access link may be temporarily unavailable during this process.
                  </Text>
                </div>}
                {editValidationError && <Text style={{ color: tokens.colorStatusDangerForeground1 }}>{editValidationError}</Text>}
                {updateMutation.isError && (
                  <Text style={{ color: tokens.colorStatusDangerForeground1 }}>
                    {updateMutation.error?.message || "Failed to update agent"}
                  </Text>
                )}
              </div>
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" onClick={() => setEditOpen(false)} disabled={updateMutation.isPending}>Cancel</Button>
              <Button appearance="primary" disabled={updateMutation.isPending} onClick={handleEditSave}>
                {updateMutation.isPending ? "Saving..." : "Save"}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      {/* Teams Instructions Dialog */}
      <Dialog open={teamsInstructionsOpen} onOpenChange={(_, data) => { if (!data.open) setTeamsInstructionsOpen(false); }}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>How to Upload the App to a Team</DialogTitle>
            <DialogContent>
              <div style={{ display: "flex", flexDirection: "column", gap: 12, paddingTop: 8 }}>
                <div style={{ padding: "8px 12px", borderRadius: tokens.borderRadiusMedium, background: tokens.colorPaletteYellowBackground1, border: `1px solid ${tokens.colorPaletteYellowBorder1}` }}>
                  <Text size={200} weight="semibold">
                    Important: The app must be uploaded to a <strong>private</strong> team. If you don&apos;t have a private team yet,{" "}
                    <a href="https://support.microsoft.com/en-au/office/create-a-team-from-scratch-in-microsoft-teams-174adf5f-846b-4780-b765-de1a0a737e2b" target="_blank" rel="noopener noreferrer" style={{ color: tokens.colorBrandForeground1 }}>create a new private team</a>.
                  </Text>
                </div>
                <Text weight="semibold" size={300}>Upload via the &quot;Manage Team&quot; tab:</Text>
                <ol style={{ margin: 0, paddingLeft: 20, fontSize: 13, lineHeight: 1.8 }}>
                  <li>Navigate to <strong>Teams</strong> on the left side of the app.</li>
                  <li>Click <strong>⋯</strong> next to your private team name and select <strong>Manage team</strong>.</li>
                  <li>Select the <strong>Apps</strong> tab at the top of the management screen.</li>
                  <li>Click <strong>Upload a custom app</strong> (bottom right corner).</li>
                </ol>
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
                  <Info20Regular style={{ color: tokens.colorBrandForeground1, flexShrink: 0, marginTop: 2 }} />
                  <Text size={200}>If you don&apos;t see the button, go to <strong>Settings &gt; Member permissions</strong> and enable <strong>Allow members to upload custom apps</strong>.</Text>
                </div>
                <div style={{ borderTop: `1px solid ${tokens.colorNeutralStroke2}`, paddingTop: 8 }}>
                  <Text size={200}>If you only need direct messages, follow the{" "}
                    <a href="https://learn.microsoft.com/en-us/microsoftteams/platform/concepts/deploy-and-publish/apps-upload#upload-your-app" target="_blank" rel="noopener noreferrer" style={{ color: tokens.colorBrandForeground1 }}>personal app sideloading guide</a>.
                  </Text>
                </div>
              </div>
            </DialogContent>
            <DialogActions>
              <Button appearance="primary" onClick={() => setTeamsInstructionsOpen(false)}>Close</Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      {/* Teams Download Dialog */}
      <Dialog open={teamsDownloadOpen} onOpenChange={(_, data) => { if (!data.open) closeTeamsDownloadDialog(); }}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Download Teams App Package</DialogTitle>
            <DialogContent>
              <div style={{ display: "flex", flexDirection: "column", gap: 16, paddingTop: 8 }}>
                <Text>Before downloading, please read and acknowledge:</Text>
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
                  <Warning20Regular style={{ color: tokens.colorStatusWarningForeground1, flexShrink: 0, marginTop: 2 }} />
                  <div>
                    <Text weight="semibold" size={300}>1. Treat as credential</Text><br />
                    <Text size={200}>This ZIP package contains your bot&apos;s identity and should be treated as a credential. Do not share it with others.</Text>
                  </div>
                </div>
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
                  <Warning20Regular style={{ color: tokens.colorStatusWarningForeground1, flexShrink: 0, marginTop: 2 }} />
                  <div>
                    <Text weight="semibold" size={300}>2. Do not add to public teams</Text><br />
                    <Text size={200}>This app is linked to your CetiRun agent which may contain your login sessions. Only install it in private chats or trusted groups.</Text>
                  </div>
                </div>
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
                  <Warning20Regular style={{ color: tokens.colorStatusWarningForeground1, flexShrink: 0, marginTop: 2 }} />
                  <div>
                    <Text weight="semibold" size={300}>3. Set up access control</Text><br />
                    <Text size={200}>
                      Configure access control from the{" "}
                      {nativeAgentUIUrl ? (
                        <a href={nativeAgentUIUrl} target="_blank" rel="noopener noreferrer" style={{ color: tokens.colorBrandForeground1 }}>Agent UI</a>
                      ) : "Agent UI"}:
                    </Text>
                    <ul style={dialogListStyle}>
                      <li>Replace the placeholder in <strong>Allow From</strong> with your own user Object ID. Your Object ID can be found in the{" "}
                        <a href="https://ms.portal.azure.com/#view/Microsoft_AAD_UsersAndTenants/UserManagementMenuBlade/~/AllUsers" target="_blank" rel="noopener noreferrer" style={{ color: tokens.colorBrandForeground1 }}>Azure portal → Users</a>.
                      </li>
                      <li>Verify that <strong>DM Policy</strong> and <strong>Group Policy</strong> are set to <strong>allowlist</strong> (this is the default).</li>
                      <li>For more details, refer to the{" "}
                        <a href="https://docs.openclaw.ai/channels/msteams#access-control-dms-+-groups" target="_blank" rel="noopener noreferrer" style={{ color: tokens.colorBrandForeground1 }}>upstream documentation</a>.
                      </li>
                    </ul>
                  </div>
                </div>
                <div style={{ display: "flex", alignItems: "flex-start", gap: 8 }}>
                  <Info20Regular style={{ color: tokens.colorBrandForeground1, flexShrink: 0, marginTop: 2 }} />
                  <div>
                    <Text weight="semibold" size={300}>Known issues</Text><br />
                    <ul style={dialogListStyle}>
                      <li>You must @mention the bot in a channel for it to reply.</li>
                      <li>Images will not work in channels due to Graph API constraints.</li>
                    </ul>
                  </div>
                </div>
                <div style={{ borderTop: `1px solid ${tokens.colorNeutralStroke2}`, paddingTop: 12 }}>
                  <Text size={200}>After downloading, see{" "}
                    <Button appearance="transparent" size="small" onClick={() => { closeTeamsDownloadDialog(); setTeamsInstructionsOpen(true); }} style={{ padding: 0, minWidth: 0, height: "auto", color: tokens.colorBrandForeground1 }}>
                      how to upload the app to a Team
                    </Button>.
                  </Text>
                </div>
              </div>
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" onClick={closeTeamsDownloadDialog}>Cancel</Button>
              <Button appearance="primary" disabled={teamsCountdown > 0} onClick={handleTeamsDownloadConfirm} icon={<ArrowDownload20Regular />}>
                {teamsCountdown > 0 ? `Confirm & Download (${teamsCountdown}s)` : "Confirm & Download"}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
      </>)}
    </div>
  );
}
