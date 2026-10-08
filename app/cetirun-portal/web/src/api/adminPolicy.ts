// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

/** Kubernetes metadata returned by policy administration endpoints. */
export interface PolicyMetadata {
  name: string;
  uid?: string;
  generation?: number;
  creationTimestamp?: string;
}

export interface PolicyCondition {
  type: string;
  status: "True" | "False" | "Unknown";
  reason?: string;
  message?: string;
}

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

async function errorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const body = await response.json();
    return body?.error?.message || fallback;
  } catch {
    return fallback;
  }
}

async function request<T>(path: string, options?: RequestInit, fallback = "Policy request failed"): Promise<T> {
  const response = await apiFetch(path, options);
  if (!response.ok) throw new Error(await errorMessage(response, fallback));
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export function listAgentPolicies(): Promise<AgentPolicyResource[]> {
  return request("/api/v1/admin/agent-policies", undefined, "Failed to list AgentPolicies");
}
export function getAgentPolicy(name: string): Promise<AgentPolicyResource> {
  return request(`/api/v1/admin/agent-policies/${encodeURIComponent(name)}`, undefined, "Failed to load AgentPolicy");
}
export function createAgentPolicy(resource: AgentPolicyResource): Promise<AgentPolicyResource> {
  return request("/api/v1/admin/agent-policies", { method: "POST", body: JSON.stringify(resource) }, "Failed to create AgentPolicy");
}
export function updateAgentPolicy(name: string, spec: Record<string, unknown>): Promise<AgentPolicyResource> {
  return request(`/api/v1/admin/agent-policies/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify({ spec }) }, "Failed to update AgentPolicy");
}
export function deleteAgentPolicy(name: string): Promise<void> {
  return request(`/api/v1/admin/agent-policies/${encodeURIComponent(name)}`, { method: "DELETE" }, "Failed to delete AgentPolicy");
}

export function listSandboxes(): Promise<SandboxResource[]> {
  return request("/api/v1/admin/sandboxes", undefined, "Failed to list Sandboxes");
}
export function getSandbox(name: string): Promise<SandboxResource> {
  return request(`/api/v1/admin/sandboxes/${encodeURIComponent(name)}`, undefined, "Failed to load Sandbox");
}
export function createSandbox(resource: SandboxResource): Promise<SandboxResource> {
  return request("/api/v1/admin/sandboxes", { method: "POST", body: JSON.stringify(resource) }, "Failed to create Sandbox");
}
export function updateSandbox(name: string, spec: Record<string, unknown>): Promise<SandboxResource> {
  return request(`/api/v1/admin/sandboxes/${encodeURIComponent(name)}`, { method: "PUT", body: JSON.stringify({ spec }) }, "Failed to update Sandbox");
}
export function deleteSandbox(name: string): Promise<void> {
  return request(`/api/v1/admin/sandboxes/${encodeURIComponent(name)}`, { method: "DELETE" }, "Failed to delete Sandbox");
}
