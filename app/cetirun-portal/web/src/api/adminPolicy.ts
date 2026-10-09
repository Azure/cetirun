// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export interface PolicyMetadata { name: string; uid?: string; resourceVersion?: string; generation?: number; creationTimestamp?: string; }
export interface PolicyResourceVersion { uid: string; resourceVersion: string; }
export interface PolicyCondition { type: string; status: "True" | "False" | "Unknown"; reason?: string; message?: string; }
export interface AgentPolicyResource {
  apiVersion?: string;
  kind?: "AgentPolicy";
  metadata: PolicyMetadata;
  spec: Record<string, unknown>;
  status?: { observedGeneration?: number; conditions?: PolicyCondition[] };
}
export interface SandboxResource {
  apiVersion?: string;
  kind?: "Sandbox";
  metadata: PolicyMetadata;
  spec: Record<string, unknown>;
  status?: { observedGeneration?: number; conditions?: PolicyCondition[] };
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await apiFetch(path, options);
  if (!response.ok) {
    let message = "Policy request failed";
    try { message = (await response.json()).error?.message || message; } catch { /* use fallback */ }
    throw new Error(message);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export const listAgentPolicies = () => request<AgentPolicyResource[]>("/api/v1/admin/agent-policies");
export const getAgentPolicy = (name: string) => request<AgentPolicyResource>(`/api/v1/admin/agent-policies/${encodeURIComponent(name)}`);
export const createAgentPolicy = (resource: AgentPolicyResource) => request<AgentPolicyResource>("/api/v1/admin/agent-policies", { method: "POST", body: JSON.stringify(resource) });
export const updateAgentPolicy = (name: string, version: PolicyResourceVersion, spec: Record<string, unknown>) => request<AgentPolicyResource>(`/api/v1/admin/agent-policies/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify({ ...version, spec }) });
export const deleteAgentPolicy = (name: string, version: PolicyResourceVersion) => request<void>(`/api/v1/admin/agent-policies/${encodeURIComponent(name)}`, { method: "DELETE", body: JSON.stringify(version) });

export const listSandboxes = () => request<SandboxResource[]>("/api/v1/admin/sandboxes");
export const getSandbox = (name: string) => request<SandboxResource>(`/api/v1/admin/sandboxes/${encodeURIComponent(name)}`);
export const createSandbox = (resource: SandboxResource) => request<SandboxResource>("/api/v1/admin/sandboxes", { method: "POST", body: JSON.stringify(resource) });
export const updateSandbox = (name: string, version: PolicyResourceVersion, spec: Record<string, unknown>) => request<SandboxResource>(`/api/v1/admin/sandboxes/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify({ ...version, spec }) });
export const deleteSandbox = (name: string, version: PolicyResourceVersion) => request<void>(`/api/v1/admin/sandboxes/${encodeURIComponent(name)}`, { method: "DELETE", body: JSON.stringify(version) });

export interface InstancePolicyPatch {
  uid: string;
  resourceVersion: string;
  team?: string;
  policyClass?: "normal" | "admin";
  sandboxRef?: string;
  monthlyBudgetUSD?: string;
}
export async function updateInstancePolicy(name: string, patch: InstancePolicyPatch): Promise<void> {
  await request(`/api/v1/admin/instances/${encodeURIComponent(name)}/policy`, { method: "PATCH", body: JSON.stringify(patch) });
}
