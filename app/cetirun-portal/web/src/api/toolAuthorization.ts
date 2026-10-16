// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export const TOOL_CATALOG_VERSION = "2026-10-16";

export type ConnectionProvider = "azure" | "microsoft" | "github" | "google";
export type ConnectionMode = "agent" | "delegated";
export type ConnectionStatus = "connecting" | "connected" | "interaction_required" | "disconnected" | "pending" | "ready";

export interface AgentIdentity {
  instanceUid: string;
  tenantId: string;
  principalId: string;
  clientId: string;
}

export interface Connection {
  id: string;
  provider: ConnectionProvider;
  accountDisplay: string;
  capabilities: string[];
  revision: number;
  status: ConnectionStatus;
  createdAt: string;
  /** Absent records were created before independent Agent identities existed. */
  mode?: ConnectionMode;
  /** Present only for Broker-derived independent Agent identities. */
  identity?: AgentIdentity;
}

export interface ConnectionStart {
  authorizationUrl: string;
  expiresAt: string;
  connectionId: string;
}

export interface ToolConstraints {
  subscriptionIds: string[];
  /** Absent from grants created before Azure write operations were available. */
  resourceGroupNames?: string[];
  repositoryIds: string[];
  baseBranches: string[];
  calendarIds: string[];
  recipientAddresses: string[];
  recipientDomains: string[];
  allowInvites: boolean;
}

export interface ToolGrant {
  id: string;
  instanceUid: string;
  connectionId: string;
  tools: string[];
  constraints: ToolConstraints;
  expiresAt: string;
  revision: number;
  authorization?: ToolGrantAuthorization;
}

export interface ToolGrantList {
  grants: ToolGrant[];
  readOnly: boolean;
}

export interface ToolGrantAuthorization {
  state: "pending" | "verified";
  source: "direct_rbac";
  verifiedAt?: string;
  validUntil?: string;
  reason?: string;
  scopes?: string[];
  actions?: string[];
  requestIds?: string[];
}

export interface ToolGrantInput {
  connectionId: string;
  tools: string[];
  constraints: ToolConstraints;
  expiresAt: string;
  revision?: number;
}

export class ToolAuthorizationError extends Error {
  constructor(message: string, readonly code?: string) {
    super(message);
  }
}

async function responseError(response: Response, fallback: string): Promise<ToolAuthorizationError> {
  const body = await response.json().catch(() => undefined);
  const error = body?.error;
  const message = typeof error?.message === "string" ? error.message : fallback;
  const code = typeof error?.code === "string" ? error.code : undefined;
  return new ToolAuthorizationError(message, code);
}

async function parse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    throw await responseError(response, "Tool authorization request failed");
  }
  return response.json() as Promise<T>;
}

export async function listConnections(signal?: AbortSignal): Promise<Connection[]> {
  return parse(await apiFetch("/api/v1/connections", { signal }));
}

export async function startConnection(provider: ConnectionProvider): Promise<ConnectionStart> {
  return parse(await apiFetch("/api/v1/connections/start", { method: "POST", body: JSON.stringify({ provider }) }));
}

export async function reconnectConnection(id: string): Promise<ConnectionStart> {
  return parse(await apiFetch(`/api/v1/connections/${encodeURIComponent(id)}/reconnect`, { method: "POST" }));
}

export async function deleteConnection(id: string): Promise<void> {
  const response = await apiFetch(`/api/v1/connections/${encodeURIComponent(id)}`, { method: "DELETE" });
  if (!response.ok) {
    throw await responseError(response, "Could not disconnect this account");
  }
}

export async function listToolGrants(name: string, signal?: AbortSignal): Promise<ToolGrantList> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-grants`, { signal });
  const grants = await parse<ToolGrant[]>(response);
  return { grants, readOnly: response.headers.get("X-CetiRun-Tool-Grants-Read-Only") === "true" };
}

export type ToolIdentityInput = { provider: "azure" };

export async function createToolIdentity(name: string, input: ToolIdentityInput = { provider: "azure" }): Promise<Connection> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-identities`, { method: "POST", body: JSON.stringify(input) }));
}

export async function createToolGrant(name: string, input: ToolGrantInput): Promise<ToolGrant> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-grants`, { method: "POST", body: JSON.stringify(input) }));
}

export async function updateToolGrant(name: string, id: string, input: ToolGrantInput & { revision: number }): Promise<ToolGrant> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-grants/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(input) }));
}

export async function deleteToolGrant(name: string, id: string, revision: number): Promise<void> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-grants/${encodeURIComponent(id)}`, { method: "DELETE", headers: { "If-Match": String(revision) } });
  if (!response.ok) {
    throw await responseError(response, "Could not revoke this tool grant");
  }
}

export async function verifyToolGrant(name: string, id: string, revision: number): Promise<ToolGrant> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-grants/${encodeURIComponent(id)}/verify`, { method: "POST", headers: { "If-Match": String(revision) } }));
}
