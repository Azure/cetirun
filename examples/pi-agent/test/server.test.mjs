// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import test from "node:test";
import { createServers } from "../server.mjs";
import { validateChat } from "../agent-api.mjs";

async function fixture(t, overrides = {}) {
  let calls = 0;
  const servers = createServers({
    models: async () => ["test-model"],
    authenticated: async () => false,
    skills: () => ["cetirun-memory"],
    login: async () => {},
    run: async ({ onText }) => { calls++; onText?.("real adapter result"); return { text: "real adapter result" }; },
    ...overrides,
  });
  for (const server of [servers.api, servers.auth]) {
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  }
  t.after(() => servers.close());
  return {
    base: `http://127.0.0.1:${servers.api.address().port}`,
    auth: `http://127.0.0.1:${servers.auth.address().port}`,
    calls: () => calls,
  };
}

const chat = { model: "test-model", messages: [{ role: "user", content: "test" }] };
const post = (body) => ({ method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
const localAuth = { "X-CetiRun-Pi-Auth": "1", "Content-Type": "application/json" };

test("health and model discovery do not execute the agent", async (t) => {
  const f = await fixture(t);
  assert.deepEqual(await (await fetch(f.base + "/healthz")).json(), { status: "ok" });
  assert.equal((await (await fetch(f.base + "/v1/models")).json()).data[0].id, "test-model");
  assert.equal(f.calls(), 0);
});

test("chat invokes the adapter once and streams valid final events", async (t) => {
  const f = await fixture(t);
  const response = await fetch(f.base + "/v1/chat/completions", post(chat));
  assert.equal(response.status, 200);
  assert.equal((await response.json()).choices[0].message.content, "real adapter result");
  const streamed = await fetch(f.base + "/v1/chat/completions", post({ ...chat, stream: true }));
  const text = await streamed.text();
  assert.equal(text.split("data: [DONE]").length, 2);
  assert.match(text, /"finish_reason":"stop"/);
  assert.equal(f.calls(), 2);
});

test("invalid extensions and history are rejected before execution", async (t) => {
  const f = await fixture(t);
  for (const value of [{ ...chat, tools: [] }, { ...chat, messages: [] }, { ...chat, stream: "yes" }]) {
    assert.throws(() => validateChat(value));
    assert.equal((await fetch(f.base + "/v1/chat/completions", post(value))).status, 400);
  }
  assert.equal(f.calls(), 0);
});

test("HTTP Memory restrictions reach the native runtime and cannot widen policy", async t => {
  const seen = [];
  const f = await fixture(t, { run: async input => { seen.push(input.memory); return { text: "fixture" }; } });
  for (const mode of ["off", "no-capture"]) {
    const response = await fetch(f.base + "/v1/chat/completions", { ...post(chat), headers: { ...post(chat).headers, "X-CetiRun-Memory-Mode": mode } });
    assert.equal(response.status, 200); await response.json();
  }
  assert.deepEqual(seen, [{ noMemory: true, noCapture: true }, { noMemory: false, noCapture: true }]);
  const response = await fetch(f.base + "/v1/chat/completions", { ...post(chat), headers: { ...post(chat).headers, "X-CetiRun-Memory-Mode": "enabled" } });
  assert.equal(response.status, 400);
  assert.equal(seen.length, 2);
});

test("auth is separate, rejects browser requests, and requires policy consent", async (t) => {
  let logins = 0;
  const f = await fixture(t, { login: async () => { logins++; } });
  assert.equal((await fetch(f.base + "/v1/login/start", post({ allowModelPolicyUpdates: true }))).status, 404);
  assert.equal((await fetch(f.auth + "/v1/status")).status, 403);
  assert.equal((await fetch(f.auth + "/v1/status", { headers: { ...localAuth, Origin: "https://attacker.example" } })).status, 403);
  assert.equal((await fetch(f.auth + "/v1/login/start", { ...post({}), headers: localAuth })).status, 400);
  const accepted = await fetch(f.auth + "/v1/login/start", { ...post({ allowModelPolicyUpdates: true }), headers: localAuth });
  assert.equal(accepted.status, 202);
  assert.equal(logins, 1);
  const status = await (await fetch(f.auth + "/v1/status", { headers: localAuth })).json();
  assert.deepEqual(status.skills, ["cetirun-memory"]);
  assert.equal("credential" in status, false);
});

test("failures are redacted and never replayed", async (t) => {
  let calls = 0;
  const f = await fixture(t, { run: async () => { calls++; throw new Error("sensitive-token-sentinel"); } });
  const response = await fetch(f.base + "/v1/chat/completions", post(chat));
  assert.equal(response.status, 502);
  assert.doesNotMatch(await response.text(), /sensitive-token-sentinel/);
  assert.equal(calls, 1);
});

test("client cancellation aborts the real adapter signal", async (t) => {
  let cancelled;
  let started;
  const seenAbort = new Promise((resolve) => { cancelled = resolve; });
  const seenStart = new Promise((resolve) => { started = resolve; });
  const f = await fixture(t, { run: ({ signal }) => new Promise((_, reject) => {
    started();
    signal.addEventListener("abort", () => { cancelled(); reject(signal.reason); }, { once: true });
  }) });
  const abort = new AbortController();
  const response = fetch(f.base + "/v1/chat/completions", { ...post(chat), signal: abort.signal }).catch(() => {});
  await seenStart;
  abort.abort();
  await Promise.race([seenAbort, new Promise((_, reject) => {
    const timer = setTimeout(() => reject(new Error("adapter was not cancelled")), 2000);
    timer.unref();
  })]);
  await response;
});
