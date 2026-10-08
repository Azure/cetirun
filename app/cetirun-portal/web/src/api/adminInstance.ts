// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";
import type { AgentConfig } from "../types/instance";

export interface AdminInstanceSummary {
  name: string;
  alias: string;
  displayName?: string;
  email?: string;
  team?: string;
  agent: AgentConfig;
  uid: string;
  resourceVersion: string;
  suspended: boolean;
  phase?: string;
  ready: boolean;
  createdAt: string;
}

export async function updateInstanceLifecycle(instance: AdminInstanceSummary, suspended: boolean): Promise<void> {
  const response = await apiFetch(`/api/v1/admin/instances/${encodeURIComponent(instance.name)}`, {
    method: "PATCH",
    body: JSON.stringify({ uid: instance.uid, resourceVersion: instance.resourceVersion, suspended }),
  });
  if (!response.ok) {
    let message = `Failed to ${suspended ? "suspend" : "resume"} Agent`;
    try {
      const body = await response.json();
      message = body?.error?.message || message;
    } catch {
      // Keep the stable fallback when the server did not return JSON.
    }
    throw new Error(message);
  }
}

export async function listAllInstances(): Promise<AdminInstanceSummary[]> {
  const response = await apiFetch("/api/v1/admin/instances");
  if (!response.ok) throw new Error(`Failed to list instances: ${response.statusText}`);
  const data = await response.json();
  return data.instances || [];
}
