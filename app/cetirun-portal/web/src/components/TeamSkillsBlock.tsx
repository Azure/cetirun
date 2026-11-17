// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { MessageBar, MessageBarBody, Text, tokens } from "@fluentui/react-components";
import type { Instance, TeamSkillState } from "../types/instance";
import { shortSkillVersion } from "../utils/skillMarketplace";
import { RestartAgentButton } from "./RestartAgentButton";
import { StatusLabel } from "./StatusLabel";

const STATES: Record<TeamSkillState, { label: string; color: "success" | "informative" | "warning" | "danger"; hint?: string }> = {
  published: { label: "Published", color: "success" },
  updating: {
    label: "Update pending",
    color: "informative",
    hint: "The skills repository has changed this skill. The Team's release does not have the change yet, so the Agent gets the version shown.",
  },
  pending: { label: "Pending", color: "informative", hint: "Not published yet for the Team's current selection. The Agent's next start waits for it." },
  retained: { label: "Retained", color: "warning", hint: "No longer valid in the skills repository. The Agent keeps the last published version." },
  missing: { label: "Missing", color: "danger", hint: "Not published from the skills repository, so the Agent does not have it." },
};

/** The Skill Marketplace skills the Agent's Team selects, with their publication state. Renders nothing without any. */
export function TeamSkillsBlock({ instance }: { instance: Instance }) {
  const skills = instance.teamSkills;
  if (!skills?.length) return null;
  return (
    <section aria-label="Team skills" className="portal-section">
      <h2 className="portal-section-title">Team skills</h2>
      <span className="portal-note">
        Chosen by your Team's administrator from the Skill Marketplace. Changes take effect when the Agent next starts.
      </span>
      {instance.teamSkillsPublicationFailed && (
        <MessageBar intent="warning">
          <MessageBarBody>
            The Skill Marketplace could not publish the Team's current skills. An administrator can see why on the Skills page.
          </MessageBarBody>
        </MessageBar>
      )}
      {instance.teamSkillsRestartNeeded && (
        <>
          <MessageBar intent="warning">
            <MessageBarBody>
              The Team's skills changed after this Agent started. The Agent uses the earlier versions until it restarts.
            </MessageBarBody>
          </MessageBar>
          {instance.restartAvailable && (
            <div style={{ display: "flex", flexWrap: "wrap", gap: 8 }}>
              <RestartAgentButton key={instance.uid} instance={instance} />
            </div>
          )}
        </>
      )}
      <ul style={{ listStyle: "none", margin: "4px 0 0", padding: 0, display: "flex", flexDirection: "column", gap: 12 }}>
        {skills.map((skill) => {
          const state = STATES[skill.state] ?? { label: skill.state, color: "informative" as const };
          return (
            <li key={skill.name} style={{ display: "flex", flexDirection: "column", gap: 2, overflowWrap: "anywhere" }}>
              <div className="portal-inline">
                <Text weight="semibold">{skill.name}</Text>
                <StatusLabel tone={state.color} size="small">{state.label}</StatusLabel>
                {skill.version && (
                  <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>Version {shortSkillVersion(skill.version)}</Text>
                )}
                {skill.repositoryVersion && (
                  <Text size={200} style={{ color: tokens.colorNeutralForeground3 }}>Repository version {shortSkillVersion(skill.repositoryVersion)}</Text>
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
