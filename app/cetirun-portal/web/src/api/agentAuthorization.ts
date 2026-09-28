// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export type AgentAuthorizationState = "disconnected" | "connecting" | "connected" | "action_required";

export interface AgentAuthorizationStatus {
  status: AgentAuthorizationState;
  account?: string;
  lastRenewed?: string;
  lastUsed?: string;
  correlationId: string;
}

export interface AgentAuthorizationStart extends AgentAuthorizationStatus {
  authorizationUrl: string;
  expiresAt: string;
}

async function parse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const body = await response.json().catch(() => undefined);
    throw new Error(body?.error?.message || "Microsoft Entra connection request failed");
  }
  return response.json() as Promise<T>;
}

export async function getAgentAuthorization(name: string, signal?: AbortSignal): Promise<AgentAuthorizationStatus> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/agent-authorization`, { signal }));
}

export async function startAgentAuthorization(name: string): Promise<AgentAuthorizationStart> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/agent-authorization/start`, { method: "POST", body: "{}" }));
}

export async function endAgentAuthorization(name: string): Promise<AgentAuthorizationStatus> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/agent-authorization`, { method: "DELETE" }));
}
