// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export type PolicyHarness = "openclaw" | "hermes";

export interface AgentPolicyGatewayBudget {
  defaultMonthlyUSD: string;
  maxMonthlyUSD: string;
}

export type AgentPolicyProtocol = "TCP" | "UDP" | "SCTP";
export type AgentPolicyIngressMode = "authenticated" | "disabled";

export interface AgentPolicyPort {
  port: number;
  protocol: AgentPolicyProtocol;
}

export interface AgentPolicyCIDRRule {
  cidr: string;
  ports: AgentPolicyPort[];
}

export interface AgentPolicyKubernetesAccess {
  serviceAccountName: string;
  roleBindings?: { namespace: string; clusterRoleName: string }[];
  clusterRoleBindings?: { clusterRoleName: string }[];
}

export interface AgentPolicyCondition {
  type: string;
  status: "True" | "False" | "Unknown" | string;
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
  observedGeneration?: number;
}

export interface AgentPolicyDTO {
  name: string;
  uid: string;
  resourceVersion: string;
  harnesses: { allowed: PolicyHarness[] };
  skills?: { allowed?: string[] };
  modelAccess?: {
    copilot?: { allowed?: boolean };
    sharedModels?: { allowed?: string[] };
    gatewayBudget?: AgentPolicyGatewayBudget;
  };
  agentIdentity?: {
    allowed?: boolean;
    required?: boolean;
    credentialServices?: string[];
  };
  entraID?: { enabled?: boolean };
  sandboxSelection?: { allowMultiple?: boolean };
  security?: { readOnlyRootFilesystem?: boolean };
  network?: {
    blockedCIDRs?: string[];
    blockedFQDNs?: string[];
    blockedPorts?: AgentPolicyPort[];
    allowedInternalCIDRs?: AgentPolicyCIDRRule[];
    ingress?: { mode?: AgentPolicyIngressMode; requireMTLS?: boolean };
  };
  kubernetesAccess?: AgentPolicyKubernetesAccess | null;
  generation?: number;
  observedGeneration?: number;
  conditions?: AgentPolicyCondition[];
  deleting?: boolean;
  createdAt?: string;
}

export interface AgentPolicyUpsertRequest {
  name?: string;
  uid?: string;
  resourceVersion?: string;
  harnesses: { allowed: PolicyHarness[] };
  skills?: { allowed?: string[] };
  modelAccess?: {
    copilot?: { allowed?: boolean };
    sharedModels?: { allowed?: string[] };
    gatewayBudget?: AgentPolicyGatewayBudget;
  };
  sandboxSelection?: { allowMultiple?: boolean };
  security?: { readOnlyRootFilesystem?: boolean };
  network?: {
    blockedCIDRs?: string[];
    blockedPorts?: AgentPolicyPort[];
    allowedInternalCIDRs?: AgentPolicyCIDRRule[];
    ingress?: { mode?: AgentPolicyIngressMode };
  };
}

async function parseError(response: Response, fallback: string): Promise<string> {
  try {
    const err = await response.json();
    return err?.error?.message || fallback;
  } catch {
    return fallback;
  }
}

export async function listPolicies(): Promise<AgentPolicyDTO[]> {
  const response = await apiFetch("/api/v1/admin/policies");
  if (!response.ok) throw new Error(await parseError(response, "Failed to list policies"));
  const data = await response.json();
  return data.policies || [];
}

export async function getPolicy(name: string): Promise<AgentPolicyDTO> {
  const response = await apiFetch(`/api/v1/admin/policies/${encodeURIComponent(name)}`);
  if (!response.ok) throw new Error(await parseError(response, "Failed to get policy"));
  return response.json();
}

export async function createPolicy(req: AgentPolicyUpsertRequest): Promise<AgentPolicyDTO> {
  const response = await apiFetch("/api/v1/admin/policies", {
    method: "POST",
    body: JSON.stringify(req),
  });
  if (!response.ok) throw new Error(await parseError(response, "Failed to create policy"));
  return response.json();
}

export class PolicyConflictError extends Error {}

export async function updatePolicy(name: string, req: AgentPolicyUpsertRequest): Promise<AgentPolicyDTO> {
  const response = await apiFetch(`/api/v1/admin/policies/${encodeURIComponent(name)}`, {
    method: "PUT",
    body: JSON.stringify(req),
  });
  if (!response.ok) {
    const message = await parseError(response, "Failed to update policy");
    throw response.status === 409 ? new PolicyConflictError(message) : new Error(message);
  }
  return response.json();
}

export async function deletePolicy(policy: Pick<AgentPolicyDTO, "name" | "uid" | "resourceVersion">): Promise<void> {
  const response = await apiFetch(`/api/v1/admin/policies/${encodeURIComponent(policy.name)}`, {
    method: "DELETE",
    body: JSON.stringify({ uid: policy.uid, resourceVersion: policy.resourceVersion }),
  });
  if (!response.ok && response.status !== 404) {
    throw new Error(await parseError(response, "Failed to delete policy"));
  }
}
