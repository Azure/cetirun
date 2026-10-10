// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export type PolicyHarness = "openclaw" | "hermes";

export interface AgentPolicyGatewayBudget {
  defaultMonthlyUSD: string;
  maxMonthlyUSD: string;
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

export async function updatePolicy(name: string, req: AgentPolicyUpsertRequest): Promise<AgentPolicyDTO> {
  const response = await apiFetch(`/api/v1/admin/policies/${encodeURIComponent(name)}`, {
    method: "PUT",
    body: JSON.stringify(req),
  });
  if (!response.ok) throw new Error(await parseError(response, "Failed to update policy"));
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
