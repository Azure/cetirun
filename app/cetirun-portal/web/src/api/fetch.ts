// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Shared fetch helper used by all portal API clients.
//
// OAuth2 Proxy handles authentication at the gateway level. No Authorization
// header is needed — requests are already authenticated. This helper only
// detects the failure modes that mean "your session is gone, refresh the page":
//
//   1. The response is 401. OAuth2 Proxy answers API requests (/api/) this way
//      when the session has expired, instead of starting a sign-in; the Portal
//      backend does the same when the identity it receives is missing.
//   2. fetch() throws TypeError because OAuth2 Proxy redirected to the IdP
//      sign-in page on a different origin and the browser blocked it (CORS).
//   3. The response is an HTML page (the OAuth2 Proxy sign-in interstitial)
//      instead of the JSON we expected.
export async function apiFetch(path: string, options: RequestInit = {}): Promise<Response> {
  let response: Response;
  try {
    response = await fetch(path, {
      ...options,
      headers: {
        ...options.headers,
        "Content-Type": "application/json",
      },
    });
  } catch {
    throw new Error("Session expired, please refresh the page");
  }

  const ct = response.headers.get("content-type") || "";
  if (response.status === 401 || response.redirected || ct.includes("text/html")) {
    throw new Error("Session expired, please refresh the page");
  }

  return response;
}
