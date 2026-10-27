// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export type SandboxResources = Record<"cpu" | "memory" | "ephemeral-storage", string>;
export interface SandboxSpec {
  type: "kata" | "gvisor";
  resources: Record<"agent" | "helpers", { requests: SandboxResources; limits: SandboxResources }>;
  storage: {
    home: { storageClassName: string; size: string; accessMode: "ReadWriteOncePod" | "ReadWriteOnce" };
    scratch: { sizeLimit: string };
  };
}
export interface SandboxDTO extends SandboxSpec {
  name: string;
  uid: string;
  resourceVersion: string;
  ready: string;
  deleting: boolean;
}
export interface SandboxUpsertRequest extends Omit<SandboxSpec, "type"> {
  type?: SandboxSpec["type"] | null;
  name?: string;
  uid?: string;
  resourceVersion?: string;
}

async function request(path: string, method = "GET", body?: unknown): Promise<Response> {
  const response = await apiFetch(`/api/v1/admin/sandboxes${path}`, { method, body: body === undefined ? undefined : JSON.stringify(body) });
  if (!response.ok) {
    let message = "Unable to manage Sandbox";
    try { message = (await response.json())?.error?.message || message; } catch { /* Use the default for non-JSON errors. */ }
    throw new Error(message);
  }
  return response;
}
export async function listSandboxes(): Promise<SandboxDTO[]> {
  return (await (await request("")).json()).sandboxes || [];
}
export async function getSandbox(name: string): Promise<SandboxDTO> {
  return (await request(`/${encodeURIComponent(name)}`)).json();
}
export async function createSandbox(body: SandboxUpsertRequest): Promise<SandboxDTO> {
  return (await request("", "POST", body)).json();
}
export async function updateSandbox(name: string, body: SandboxUpsertRequest): Promise<SandboxDTO> {
  return (await request(`/${encodeURIComponent(name)}`, "PUT", body)).json();
}
export async function deleteSandbox(box: Pick<SandboxDTO, "name" | "uid" | "resourceVersion">): Promise<void> {
  await request(`/${encodeURIComponent(box.name)}`, "DELETE", { uid: box.uid, resourceVersion: box.resourceVersion });
}
