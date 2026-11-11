// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { InMemoryCredentialStore } from "@earendil-works/pi-ai";
import { streamSimple } from "@earendil-works/pi-ai/compat";
import { estimateContextTokens } from "@earendil-works/pi-ai/utils/estimate";
import { clampMaxTokensToContext } from "@earendil-works/pi-ai/api/simple-options";
import { randomUUID } from "node:crypto";
import { AsyncLocalStorage } from "node:async_hooks";
import { RunError } from "./run-error.mjs";

export const GATEWAY_PROVIDER = "cetirun-gateway";
const MODEL_LIMITS_ENV = "CETIRUN_GATEWAY_MODEL_LIMITS";
const MODEL_LIMITS_VERSION = 1;
const MAX_MODELS = 256;
const MAX_MODEL_ID_BYTES = 256;
const MAX_LIMITS_BYTES = 64 * 1024;
const MAX_SAFE_TOKENS = Number.MAX_SAFE_INTEGER;
const DEFAULT_MAX_TOKENS = 2048;
const CONTEXT_SAFETY_TOKENS = 4096;
const budgetState = new AsyncLocalStorage();

export async function withGatewayBudget(callback) {
  const state = { error: undefined };
  return budgetState.run(state, () => callback(state));
}

function positiveSafeInteger(value) {
  return Number.isSafeInteger(value) && value > 0 && value <= MAX_SAFE_TOKENS;
}

/** Platform-owned limits; malformed input is unavailable, never a fallback. */
export function gatewayModelLimits(env = process.env) {
  const raw = env[MODEL_LIMITS_ENV];
  if (typeof raw !== "string" || !raw || Buffer.byteLength(raw) > MAX_LIMITS_BYTES) return new Map();
  let value;
  try { value = JSON.parse(raw); } catch { return new Map(); }
  if (!value || typeof value !== "object" || Array.isArray(value) || value.version !== MODEL_LIMITS_VERSION ||
    Object.keys(value).some(key => !["version", "models"].includes(key)) ||
    !Array.isArray(value.models) || value.models.length > MAX_MODELS) return new Map();
  const limits = new Map();
  for (const entry of value.models) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry) ||
      Object.keys(entry).some(key => !["id", "contextWindow", "maxOutputTokens"].includes(key)) ||
      typeof entry.id !== "string" || !entry.id || entry.id.trim() !== entry.id || Buffer.byteLength(entry.id) > MAX_MODEL_ID_BYTES || /\p{Cc}/u.test(entry.id) ||
      !positiveSafeInteger(entry.contextWindow) || !positiveSafeInteger(entry.maxOutputTokens) || limits.has(entry.id)) return new Map();
    limits.set(entry.id, { contextWindow: entry.contextWindow, maxTokens: entry.maxOutputTokens });
  }
  return limits;
}

export function gatewayConfiguration(env = process.env) {
  const base = env.OPENAI_BASE_URL;
  const key = env.OPENAI_API_KEY;
  if (!base && !key) return undefined;
  if (!base || !key || /[\r\n]/.test(key)) throw new Error("Both Gateway environment variables are required");
  const url = new URL(base);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash ||
    !/^\/v1\/?$/.test(url.pathname)) throw new Error("Invalid Gateway API base");
  return { baseURL: url.href.replace(/\/$/, ""), apiKey: key, modelLimits: gatewayModelLimits(env) };
}

export function gatewayCredentials() {
  return new InMemoryCredentialStore();
}

// No arbitrary provider URLs, persistent credentials or model-response logging.
export function gatewayAccess(modelRuntime, config) {
  const allowed = new URL(config.baseURL);
  const validateRequest = (input) => {
    const url = new URL(input instanceof Request ? input.url : input);
    if (url.origin !== allowed.origin || !["/v1/models", "/v1/chat/completions"].includes(url.pathname) || url.search) {
      throw new Error("Gateway request left the approved API");
    }
  };
  const boundedFetch = (input, init = {}) => {
    validateRequest(input);
    return fetch(input, { ...init, redirect: "error" });
  };
  const register = (ids) => modelRuntime.registerProvider(GATEWAY_PROVIDER, {
    name: "CetiRun AI Gateway", baseUrl: config.baseURL, api: "openai-completions",
    models: ids.map((id) => {
      // An unavailable limit remains discoverable, but it can never dispatch:
      // gatewayStream rejects before the SDK serializes a completion request.
      const limits = config.modelLimits.get(id);
      return {
      id, name: id, reasoning: false, input: ["text"],
      contextWindow: limits?.contextWindow ?? 1, maxTokens: Math.min(DEFAULT_MAX_TOKENS, limits?.maxTokens ?? 1),
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      compat: { supportsStore: false },
      };
    }),
    streamSimple: (model, context, options) => gatewayStream(model, context, options, config, boundedFetch, validateRequest),
  });
  register([]);
  return {
    async models(signal) {
      const response = await boundedFetch(config.baseURL + "/models", {
        headers: { Authorization: "Bearer " + config.apiKey, Accept: "application/json" }, signal,
      });
      if (!response.ok) {
        await response.body?.cancel();
        throw new Error("Gateway model discovery is unavailable");
      }
      const reader = response.body.getReader();
      const chunks = [];
      let bytes = 0;
      try {
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          bytes += value.byteLength;
          if (bytes > 256 * 1024) throw new Error("Gateway catalog is too large");
          chunks.push(value);
        }
      } finally {
        await reader.cancel();
      }
      const catalog = JSON.parse(Buffer.concat(chunks).toString("utf8"));
      if (!Array.isArray(catalog.data) || catalog.data.length > MAX_MODELS) throw new Error("Invalid Gateway catalog");
      let ids = catalog.data.map((model) => model?.id);
      if (ids.some((id) => typeof id !== "string" || !id.trim() || Buffer.byteLength(id) > MAX_MODEL_ID_BYTES || /[\x00-\x1f\x7f]/.test(id)) || new Set(ids).size !== ids.length) {
        throw new Error("Invalid Gateway model identifier");
      }
      // LiteLLM expands populated access groups, but lists a grant literally
      // while its group has no deployments. Those grants are not model IDs.
      ids = ids.filter((id) => id !== "cetirun-shared" &&
        !/^cetirun-copilot-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id));
      register(ids);
      await modelRuntime.setRuntimeApiKey(GATEWAY_PROVIDER, config.apiKey, { signal });
      return modelRuntime.getAvailable(GATEWAY_PROVIDER, { signal });
    },
  };
}

