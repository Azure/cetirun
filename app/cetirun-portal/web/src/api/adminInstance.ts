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
  phase?: string;
  ready: boolean;
  suspended: boolean;
  sandboxRef?: string;
  policyClass?: "normal" | "admin";
  monthlyBudgetUSD?: string;
  policy?: { phase?: "Pending" | "Applied" | "Denied" | "Unknown"; effectivePolicyRef?: { name: string; class: "normal" | "admin" } };
  createdAt: string;
}

export async function listAllInstances(): Promise<AdminInstanceSummary[]> {
  const response = await apiFetch("/api/v1/admin/instances");
  if (!response.ok) throw new Error(`Failed to list instances: ${response.statusText}`);
  const data = await response.json();
  return data.instances || [];
}
