// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import {
  createAgentSession, createExtensionRuntime, loadSkillsFromDir,
  ModelRuntime, SessionManager, SettingsManager,
} from "@earendil-works/pi-coding-agent";

export const PROVIDER = "github-copilot";

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

export async function createPiRuntime(home) {
  const agentDir = path.join(home, ".pi", "agent");
  const cwd = path.join(home, "workspace");
  await mkdir(agentDir, { recursive: true, mode: 0o700 });
  await mkdir(cwd, { recursive: true, mode: 0o700 });
  const resourceLoader = await approvedResources(home);
  const modelRuntime = await ModelRuntime.create({
    authPath: path.join(agentDir, "auth.json"),
    modelsPath: null,
    allowModelNetwork: false,
    refreshOnCreate: false,
  });
  async function availableModels(signal) {
    await modelRuntime.refresh({ allowNetwork: false, providers: [PROVIDER], signal });
    return modelRuntime.getAvailable(PROVIDER, { signal });
  }
  return {
    skills: () => resourceLoader.getSkills().skills.map(({ name }) => name),
    async authenticated() {
      return (await modelRuntime.listCredentials()).some((entry) => entry.providerId === PROVIDER);
    },
    async models(signal) {
      return (await availableModels(signal)).map(({ id }) => id);
    },
    async login(signal, notify) {
      // Pi stores and refreshes its own credential. Never return it over HTTP.
      await modelRuntime.login(PROVIDER, "oauth", {
        signal,
        prompt: async () => "",
        notify: (event) => {
          if (event.type === "device_code") {
            const url = new URL(event.verificationUri);
            if (url.protocol !== "https:" || url.hostname !== "github.com") {
              throw new Error("Unexpected GitHub verification URL");
            }
            notify({ state: "awaiting-device", userCode: event.userCode, verificationUri: url.href });
          }
        },
      });
    },
    async run({ model: id, messages, signal, onText }) {
      const available = await availableModels(signal);
      const model = available.find((candidate) => candidate.id === id);
      if (!model) throw new Error("Requested Copilot model is unavailable");
      const manager = SessionManager.inMemory(cwd);
      const system = messages.filter((message) => message.role === "system").map((message) => message.content);
      const conversation = messages.filter((message) => message.role !== "system");
      for (const message of conversation.slice(0, -1)) {
        if (message.role === "user") {
          manager.appendMessage({ role: "user", content: message.content, timestamp: Date.now() });
        } else {
          manager.appendMessage({
            role: "assistant", content: [{ type: "text", text: message.content }],
            api: model.api, provider: model.provider, model: model.id, stopReason: "stop", timestamp: Date.now(),
            usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
              cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
          });
        }
      }
      const loader = { ...resourceLoader, getAppendSystemPrompt: () => system };
      const { session } = await createAgentSession({
        cwd, agentDir, model, modelRuntime, resourceLoader: loader, sessionManager: manager,
        tools: ["read", "bash", "edit", "write", "grep", "find", "ls"],
        settingsManager: SettingsManager.inMemory({
          compaction: { enabled: false }, retry: { enabled: false }, enableInstallTelemetry: false,
        }),
      });
      let toolCalls = 0;
      let limitExceeded = false;
      const abort = () => { void session.abort().catch(() => {}); };
      signal.addEventListener("abort", abort, { once: true });
      const unsubscribe = session.subscribe((event) => {
        if (event.type === "tool_execution_start" && ++toolCalls > 8) {
          limitExceeded = true;
          abort();
        }
        if (event.type === "message_update" && event.assistantMessageEvent.type === "text_delta") {
          onText?.(event.assistantMessageEvent.delta);
        }
      });
      try {
        signal.throwIfAborted();
        await session.prompt(conversation.at(-1).content);
        signal.throwIfAborted();
        if (limitExceeded) throw new Error("Example tool budget exceeded");
        const last = session.messages.findLast((message) => message.role === "assistant");
        if (!last || ["error", "aborted"].includes(last.stopReason)) throw new Error("Pi run did not complete");
        return { text: session.getLastAssistantText() ?? "", toolCalls };
      } finally {
        signal.removeEventListener("abort", abort);
        unsubscribe();
        session.dispose();
      }
    },
  };
}
