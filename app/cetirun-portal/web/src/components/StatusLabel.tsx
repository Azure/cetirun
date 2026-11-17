// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { ReactNode } from "react";

export type StatusTone = "success" | "warning" | "danger" | "informative" | "subtle";

/** A state as a colored lamp and its name. Color is never the only signal. */
export function StatusLabel({ tone, size, children }: { tone: StatusTone; size?: "small"; children: ReactNode }) {
  return (
    <span className={`status-label status-label--${tone}${size === "small" ? " status-label--small" : ""}`}>
      {children}
    </span>
  );
}
