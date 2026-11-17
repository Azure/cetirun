// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
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
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  type SelectTabData,
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
} from "@fluentui/react-icons";
import { useState, useRef, useCallback, useEffect } from "react";
import { useNavigate } from "react-router-dom";
import { useDeleteInstanceByName, useUpdateInstanceByName, useUpgradeInstanceByName, useInstanceStatusByNameQuery } from "../hooks/useInstance";
import { useFeatures } from "../hooks/useFeatures";
import { useAIGatewayCatalog } from "../hooks/useAIGateway";
import type { Instance, InstanceVersion, UpdateInstanceRequest } from "../types/instance";
import { agentTypeLabel } from "../utils/instance";
import { AgentPhase } from "./Lifecycle";
import { StatusTab } from "./StatusTab";
import { currentGovernance, GovernanceNotice } from "./GovernanceNotice";
import { ToolAuthorizationBlock } from "./ToolAuthorizationBlock";
import { RegistryAuthBlock } from "./RegistryAuthBlock";
import { AIGatewayBlock } from "./AIGatewayBlock";
import { TeamSkillsBlock } from "./TeamSkillsBlock";
import { RestartAgentButton } from "./RestartAgentButton";
import { SuspendAgentButton } from "./SuspendAgentButton";

export function OverviewSection({ instance }: { instance: Instance }) {
  const navigate = useNavigate();
  const deleteMutation = useDeleteInstanceByName(instance.instanceName);
  const updateMutation = useUpdateInstanceByName(instance.instanceName);
  const upgradeMutation = useUpgradeInstanceByName(instance.instanceName);
  const { data: features } = useFeatures();
  const [selectedTab, setSelectedTab] = useState<string>("overview");
  const overview = selectedTab === "overview";
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

  const [editDisplayName, setEditDisplayName] = useState("");
  const [editTeam, setEditTeam] = useState(instance.team);
  const [editGatewayToken, setEditGatewayToken] = useState("");
  const [editTailscaleAuthKey, setEditTailscaleAuthKey] = useState("");
  const [disableTailscale, setDisableTailscale] = useState(false);
  const [editSnapshot, setEditSnapshot] = useState<Instance | null>(null);
  const [editImage, setEditImage] = useState("");
  const [editPort, setEditPort] = useState("");
  const [editWebUI, setEditWebUI] = useState(false);
  const [editValidationError, setEditValidationError] = useState("");
  const gatewayRouted = !!instance.aiGateway?.routed;
  const governance = currentGovernance(instance);
  const readiness = !instance.suspended && !governance &&
    (instance.phase === "Provisioning" || instance.phase === "Failed")
    ? instance.conditions?.find((condition) => condition.type === "Ready" && condition.status === "False" &&
      (!condition.observedGeneration || instance.generation === undefined || condition.observedGeneration === instance.generation))
    : undefined;
  const canRetryWorkload = readiness?.reason === "PodNotReady" && instance.suspendAvailable &&
    !instance.restartAvailable && instance.restartRequestId === instance.observedRestartRequestId;

  useEffect(() => {
    return () => {
      clearTimeout(copiedAgentCredentialTimer.current);
      clearTimeout(copiedTailscaleKeyTimer.current);
    };
  }, []);

  const agentCredential = instance.agent.type === "custom" ? undefined : instance.agent.type === "hermes" ? instance.apiServerKey : instance.gatewayToken;
  const agentCredentialLabel = instance.agent.type === "hermes" ? "Hermes API / Webapp Key (user: cetirun)" : "Gateway Token";
  const nativeIntegrations = instance.agent.type !== "custom";
  const customCatalog = useAIGatewayCatalog(editOpen && !nativeIntegrations);
  const customImages = customCatalog.data?.customImages;
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
    setDisableTailscale(false);
    setEditOpen(true);
  };

  const handleEditSave = () => {
    const original = editSnapshot ?? instance;
    const req: UpdateInstanceRequest = { uid: original.uid, resourceVersion: original.resourceVersion };
    const agent: NonNullable<UpdateInstanceRequest["agent"]> = {};
    const image = editImage.trim();
    const port = editPort.trim() === "" ? null : Number(editPort);
    if (port !== null && (!Number.isInteger(port) || port < 1 || port > 65535)) {
      setEditValidationError("HTTP port must be an integer between 1 and 65535, or left blank.");
      return;
    }
    if (editWebUI && port === null) {
      setEditValidationError("A port is required when Web UI is enabled.");
      return;
    }
    if (!image && original.agent.image) {
      setEditValidationError("A pinned Agent image cannot be cleared.");
      return;
    }
    if (!nativeIntegrations && image !== (original.agent.image ?? "")) {
      if (customCatalog.isFetching || customCatalog.isError || customImages == null) {
        setEditValidationError("Custom image approvals are not available yet. Try again after the catalog loads.");
        return;
      }
      if (!customImages.includes(image)) {
        setEditValidationError("Choose an approved Custom image pinned by sha256 digest. Tags and unapproved images cannot run.");
        return;
      }
    }
    setEditValidationError("");
    if (port !== (original.agent.port ?? null)) agent.port = port;
    if (!nativeIntegrations && editWebUI !== (original.agent.webUI ?? false)) agent.webUI = editWebUI;
    if (image !== (original.agent.image ?? "")) agent.image = image;
    if (Object.keys(agent).length) req.agent = agent;
    if (editDisplayName.trim() !== original.displayName) req.displayName = editDisplayName.trim();
    if (nativeIntegrations) {
      if (editGatewayToken) req.gatewayToken = editGatewayToken;
      if (disableTailscale) { req.tailscaleAuthKey = ""; } else if (editTailscaleAuthKey) { req.tailscaleAuthKey = editTailscaleAuthKey; }
    }
    if (Object.keys(req).length === 2) { setEditOpen(false); return; }
    updateMutation.mutate(req, { onSuccess: () => setEditOpen(false) });
  };

  return (
    <div className="portal-detail">
      <div className="portal-detail-inner">
        <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 12, marginBottom: 16 }}>
          <h1 className="portal-detail-title">{instance.displayName.trim() || instance.instanceName}</h1>
          <AgentPhase phase={instance.phase} />
        </div>
        <GovernanceNotice instance={instance} />
        <TabList
          size="medium"
          selectedValue={selectedTab}
          onTabSelect={(_, data: SelectTabData) => setSelectedTab(data.value as string)}
          style={{ marginBottom: 20, marginInline: -10 }}
        >
          <Tab value="overview">Overview</Tab>
          <Tab value="status">Status</Tab>
        </TabList>

        {selectedTab === "status" && (
          <StatusTab data={statusQuery.data} isLoading={statusQuery.isLoading} />
        )}

        <div className="overview-grid">
          <div className="overview-lead">
            {overview && (<>
              {instance.phase === "Suspended" && (
                <div className="portal-callout portal-callout--warning">
                  <Warning20Regular />
                  <span>The controller reports Suspended. This is not proof of Pod termination or removal of every access path.</span>
                </div>
              )}

              {readiness ? (
                <MessageBar intent="warning" style={{ marginBottom: 16, overflowWrap: "anywhere" }}>
                  <MessageBarBody>
                    <MessageBarTitle>Agent is not ready</MessageBarTitle>
                    <Text block>Reported reason: {readiness.reason}{readiness.message ? `: ${readiness.message}` : ""}</Text>
                    {canRetryWorkload && <Text block>
                      If the workload stays unready, you can select Suspend Agent below. This interrupts active sessions and retains HOME.
                      Wait for the controller to report Suspended, then select Resume Agent to retry with the current configuration.
                      An accepted resume does not mean the Agent is Ready. If it stays unready, ask your administrator to check the reported cause.
                    </Text>}
                    <Button appearance="subtle" onClick={() => setSelectedTab("status")}>View Status</Button>
                  </MessageBarBody>
                </MessageBar>
              ) : instance.phase === "Provisioning" && !governance ? <Spinner size="small" label="Provisioning your agent..." style={{ justifyContent: "flex-start", marginBottom: 16 }} /> : null}
            </>)}

            {/* Keep lifecycle requests mounted while the owner checks the Status tab. */}
            <div className="portal-actions" style={{ display: overview && instance.phase !== "Deleting" ? "flex" : "none",
              flexWrap: "wrap", gap: 8 }}>
              {instance.upgradeAvailable && (
                <Button appearance="primary" icon={<ArrowSync20Regular />} onClick={() => upgradeMutation.mutate(instance)} disabled={upgradeMutation.isPending}>
                  {upgradeMutation.isPending ? "Upgrading..." : "Upgrade to Latest"}
                </Button>
              )}
              <Button appearance="secondary" icon={<Edit20Regular />} onClick={openEditDialog} disabled={updateMutation.isPending}>Edit Agent</Button>
              {(instance.restartAvailable || instance.restartRequestId) && <RestartAgentButton key={instance.uid} instance={instance} />}
              {instance.suspended !== undefined && <SuspendAgentButton instance={instance} />}
              <Button className="portal-danger-quiet" appearance="secondary" onClick={() => { setDeleteVersion({ uid: instance.uid, resourceVersion: instance.resourceVersion }); setConfirmOpen(true); }} disabled={deleteMutation.isPending}>
                {deleteMutation.isPending ? "Deleting..." : "Delete Agent"}
              </Button>
            </div>
            {overview && upgradeMutation.isError && (
              <span className="portal-error-text">
                {upgradeMutation.error?.message || "Failed to upgrade agent"}
              </span>
            )}
            {overview && deleteMutation.isError && (
              <span className="portal-error-text">
                {deleteMutation.error?.message || "Failed to delete agent"}
              </span>
            )}
          </div>

          {overview && (
            <div className="overview-aside">
              <span className="portal-byline">
                Agent: {instance.instanceName} | Owner: {instance.alias}{instance.team ? ` | Team: ${instance.team}` : ""}
              </span>
              <div aria-label="Requested Agent configuration" className="portal-spec">
                <span>Type: {agentTypeLabel(instance.agent.type)}</span>
                <span>Image: {instance.agent.image ?? "Platform default (not yet pinned)"}</span>
                <span>Username: {instance.agent.username ?? "vibe"}</span>
                <span>{portLabel}: {portValue}</span>
              </div>

              {agentCredential && (
                <div className="portal-secret">
                  <Text weight="semibold">{agentCredentialLabel}: </Text>
                  <span className="portal-secret-value">
                    {showAgentCredential ? agentCredential : "•".repeat(32)}
                  </span>
                  <Button appearance="subtle" size="small" icon={showAgentCredential ? <EyeOff20Regular /> : <Eye20Regular />} onClick={() => setShowAgentCredential(!showAgentCredential)} aria-label={showAgentCredential ? "Hide credential" : "Show credential"} />
                  <Button appearance="subtle" size="small" icon={copiedAgentCredential ? <Checkmark20Regular /> : <Copy20Regular />} onClick={handleCopyAgentCredential} aria-label="Copy credential" />
                </div>
              )}

              {instance.tailscaleAuthKey && features?.tailscaleEnabled && (
                <div className="portal-secret">
                  <Text weight="semibold">Tailscale Auth Key: </Text>
                  <span className="portal-secret-value">
                    {showTailscaleKey ? instance.tailscaleAuthKey : "•".repeat(32)}
                  </span>
                  <Button appearance="subtle" size="small" icon={showTailscaleKey ? <EyeOff20Regular /> : <Eye20Regular />} onClick={() => setShowTailscaleKey(!showTailscaleKey)} aria-label={showTailscaleKey ? "Hide Tailscale auth key" : "Show Tailscale auth key"} />
                  <Button appearance="subtle" size="small" icon={copiedTailscaleKey ? <Checkmark20Regular /> : <Copy20Regular />} onClick={handleCopyTailscaleKey} aria-label="Copy Tailscale auth key" />
                </div>
              )}
            </div>
          )}

          {overview && (
            <div className="overview-sections">
              {gatewayRouted && !instance.suspended && instance.phase !== "Suspended" && <AIGatewayBlock instance={instance} />}

              <TeamSkillsBlock instance={instance} />

              {features?.agentToolsEnabled && <ToolAuthorizationBlock instance={instance} />}

              {instance.agent.registrySecretRef && <RegistryAuthBlock key={instance.uid || instance.instanceName} instance={instance} />}
            </div>
          )}
        </div>
      </div>

      {overview && (<>
      {/* Delete Dialog */}
      <Dialog open={confirmOpen} onOpenChange={(_, data) => setConfirmOpen(data.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Delete Agent</DialogTitle>
            <DialogContent>
              Are you sure you want to delete this agent? This action cannot be undone.
              {deleteMutation.isError && <span className="portal-error-text">{deleteMutation.error?.message || "Failed to delete agent"}</span>}
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" onClick={() => setConfirmOpen(false)} disabled={deleteMutation.isPending}>Cancel</Button>
              <Button className="portal-danger" appearance="primary" disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate(deleteVersion, { onSuccess: () => navigate("/") })}>Delete</Button>
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
                <Field label="Agent Image" hint={nativeIntegrations ? "Overrides require platform approval and Harness compatibility. A pinned image cannot be cleared." : "Choose an approved sha256 image from the suggestions. Changing the image restarts a running Agent and retains HOME. The image must support the existing data and image user."}>
                  <Input value={editImage} onChange={(_, data) => { setEditImage(data.value); setEditValidationError(""); }} maxLength={2048}
                    list={nativeIntegrations ? undefined : "edit-approved-custom-images"} />
                  {!nativeIntegrations && <datalist id="edit-approved-custom-images">
                    {customImages?.map((approved) => <option key={approved} value={approved} />)}
                  </datalist>}
                </Field>
                {!nativeIntegrations && <Switch checked={editWebUI} label="Serve Web UI" onChange={(_, data) => setEditWebUI(data.checked)} />}
                <Field label={editWebUI ? "Web UI Port" : "HTTP Port (optional)"} required={editWebUI} hint={portHint}>
                  <Input type="number" min={1} max={65535} step={1} value={editPort} onChange={(_, data) => setEditPort(data.value)} />
                </Field>
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
                {nativeIntegrations && <div className="portal-callout portal-callout--info" style={{ marginBottom: 0 }}>
                  <Info20Regular />
                  <span>
                    Updating the model, Gateway Token{features?.tailscaleEnabled ? " or Tailscale Auth Key" : ""} will restart the gateway. The access link may be temporarily unavailable during this process.
                  </span>
                </div>}
                {editValidationError && <span className="portal-error-text">{editValidationError}</span>}
                {updateMutation.isError && (
                  <span className="portal-error-text">
                    {updateMutation.error?.message || "Failed to update agent"}
                  </span>
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

      </>)}
    </div>
  );
}
