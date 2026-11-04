// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { chmod, cp, mkdtemp, mkdir, readFile, writeFile, rm, lstat, readdir, symlink, copyFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { createMemoryBridge, digest, loadAssets, MEMORY_TOOL } from "../assets/adapters/common/bridge.mjs";
import { registerMemory, privateOrigin, evaluationRoute } from "../assets/adapters/openclaw/index.mjs";

const moduleDir = fileURLToPath(new URL("../", import.meta.url));
const stage = await mkdtemp(path.join(os.tmpdir(), "memory-native-tests-"));
const helper = path.join(stage, "cetirun-memory");
execFileSync("go", ["build", "-o", helper, "./cmd/cetirun-memory"], { cwd: moduleDir, env: { ...process.env, GOPROXY: "off", GOTOOLCHAIN: "local" } });
test.after(() => rm(stage, { recursive: true, force: true }));

test("installed CLI recovers process loss and keeps writes working after repair", () => {
  const output = execFileSync("python3", [path.join(moduleDir, "tests/cli_recovery.py"), "--helper", helper, "--work-dir", stage], { timeout: 120000, encoding: "utf8" });
  assert.ok(Object.values(JSON.parse(output)).every(result => result === "passed"));
});

async function fixture(t) {
  const home = await mkdtemp(path.join(stage, "home-"));
  const assets = path.join(home, "assets"), workspace = path.join(home, "workspace");
  await mkdir(assets); await mkdir(workspace);
  execFileSync(helper, ["install-assets", assets]);
  await mkdir(path.join(home, ".agents"));
  await cp(path.join(assets, "AGENTS.md"), path.join(home, ".agents/AGENTS.md"));
  await chmod(path.join(home, ".agents/AGENTS.md"), 0o600);
  await cp(path.join(assets, "skills"), path.join(home, ".agents/skills"), { recursive: true });
  const env = { ...process.env, HOME: home, CETIRUN_MEMORY_ASSETS: assets, CETIRUN_MEMORY_ROOT: path.join(home, ".cetirun/memory") };
  const bridge = await createMemoryBridge(env, { assets: await loadAssets(assets) });
  const binding = { entrypoint: "custom", nativeVersion: "fixture/1", sessionId: "native-session", opportunityId: "native-task", cwd: workspace,
    audience: "owner-private", destination: { kind: "owner-private" }, model: "fixture/model", modelRevision: digest("fixture-route"), modelEvaluation: true };
  return { home, assets, workspace, env, bridge, binding };
}
function note(f) {
  return { kind: "fact", title: "Private fixture marker", scope: { project: f.workspace }, body: "PRIVATE-MEMORY-FIXTURE-CONTENT",
    temporalClass: "historical", status: "active", revalidation: { mode: "on-change" }, sources: [{ kind: "fixture", ref: "native-fixture-source", origin: "external-claim", assurance: "reported", observedAt: "2026-10-08T00:00:00Z" }] };
}

test("native Memory tools describe valid query and capture payloads", () => {
  const payload = MEMORY_TOOL.parameters.properties.payload;
  assert.equal(payload.properties?.queryTerms?.type, "array", "queryTerms must be visible before the first tool call");
  assert.equal(payload.properties.queryTerms.maxItems, 8);
  assert.equal(payload.properties.limit.maximum, 20);
  assert.equal(payload.properties.sourceRef?.type, "string", "source checks must describe their workspace-file input");
  assert.match(MEMORY_TOOL.parameters.properties.command.description ?? "", /query.*stored note bodies/);
  const note = payload.properties.note;
  for (const field of ["kind", "title", "body", "scope", "temporalClass", "revalidation", "sources"]) {
    assert.ok(note.required.includes(field), `missing required note input ${field}`);
  }
  assert.ok(note.properties.sources.items.properties.origin.enum.includes("user-stated"));
  assert.match(payload.properties.procedure.description, /prepare/);
  assert.equal(payload.properties.procedure.properties.payload.properties.maxResults.maximum, 10);
  assert.equal(MEMORY_TOOL.parameters.additionalProperties, false);
  assert.equal(payload.properties.requestContext, undefined);
  assert.equal(payload.properties.root, undefined);
});

