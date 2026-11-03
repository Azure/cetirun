// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { Badge, Text, tokens } from "@fluentui/react-components";
import type { TeamSkill, TeamSkillState } from "../types/instance";
import { shortSkillVersion } from "../utils/skillMarketplace";

const STATES: Record<TeamSkillState, { label: string; color: "success" | "informative" | "warning" | "danger"; hint?: string }> = {
  published: { label: "Published", color: "success" },
  pending: { label: "Pending", color: "informative", hint: "Being published for the Team's current selection. The Agent gets it at its next start." },
  retained: { label: "Retained", color: "warning", hint: "No longer valid in the skills repository. The Agent keeps the last published version." },
  missing: { label: "Missing", color: "danger", hint: "Not published from the skills repository, so the Agent does not have it." },
};

/** The Skill Marketplace skills the Agent's Team selects, with their publication state. Renders nothing without any. */
export function TeamSkillsBlock({ skills }: { skills?: TeamSkill[] }) {
  if (!skills?.length) return null;
  return (
    <section
      aria-label="Team skills"
      style={{
        marginTop: 8,
        marginBottom: 8,
        padding: 12,
        borderRadius: tokens.borderRadiusMedium,
        border: `1px solid ${tokens.colorNeutralStroke1}`,
        display: "flex",
        flexDirection: "column",
        gap: 8,
      }}
    >
      <Text weight="semibold">Team skills</Text>
      <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>
        Chosen by your Team's administrator from the Skill Marketplace. Changes take effect when the Agent next starts.
      </Text>
      <ul style={{ listStyle: "none", margin: 0, padding: 0, display: "flex", flexDirection: "column", gap: 8 }}>
        {skills.map((skill) => {
          const state = STATES[skill.state] ?? { label: skill.state, color: "informative" as const };
          return (
            <li key={skill.name} style={{ display: "flex", flexDirection: "column", gap: 2, overflowWrap: "anywhere" }}>
              <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: 8 }}>
                <Text weight="semibold">{skill.name}</Text>
                <Badge appearance="tint" color={state.color}>{state.label}</Badge>
                {skill.version && (
                  <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>Version {shortSkillVersion(skill.version)}</Text>
                )}
              </div>
              {skill.description && <Text size={200}>{skill.description}</Text>}
              {state.hint && <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>{state.hint}</Text>}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
