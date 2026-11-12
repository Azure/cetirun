// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { Instance } from "../types/instance";
import { SESSION_EXPIRED_MESSAGE } from "./fetch";
import { fetchInstanceByName } from "./instance";

const SIGN_IN_FAILED = "Could not sign in to the Hermes Webapp. Please try again.";

/** Use the current owner-scoped credential, and let Hermes set its own cookies. */
export async function signInToHermes(
  expected: Pick<Instance, "uid" | "instanceName">,
  signal: AbortSignal,
): Promise<string> {
  try {
    signal.throwIfAborted();
    const instance = await fetchInstanceByName(expected.instanceName, signal);
    signal.throwIfAborted();
    if (!expected.uid || instance.uid !== expected.uid || instance.instanceName !== expected.instanceName || instance.agent.type !== "hermes") {
      throw new Error("The Agent changed. Refresh the page before opening its Webapp.");
    }
    if (instance.phase !== "Ready" || instance.suspended) {
      throw new Error("The Hermes Agent is not ready. Wait for Ready, then try again.");
    }

    const prefix = `/ui/${encodeURIComponent(instance.instanceName)}/`;
    let target: URL;
    try {
      target = new URL(instance.accessUrl ?? "");
    } catch {
      throw new Error("The Hermes Webapp address is unavailable. Refresh the page and try again.");
    }
    // The password must go only to this instance's UI on the Portal origin.
    // Build the destination again to discard any token query or fragment.
    if (target.origin !== window.location.origin || target.username || target.password ||
      (target.pathname !== prefix && target.pathname !== prefix.slice(0, -1))) {
      throw new Error("Automatic Hermes sign-in requires this Agent's Webapp on the Portal address.");
    }
    target = new URL(prefix, window.location.origin);
    if (!instance.apiServerKey) {
      throw new Error("The Hermes Webapp key is unavailable. Refresh the page and try again.");
    }

    let response: Response;
    try {
      response = await fetch(new URL("auth/password-login", target), {
        method: "POST",
        mode: "same-origin",
        credentials: "same-origin",
        redirect: "manual",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ provider: "basic", username: "cetirun", password: instance.apiServerKey, next: "/" }),
        signal,
      });
    } catch {
      signal.throwIfAborted();
      throw new Error("Could not reach the Hermes Webapp. Check your Portal session and try again.");
    }
    signal.throwIfAborted();
    if (response.type === "opaqueredirect" || response.redirected ||
      (response.status >= 300 && response.status < 400) || response.headers.get("content-type")?.includes("text/html")) {
      throw new Error(SESSION_EXPIRED_MESSAGE);
    }
    if (response.status === 401) {
      throw new Error("Hermes rejected the sign-in key. Refresh the Agent page and try again.");
    }
    if (response.status === 403) {
      throw new Error("Access to the Hermes Webapp was denied. Refresh the page and try again.");
    }
    if (response.status === 429) {
      throw new Error("Too many Hermes sign-in attempts. Wait a minute, then try again.");
    }
    if (!response.ok) throw new Error(SIGN_IN_FAILED);
    const result: unknown = await response.json().catch(() => null);
    signal.throwIfAborted();
    if (result === null || typeof result !== "object" || !("ok" in result) || result.ok !== true) {
      throw new Error(SIGN_IN_FAILED);
    }
    // The destination is the verified Agent UI, not a redirect from the reply.
    return target.href;
  } catch (error) {
    // apiFetch translates network errors into session expiry, including aborts.
    signal.throwIfAborted();
    throw error;
  }
}
