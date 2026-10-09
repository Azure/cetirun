// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export interface TeamSkillStorage {
  resourceGroup: string;
  storageAccount: string;
  containerName: string;
  storageClassName?: string;
  storageSize?: string;
  mountPath?: string;
}

export interface TeamDTO {
  name: string;
  role?: string;
  description?: string[];
  defaultProvider?: string;
  skills?: string[];
  skillStorage?: TeamSkillStorage;
  normalAgentPolicyRef: string;
  adminAgentPolicyRef?: string;
  sandboxRefs: string[];
  defaultSandboxRef: string;
  createdAt?: string;
}

export interface TeamUpsertRequest {
  name?: string;
  role?: string;
  description?: string[];
  defaultProvider?: string;
  skills?: string[];
  skillStorage?: TeamSkillStorage;
  normalAgentPolicyRef: string;
  adminAgentPolicyRef?: string;
  sandboxRefs: string[];
  defaultSandboxRef: string;
}

async function parseError(response: Response, fallback: string): Promise<string> {
  try {
    const err = await response.json();
    return err?.error?.message || fallback;
  } catch {
    return fallback;
  }
}

export async function listTeams(): Promise<TeamDTO[]> {
  const response = await apiFetch("/api/v1/admin/teams");
  if (!response.ok) throw new Error(await parseError(response, "Failed to list teams"));
  const data = await response.json();
  return data.teams || [];
}

export async function getTeam(name: string): Promise<TeamDTO> {
  const response = await apiFetch(`/api/v1/admin/teams/${encodeURIComponent(name)}`);
  if (!response.ok) throw new Error(await parseError(response, "Failed to get team"));
  return response.json();
}

export async function createTeam(req: TeamUpsertRequest): Promise<TeamDTO> {
  const response = await apiFetch("/api/v1/admin/teams", {
    method: "POST",
    body: JSON.stringify(req),
  });
  if (!response.ok) throw new Error(await parseError(response, "Failed to create team"));
  return response.json();
}

export async function updateTeam(name: string, req: TeamUpsertRequest): Promise<TeamDTO> {
  const response = await apiFetch(`/api/v1/admin/teams/${encodeURIComponent(name)}`, {
    method: "PUT",
    body: JSON.stringify(req),
  });
  if (!response.ok) throw new Error(await parseError(response, "Failed to update team"));
  return response.json();
}

export async function deleteTeam(name: string): Promise<void> {
  const response = await apiFetch(`/api/v1/admin/teams/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
  if (!response.ok && response.status !== 404) {
    throw new Error(await parseError(response, "Failed to delete team"));
  }
}
