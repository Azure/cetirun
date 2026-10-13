// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { createMemoryBridge, digest, MEMORY_TOOL, restrictiveMode, restoredRestrictions } from "../common/bridge.mjs";

export function privateOrigin(ctx) {
  if (ctx.jobId || ctx.inputProvenance?.kind !== "external_user" || ctx.inputProvenance?.originSessionId || ctx.inputProvenance?.sourceSessionKey || ctx.inputProvenance?.sourceRole || ctx.inputProvenance?.jobId) return false;
  const chat = ctx.channelContext?.chat;
  if (["group", "channel", "room"].includes(chat?.type)) return false;
  if (["webchat", "cli"].includes(ctx.messageProvider) && !ctx.channel) return true;
  return chat?.type === "direct" && ctx.channelContext?.sender?.isOwner === true;
}

// The pinned direct chat-completions transport disables SDK retries. Other
// transports can replay or switch transport, so they are not qualified here.
export function evaluationRoute(api, ctx) {
  if (api.runtime.version !== "2026.9.6" || typeof api.runtime.llm?.complete !== "function") return false;
  const config = api.config;
  const agent = config?.agents?.list?.find(item => item.id === ctx.agentId);
  const configured = agent?.model ?? config?.agents?.defaults?.model;
  const primary = typeof configured === "string" ? configured : configured?.primary;
  const provider = config?.models?.providers?.[ctx.modelProviderId];
  const model = provider?.models?.find(item => item.id === ctx.modelId);
  return primary === `${ctx.modelProviderId}/${ctx.modelId}` && !!model && (model.api ?? provider.api) === "openai-completions";
}

function modelRevision(api, ctx) {
  const provider = api.config?.models?.providers?.[ctx.modelProviderId];
  const model = provider?.models?.find(item => item.id === ctx.modelId);
  return digest(JSON.stringify({ provider: ctx.modelProviderId, model: ctx.modelId, api: model?.api ?? provider?.api,
    baseUrl: provider?.baseUrl, contextWindow: model?.contextWindow, maxTokens: model?.maxTokens, reasoning: model?.reasoning }));
}

