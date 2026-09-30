// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export interface SharedModel {
  name: string;
  contextWindow?: number;
  maxOutputTokens?: number;
}

export interface AIGatewayCatalog {
  /** False until the operator has published the shared models. */
  known: boolean;
  sharedModels: SharedModel[];
}

export type CopilotLoginState = "none" | "pending" | "succeeded" | "expired" | "denied" | "failed";

/** A GitHub Copilot sign-in run by the Portal; never carries a token. */
export interface CopilotLogin {
  state: CopilotLoginState;
  userCode?: string;
  verificationUri?: string;
  expiresAt?: string;
  /** Seconds between polls while pending. */
  interval?: number;
  message?: string;
}

async function errorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const body = await response.json();
    return body?.error?.message || fallback;
  } catch {
    return fallback;
  }
}

export async function getAIGatewayCatalog(): Promise<AIGatewayCatalog> {
  const response = await apiFetch("/api/v1/ai-gateway/catalog");
  if (!response.ok) throw new Error(await errorMessage(response, "Failed to load the shared models"));
  return response.json();
}

function loginPath(instanceName: string): string {
  return `/api/v1/instances/${encodeURIComponent(instanceName)}/copilot/login`;
}

export async function startCopilotLogin(instanceName: string): Promise<CopilotLogin> {
  const response = await apiFetch(loginPath(instanceName), { method: "POST" });
  if (!response.ok) throw new Error(await errorMessage(response, "Failed to start the GitHub Copilot sign-in"));
  return response.json();
}

export async function pollCopilotLogin(instanceName: string): Promise<CopilotLogin> {
  const response = await apiFetch(loginPath(instanceName));
  if (!response.ok) throw new Error(await errorMessage(response, "Failed to check the GitHub Copilot sign-in"));
  return response.json();
}

export async function cancelCopilotLogin(instanceName: string): Promise<void> {
  const response = await apiFetch(loginPath(instanceName), { method: "DELETE" });
  if (!response.ok && response.status !== 404) {
    throw new Error(await errorMessage(response, "Failed to cancel the GitHub Copilot sign-in"));
  }
}
