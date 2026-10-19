// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { Instance, InstanceVersion, CreateInstanceRequest, UpdateInstanceRequest, RestartInstanceRequest, StatusResponse } from "../types/instance";
import { apiFetch } from "./fetch";

export type InstanceResponse = Instance;

export class CreateInstanceError extends Error {
  instanceName?: string;
  constructor(message: string, instanceName?: string) {
    super(message);
    this.instanceName = instanceName;
  }
}

export async function getInstance(): Promise<Instance | null> {
  const response = await apiFetch("/api/v1/instance");
  if (response.status === 404) return null;
  if (!response.ok) throw new Error("Failed to get instance");
  return response.json();
}

export async function createInstance(req: CreateInstanceRequest): Promise<Instance> {
  const { requestId, ...body } = req;
  const response = await apiFetch("/api/v1/instances", {
    method: "POST",
    headers: requestId ? { "Idempotency-Key": requestId } : undefined,
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    const err = await response.json();
    throw new CreateInstanceError(err.error?.message || "Failed to create instance", err.instanceName);
  }
  return response.json();
}

export async function updateInstance(req: UpdateInstanceRequest): Promise<Instance> {
  const response = await apiFetch("/api/v1/instance", {
    method: "PATCH",
    body: JSON.stringify(req),
  });
  if (!response.ok) {
    const err = await response.json();
    throw new Error(err.error?.message || "Failed to update instance");
  }
  return response.json();
}

export async function upgradeInstance(version: InstanceVersion): Promise<Instance> {
  const response = await apiFetch("/api/v1/instance/upgrade", {
    method: "POST", body: JSON.stringify({ uid: version.uid, resourceVersion: version.resourceVersion }),
  });
  if (!response.ok) {
    const err = await response.json();
    throw new Error(err.error?.message || "Failed to upgrade instance");
  }
  return response.json();
}

export async function getInstanceStatus(): Promise<StatusResponse> {
  const response = await apiFetch("/api/v1/instance/status");
  if (!response.ok) {
    let err: any;
    try {
      err = await response.json();
    } catch {
      // Ignore JSON parsing errors and fall back to generic message below.
    }
    throw new Error(err?.error?.message || "Failed to get instance status");
  }
  return response.json();
}

export async function downloadTeamsApp(instanceName: string): Promise<void> {
  let response: Response;
  try {
    response = await fetch(`/api/v1/instances/${encodeURIComponent(instanceName)}/teams-app`);
  } catch {
    throw new Error("Session expired, please refresh the page");
  }

  const ct = response.headers.get("content-type") || "";
  if (response.status === 401 || response.redirected || ct.includes("text/html")) {
    throw new Error("Session expired, please refresh the page");
  }

  if (!response.ok) {
    let err: any;
    try {
      err = await response.json();
    } catch {
      // Response may not be JSON (e.g. HTML from OAuth2 Proxy).
    }
    throw new Error(err?.error?.message || "Failed to download Teams app");
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "cetirun-teams-app.zip";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 0);
}

export async function upgradeInstanceByName(name: string, version: InstanceVersion): Promise<Instance> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/upgrade`, {
    method: "POST", body: JSON.stringify({ uid: version.uid, resourceVersion: version.resourceVersion }),
  });
  if (!response.ok) {
    const err = await response.json();
    throw new Error(err.error?.message || "Failed to upgrade instance");
  }
  return response.json();
}

export async function restartInstanceByName(name: string, request: RestartInstanceRequest): Promise<Instance> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/restart`, {
    method: "POST",
    headers: { "Idempotency-Key": request.requestId },
    body: JSON.stringify({ uid: request.uid, resourceVersion: request.resourceVersion }),
  });
  if (!response.ok) {
    const error = await response.json();
    throw new Error(error.error?.message || "Failed to request Agent restart");
  }
  return response.json();
}

export async function getInstanceStatusByName(name: string): Promise<StatusResponse> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}/status`);
  if (!response.ok) {
    let err: any;
    try {
      err = await response.json();
    } catch {
      // Ignore JSON parsing errors and fall back to generic message below.
    }
    throw new Error(err?.error?.message || "Failed to get instance status");
  }
  return response.json();
}

export async function fetchInstances(): Promise<InstanceResponse[]> {
  const response = await apiFetch("/api/v1/instances");
  if (!response.ok) throw new Error(`Failed to fetch instances: ${response.statusText}`);
  return response.json();
}

export async function fetchInstanceByName(name: string): Promise<InstanceResponse> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}`);
  if (!response.ok) throw new Error(`Failed to fetch instance ${name}: ${response.statusText}`);
  return response.json();
}

export async function deleteInstanceByName(name: string, version: InstanceVersion): Promise<void> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}`, {
    method: "DELETE",
    body: JSON.stringify({ uid: version.uid, resourceVersion: version.resourceVersion }),
  });
  if (!response.ok && response.status !== 404) {
    const text = await response.text().catch(() => "");
    let message = "Failed to delete instance";
    try {
      const err = JSON.parse(text);
      message = err.error?.message || message;
    } catch {
      if (text) message = text;
    }
    throw new Error(message);
  }
}

export async function updateInstanceByName(name: string, req: UpdateInstanceRequest): Promise<Instance> {
  const response = await apiFetch(`/api/v1/instances/${encodeURIComponent(name)}`, {
    method: "PATCH",
    body: JSON.stringify(req),
  });
  if (!response.ok) {
    const err = await response.json();
    throw new Error(err.error?.message || "Failed to update instance");
  }
  return response.json();
}
