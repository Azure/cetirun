// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { useQuery } from "@tanstack/react-query";
import type { TeamSummary } from "../types/instance";

export const GITHUB_COPILOT_PROVIDER = "github_copilot";

async function fetchTeams(): Promise<TeamSummary[]> {
  const res = await fetch("/api/v1/teams");
  if (!res.ok) throw new Error(`Failed to fetch teams: ${res.status}`);
  return res.json();
}

export function useTeams() {
  return useQuery({
    queryKey: ["teams"],
    queryFn: fetchTeams,
    staleTime: 30_000,
  });
}

// A missing team (or a team without an explicit provider) follows the
// operator's GitHub Copilot default. Keep this aligned with buildHelmValues.
export function useEffectiveTeamProvider(teamName: string): string {
  const { data: teams = [] } = useTeams();
  const provider = teams.find((team) => team.name === teamName)?.provider?.trim();
  return provider || GITHUB_COPILOT_PROVIDER;
}
