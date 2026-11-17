// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import type { ReactNode } from "react";
import type { Instance } from "../types/instance";

/** How the lifecycle glyph draws an Agent phase. */
export type LifecycleState = "ready" | "busy" | "waiting" | "suspended" | "failed" | "deleting" | "unknown";

const STATES = new Map<string, LifecycleState>(Object.entries({
  Ready: "ready",
  Provisioning: "busy",
  Pending: "waiting",
  Suspended: "suspended",
  Failed: "failed",
  Deleting: "deleting",
} satisfies Record<Instance["phase"], LifecycleState>));

export function lifecycleState(phase?: string): LifecycleState {
  return STATES.get(phase ?? "") ?? "unknown";
}

const ring = <circle cx="8" cy="8" r="6.25" stroke="currentColor" strokeWidth="1.5" />;
// Eight even dashes around the same ring.
const dashedRing = (
  <circle cx="8" cy="8" r="6.25" stroke="currentColor" strokeWidth="1.5" strokeDasharray="2.6 2.309" transform="rotate(-90 8 8)" />
);
const mark = (d: string) => <path d={d} stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />;

const SHAPES: Record<LifecycleState, ReactNode> = {
  ready: <>{ring}<circle cx="8" cy="8" r="3" fill="currentColor" /></>,
  busy: <>
    <circle cx="8" cy="8" r="6.25" stroke="currentColor" strokeOpacity="0.28" strokeWidth="1.5" />
    <path className="lifecycle-arc" d="M8 1.75a6.25 6.25 0 0 1 6.25 6.25" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
  </>,
  waiting: dashedRing,
  suspended: <>{ring}{mark("M6.5 5.75v4.5M9.5 5.75v4.5")}</>,
  failed: <>{ring}{mark("M5.9 5.9l4.2 4.2M10.1 5.9l-4.2 4.2")}</>,
  deleting: <>{dashedRing}{mark("M5.5 8h5")}</>,
  unknown: ring,
};

/**
 * The state of an Agent as a shape: a dashed ring while it waits, a turning
 * arc while it starts, a filled ring when it is ready. The glyph is
 * decorative; the phase is always written next to it.
 */
export function LifecycleGlyph({ state, size }: { state: LifecycleState; size: number }) {
  return (
    <svg className={`lifecycle lifecycle--${state}`} width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true" focusable="false">
      {SHAPES[state]}
    </svg>
  );
}

/** An Agent phase: the lifecycle glyph and the phase as the API reports it. */
export function AgentPhase({ phase, state = lifecycleState(phase), size }: {
  phase: string;
  /** Overrides the glyph when the phase alone does not tell the state. */
  state?: LifecycleState;
  size?: "small";
}) {
  return (
    <span className={size === "small" ? "agent-phase agent-phase--small" : "agent-phase"}>
      <LifecycleGlyph state={state} size={size === "small" ? 14 : 16} />
      {phase}
    </span>
  );
}