test("an unavailable source check does not hide a stored note", async t => {
  const f = await fixture(t);
  const session = f.bridge.session(f.binding);
  const captured = await session.tool({ command: "capture", payload: { note: note(f) } });
  assert.equal(captured.ok, true);
  const checked = await session.tool({ command: "source", payload: { ref: captured.data.refs[0] } });
  assert.equal(checked.ok, false);
  assert.equal(checked.reason, "SourceAssuranceUnavailable");
  assert.equal(checked.retryable, false);
  const queried = await session.tool({ command: "query", payload: { queryTerms: [note(f).title], limit: 1 } });
  assert.equal(queried.ok, true);
  assert.equal(queried.data.hits[0].note.body, note(f).body);
});

// The before_prompt_build context OpenClaw 2026.9.6 builds for an owner's
// Control UI or TUI turn: embedded-agent-runner/run/attempt-prompt-build.ts with
// buildAgentHookContextChannelFields. chat.send leaves the provenance unset, the
// channel falls back to the "webchat" provider, the internal route uses the
// session key as the target, and operator UI clients carry no sender ID.
function controlUiContext(overrides = {}) {
  return { sessionId: "web-session", sessionKey: "agent:main:main", runId: "run-1", agentId: "main", trigger: "user",
    messageProvider: "webchat", channel: "webchat", channelId: "agent:main:main", chatId: "agent:main:main", ...overrides };
}
function channelContext(provider, chatType, overrides = {}) {
  return controlUiContext({ messageProvider: provider, channel: provider, channelId: "fixture-conversation", chatId: "fixture-conversation",
    senderId: "fixture-sender", channelContext: { sender: { id: "fixture-sender" }, chat: { id: "fixture-conversation", ...(chatType ? { type: chatType } : {}) } },
    ...overrides });
}

test("OpenClaw treats only the owner's operator turns as private", () => {
  const cases = [
    ["Control UI or TUI turn", controlUiContext(), true],
    ["dashboard session", controlUiContext({ sessionKey: "agent:main:dashboard:fixture", channelId: "agent:main:dashboard:fixture" }), true],
    ["operator input marked as a person's input", controlUiContext({ inputProvenance: { kind: "external_user", sourceChannel: "fixture" } }), true],
    ["local CLI turn", controlUiContext({ messageProvider: "cli", channel: "cli" }), true],
    ["channel direct message a channel plugin marks as the owner",
      channelContext("slack", "direct", { channelContext: { sender: { id: "fixture-sender", isOwner: true }, chat: { id: "fixture-conversation", type: "direct" } } }), true],
    ["another Gateway client, such as the public webchat client", controlUiContext({ senderId: "webchat-ui" }), false],
    ["heartbeat", controlUiContext({ trigger: "heartbeat" }), false],
    ["cron job", controlUiContext({ trigger: "cron", jobId: "fixture-job", sessionKey: "agent:main:cron:fixture-job:run:fixture-run" }), false],
    ["memory flush", controlUiContext({ trigger: "memory" }), false],
    ["turn without a trigger", controlUiContext({ trigger: undefined }), false],
    ["inter-session message", controlUiContext({ inputProvenance: { kind: "inter_session", sourceTool: "sessions_send" } }), false],
    ["internal system input", controlUiContext({ inputProvenance: { kind: "internal_system", sourceTool: "fixture" } }), false],
    ["person's input relayed from another session", controlUiContext({ inputProvenance: { kind: "external_user", sourceSessionKey: "agent:main:other" } }), false],
    ["sub-agent session", controlUiContext({ sessionKey: "agent:main:subagent:fixture" }), false],
    ["cron session", controlUiContext({ sessionKey: "agent:main:cron:fixture-job" }), false],
    ["webchat turn routed to another channel", controlUiContext({ channel: "slack" }), false],
    ["voice transcript from a paired node", controlUiContext({ messageProvider: "node", channel: "node",
      inputProvenance: { kind: "external_user", sourceChannel: "voice", sourceTool: "gateway.voice.transcript" } }), false],
    ["channel direct message", channelContext("slack", "direct"), false],
    ["channel message without a chat type", channelContext("telegram"), false],
    ["group chat", channelContext("slack", "group"), false],
  ];
  for (const [name, ctx, expected] of cases) assert.equal(privateOrigin(ctx), expected, name);
});

