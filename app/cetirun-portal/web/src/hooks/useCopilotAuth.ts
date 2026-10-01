// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  cancelCopilotLogin,
  fetchCopilotAuthStatus,
  fetchCopilotModels,
  type CopilotModelsResponse,
  pollCopilotLoginStatus,
  startCopilotLogin,
  type CopilotAuthStatus,
  type LoginStartResponse,
  type LoginState,
} from "../api/copilotAuth";
import type { Instance } from "../types/instance";
import { instanceGatewayUrl } from "../utils/instance";

/**
 * Lower-bound interval (ms) for status polling. Server caches for 30s, so
 * faster polling just hits the cache; we still refresh frequently while a
 * login is in flight so the badge flips to Authenticated quickly.
 */
const STATUS_POLL_MS = 60_000;
const ACTIVE_LOGIN_POLL_MS = 3_000;

/**
 * Returns true iff the auth UI should be shown for this instance.
 * Both Hermes and OpenClaw expose the in-pod cetirun-auth helper service;
 * the UI requires the pod to be ready and the bearer key to be available.
 */
export function isCopilotAuthSupported(instance: Instance | undefined): boolean {
  if (!instance) return false;
  if (instance.agent.type !== "hermes" && instance.agent.type !== "openclaw") return false;
  if (instance.phase !== "Ready") return false;
  if (!instance.cetirunAuthKey) return false;
  if (!instanceGatewayUrl(instance)) return false;
  return true;
}

export function useCopilotAuthStatusQuery(instance: Instance | undefined) {
  const baseUrl = instance ? instanceGatewayUrl(instance) : undefined;
  const apiKey = instance?.cetirunAuthKey;
  const supported = isCopilotAuthSupported(instance);

  return useQuery<CopilotAuthStatus>({
    queryKey: ["copilotAuthStatus", instance?.instanceName],
    queryFn: ({ signal }) => fetchCopilotAuthStatus(baseUrl!, apiKey!, signal),
    enabled: supported,
    refetchInterval: STATUS_POLL_MS,
    refetchIntervalInBackground: false,
    retry: false,
    staleTime: 15_000,
  });
}

/** Loads the live catalog for this instance's authenticated Copilot account. */
export function useCopilotModelsQuery(instance: Instance | undefined) {
  const baseUrl = instance ? instanceGatewayUrl(instance) : undefined;
  const apiKey = instance?.cetirunAuthKey;
  const supported = isCopilotAuthSupported(instance);
  return useQuery<CopilotModelsResponse>({
    queryKey: ["copilotModels", instance?.instanceName],
    queryFn: ({ signal }) => fetchCopilotModels(baseUrl!, apiKey!, signal),
    enabled: supported,
    retry: false,
    staleTime: 5 * 60_000,
  });
}

type LoginPhase =
  | "idle"
  | "starting"
  | "awaiting_user"
  | "succeeded"
  | "cancelled"
  | "expired"
  | "error";

export interface CopilotLoginState {
  phase: LoginPhase;
  start: LoginStartResponse | null;
  errorMessage: string | null;
}

export interface UseCopilotLoginResult {
  state: CopilotLoginState;
  startLogin: () => Promise<void>;
  cancel: () => Promise<void>;
  reset: () => void;
}

/**
 * Drives a single GitHub Copilot device-code login flow.
 * Polls the in-pod service every few seconds while ``phase === awaiting_user``
 * and resolves to ``succeeded`` once the pod has the token.
 */
export function useCopilotLogin(instance: Instance | undefined): UseCopilotLoginResult {
  const queryClient = useQueryClient();
  const baseUrl = instance ? instanceGatewayUrl(instance) : undefined;
  const apiKey = instance?.cetirunAuthKey;

  const [state, setState] = useState<CopilotLoginState>({
    phase: "idle",
    start: null,
    errorMessage: null,
  });

  // Track the in-flight session so a re-render or unmount can cancel/poll it.
  const sessionIdRef = useRef<string | null>(null);
  const pollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const stopPolling = () => {
    if (pollTimerRef.current) {
      clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    if (abortRef.current) {
      abortRef.current.abort();
      abortRef.current = null;
    }
  };

  // Cancel any in-flight network work + timers on unmount.
  useEffect(() => {
    return () => {
      stopPolling();
    };
  }, []);

  const onTerminal = (newState: LoginState, message?: string) => {
    if (newState === "authenticated") {
      setState((s) => ({ ...s, phase: "succeeded", errorMessage: null }));
      // Surface the new auth state on the UI immediately rather than waiting
      // for the status query's normal refetch interval.
      void queryClient.invalidateQueries({
        queryKey: ["copilotAuthStatus", instance?.instanceName],
      });
      void queryClient.invalidateQueries({
        queryKey: ["copilotModels", instance?.instanceName],
      });
    } else if (newState === "cancelled") {
      setState((s) => ({ ...s, phase: "cancelled", errorMessage: null }));
    } else if (newState === "expired") {
      setState((s) => ({
        ...s,
        phase: "expired",
        errorMessage: message ?? "Device code expired. Please try again.",
      }));
    } else {
      setState((s) => ({
        ...s,
        phase: "error",
        errorMessage: message ?? "Authorization failed.",
      }));
    }
  };

  const scheduleNextPoll = (sessionId: string) => {
    pollTimerRef.current = setTimeout(async () => {
      pollTimerRef.current = null;
      if (sessionIdRef.current !== sessionId) return;
      if (!baseUrl || !apiKey) return;

      const controller = new AbortController();
      abortRef.current = controller;
      try {
        const res = await pollCopilotLoginStatus(
          baseUrl,
          apiKey,
          sessionId,
          controller.signal,
        );
        if (sessionIdRef.current !== sessionId) return;
        if (res.state === "pending") {
          scheduleNextPoll(sessionId);
        } else {
          sessionIdRef.current = null;
          onTerminal(res.state, res.message);
        }
      } catch (err) {
        if ((err as { name?: string }).name === "AbortError") return;
        if (sessionIdRef.current !== sessionId) return;
        // Transient network errors should not kill the flow — keep polling.
        scheduleNextPoll(sessionId);
      } finally {
        if (abortRef.current === controller) abortRef.current = null;
      }
    }, ACTIVE_LOGIN_POLL_MS);
  };

  const startLogin = async () => {
    if (!baseUrl || !apiKey) {
      setState({
        phase: "error",
        start: null,
        errorMessage: "Instance is not ready yet.",
      });
      return;
    }

    stopPolling();
    setState({ phase: "starting", start: null, errorMessage: null });

    try {
      const start = await startCopilotLogin(baseUrl, apiKey);
      sessionIdRef.current = start.sessionId;
      setState({ phase: "awaiting_user", start, errorMessage: null });
      scheduleNextPoll(start.sessionId);
    } catch (err) {
      sessionIdRef.current = null;
      setState({
        phase: "error",
        start: null,
        errorMessage: (err as Error).message,
      });
    }
  };

  const cancel = async () => {
    const sessionId = sessionIdRef.current;
    sessionIdRef.current = null;
    stopPolling();
    if (sessionId && baseUrl && apiKey) {
      try {
        await cancelCopilotLogin(baseUrl, apiKey, sessionId);
      } catch {
        /* best-effort */
      }
    }
    setState((s) => ({ ...s, phase: "cancelled", errorMessage: null }));
  };

  const reset = () => {
    sessionIdRef.current = null;
    stopPolling();
    setState({ phase: "idle", start: null, errorMessage: null });
  };

  return { state, startLogin, cancel, reset };
}
