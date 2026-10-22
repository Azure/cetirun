// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createConnectionBinding,
  createToolIdentity,
  deleteConnectionBinding,
  listConnectionBindings,
  listConnections,
} from "../api/toolAuthorization";

/**
 * The Agent UID is part of every key. An Agent name may be deleted then reused,
 * and its replacement must never receive the previous Agent's cached state.
 */
export function useAzureToolAuthorization(instanceName: string, instanceUid: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const connectionsKey = ["azure-tool-connections", instanceUid] as const;
  const bindingsKey = ["azure-connection-bindings", instanceUid] as const;
  const connections = useQuery({
    queryKey: connectionsKey,
    queryFn: ({ signal }) => listConnections(signal),
    enabled,
  });
  const bindings = useQuery({
    queryKey: bindingsKey,
    queryFn: ({ signal }) => listConnectionBindings(instanceName, signal),
    enabled,
  });
  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: connectionsKey }),
      queryClient.invalidateQueries({ queryKey: bindingsKey }),
    ]);
  };

  return {
    connections,
    bindings,
    connect: useMutation({
      mutationFn: async () => {
        const identity = await createToolIdentity(instanceName);
        return createConnectionBinding(instanceName, identity.id);
      },
      // Refresh even when identity creation succeeded but binding did not.
      onSettled: refresh,
    }),
    disconnect: useMutation({
      mutationFn: ({ id, revision }: { id: string; revision: number }) => deleteConnectionBinding(instanceName, id, revision),
      onSettled: refresh,
    }),
    refresh,
  };
}