test("generic Custom bridge runs the real helper, records usage, and obeys restrictions", async t => {
  const f = await fixture(t);
  const session = f.bridge.session(f.binding);
  assert.equal((await session.tool({ command: "status", payload: {} })).data.initialized, false);
  for (const command of ["prepare", "lint", "query"]) await session.tool({ command, payload: command === "query" ? { queryTerms: ["unknown"] } : {} });
  await assert.rejects(lstat(f.env.CETIRUN_MEMORY_ROOT), { code: "ENOENT" });
  const captured = await session.tool({ command: "capture", payload: { note: note(f) } }, "capture-fixture");
  assert.equal(captured.ok, true, JSON.stringify(captured));
  const queried = await session.tool({ command: "query", payload: { queryTerms: ["Private fixture marker"] } }, "lookup-fixture");
  assert.equal(queried.data.hits[0].note.body, "PRIVATE-MEMORY-FIXTURE-CONTENT");
  const trace = await session.tool({ command: "trace", payload: { ref: captured.data.refs[0] } }, "trace-fixture");
  assert.ok(trace.data.entries.some(entry => entry.relationship === "injected"));
  const restricted = f.bridge.session({ ...f.binding, noCapture: true });
  const denied = await restricted.tool({ command: "capture", payload: { note: note(f) } });
  assert.equal(denied.reason, "CaptureProhibited");
  for (const boundary of [{ audience: "shared" }, { noMemory: true }, { parentSessionId: "parent" }]) {
    const content = await f.bridge.session({ ...f.binding, ...boundary }).context();
    assert.ok(!content.includes("Private fixture marker"));
  }
  let calls = 0;
  const result = await session.tool({ command: "evaluate", payload: { ref: captured.data.refs[0] } }, "not-authorized", async () => { calls++; });
  assert.equal(result.ok, false); assert.equal(calls, 0);
  assert.equal((await loadAssets(f.assets)).manifest.adapterVersion, "memory-adapter/v1");
  assert.ok((await readFile(path.join(f.assets, "adapters/hermes/__init__.py"), "utf8")).includes("def register("));
});

