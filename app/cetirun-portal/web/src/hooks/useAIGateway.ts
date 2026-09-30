// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  cancelCopilotLogin,
  getAIGatewayCatalog,
  pollCopilotLogin,
  startCopilotLogin,
  type CopilotLogin,
} from "../api/aiGateway";

export function useAIGatewayCatalog(enabled: boolean) {
  return useQuery({
    queryKey: ["ai-gateway", "catalog"],
    queryFn: getAIGatewayCatalog,
    enabled,
    staleTime: 60_000,
  });
}

const MIN_POLL_SECONDS = 5;

/**
 * Runs the Portal's GitHub Copilot sign-in for an Agent: start, poll at the
 * interval GitHub asks for, cancel. The Portal backend keeps the sign-in, so
 * a reload can resume it by polling.
 */
export function useCopilotGatewayLogin(instanceName: string) {
  const queryClient = useQueryClient();
  const [login, setLogin] = useState<CopilotLogin | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const active = useRef(true);

  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      clearTimeout(timer.current);
    };
  }, []);

  const schedule = useCallback(
    (current: CopilotLogin) => {
      clearTimeout(timer.current);
      if (current.state !== "pending") {
        if (current.state === "succeeded") {
          void queryClient.invalidateQueries({ queryKey: ["instance", instanceName] });
        }
        return;
      }
      const seconds = Math.max(current.interval ?? MIN_POLL_SECONDS, MIN_POLL_SECONDS);
      timer.current = setTimeout(async () => {
        try {
          const next = await pollCopilotLogin(instanceName);
          if (!active.current) return;
          setLogin(next);
          setError(null);
          schedule(next);
        } catch (err) {
          if (!active.current) return;
          setError(err instanceof Error ? err.message : String(err));
          // Keep trying while GitHub still accepts the code.
          schedule(current);
        }
      }, seconds * 1000);
    },
    [instanceName, queryClient],
  );

  const start = useCallback(async () => {
    setBusy(true);
    setError(null);
    try {
      const started = await startCopilotLogin(instanceName);
      if (!active.current) return;
      setLogin(started);
      schedule(started);
    } catch (err) {
      if (active.current) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (active.current) setBusy(false);
    }
  }, [instanceName, schedule]);

  const cancel = useCallback(async () => {
    clearTimeout(timer.current);
    setLogin(null);
    setError(null);
    try {
      await cancelCopilotLogin(instanceName);
    } catch (err) {
      if (active.current) setError(err instanceof Error ? err.message : String(err));
    }
  }, [instanceName]);

  const reset = useCallback(() => {
    clearTimeout(timer.current);
    setLogin(null);
    setError(null);
  }, []);

  return { login, error, busy, start, cancel, reset };
}
