// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The pinned native runtime talks to a scripted loopback model. This checks
// the request and real file/shell tools, not a model's instruction adherence.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import os from "node:os";
import path from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";
import { promisify } from "node:util";
import { GUIDANCE } from "../../app/cetirun-chart/agents/openclaw/compat/apply-skill-paths.mjs";

const execute = promisify(execFile);
const filename = fileURLToPath(import.meta.url);
const repository = path.resolve(path.dirname(filename), "../..");
const success = "CETIRUN_SKILL_SCRIPT_OK";
const failure = "CETIRUN_SKILL_SCRIPT_FAILED";
const scenarios = ["relative", "absolute", "retained-snapshot", "compact-catalog", "minimal", "missing-subdirectory", "read-denied"];

function shellQuote(value) {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

function messageText(content) {
  return typeof content === "string" ? content
    : content.filter(part => part.type === "text").map(part => part.text).join("\n");
}

async function runScenario(source, scenario) {
  assert.ok(scenarios.includes(scenario));
  const root = await mkdtemp(path.join(os.tmpdir(), "cetirun-native-skill-"));
  const workspace = path.join(root, "workspace");
  const skillDirectory = path.join(root, "skills with spaces", "example-probe");
  const skillFile = path.join(skillDirectory, "SKILL.md");
  const scriptFile = path.join(skillDirectory, "scripts/probe.sh");
  // This is the disposable child process's real home, not just OpenClaw's
  // state override. Some native OS-home resolvers intentionally ignore
  // OPENCLAW_HOME. Never run this setup in the parent test process.
  process.env.HOME = root;
  process.env.USERPROFILE = root;
  process.env.XDG_CONFIG_HOME = path.join(root, ".config");
  process.env.XDG_CACHE_HOME = path.join(root, ".cache");
  process.env.XDG_DATA_HOME = path.join(root, ".local/share");
  process.env.XDG_STATE_HOME = path.join(root, ".local/state");
  process.env.OPENCLAW_HOME = root;
  process.env.OPENCLAW_STATE_DIR = path.join(root, ".openclaw");
  process.env.OPENCLAW_CONFIG_PATH = path.join(root, "config.json");
  process.env.OPENCLAW_LOG_LEVEL = "error";
  assert.equal(os.homedir(), root);
  const native = name => import(pathToFileURL(path.join(source, "dist", name)).href);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(new Error("Native skill check timed out")), 60000);
  const expectedRequests = scenario === "read-denied" ? 1 : scenario === "missing-subdirectory" ? 2 : 3;
  const requests = [];
  let assertionFailure, admission;
  const originalFetch = globalThis.fetch;
  const server = createServer(async (request, response) => {
    try {
      assert.equal(request.method, "POST");
      assert.equal(request.url, "/v1/chat/completions");
      let raw = "";
      for await (const part of request) {
        raw += part;
        assert.ok(raw.length < 2 * 1024 * 1024, "Unexpectedly large synthetic request");
      }
      const body = JSON.parse(raw);
      requests.push(body);
      assert.ok(requests.length <= expectedRequests, "Unexpected retry or extra model request");
      const system = body.messages.filter(message => ["system", "developer"].includes(message.role))
        .map(message => messageText(message.content)).join("\n");
      const tools = body.tools.map(tool => tool.function.name);
      const toolResults = body.messages.filter(message => message.role === "tool")
        .map(message => JSON.stringify(message.content)).join("\n");
      let delta, finishReason;
      const toolCall = (name, args) => ({ role: "assistant", tool_calls: [{
        index: 0, id: `call_${requests.length}`, type: "function",
        function: { name, arguments: JSON.stringify(args) },
      }] });
      if (requests.length === 1) {
        assert.ok(!raw.includes(success), "The expected output must not be in the initial request");
        if (scenario === "read-denied") {
          assert.ok(!tools.includes("read"));
          assert.ok(!system.includes("<available_skills>"), "No skill catalog without an available reader");
          for (const line of GUIDANCE) assert.ok(!system.includes(line), "No skill guidance without an available reader");
          delta = { role: "assistant", content: "READ_UNAVAILABLE" };
          finishReason = "stop";
        } else {
          assert.ok(tools.includes("read") && tools.includes("exec"));
          const entry = [...system.matchAll(/<skill>([\s\S]*?)<\/skill>/g)]
            .find(match => /<name>example-probe<\/name>/.test(match[1]));
          assert.ok(entry, "Installed skill missing from provider input");
          const advertisedPath = entry[1].match(/<location>([^<]+)<\/location>/)?.[1];
          assert.ok(advertisedPath, "Installed skill location missing from provider input");
          // The native formatter can abbreviate a path under the isolated home.
          // Verify its exact target and pass that advertised form to real read.
          const resolvedPath = advertisedPath.startsWith("~/")
            ? path.join(os.homedir(), advertisedPath.slice(2)) : advertisedPath;
          assert.equal(resolvedPath, skillFile);
          assert.ok(system.includes("read exact <location>"), "Skill-read instruction missing");
          assert.ok(system.includes("resolve it against the skill directory"), "Relative-path rule missing");
          assert.ok(system.includes("use that absolute path in tool commands"));
          for (const line of GUIDANCE) assert.ok(system.includes(line), "Per-run complete-path guidance missing from provider input");
          if (scenario === "compact-catalog") assert.ok(!entry[1].includes("<description>"));
          // Intentionally repeat the observed error in the negative control.
          // The runtime must report it, not silently rewrite the command.
          delta = scenario === "missing-subdirectory"
            ? toolCall("exec", { command: `cd ${shellQuote(skillDirectory)} && /bin/sh probe.sh` })
            : toolCall("read", { path: advertisedPath });
          finishReason = "tool_calls";
        }
      } else if (scenario === "missing-subdirectory") {
        assert.match(toolResults, /No such file|cannot open|not found/i);
        assert.ok(!toolResults.includes(success), "Wrong script path must not execute a different file");
        delta = { role: "assistant", content: failure };
        finishReason = "stop";
      } else if (requests.length === 2) {
        assert.ok(toolResults.includes("/bin/sh scripts/probe.sh"), "Native read lost the nested script path");
        assert.ok(!toolResults.includes(success));
        delta = toolCall("exec", scenario === "absolute"
          ? { command: `/bin/sh ${shellQuote(scriptFile)}`, workdir: workspace }
          : { command: "/bin/sh scripts/probe.sh", workdir: skillDirectory });
        finishReason = "tool_calls";
      } else {
        assert.ok(toolResults.includes(success), "Native shell did not return script stdout");
        delta = { role: "assistant", content: success };
        finishReason = "stop";
      }
      response.writeHead(200, { "Content-Type": "text/event-stream" });
      const frame = (value, reason) => `data: ${JSON.stringify({
        id: `fixture-${requests.length}`, object: "chat.completion.chunk", created: 1,
        model: "gpt-4.1-mini", choices: [{ index: 0, delta: value, finish_reason: reason }],
      })}\n\n`;
      response.end(frame(delta, null) + frame({}, finishReason) + "data: [DONE]\n\n");
    } catch (error) {
      assertionFailure = error;
      response.writeHead(400, { "Content-Type": "application/json" });
      response.end('{"error":{"message":"Synthetic request assertion failed"}}');
      controller.abort(error);
    }
  });
  try {
    await mkdir(workspace, { recursive: true });
    await mkdir(path.dirname(scriptFile), { recursive: true });
    await writeFile(skillFile, "---\nname: example-probe\ndescription: Print a synthetic marker with the bundled script.\n---\n"
      + "Read this skill before acting. Run `/bin/sh scripts/probe.sh` from this skill directory. Report stdout verbatim.\n", { mode: 0o444 });
    await writeFile(scriptFile, `#!/bin/sh\nprintf '%s\\n' '${success}'\n`, { mode: 0o444 });
    await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
    const origin = `http://127.0.0.1:${server.address().port}`;
    globalThis.fetch = (input, init) => {
      const url = new URL(input instanceof Request ? input.url : String(input));
      assert.equal(url.origin, origin, "Only the synthetic model endpoint is permitted");
      return originalFetch(input, init);
    };
    const config = {
      agents: { entries: { main: { workspace } }, defaults: {
        workspace, skipBootstrap: true, model: { primary: "cetirun/gpt-4.1-mini" },
      } },
      tools: { profile: "full", ...(scenario === "read-denied" ? { deny: ["read"] } : {}),
        exec: { host: "gateway", security: "full", ask: "off" } },
      skills: { load: { extraDirs: [path.dirname(skillDirectory)], watch: false } },
      plugins: { enabled: false },
      models: { providers: { cetirun: {
        baseUrl: `${origin}/v1`, api: "openai-completions", apiKey: "sk-dummy",
        models: [{ id: "gpt-4.1-mini", name: "Synthetic model", api: "openai-completions",
          input: ["text"], reasoning: false, contextWindow: 128000, maxTokens: 2048,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } }],
      } } },
    };
    await writeFile(process.env.OPENCLAW_CONFIG_PATH, JSON.stringify(config));
    let skillsSnapshot;
    if (["retained-snapshot", "compact-catalog"].includes(scenario)) {
      const { t: snapshot } = await native("workspace-skill-prompt-BuT-L5LS.mjs");
      skillsSnapshot = await snapshot(workspace, { config, agentId: "main" });
      assert.ok(skillsSnapshot.prompt.includes("example-probe"));
      if (scenario === "compact-catalog") {
        const { i: compact } = await native("skill-contract-BkCILPDQ.mjs");
        skillsSnapshot.prompt = compact(skillsSnapshot.resolvedSkills, { descriptionMaxChars: 0 });
      }
      // Old catalog text does not need to be rewritten in retained HOME.
      for (const line of GUIDANCE) assert.ok(!skillsSnapshot.prompt.includes(line));
    }
    const savedSnapshot = JSON.stringify(skillsSnapshot);
    const { t: run } = await native("embedded-agent-BaH7wGBd.mjs");
    const { a: createRun, c: prepare } = await native("admitted-run-context-BNasoszr.mjs");
    const runId = randomUUID();
    admission = prepare({ cfg: config, operationalRunInstance: createRun(runId), facts: {
      runId, agentId: "main", ingress: { kind: "cli", boundary: "local", rawSourceRef: "synthetic", state: "present" },
    } });
    const result = await run({
      preparedRunAdmission: admission, sessionPersistence: "detached", sessionId: randomUUID(),
      sessionKey: "agent:main:synthetic", agentId: "main", runId, workspaceDir: workspace, cwd: workspace,
      agentDir: path.join(root, ".openclaw/agents/main/agent"), config,
      provider: "cetirun", model: "gpt-4.1-mini", trigger: "user", messageProvider: "webchat",
      prompt: "Use the installed example-probe skill. Read its instructions, run its bundled scripts/probe.sh with /bin/sh, and report stdout.",
      timeoutMs: 55000, abortSignal: controller.signal,
      skillsSnapshot, promptMode: scenario === "minimal" ? "minimal" : "full",
    });
    if (assertionFailure) throw assertionFailure;
    assert.equal(requests.length, expectedRequests);
    assert.equal(JSON.stringify(skillsSnapshot), savedSnapshot, "The retained snapshot must not be rewritten");
    const text = result.payloads?.map(payload => payload.text ?? "").join("\n") ?? "";
    assert.equal(text, scenario === "read-denied" ? "READ_UNAVAILABLE" : scenario === "missing-subdirectory" ? failure : success);
    console.log(JSON.stringify({ scenario, status: "PASS", scriptedRequests: requests.length, realModelRequests: 0 }));
  } finally {
    admission?.close();
    clearTimeout(timer);
    globalThis.fetch = originalFetch;
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    await rm(root, { recursive: true, force: true });
  }
}

