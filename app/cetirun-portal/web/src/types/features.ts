import type { AgentType } from "./instance";

export interface Features {
  tailscaleEnabled: boolean;
  maxInstancesPerUser: number; // 0 = unlimited
  allowedAgentTypes?: AgentType[];
}
