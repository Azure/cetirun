// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export interface AgentIdentity {
  instanceUid: string;
  tenantId: string;
  principalId: string;
  clientId: string;
}

/** An Azure independent identity owned by one Agent. */
export interface Connection {
  id: string;
  provider: "azure";
  accountDisplay: string;
  services: string[];
  revision: number;
  status: string;
  createdAt: string;
  mode: "agent";
  identity?: AgentIdentity;
}

export interface ConnectionBinding {
  id: string;
  instanceUid: string;
  connectionId: string;
  services: string[];
  enabled: boolean;
  revision: number;
}

export interface ConnectionBindingList {
  bindings: ConnectionBinding[];
  /** A migrated/unsupported runtime cannot create bindings, but may disconnect. */
  readOnly: boolean;
}

export class ToolAuthorizationError extends Error {
  constructor(message: string, readonly code?: string) {
    super(message);
    this.name = "ToolAuthorizationError";
  }
}

async function responseError(response: Response, fallback: string): Promise<ToolAuthorizationError> {
  const body = await response.json().catch(() => undefined);
  const error = body?.error;
  return new ToolAuthorizationError(
    typeof error?.message === "string" ? error.message : fallback,
    typeof error?.code === "string" ? error.code : undefined,
  );
}

async function parse<T>(response: Response): Promise<T> {
  if (!response.ok) throw await responseError(response, "Azure connection request failed");
  return response.json() as Promise<T>;
}

export async function listConnections(signal?: AbortSignal): Promise<Connection[]> {
  return parse(await apiFetch("/api/v1/connections", { signal }));
}

export async function listConnectionBindings(name: string, signal?: AbortSignal): Promise<ConnectionBindingList> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/connection-bindings`, { signal });
  const bindings = await parse<ConnectionBinding[]>(response);
  return { bindings, readOnly: response.headers.get("X-CetiRun-Connection-Bindings-Read-Only") === "true" };
}

export async function createToolIdentity(name: string): Promise<Connection> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/tool-identities`, {
    method: "POST",
    body: JSON.stringify({ provider: "azure" }),
  }));
}

export async function createConnectionBinding(name: string, connectionId: string): Promise<ConnectionBinding> {
  return parse(await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/connection-bindings`, {
    method: "POST",
    body: JSON.stringify({ connectionId, services: ["azure_arm"], enabled: true }),
  }));
}

export async function deleteConnectionBinding(name: string, id: string, revision: number): Promise<void> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/connection-bindings/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: { "If-Match": String(revision) },
  });
  if (!response.ok) throw await responseError(response, "Could not disconnect this Agent from Azure");
}
