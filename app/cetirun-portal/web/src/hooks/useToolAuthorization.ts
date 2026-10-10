// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createToolGrant,
  deleteConnection,
  deleteToolGrant,
  listConnections,
  listToolGrants,
  reconnectConnection,
  startConnection,
  updateToolGrant,
  type ConnectionProvider,
  type ToolGrantInput,
} from "../api/toolAuthorization";

export function useToolConnections(enabled: boolean) {
  const queryClient = useQueryClient();
  const queryKey = ["tool-connections"] as const;
  const connections = useQuery({ queryKey, queryFn: ({ signal }) => listConnections(signal), enabled });
  const refresh = () => queryClient.invalidateQueries({ queryKey });
  return {
    connections,
    start: useMutation({ mutationFn: (provider: ConnectionProvider) => startConnection(provider), onSuccess: refresh }),
    reconnect: useMutation({ mutationFn: reconnectConnection, onSuccess: refresh }),
    remove: useMutation({ mutationFn: deleteConnection, onSuccess: refresh }),
  };
}

export function useToolGrants(name: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const queryKey = ["tool-grants", name] as const;
  const grants = useQuery({ queryKey, queryFn: ({ signal }) => listToolGrants(name, signal), enabled });
  const refresh = () => queryClient.invalidateQueries({ queryKey });
  return {
    grants,
    create: useMutation({ mutationFn: (input: ToolGrantInput) => createToolGrant(name, input), onSuccess: refresh }),
    update: useMutation({ mutationFn: ({ id, input }: { id: string; input: ToolGrantInput & { revision: number } }) => updateToolGrant(name, id, input), onSuccess: refresh }),
    remove: useMutation({ mutationFn: ({ id, revision }: { id: string; revision: number }) => deleteToolGrant(name, id, revision), onSuccess: refresh }),
  };
}
