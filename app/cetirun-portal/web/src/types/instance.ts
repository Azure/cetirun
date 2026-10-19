// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

export interface TeamSummary {
  name: string;
  role: string;
  provider: string;
}

export type AgentType = "openclaw" | "hermes" | "custom";

export interface AgentConfig {
  type: AgentType;
  image?: string;
  username?: string;
  port?: number;
  webUI?: boolean;
  registrySecretRef?: { name: string };
}

export interface InstanceVersion {
  uid: string;
  resourceVersion: string;
}

export interface RegistryAuth {
  username: string;
  password: string;
}

export interface Instance extends InstanceVersion {
  instanceName: string;
  agent: AgentConfig;
  registryAuthConfigured?: boolean;
  generation?: number;
  observedGeneration?: number;
  alias: string;
  displayName: string;
  team: string;
  /** Per-instance GitHub Copilot model override; empty means deployment default. */
  model?: string;
  phase: "Pending" | "Provisioning" | "Ready" | "Failed" | "Deleting" | "Suspended";
  accessUrl?: string;              // Gateway access URL; may have token appended when available
  webUIUrl?: string;
  gatewayToken?: string;           // Token for gateway access, returned separately when available
  apiServerKey?: string;           // Hermes API server key, returned separately when available
  cetirunAuthKey?: string;         // Bearer key for the in-pod cetirun-auth helper (both agent types)
  tailscaleAuthKey?: string;       // Tailscale auth key, returned when Tailscale is enabled
  conditions?: ConditionStatus[];
  createdAt: string;
  lastReconciled?: string;
  upgradeAvailable: boolean;
  teamsEnabled?: boolean;
  teamsState?: "Provisioning" | "PendingAppRegistration" | "Ready" | "Failed" | "Deleting";
  teamsAppId?: string;
  teamsMessage?: string;
  agentIdentity?: {
    enabled: boolean;
    phase?: "Disabled" | "Provisioning" | "Ready" | "Degraded" | "Failed" | "Deleting";
    reason?: string;
  };
  /** The Agent's models in the platform AI Gateway; absent when it has none. */
  aiGateway?: AIGatewayInfo;
}

export type CopilotProxyPhase = "SignInRequired" | "Starting" | "Ready" | "Degraded";

export interface AIGatewayInfo {
  /** Whether the Agent's model traffic goes through the Gateway. */
  routed: boolean;
  phase?: string;
  reason?: string;
  sharedModels: boolean;
  githubCopilot: boolean;
  /** Models the Agent is configured with, as it names them. */
  models: string[];
  defaultModel?: string;
  copilot?: {
    phase?: CopilotProxyPhase;
    reason?: string;
    models: string[];
    /** Account models left out because the Gateway has no price for them. */
    unpriced?: string[];
  };
  /** The Team's smart-routing models for this Agent: usable or why not. */
  modelRouters?: { name: string; granted: boolean; reason?: string }[];
}

export interface ModelAccess {
  sharedModels?: boolean;
  githubCopilot?: boolean;
}

export interface ConditionStatus {
  type: string;
  status: "True" | "False" | "Unknown";
  reason: string;
  message: string;
}

export interface CreateInstanceRequest {
  instanceName?: string;
  agent: Omit<AgentConfig, "registrySecretRef" | "type"> & { type?: AgentType };
  registryAuth?: RegistryAuth;
  requestId?: string;
  displayName?: string;
  team?: string;
  gatewayToken?: string;
  tailscaleAuthKey?: string;
  model?: string;
  teamsEnabled?: boolean;
  agentIdentityEnabled?: boolean;
  /** OpenClaw Agents with the AI Gateway enabled. */
  modelAccess?: ModelAccess;
}

export interface UpdateInstanceRequest extends InstanceVersion {
  agent?: {
    type?: AgentType;
    image?: string | null;
    username?: string;
    port?: number | null;
    webUI?: boolean | null;
  };
  displayName?: string;
  team?: string;
  gatewayToken?: string;
  tailscaleAuthKey?: string; // "" = disable tailscale
  model?: string; // "" = use deployment default
  teamsEnabled?: boolean;
}

export interface StatusResponse {
  conditions: ConditionStatus[];
  events: EventItem[];
  nsEvents: EventItem[];
  pods: PodStatus[];
}

export interface EventItem {
  type: string;
  reason: string;
  message: string;
  source: string;
  firstSeen: string;
  lastSeen: string;
  count: number;
}

export interface PodStatus {
  name: string;
  phase: string;
  ready: boolean;
  restartCount: number;
}