if (process.argv[2] === "--scenario") {
  await runScenario(process.argv[3], process.argv[4]);
} else {
  const source = process.env.CETIRUN_OPENCLAW_SOURCE;
  let directory, host;
  before(async () => {
    if (!source) return;
    assert.equal(JSON.parse(await readFile(path.join(source, "package.json"), "utf8")).version, "2026.9.6");
    directory = await mkdtemp(path.join(os.tmpdir(), "cetirun-skill-host-"));
    host = path.join(directory, "node_modules/openclaw");
    // Apply the image's pinned compatibility changes to a disposable copy,
    // preserving the separately installed package and its dependencies.
    await cp(source, host, { recursive: true });
    for (const name of await readdir(path.dirname(source))) {
      if (name !== "openclaw") await symlink(path.join(path.dirname(source), name), path.join(directory, "node_modules", name));
    }
    for (const name of ["apply-workload-mcp.mjs", "apply-gateway-budget.mjs", "apply-skill-paths.mjs"]) {
      await execute(process.execPath, [path.join(repository, "app/cetirun-chart/agents/openclaw/compat", name), host]);
    }
    const patchedPrompt = await readFile(path.join(host, "dist/system-prompt-params-DdCWVctq.mjs"), "utf8");
    const installer = path.join(repository, "app/cetirun-chart/agents/openclaw/compat/apply-skill-paths.mjs");
    await execute(process.execPath, [installer, host]);
    await execute(process.execPath, [installer, host, "--check"]);
    assert.equal(await readFile(path.join(host, "dist/system-prompt-params-DdCWVctq.mjs"), "utf8"), patchedPrompt);
    await execute("python3", [path.join(repository, "app/cetirun-chart/configs/apply-memory-skills.py"), "--runtime", "openclaw", "--root", host]);
  });
  after(async () => { if (directory) await rm(directory, { recursive: true, force: true }); });
  for (const scenario of scenarios) {
    test(`pinned OpenClaw skill request and native tools: ${scenario}`, {
      skip: !source && "Set CETIRUN_OPENCLAW_SOURCE to the separately installed pinned package",
      timeout: 90000,
    }, async () => {
      // Do not inherit provider credentials, proxies or a user's native state.
      const { stdout } = await execute(process.execPath, [filename, "--scenario", host, scenario], {
        env: { PATH: process.env.PATH, LANG: "C.UTF-8", NO_PROXY: "127.0.0.1,localhost" },
        cwd: directory, timeout: 75000, maxBuffer: 1024 * 1024,
      });
      const receipt = JSON.parse(stdout.trim().split("\n").at(-1));
      assert.equal(receipt.status, "PASS");
      assert.equal(receipt.realModelRequests, 0);
    });
  }
}