test("OpenClaw hooks use native origin, refresh context, and preserve unrelated messages", async t => {
  const f = await fixture(t), hooks = new Map();
  let factory, modelCalls = 0;
  registerMemory({ runtime: { version: "2026.9.6", llm: { complete: async () => { modelCalls++; throw new Error("no model authorized"); } } },
    on: (name, fn) => hooks.set(name, fn), registerTool: registration => { assert.equal(registration.contextVersion, 2); factory = registration.create; } }, { bridge: f.bridge });
  const ctx = controlUiContext({ workspaceDir: f.workspace, modelProviderId: "fixture", modelId: "model", assertInvocationCurrent() {} });
  const messages = [{ role: "user", content: "Unrelated native history" }];
  const event = { prompt: "fixture", currentUserMessage: "fixture", currentUserMessageId: "request-1", messages };
  assert.equal(privateOrigin(ctx), true);
  await hooks.get("before_prompt_build")(event, ctx);
  const tool = factory(ctx);
  const result = await tool.execute("native-write", { command: "capture", payload: { note: note(f) } });
  assert.equal(result.details.ok, true, JSON.stringify(result.details));
  const context = await hooks.get("before_prompt_build")({ ...event, currentUserMessageId: "request-2" }, { ...ctx, runId: "run-2" });
  assert.ok(context.prependContext.includes("Private fixture marker"));
  assert.equal(`${context.prependSystemContext}\n${context.prependContext}`.split("## CetiRun private memory").length - 1, 1);
  assert.deepEqual(messages, [{ role: "user", content: "Unrelated native history" }]);
  await hooks.get("llm_input")({}, { ...ctx, runId: "run-2" });
  assert.ok(hooks.get("before_tool_call")({ toolName: "sessions_send" }, { ...ctx, runId: "run-2" }).requireApproval);
  assert.equal((await tool.execute("stale-tool", { command: "status", payload: {} })).isError, true);
  await hooks.get("before_prompt_build")({ ...event, currentUserMessage: "/memory no-capture", currentUserMessageId: "request-3" }, { ...ctx, runId: "run-3" });
  const denied = await factory({ ...ctx, runId: "run-3" }).execute("native-write-2", { command: "capture", payload: { note: note(f) } });
  assert.equal(denied.details.reason, "CaptureProhibited");
  const shared = await hooks.get("before_prompt_build")({ ...event, currentUserMessageId: "shared-request" }, { ...channelContext("slack", "group"), runId: "run-4", workspaceDir: f.workspace, sessionId: "web-session" });
  assert.ok(!shared.prependContext.includes("Private fixture marker"));
  await hooks.get("agent_end")({ success: true }, { ...ctx, runId: "run-4" });
  assert.equal(modelCalls, 0);
  hooks.get("session_end")({}, ctx);
  const restored = await hooks.get("before_prompt_build")({ ...event, messages: [{ role: "user", content: "/memory off" }], currentUserMessageId: "resumed" }, { ...ctx, runId: "resumed" });
  assert.ok(!restored.prependContext.includes("Private fixture marker"));
  const api = { runtime: { version: "2026.9.6", llm: { complete() {} } }, config: { agents: { defaults: { model: "fixture/model" } }, models: { providers: { fixture: { api: "openai-completions", models: [{ id: "model" }] } } } } };
  assert.equal(evaluationRoute(api, ctx), true);
  assert.equal(evaluationRoute(api, { ...ctx, modelId: "unqualified" }), false);
  api.config.models.providers.fixture.api = "openai-responses";
  assert.equal(evaluationRoute(api, ctx), false);
});

test("the native bridge loads the common Agent context, not a second asset copy", async t => {
  const f = await fixture(t);
  const context = "Synthetic platform preamble.\n" + await readFile(path.join(f.home, ".agents/AGENTS.md"), "utf8");
  await writeFile(path.join(f.home, ".agents/AGENTS.md"), context);
  const bridge = await createMemoryBridge(f.env, { assets: await loadAssets(f.assets) });
  assert.equal(bridge.assets.canonical, context);
  const composed = await bridge.session(f.binding).context();
  assert.ok(composed.startsWith("Synthetic platform preamble."));
  assert.equal(composed.split("## CetiRun private memory").length - 1, 1);
  await rm(path.join(f.home, ".agents/AGENTS.md"));
  await assert.rejects(createMemoryBridge(f.env, { assets: await loadAssets(f.assets) }));
});

test("OpenClaw asset failures disable Memory without an unhandled rejection", async () => {
  const hooks = new Map();
  registerMemory({ on: (name, fn) => hooks.set(name, fn), registerTool() {} }, { bridge: Promise.reject(new Error("synthetic asset mismatch")) });
  assert.match((await hooks.get("before_prompt_build")({}, {})).prependSystemContext, /Memory is unavailable/);
});