export function registerMemory(api, options = {}) {
  const bridgePromise = (options.bridge ? Promise.resolve(options.bridge) : createMemoryBridge()).catch(() => null);
  const sessions = new Map();
  const key = ctx => ctx.sessionId ?? ctx.sessionKey;
  const prune = () => { if (sessions.size > 128) { const id = sessions.keys().next().value; sessions.get(id)?.controller.abort(); sessions.delete(id); } };
  api.on("before_prompt_build", async (event, ctx) => {
    const bridge = await bridgePromise;
    if (!bridge) return { prependSystemContext: "CetiRun Memory is unavailable. Do not claim private memory was read or saved, or bypass the missing native binding." };
    const id = key(ctx);
    if (!id || !ctx.workspaceDir) return { prependSystemContext: bridge.assets.canonical };
    const previous = sessions.get(id);
    const owner = privateOrigin(ctx);
    const ownerText = event.currentUserMessage ?? event.prompt ?? "";
    const opportunity = event.currentUserMessageId ?? ctx.runId ?? digest(ownerText);
    if (previous?.runId === ctx.runId && previous?.opportunity === opportunity && previous.owner === owner && previous.ownerText === ownerText && !previous.controller.signal.aborted) {
      const context = await previous.memory.context({ includeCanonical: false });
      ctx.hookInvocation?.assertActive();
      if (previous.controller.signal.aborted || sessions.get(id) !== previous) return;
      previous.injected ||= owner && !previous.memory.binding.noMemory;
      return { prependSystemContext: bridge.assets.canonical, prependContext: context };
    }
    previous?.controller.abort();
    const priorControls = previous?.controls ?? restoredRestrictions(event.messages);
    const controls = owner ? restrictiveMode(ownerText, priorControls) : priorControls;
    const controller = new AbortController();
    const memory = bridge.session({
      entrypoint: "openclaw", nativeVersion: api.runtime.version,
      sessionId: id, opportunityId: opportunity,
      cwd: ctx.workspaceDir, audience: owner ? "owner-private" : "unknown",
      destination: { kind: owner ? "owner-private" : "unknown" },
      ...(owner ? controls : { noMemory: true, noCapture: true }), model: `${ctx.modelProviderId ?? ""}/${ctx.modelId ?? ""}`,
      modelRevision: modelRevision(api, ctx),
      modelEvaluation: evaluationRoute(api, ctx),
    }, controller.signal);
    const session = { memory, controls, controller, runId: ctx.runId, opportunity, owner, ownerText,
      injected: previous?.runId === ctx.runId && previous.injected || false };
    sessions.set(id, session);
    prune();
    if (owner) await memory.ownerPreference(ownerText);
    const context = await memory.context({ includeCanonical: false });
    ctx.hookInvocation?.assertActive();
    if (controller.signal.aborted || sessions.get(id) !== session) return;
    session.injected ||= owner && !memory.binding.noMemory;
    return { prependSystemContext: bridge.assets.canonical, prependContext: context };
  });
  api.on("llm_input", async (_event, ctx) => {
    const session = sessions.get(key(ctx));
    if (session && session.runId === ctx.runId && !session.controller.signal.aborted) await session.memory.injected();
  });
  api.registerTool({ contextVersion: 2, create: ctx => ({
    ...MEMORY_TOOL, label: "Private memory",
    async execute(id, params, signal) {
      ctx.assertInvocationCurrent();
      const session = sessions.get(key(ctx));
      if (!session || session.controller.signal.aborted || ctx.runId && session.runId !== ctx.runId) return { content: [{ type: "text", text: "Memory session unavailable" }], isError: true };
      const cancel = () => session.controller.abort();
      signal?.addEventListener("abort", cancel, { once: true });
      try {
        if (signal?.aborted) cancel();
        const result = await session.memory.tool(params, id, async (ticket, abortSignal) => {
          ctx.assertInvocationCurrent();
          const result = await api.runtime.llm.complete({
            systemPrompt: ticket.system, messages: [{ role: "user", content: ticket.prompt }],
            maxTokens: ticket.maxTokens, temperature: 0,
            signal: AbortSignal.any([abortSignal, AbortSignal.timeout(ticket.timeoutSeconds * 1000)]),
            purpose: "cetirun-memory/artifact-contract-v1",
          });
          const model = `${result.provider}/${result.model}`;
          return { output: result.text, outcome: result.stopReason === "stop" ? "completed" : "unknown", model, requests: 1, tokens: result.usage?.totalTokens ?? null };
        });
        return { content: [{ type: "text", text: JSON.stringify(result) }], details: result, isError: !result.ok };
      } finally { signal?.removeEventListener("abort", cancel); }
    },
  }) }, { name: MEMORY_TOOL.name });
  api.on("after_tool_call", async (event, ctx) => {
    const session = sessions.get(key(ctx));
    if (event.error && session && session.runId === ctx.runId && !session.controller.signal.aborted) await session.memory.feedback(event.toolName, "failure", event.toolCallId ?? ctx.toolCallId);
  });
  api.on("before_tool_call", (event, ctx) => {
    const session = sessions.get(key(ctx));
    if (!session?.injected || session.runId !== ctx.runId || !["message", "sessions_send", "sessions_spawn"].includes(event.toolName)) return;
    return { requireApproval: { title: "Check private memory before sharing", description: "This turn received Agent-private context. Confirm the exact outgoing task or message and its recipient; share only the minimum authorized content. Child tasks do not inherit Memory access.", severity: "warning", timeoutMs: 60000 } };
  });
  api.on("agent_end", async (event, ctx) => {
    const session = sessions.get(key(ctx));
    if (!session || session.runId !== ctx.runId) return;
    if (event.success) await session.memory.boundary();
    session.controller.abort();
  });
  for (const hook of ["before_reset", "session_end"]) api.on(hook, (_event, ctx) => {
    const id = key(ctx); sessions.get(id)?.controller.abort(); sessions.delete(id);
  });
}

export default { id: "cetirun-memory-context", name: "CetiRun private memory", register: registerMemory };
