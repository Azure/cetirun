/**
 * Client for the in-pod cetirun-auth helper service.
 *
 * Frontend talks directly to the per-instance pod (Hermes or OpenClaw) via the
 * Istio-routed prefix ``${baseUrl}/cetirun-auth/...``. Authentication uses the
 * dedicated ``CETIRUN_AUTH_KEY`` bearer token (surfaced as
 * ``cetirunAuthKey`` on the instance API response); the legacy
 * ``API_SERVER_KEY`` is still accepted server-side as a fallback for older
 * Hermes deployments.
 */

export interface CopilotAuthStatus {
  authenticated: boolean;
  provider: string;
  model: string;
  /** Server-side seconds-since-epoch when the status was computed. */
  lastCheckedAt: number;
}

export interface CopilotModel {
  id: string;
  displayName: string;
  supportedEndpoints?: string[];
}

export interface CopilotModelsResponse {
  models: CopilotModel[];
  source: "github-copilot";
  cached: boolean;
}

export interface LoginStartResponse {
  sessionId: string;
  userCode: string;
  verificationUri: string;
  expiresIn: number;
  interval: number;
}

export type LoginState =
  | "pending"
  | "authenticated"
  | "expired"
  | "cancelled"
  | "error";

export interface LoginStatusResponse {
  state: LoginState;
  message?: string;
}

function authUrl(baseUrl: string, path: string): string {
  return `${baseUrl.replace(/\/$/, "")}/cetirun-auth${path}`;
}

function authHeaders(apiKey: string): HeadersInit {
  return {
    Authorization: `Bearer ${apiKey}`,
    Accept: "application/json",
  };
}

async function readError(res: Response): Promise<string> {
  try {
    const body = await res.text();
    if (body) {
      try {
        const parsed = JSON.parse(body) as { error?: string | { message?: string } };
        if (typeof parsed?.error === "string") return parsed.error;
        if (parsed?.error?.message) return parsed.error.message;
      } catch {
        /* not JSON */
      }
      return body.slice(0, 200);
    }
  } catch {
    /* ignore */
  }
  return `HTTP ${res.status}`;
}

export async function fetchCopilotAuthStatus(
  baseUrl: string,
  apiKey: string,
  signal?: AbortSignal,
): Promise<CopilotAuthStatus> {
  const res = await fetch(authUrl(baseUrl, "/v1/status"), {
    method: "GET",
    headers: authHeaders(apiKey),
    signal,
  });
  if (!res.ok) {
    throw new Error(`Failed to load auth status: ${await readError(res)}`);
  }
  return (await res.json()) as CopilotAuthStatus;
}

export async function fetchCopilotModels(
  baseUrl: string,
  apiKey: string,
  signal?: AbortSignal,
): Promise<CopilotModelsResponse> {
  const res = await fetch(authUrl(baseUrl, "/v1/models"), {
    method: "GET",
    headers: authHeaders(apiKey),
    signal,
  });
  if (!res.ok) {
    throw new Error(`Failed to load GitHub Copilot models: ${await readError(res)}`);
  }
  return (await res.json()) as CopilotModelsResponse;
}

export async function startCopilotLogin(
  baseUrl: string,
  apiKey: string,
  signal?: AbortSignal,
): Promise<LoginStartResponse> {
  const res = await fetch(authUrl(baseUrl, "/v1/login/start"), {
    method: "POST",
    headers: {
      ...authHeaders(apiKey),
      "Content-Type": "application/json",
    },
    body: "{}",
    signal,
  });
  if (!res.ok) {
    throw new Error(`Failed to start login: ${await readError(res)}`);
  }
  return (await res.json()) as LoginStartResponse;
}

export async function pollCopilotLoginStatus(
  baseUrl: string,
  apiKey: string,
  sessionId: string,
  signal?: AbortSignal,
): Promise<LoginStatusResponse> {
  const url =
    authUrl(baseUrl, "/v1/login/status") +
    `?session_id=${encodeURIComponent(sessionId)}`;
  const res = await fetch(url, {
    method: "GET",
    headers: authHeaders(apiKey),
    signal,
  });
  if (!res.ok) {
    throw new Error(`Failed to check login status: ${await readError(res)}`);
  }
  return (await res.json()) as LoginStatusResponse;
}

export async function cancelCopilotLogin(
  baseUrl: string,
  apiKey: string,
  sessionId: string,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(authUrl(baseUrl, "/v1/login/cancel"), {
    method: "POST",
    headers: {
      ...authHeaders(apiKey),
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ sessionId }),
    signal,
  });
  if (!res.ok && res.status !== 404) {
    throw new Error(`Failed to cancel login: ${await readError(res)}`);
  }
}
