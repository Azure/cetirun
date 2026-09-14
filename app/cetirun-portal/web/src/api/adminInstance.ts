// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export interface AdminInstanceSummary {
  name: string;
  alias: string;
  displayName?: string;
  email?: string;
  team?: string;
  agentType?: string;
  phase?: string;
  ready: boolean;
  createdAt: string;
}

export async function listAllInstances(): Promise<AdminInstanceSummary[]> {
  const response = await apiFetch("/api/v1/admin/instances");
  if (!response.ok) throw new Error(`Failed to list instances: ${response.statusText}`);
  const data = await response.json();
  return data.instances || [];
}
