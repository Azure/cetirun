import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  getInstance,
  createInstance,
  deleteInstanceByName,
  updateInstance,
  updateInstanceByName,
  upgradeInstance,
  upgradeInstanceByName,
  getInstanceStatus,
  getInstanceStatusByName,
  fetchInstances,
  fetchInstanceByName,
} from "../api/instance";
import type { CreateInstanceRequest, UpdateInstanceRequest } from "../types/instance";

const INSTANCE_KEY = ["instance"] as const;

export function useInstanceQuery() {
  return useQuery({
    queryKey: INSTANCE_KEY,
    queryFn: () => getInstance(),
    refetchInterval: (query) => {
      // Stop polling when the session has expired to avoid wasting resources.
      if (query.state.error?.message.includes("Session expired")) return false;

      const data = query.state.data;
      if (
        data &&
        (data.phase === "Provisioning" ||
          data.phase === "Pending" ||
          data.phase === "Deleting")
      ) {
        return 5_000;
      }
      return 30_000;
    },
    retry: (failureCount, error) => {
      // Don't retry on session expiry - it will just keep failing.
      if (error.message.includes("Session expired")) return false;
      return failureCount < 1;
    },
  });
}

export function useCreateInstance(options?: { onSuccess?: () => void }) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (req: CreateInstanceRequest) => createInstance(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: INSTANCE_KEY });
      queryClient.invalidateQueries({ queryKey: ["instances"] });
      options?.onSuccess?.();
    },
  });
}

export function useDeleteInstanceByName(name: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => deleteInstanceByName(name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: INSTANCE_KEY });
      queryClient.invalidateQueries({ queryKey: ["instance", name] });
      queryClient.invalidateQueries({ queryKey: ["instances"] });
    },
  });
}

export function useUpdateInstance() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (req: UpdateInstanceRequest) => updateInstance(req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: INSTANCE_KEY });
    },
  });
}

export function useUpdateInstanceByName(name: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (req: UpdateInstanceRequest) => updateInstanceByName(name, req),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["instance", name] });
      queryClient.invalidateQueries({ queryKey: ["instances"] });
    },
  });
}

export function useUpgradeInstance() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => upgradeInstance(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: INSTANCE_KEY });
    },
  });
}

export function useUpgradeInstanceByName(name: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: () => upgradeInstanceByName(name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["instance", name] });
      queryClient.invalidateQueries({ queryKey: ["instance", name, "status"] });
      queryClient.invalidateQueries({ queryKey: ["instances"] });
    },
  });
}

export function useInstanceStatusQuery(enabled: boolean) {
  return useQuery({
    queryKey: ["instance", "status"],
    queryFn: () => getInstanceStatus(),
    enabled,
    refetchInterval: (query) => {
      if (query.state.error?.message.includes("Session expired")) return false;
      return 10_000;
    },
    refetchIntervalInBackground: false,
    retry: (failureCount, error) => {
      if (error.message.includes("Session expired")) return false;
      return failureCount < 1;
    },
  });
}

export function useInstanceStatusByNameQuery(name: string, enabled: boolean) {
  return useQuery({
    queryKey: ["instance", name, "status"],
    queryFn: () => getInstanceStatusByName(name),
    enabled,
    refetchInterval: (query) => {
      if (query.state.error?.message.includes("Session expired")) return false;
      return 10_000;
    },
    refetchIntervalInBackground: false,
    retry: (failureCount, error) => {
      if (error.message.includes("Session expired")) return false;
      return failureCount < 1;
    },
  });
}

export function useInstancesQuery() {
  return useQuery({
    queryKey: ["instances"],
    queryFn: fetchInstances,
  });
}

export function useInstanceByNameQuery(name: string | undefined) {
  return useQuery({
    queryKey: ["instance", name],
    queryFn: () => fetchInstanceByName(name!),
    enabled: !!name,
  });
}
