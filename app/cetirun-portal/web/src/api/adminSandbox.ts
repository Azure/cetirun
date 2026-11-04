// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";
import { adminResponseError } from "./adminError";

export type SandboxResources = Record<"cpu" | "memory" | "ephemeral-storage", string>;
export interface SandboxSpec {
  type: "kata" | "gvisor" | "container";
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
  generation: number;
  observedGeneration?: number;
  conditions?: { type: string; status: string; reason?: string; message?: string; observedGeneration?: number }[];
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
    throw await adminResponseError(response, "Unable to manage Sandbox");
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