async function openclawFixture(t, bridgeOverride) {
  const f = await fixture(t), hooks = new Map();
  await f.bridge.session(f.binding).tool({ command: "capture", payload: { note: note(f) } }, "seed");
  let factory;
  registerMemory({ runtime: { version: "2026.9.6" }, on: (name, fn) => hooks.set(name, fn),
    registerTool: registration => { factory = registration.create; } }, { bridge: bridgeOverride?.(f.bridge) ?? f.bridge });
  const ctx = controlUiContext({ sessionId: "sdk-shape", runId: "owner-1", workspaceDir: f.workspace, assertInvocationCurrent() {} });
  return { ...f, hooks, ctx, tool: context => factory(context) };
}

test("OpenClaw embedded prompt-only owner input immediately disables Memory", async t => {
  const f = await openclawFixture(t);
  const result = await f.hooks.get("before_prompt_build")({ prompt: "/memory off", messages: [] }, f.ctx);
  assert.ok(!result.prependContext.includes("Private fixture marker"), "off leaked private navigation");
  const capture = await f.tool(f.ctx).execute("denied-write", { command: "capture", payload: { note: note(f) } });
  assert.equal(capture.details.reason, "MemoryDisabled");
});

test("OpenClaw internal restrictions do not become owner preferences", async t => {
  const f = await openclawFixture(t);
  const event = { prompt: "Synthetic owner task", messages: [] };
  await f.hooks.get("before_prompt_build")(event, f.ctx);
  const internal = await f.hooks.get("before_prompt_build")(event, { ...f.ctx, runId: "internal", inputProvenance: { kind: "internal_system", sourceTool: "fixture" } });
  assert.ok(!internal.prependContext.includes("Private fixture marker"));
  const resumed = await f.hooks.get("before_prompt_build")(event, { ...f.ctx, runId: "owner-2" });
  assert.ok(resumed.prependContext.includes("Private fixture marker"), "internal turn permanently disabled owner Memory");
});

test("OpenClaw outbound guard is armed before asynchronous injection accounting", async t => {
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  const f = await openclawFixture(t, bridge => ({ ...bridge, session(binding, signal) {
    const session = bridge.session(binding, signal);
    return { ...session, async injected() { await pending; return session.injected(); } };
  } }));
  await f.hooks.get("before_prompt_build")({ prompt: "Synthetic task", messages: [] }, f.ctx);
  const accounting = f.hooks.get("llm_input")({}, f.ctx);
  try {
    for (const toolName of ["message", "sessions_send", "sessions_spawn"]) {
      assert.ok(f.hooks.get("before_tool_call")({ toolName }, f.ctx)?.requireApproval, `${toolName} raced the Memory guard`);
    }
  } finally { release(); await accounting; }
});

test("OpenClaw turns that cannot use Memory start no helper for context or the boundary", async t => {
  const calls = [];
  const f = await openclawFixture(t, bridge => ({ ...bridge, session(binding, signal) {
    const session = bridge.session(binding, signal);
    return { ...session,
      async ownerPreference(text) { calls.push("ownerPreference"); return session.ownerPreference(text); },
      async context(options) { calls.push("context"); return session.context(options); },
      async boundary() { calls.push("boundary"); return session.boundary(); } };
  } }));
  const group = { ...channelContext("slack", "group"), sessionId: "group-session", runId: "group-1", workspaceDir: f.workspace };
  const shared = await f.hooks.get("before_prompt_build")({ prompt: "Synthetic group question", messages: [] }, group);
  assert.match(shared.prependContext, /not available in this conversation/);
  await f.hooks.get("agent_end")({ success: true }, group);
  const off = { ...f.ctx, runId: "owner-off" };
  assert.match((await f.hooks.get("before_prompt_build")({ prompt: "/memory off", messages: [] }, off)).prependContext, /off for this conversation/);
  await f.hooks.get("agent_end")({ success: true }, off);
  assert.deepEqual(calls, []);
  const on = { ...f.ctx, runId: "owner-on" };
  assert.ok((await f.hooks.get("before_prompt_build")({ prompt: "/memory on", messages: [] }, on)).prependContext.includes("Private fixture marker"));
  await f.hooks.get("agent_end")({ success: true }, on);
  assert.deepEqual(calls, ["ownerPreference", "context", "boundary"]);
});