function gatewayStream(model, context, options, config, boundedFetch, validateRequest) {
  const callID = randomUUID();
  const diagnostic = (state, fields = {}) => {
    const record = { log_type: "cetirun.pi.gateway_budget", call_id: callID, state, ...fields };
    // The structured record deliberately has only fixed state/reason strings,
    // numeric budgets/status, and correlation IDs. It never includes a prompt,
    // tool argument, API key, URL, or model response.
    console.info(JSON.stringify(record));
    if (config.diagnostics) {
      config.diagnostics.push(record);
      if (config.diagnostics.length > 128) config.diagnostics.shift();
    }
  };
  const limits = config.modelLimits.get(model.id);
  if (!limits) {
    diagnostic("rejected", { reason: "model_limits_unavailable" });
    throw guardedFailure("model_limits_unavailable");
  }
  const requested = Math.min(options?.maxTokens ?? model.maxTokens, limits.maxTokens, DEFAULT_MAX_TOKENS);
  const estimate = estimateContextTokens(context).tokens;
  if (!positiveSafeInteger(requested) || requested === 1 || !Number.isSafeInteger(estimate) || estimate < 0 ||
    estimate + CONTEXT_SAFETY_TOKENS + requested > limits.contextWindow) {
    diagnostic("rejected", { reason: "context_budget_exceeded", estimate, contextWindow: limits.contextWindow, requested });
    throw guardedFailure("context_budget_exceeded");
  }
  const effective = clampMaxTokensToContext({ ...model, contextWindow: limits.contextWindow }, context, requested);
  if (effective !== requested) {
    diagnostic("rejected", { reason: "context_budget_exceeded", estimate, contextWindow: limits.contextWindow, requested, effective });
    throw guardedFailure("context_budget_exceeded");
  }
  let wire;
  const auditedFetch = async (input, init) => {
    const request = await gatewayCompletionRequest(input, init, wire);
    validateRequest(request);
    request.signal.throwIfAborted();
    diagnostic("sent", { estimate, contextWindow: limits.contextWindow, requested, effective, wire });
    return boundedFetch(request);
  };
  return streamSimple({ ...model, contextWindow: limits.contextWindow, maxTokens: Math.min(limits.maxTokens, DEFAULT_MAX_TOKENS) }, context, {
    ...options,
    maxTokens: requested,
    apiKey: config.apiKey,
    maxRetries: 0,
    fetch: auditedFetch,
    onPayload: async (params, payloadModel) => {
      const next = await options?.onPayload?.(params, payloadModel) ?? params;
      const completion = next.max_completion_tokens;
      const legacy = next.max_tokens;
      if (completion !== undefined && legacy !== undefined && completion !== legacy) throw guardedFailure("context_budget_exceeded");
      wire = completion ?? legacy;
      if (!positiveSafeInteger(wire) || wire !== requested) throw guardedFailure("context_budget_exceeded");
      return next;
    },
    onResponse: async (response, responseModel) => {
      await options?.onResponse?.(response, responseModel);
      const requestID = response.headers?.["x-request-id"] ?? response.headers?.["request-id"];
      const litellmCallID = response.headers?.["x-litellm-call-id"];
      diagnostic("response_headers", { status: response.status,
        ...(safeIdentifier(requestID) ? { gateway_request_id: requestID } : {}),
        ...(safeIdentifier(litellmCallID) ? { litellm_call_id: litellmCallID } : {}) });
    },
  });
}

function guardedFailure(code) {
  const state = budgetState.getStore();
  if (state) state.error = code;
  return new RunError(code);
}

// Inspect a clone of the exact request that will be sent. Reading a Request
// constructed from the caller's original body would otherwise consume it.
export async function gatewayCompletionRequest(input, init, expectedCap) {
  const request = new Request(input instanceof Request ? input.clone() : input, init);
  const cap = await serializedCap(request);
  if (!positiveSafeInteger(cap) || cap !== expectedCap) throw guardedFailure("context_budget_exceeded");
  return request;
}

async function serializedCap(request) {
  let raw;
  try {
    raw = await request.clone().text();
  } catch { return undefined; }
  try {
    const body = JSON.parse(raw);
    const completion = body?.max_completion_tokens;
    const legacy = body?.max_tokens;
    return completion !== undefined && legacy !== undefined && completion !== legacy ? undefined : completion ?? legacy;
  } catch { return undefined; }
}

function safeIdentifier(value) {
  return typeof value === "string" && value.length > 0 && value.length <= 1024 && !/[\x00-\x1f\x7f]/.test(value);
}
