import type { Instance } from "../types/instance";

/** Badge color for an instance phase. */
export const phaseBadgeColor: Record<Instance["phase"], "success" | "warning" | "danger" | "informative"> = {
  Ready: "success",
  Provisioning: "warning",
  Pending: "informative",
  Failed: "danger",
  Deleting: "warning",
  Suspended: "warning",
};

/** Derive the base URL (origin + pathname, no query params) from an instance accessUrl. */
export function parseBaseUrl(accessUrl: string | undefined): string | undefined {
  if (!accessUrl) return undefined;
  try {
    const u = new URL(accessUrl);
    if (u.protocol !== "https:" && u.protocol !== "http:") return undefined;
    return u.origin + u.pathname.replace(/\/$/, "");
  } catch {
    return undefined;
  }
}

/**
 * Return the browser-native UI entrypoint for an instance.
 *
 * Each supported agent serves its browser-native UI below /ui/<instanceName>.
 */
export function agentUIUrl(instance: Pick<Instance, "agentType" | "accessUrl">): string | undefined {
  const baseUrl = parseBaseUrl(instance.accessUrl);
  if (!baseUrl) return undefined;
  return `${baseUrl}/`;
}

/**
 * Return the gateway base URL for instance services such as the embedded
 * terminal, desktop, and CetiRun authentication helper. These services are
 * intentionally separate from the agent's browser-native /ui route.
 */
export function instanceGatewayUrl(instance: Pick<Instance, "instanceName" | "accessUrl">): string | undefined {
  if (!instance.accessUrl) return undefined;
  try {
    const u = new URL(instance.accessUrl);
    if (u.protocol !== "https:" && u.protocol !== "http:") return undefined;
    return `${u.origin}/${encodeURIComponent(instance.instanceName)}`;
  } catch {
    return undefined;
  }
}
