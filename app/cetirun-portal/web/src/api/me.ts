// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { apiFetch } from "./fetch";

export interface Me {
  alias: string;
  displayName: string;
  email: string;
  isAdmin: boolean;
}

export async function fetchMe(): Promise<Me> {
  const response = await apiFetch("/api/v1/me");
  if (!response.ok) {
    throw new Error(`Failed to fetch me: ${response.statusText}`);
  }
  return response.json();
}
