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
  uid: string;
  resourceVersion: string;
  agentPolicyRef: string;
  sandboxRefs: string[];
  defaultSandboxRef: string;
  name: string;
  role?: string;
  description?: string[];
  defaultProvider?: string;
  skills?: string[];
  skillStorage?: TeamSkillStorage;
  createdAt?: string;
}

export interface TeamUpsertRequest {
  uid?: string;
  resourceVersion?: string;
  agentPolicyRef: string;
  sandboxRefs: string[];
  defaultSandboxRef: string;
  name?: string;
  role?: string;
  description?: string[];
  defaultProvider?: string;
  skills?: string[];
  skillStorage?: TeamSkillStorage;
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

export async function deleteTeam(team: Pick<TeamDTO, "name" | "uid" | "resourceVersion">): Promise<void> {
  const response = await apiFetch(`/api/v1/admin/teams/${encodeURIComponent(team.name)}`, {
    method: "DELETE",
    body: JSON.stringify({ uid: team.uid, resourceVersion: team.resourceVersion }),
  });
  if (!response.ok && response.status !== 404) {
    throw new Error(await parseError(response, "Failed to delete team"));
  }
}
