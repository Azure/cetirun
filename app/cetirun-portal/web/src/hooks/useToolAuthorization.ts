// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createConnectionBinding,
  createToolIdentity,
  deleteConnectionBinding,
  listConnections,
  listConnectionBindings,
  reconnectConnection,
  startConnection,
  updateConnectionBinding,
  type ConnectionProvider,
  type ToolIdentityInput,
  type ConnectionBindingInput,
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
  };
}

export function useConnectionBindings(name: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const queryKey = ["connection-bindings", name] as const;
  const bindings = useQuery({ queryKey, queryFn: ({ signal }) => listConnectionBindings(name, signal), enabled });
  const refresh = async () => {
    await Promise.all([queryClient.invalidateQueries({ queryKey }), queryClient.invalidateQueries({ queryKey: ["connection-usage"] })]);
  };
  return {
    bindings,
    create: useMutation({ mutationFn: (input: ConnectionBindingInput) => createConnectionBinding(name, input), onSuccess: refresh }),
    update: useMutation({ mutationFn: ({ id, input }: { id: string; input: ConnectionBindingInput & { revision: number } }) => updateConnectionBinding(name, id, input), onSuccess: refresh }),
    remove: useMutation({ mutationFn: ({ id, revision }: { id: string; revision: number }) => deleteConnectionBinding(name, id, revision), onSuccess: refresh }),
  };
}

export function useToolIdentity(name: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ToolIdentityInput) => createToolIdentity(name, input),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["tool-connections"] }),
  });
}
