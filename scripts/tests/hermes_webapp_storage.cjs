// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { stripTypeScriptTypes } = require("node:module");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");

async function main() {
  assert.ok(process.env.CHROMIUM_EXECUTABLE, "set CHROMIUM_EXECUTABLE to a prepared Chromium binary");
  assert.ok(process.env.HERMES_QUALIFICATION_SOURCE, "set HERMES_QUALIFICATION_SOURCE to the pinned source");
  const module = stripTypeScriptTypes(fs.readFileSync(path.resolve(__dirname,
    "../../app/cetirun-chart/agents/hermes/compat/webapp_storage.ts"), "utf8"));
  const handoff = execFileSync("python3", ["-B", "-c", [
    "import ast, importlib.util, pathlib, sys",
    "spec = importlib.util.spec_from_file_location('patch', sys.argv[2])",
    "patch = importlib.util.module_from_spec(spec); spec.loader.exec_module(patch)",
    "source = pathlib.Path(sys.argv[1], 'hermes_cli/web_routers/webapp.py').read_text()",
    "tree = ast.parse(patch.prepare_handoff_patch(source))",
    "print(next(ast.literal_eval(node.value) for node in tree.body if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == '_WINDOW_SCRIPT' for target in node.targets)))"
  ].join("\n"), process.env.HERMES_QUALIFICATION_SOURCE,
  path.resolve(__dirname, "../../app/cetirun-chart/agents/hermes/compat/apply_webapp_storage.py")], { encoding: "utf8" });
  const server = http.createServer((request, response) => {
    if (request.url === "/scope.js") {
      response.writeHead(200, { "Content-Type": "text/javascript" });
      return response.end(module);
    }
    const url = new URL(request.url, "http://127.0.0.1");
    const prefix = url.pathname === "/native" ? undefined : url.pathname.split("/").slice(0, 3).join("/");
    if (url.pathname.endsWith("/webapp/window-session")) {
      assert.match(request.headers["x-hermes-window-ticket"], /^[A-Za-z0-9_-]{43}$/);
      response.writeHead(200, { "Content-Type": "application/json" });
      return response.end(JSON.stringify({ token: "g".repeat(43) }));
    }
    if (url.pathname.endsWith("/webapp/window")) {
      response.writeHead(200, { "Content-Type": "text/html" });
      return response.end(`<p id="status"></p><script>${handoff.replace("__BASE_PATH__", JSON.stringify(prefix))}</script>`);
    }
    const seed = url.searchParams.has("seed") ? `
      sessionStorage.setItem('hermes.webapp.session.v1:' + JSON.stringify([location.origin, '/ui/a']), 'fresh-launch-grant');
      sessionStorage.setItem('hermes.webapp.session.v1:' + JSON.stringify([location.origin, '/ui/b']), 'other-launch-grant');` : "";
    response.writeHead(200, { "Content-Type": "text/html" });
    response.end(`<script>window.__HERMES_BASE_PATH__ = ${JSON.stringify(prefix) || "undefined"};
      window.originalLocal = localStorage; window.originalSession = sessionStorage;
      window.originalChannel = BroadcastChannel; ${seed}</script>
      <script type="module">import { scopeWebappStorage } from '/scope.js';
      scopeWebappStorage(); scopeWebappStorage(); window.scopeReady = true;</script>`);
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const origin = "http://127.0.0.1:" + server.address().port;
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_EXECUTABLE, headless: true });
  try {
    const context = await browser.newContext();
    const page = async prefix => {
      const current = await context.newPage();
      await current.goto(origin + prefix);
      await current.waitForFunction(() => window.scopeReady);
      return current;
    };
    const native = await page("/native");
    assert.equal(await native.evaluate(() => localStorage === window.originalLocal &&
      sessionStorage === window.originalSession && BroadcastChannel === window.originalChannel), true);
    await native.evaluate(() => localStorage.setItem("hermes.desktop.pluginDecisions.v2", '{"legacy":true}'));
    const [a, anotherA, b] = await Promise.all([page("/ui/a"), page("/ui/a"), page("/ui/b")]);
    for (const current of [a, anotherA, b]) {
      assert.equal(await current.evaluate(() => localStorage.getItem("hermes.desktop.pluginDecisions.v2")), null);
      await current.evaluate(() => {
        window.events = [];
        addEventListener("storage", event => window.events.push({ key: event.key, value: event.newValue,
          local: event.storageArea === localStorage }));
        window.messages = [];
        window.channel = new BroadcastChannel("hermes:sessions");
        window.channel.onmessage = event => window.messages.push(event.data);
      });
    }
    await a.evaluate(() => {
      localStorage.setItem("draft", "instance-a");
      localStorage.property = 42;
      sessionStorage.setItem("session", "instance-a-session");
    });
    await anotherA.waitForFunction(() => window.events.some(event => event.key === "draft"));
    assert.deepEqual(await anotherA.evaluate(() => window.events.find(event => event.key === "draft")),
      { key: "draft", value: "instance-a", local: true });
    await b.evaluate(() => localStorage.setItem("draft", "instance-b"));
    assert.deepEqual(await a.evaluate(() => ({ value: localStorage.getItem("draft"), property: localStorage.property,
      keys: Object.keys(localStorage).sort(), length: localStorage.length, missing: localStorage.key(50) })),
    { value: "instance-a", property: "42", keys: ["draft", "property"], length: 2, missing: null });
    assert.equal(await b.evaluate(() => localStorage.getItem("draft")), "instance-b");
    await a.evaluate(() => { delete localStorage.property; localStorage.clear(); });
    await anotherA.waitForFunction(() => window.events.some(event => event.key === "draft" && event.value === null));
    assert.equal(await a.evaluate(() => localStorage.length), 0);
    assert.equal(await b.evaluate(() => localStorage.getItem("draft")), "instance-b");
    assert.equal(await native.evaluate(() => localStorage.getItem("hermes.desktop.pluginDecisions.v2")), '{"legacy":true}');

    await a.evaluate(() => window.channel.postMessage("from-a"));
    await anotherA.waitForFunction(() => window.messages.includes("from-a"));
    await b.evaluate(() => window.channel.postMessage("from-b"));
    await b.waitForTimeout(100);
    assert.deepEqual(await b.evaluate(() => window.events), []);
    assert.deepEqual(await b.evaluate(() => window.messages), []);
    assert.deepEqual(await a.evaluate(() => window.messages), []);
    assert.deepEqual(await anotherA.evaluate(() => window.messages), ["from-a"]);
    assert.equal(await a.evaluate(() => window.channel.name), "hermes:sessions");
    await a.evaluate(() => dispatchEvent(new StorageEvent("storage", { key: "hydrated", newValue: "yes" })));
    assert.equal(await a.evaluate(() => window.events.some(event => event.key === "hydrated")), true);

    await a.goto(origin + "/ui/b");
    await a.waitForFunction(() => window.scopeReady);
    assert.equal(await a.evaluate(() => sessionStorage.getItem("session")), null);
    await a.evaluate(() => { sessionStorage.setItem("other", "b"); sessionStorage.clear(); });
    await a.goto(origin + "/ui/a");
    await a.waitForFunction(() => window.scopeReady);
    assert.equal(await a.evaluate(() => sessionStorage.getItem("session")), "instance-a-session");
    await a.evaluate(() => sessionStorage.setItem("hermes.webapp.session.v1:" +
      JSON.stringify([location.origin, "/ui/a"]), "old-scoped-grant"));
    await a.goto(origin + "/ui/a?seed=1");
    await a.waitForFunction(() => window.scopeReady);
    assert.deepEqual(await a.evaluate(() => {
      const current = "hermes.webapp.session.v1:" + JSON.stringify([location.origin, "/ui/a"]);
      const other = "hermes.webapp.session.v1:" + JSON.stringify([location.origin, "/ui/b"]);
      return { fresh: sessionStorage.getItem(current) === "fresh-launch-grant",
        rawConsumed: window.originalSession.getItem(current) === null,
        otherPreserved: window.originalSession.getItem(other) === "other-launch-grant",
        otherHidden: sessionStorage.getItem(other) === null };
    }), { fresh: true, rawConsumed: true, otherPreserved: true, otherHidden: true });
    const handoffID = "11111111-2222-4333-8444-555555555555";
    await a.evaluate(id => {
      window.handoffDone = false;
      window.handoffChannel = new BroadcastChannel("hermes.webapp.window:" + id);
      window.handoffChannel.onmessage = event => {
        if (event.data.type === "ready") window.handoffChannel.postMessage({ ticket: "t".repeat(43) });
        if (event.data.type === "done") window.handoffDone = true;
      };
    }, handoffID);
    const child = await context.newPage();
    await child.goto(origin + "/ui/a/webapp/window#channel=" + handoffID);
    await child.waitForFunction(() => window.scopeReady);
    await a.waitForFunction(() => window.handoffDone);
    assert.equal(await child.evaluate(() => sessionStorage.getItem("hermes.webapp.session.v1:" +
      JSON.stringify([location.origin, "/ui/a"])) === "g".repeat(43)), true);
    await context.close();
    console.log(JSON.stringify({ passed: true, localAndSessionStorageIsolated: true, scopedEnumerationAndClear: true,
      sameInstanceEventsAndChannels: true, crossInstanceEventsAndChannelsBlocked: true, nativeDesktopUnchanged: true,
      currentInstanceLaunchGrantPreserved: true, nativeChildWindowHandoff: true }));
  } finally {
    await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
