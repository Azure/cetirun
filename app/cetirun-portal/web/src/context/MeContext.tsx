import { createContext, useContext, type ReactNode } from "react";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { fetchMe, type Me } from "../api/me";

interface MeContextValue {
  me: Me | undefined;
  isLoading: boolean;
  error: unknown;
}

const MeContext = createContext<MeContextValue | undefined>(undefined);

export function MeProvider({ children }: { children: ReactNode }) {
  const query: UseQueryResult<Me, Error> = useQuery({
    queryKey: ["me"],
    queryFn: fetchMe,
    staleTime: 60_000,
  });
  return (
    <MeContext.Provider value={{ me: query.data, isLoading: query.isLoading, error: query.error }}>
      {children}
    </MeContext.Provider>
  );
}

export function useMe(): MeContextValue {
  const ctx = useContext(MeContext);
  if (!ctx) throw new Error("useMe must be used within MeProvider");
  return ctx;
}
