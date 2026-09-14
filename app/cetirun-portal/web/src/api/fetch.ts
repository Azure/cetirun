// Shared fetch helper used by all portal API clients.
//
// OAuth2 Proxy handles authentication at the gateway level. No Authorization
// header is needed — requests are already authenticated. This helper only
// detects the two failure modes that look successful at the network layer
// but actually mean "your session is gone, refresh the page":
//
//   1. fetch() throws TypeError because OAuth2 Proxy redirected to the IdP
//      sign-in page on a different origin and the browser blocked it (CORS).
//   2. The response is an HTML page (the OAuth2 Proxy sign-in interstitial)
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
  if (response.redirected || ct.includes("text/html")) {
    throw new Error("Session expired, please refresh the page");
  }

  return response;
}