test("pinned Hermes host APIs load the adapter and execute bounded native calls", { skip: !process.env.MEMORY_HERMES_SOURCE }, async t => {
  const f = await fixture(t);
  const output = execFileSync(process.env.MEMORY_HERMES_PYTHON ?? "python3", ["-B", path.join(moduleDir, "tests/hermes_host.py")], {
    env: { ...f.env, MEMORY_TEST_ASSETS: f.assets, MEMORY_TEST_WORKSPACE: f.workspace, MEMORY_HERMES_SOURCE: process.env.MEMORY_HERMES_SOURCE,
      HERMES_HOME: path.join(f.home, ".hermes"), HERMES_SKIP_AUTO_UPDATE: "1", PYTHONDONTWRITEBYTECODE: "1" }, encoding: "utf8", timeout: 30000,
  });
  assert.match(output, /Pinned Hermes host checks passed/);
});

async function skillHost(source, runtime, changed) {
  const target = await mkdtemp(path.join(stage, `${runtime}-skills-`));
  async function populate(relative = "") {
    const directory = path.join(target, relative);
    await mkdir(directory, { recursive: true });
    for (const name of await readdir(path.join(source, relative))) {
      const file = path.join(relative, name), original = path.join(source, file), destination = path.join(target, file);
      if (changed.includes(file)) await copyFile(original, destination);
      else if (changed.some(entry => entry.startsWith(`${file}/`))) await populate(file);
      else await symlink(original, destination);
    }
  }
  await populate();
  const installer = path.resolve(moduleDir, "../cetirun-chart/configs/apply-memory-skills.py");
  const args = [installer, "--runtime", runtime, "--root", target];
  execFileSync("python3", [...args, "--check"]);
  execFileSync("python3", args);
  assert.match(execFileSync("python3", args, { encoding: "utf8" }), /0 file\(s\) updated/);
  return target;
}

test("pinned Hermes protects Memory discovery through live shared-skill changes", { skip: !process.env.MEMORY_HERMES_SOURCE }, async () => {
  const host = await skillHost(process.env.MEMORY_HERMES_SOURCE, "hermes", ["agent/skill_utils.py", "tools/skills_tool.py"]);
  const output = execFileSync(process.env.MEMORY_HERMES_PYTHON ?? "python3", ["-B", path.join(moduleDir, "tests/hermes_skills.py")], {
    env: { ...process.env, MEMORY_HERMES_SOURCE: host, PYTHONDONTWRITEBYTECODE: "1" }, encoding: "utf8", timeout: 30000,
  });
  assert.match(output, /Pinned Hermes live skill-source checks passed/);
});

test("pinned OpenClaw protects Memory discovery through live shared-skill changes", { skip: !process.env.MEMORY_OPENCLAW_SOURCE }, async () => {
  const host = await skillHost(process.env.MEMORY_OPENCLAW_SOURCE, "openclaw", ["dist/workspace-skill-loader-CoQ1pd8t.mjs"]);
  const output = execFileSync(process.execPath, [path.join(moduleDir, "tests/openclaw_skills.mjs")], {
    env: { ...process.env, MEMORY_OPENCLAW_SOURCE: host }, encoding: "utf8", timeout: 30000,
  });
  assert.match(output, /Pinned OpenClaw live skill-source checks passed/);
});

test("pinned OpenClaw loads the packaged plugin and bounds direct native requests", { skip: !process.env.MEMORY_OPENCLAW_SOURCE }, async t => {
  const f = await fixture(t);
  const output = execFileSync(process.execPath, [path.join(moduleDir, "tests/openclaw_host.mjs")], {
    env: { ...f.env, MEMORY_TEST_ASSETS: f.assets, MEMORY_TEST_WORKSPACE: f.workspace, MEMORY_OPENCLAW_SOURCE: process.env.MEMORY_OPENCLAW_SOURCE,
      OPENCLAW_STATE_DIR: path.join(f.home, ".openclaw"), OPENCLAW_CONFIG_PATH: path.join(f.home, ".openclaw/config.json"), OPENCLAW_NO_RESPAWN: "1" },
    encoding: "utf8", timeout: 45000,
  });
  assert.match(output, /Pinned OpenClaw host checks passed/);
});

