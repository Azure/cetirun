// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { readFile } from "node:fs/promises";
import path from "node:path";
import { createExtensionRuntime, loadSkillsFromDir } from "@earendil-works/pi-coding-agent";

/** Load only the platform's read-only context, without global/project plugins. */
export async function approvedResources(home) {
  const root = path.join(home, ".agents");
  const canonical = path.join(root, "AGENTS.md");
  const content = await readFile(canonical, "utf8");
  const skills = loadSkillsFromDir({ dir: path.join(root, "skills"), source: "cetirun" });
  if (!content.trim() || !skills.skills.some((skill) => skill.name === "cetirun-memory")) {
    throw new Error("Canonical context and cetirun-memory are required");
  }
  const extensions = { extensions: [], errors: [], runtime: createExtensionRuntime() };
  return {
    getExtensions: () => extensions,
    getSkills: () => skills,
    getPrompts: () => ({ prompts: [], diagnostics: [] }),
    getThemes: () => ({ themes: [], diagnostics: [] }),
    getAgentsFiles: () => ({ agentsFiles: [{ path: canonical, content }] }),
    getSystemPrompt: () => undefined,
    getSystemPromptSource: () => undefined,
    getAppendSystemPrompt: () => [],
    getAppendSystemPromptSources: () => [],
    extendResources: () => {},
    reload: async () => {},
  };
}
