// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { endAgentAuthorization, getAgentAuthorization, startAgentAuthorization } from "../api/agentAuthorization";

export function useAgentAuthorization(name: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const queryKey = ["agent-authorization", name] as const;
  const status = useQuery({
    queryKey,
    queryFn: ({ signal }) => getAgentAuthorization(name, signal),
    enabled,
    refetchInterval: (query) => query.state.data?.status === "connecting" ? 2_000 : 30_000,
  });
  const start = useMutation({
    mutationFn: () => startAgentAuthorization(name),
    onSuccess: () => queryClient.invalidateQueries({ queryKey }),
  });
  const end = useMutation({
    mutationFn: () => endAgentAuthorization(name),
    onSuccess: () => queryClient.invalidateQueries({ queryKey }),
  });
  return { status, start, end };
}