test("pinned Pi SDK executes the real memory tool and isolated evaluator", { skip: process.env.MEMORY_PI_NATIVE !== "true" }, async t => {
  const f = await fixture(t);
  const output = execFileSync(process.execPath, [path.join(moduleDir, "tests/pi_host.mjs")], {
    cwd: path.resolve(moduleDir, "../.."), env: { ...f.env, MEMORY_TEST_ASSETS: f.assets, MEMORY_TEST_WORKSPACE: f.workspace }, encoding: "utf8", timeout: 30000,
  });
  assert.match(output, /Pinned Pi SDK and real helper checks passed/);
});

test("pinned Pi SDK tightens steering and follow-up inputs and propagates cancellation", { skip: process.env.MEMORY_PI_NATIVE !== "true" }, async t => {
  const f = await fixture(t);
  const output = execFileSync(process.execPath, [path.join(moduleDir, "tests/pi_lifecycle.mjs")], {
    cwd: path.resolve(moduleDir, "../.."), env: { ...f.env, MEMORY_TEST_ASSETS: f.assets, MEMORY_TEST_WORKSPACE: f.workspace }, encoding: "utf8", timeout: 30000,
  });
  assert.match(output, /Pinned Pi lifecycle checks passed/);
});

test("Hermes adapter uses the same helper through request middleware and native tools", async t => {
  const f = await fixture(t);
  const output = execFileSync("python3", ["-B", path.join(moduleDir, "tests/hermes_adapter.py")], {
    env: { ...f.env, MEMORY_TEST_ASSETS: f.assets, MEMORY_TEST_WORKSPACE: f.workspace, PYTHONDONTWRITEBYTECODE: "1" }, encoding: "utf8",
  });
  assert.match(output, /Hermes native adapter checks passed/);
});

test("Custom stdio exposes fixed tools and narrows per-request capture without sampling", async t => {
  const f = await fixture(t);
  const child = spawn(helper, ["mcp", "--stdio", "--owner-private"], { cwd: f.workspace, env: f.env, stdio: ["pipe", "pipe", "pipe"] });
  t.after(() => child.kill());
  let id = 0, errors = "";
  child.stderr.on("data", chunk => { errors += chunk; });
  const pending = new Map();
  createInterface({ input: child.stdout }).on("line", line => { const result = JSON.parse(line); pending.get(result.id)?.(result); pending.delete(result.id); });
  const call = (method, params) => new Promise(resolve => { const requestId = ++id; pending.set(requestId, resolve); child.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: requestId, method, params }) + "\n"); });
  const initialized = await call("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "synthetic-custom", version: "1" } });
  assert.equal(initialized.result.capabilities.sampling, undefined);
  assert.equal((await call("tools/list", {})).result.tools[0].name, "cetirun_memory");
  const params = { name: "cetirun_memory", arguments: { command: "capture", payload: { note: note(f) } } };
  let result = await call("tools/call", params);
  assert.equal(JSON.parse(result.result.content[0].text).ok, true);
  result = await call("tools/call", { ...params, _meta: { "cetirun/noCapture": true } });
  assert.equal(JSON.parse(result.result.content[0].text).reason, "CaptureProhibited");
  result = await call("tools/call", { name: "cetirun_memory", arguments: { command: "query", payload: { queryTerms: ["Private fixture marker"] } } });
  assert.equal(JSON.parse(result.result.content[0].text).data.hits[0].note.body, "PRIVATE-MEMORY-FIXTURE-CONTENT");
  assert.equal(errors, "");
  child.stdin.end();
  await new Promise(resolve => child.once("exit", resolve));
});
