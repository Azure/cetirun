// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { AgentType } from "./instance";

export interface Features {
  customChatEnabled?: boolean;
  tailscaleEnabled: boolean;
  agentIdentityEnabled: boolean;
  /** Azure Agent connection management in the Portal. */
  agentToolsEnabled: boolean;
  maxInstancesPerUser: number; // 0 = unlimited
  allowedAgentTypes?: AgentType[];
  /** The platform AI Gateway: model sources, GitHub Copilot sign-in, Team required. */
  aiGatewayEnabled?: boolean;
}
