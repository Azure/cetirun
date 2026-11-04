// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";

// Input is synthetic fixture configuration on stdin. Run against a loopback
// HTTPS proxy and two isolated candidate runtimes; never a user's browser profile.
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || "playwright");

async function main() {
  const input = JSON.parse(fs.readFileSync(0, "utf8"));
  const origin = new URL(input.origin);
  assert.equal(origin.protocol, "https:");
  assert.ok(["127.0.0.1", "localhost"].includes(origin.hostname), "use the isolated loopback fixture");
  assert.equal(input.instances.length, 2);
  assert.notEqual(input.instances[0].password, input.instances[1].password);
  fs.mkdirSync(input.outputDir, { recursive: true, mode: 0o700 });
  const browser = await chromium.launch({ headless: true, executablePath: input.executablePath });
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 1000 } });
  const pages = [], escaped = [], errors = [], failedResponses = [], blockedFonts = [], blockedCatalogs = [], assets = new Set();
  const pluginCatalog = "https://nousresearch.github.io/hermes-agent/docs/api/plugins.json";
  const redact = text => input.instances.reduce((value, instance) => value.replaceAll(instance.password, "[redacted]"), String(text))
    .replace(/[A-Za-z0-9_-]{40,}/g, "[redacted]");
  await context.route("**/*", async route => {
    const request = route.request();
    const url = new URL(request.url());
    if (["data:", "blob:"].includes(url.protocol)) return route.continue();
    if (url.hostname === "fonts.googleapis.com" || url.hostname === "fonts.gstatic.com") {
      blockedFonts.push(url.hostname);
      return route.abort();
    }
    if (url.href === pluginCatalog) {
      blockedCatalogs.push(url.href);
      return route.abort();
    }
    if (url.origin !== origin.origin || !input.instances.some(instance => url.pathname.startsWith(instance.prefix + "/"))) {
      escaped.push(url.origin + url.pathname);
      return route.abort();
    }
    return route.continue();
  });
  try {
    for (const [index, instance] of input.instances.entries()) {
      assert.match(instance.prefix, /^\/ui\/[a-z][a-z0-9-]*$/);
      const page = await context.newPage();
      pages.push(page);
      page.on("pageerror", error => errors.push(redact(error.message)));
      page.on("console", message => {
        const url = message.location().url || "";
        if (message.type() === "error" && !url.startsWith("https://fonts.googleapis.com/") && url !== pluginCatalog && !url.endsWith("/auth/password-login")) errors.push(redact(message.text()));
      });
      page.on("websocket", connection => {
        const url = new URL(connection.url());
        if (url.host !== origin.host || !url.pathname.startsWith(instance.prefix + "/")) escaped.push("websocket:" + url.pathname);
      });
      page.on("response", response => {
        const url = new URL(response.url());
        if (/\.(js|css)$/.test(url.pathname) && response.status() === 200) assets.add(url.pathname);
        if (response.status() >= 400) failedResponses.push({ path: url.pathname, status: response.status() });
      });
      await page.goto(origin.origin + instance.prefix + "/", { waitUntil: "domcontentloaded" });
      await page.locator('input[name="username"]').waitFor();
      const before = await context.cookies(origin.origin + instance.prefix + "/");
      assert.ok(!before.some(cookie => cookie.name.includes("hermes_webapp_signin_")), "another instance logged this browser in");
      await page.locator('input[name="username"]').fill("cetirun");
      await page.locator('input[name="password"]').fill(instance.password);
      await page.locator('form[data-provider="basic"] button[type="submit"]').click();
      await page.waitForFunction(prefix => window.__HERMES_BASE_PATH__ === prefix && document.querySelector("#root")?.childElementCount > 0, instance.prefix);
      await page.locator('input[name="password"]').waitFor({ state: "detached" });
      await page.waitForFunction(() => document.querySelectorAll("button").length > 3 && document.body.innerText.trim() !== "CONNECTING");
      const decline = page.getByRole("button", { name: "No thanks", exact: true });
      if (await decline.isVisible()) await decline.click();
      const auth = await page.evaluate(async prefix => {
        const response = await fetch(prefix + "/api/auth/me");
        return { status: response.status, provider: (await response.json()).provider };
      }, instance.prefix);
      assert.deepEqual(auth, { status: 200, provider: "basic" });
      await ping(page, instance.prefix);
      if (input.files) await files(page, instance.prefix, index);
      if (input.sse) await stream(page, instance);
      const cookieJar = await context.cookies(origin.origin + instance.prefix + "/");
      const authCookies = cookieJar.filter(cookie => cookie.name.startsWith("__Secure-hermes_"));
      assert.ok(authCookies.length >= 3);
      assert.ok(authCookies.every(cookie => cookie.path === instance.prefix || cookie.path === instance.prefix + "/"));
      await page.getByText("Scheduled jobs", { exact: true }).click({ trial: true });
      if (input.artifact && index === 0) {
        const composer = page.locator('[data-slot="composer-rich-input"][contenteditable="true"]');
        await composer.fill("Create the synthetic HTML fixture artifact.");
        await composer.press("Enter");
        const card = page.locator('[data-slot="aui_artifact-card"]', { hasText: "Hermes fixture artifact" });
        await card.waitFor({ timeout: 60000 });
        await card.click();
        const preview = await artifactFrame(page);
        await preview.getByRole("button", { name: "Count: 0" }).click();
        await preview.getByRole("button", { name: "Count: 1" }).waitFor();
      }
      if (instance.pluginSHA) {
        const identifier = "file:///home/vibe/qualification-plugin";
        const inspected = await rpc(page, instance.prefix, "plugins.manage", { action: "inspect", identifier });
        assert.ok(inspected.ok && inspected.agent && inspected.desktop && inspected.sha === instance.pluginSHA);
        const installed = await rpc(page, instance.prefix, "plugins.manage", {
          action: "install", identifier, ref: instance.pluginSHA, inspected_sha: instance.pluginSHA, enable: true,
        });
        assert.equal(installed.ok, true);
        assert.equal(await page.evaluate(async prefix => (await fetch(prefix + "/api/desktop-plugins/reconcile", { method: "POST" })).status, instance.prefix), 200);
        await page.goto(origin.origin + instance.prefix + "/#/capabilities?tab=plugins&plugin=cetirun-qualification", { waitUntil: "domcontentloaded" });
        const toggle = page.getByRole("switch", { name: /Desktop:.*qualification/i });
        await toggle.waitFor();
        assert.equal(await toggle.getAttribute("aria-checked"), "false", "another instance enabled this plugin");
        if (index === 0) {
          await toggle.click();
          await page.getByTestId("cetirun-qualification-plugin").waitFor();
          await toggle.click();
          await page.getByTestId("cetirun-qualification-plugin").waitFor({ state: "detached" });
          await page.reload({ waitUntil: "domcontentloaded" });
          await page.goto(origin.origin + instance.prefix + "/#/capabilities?tab=plugins&plugin=cetirun-qualification", { waitUntil: "domcontentloaded" });
          await toggle.waitFor();
          assert.equal(await toggle.getAttribute("aria-checked"), "false");
          await toggle.click();
          await page.getByTestId("cetirun-qualification-plugin").waitFor();
        } else {
          assert.equal(await page.getByTestId("cetirun-qualification-plugin").count(), 0);
        }
      } else {
        assert.equal(await page.getByTestId("cetirun-qualification-plugin").count(), 0);
      }
      await page.screenshot({ path: path.join(input.outputDir, `instance-${index + 1}.png`), fullPage: true });
      await page.reload({ waitUntil: "domcontentloaded" });
      await page.waitForFunction(prefix => window.__HERMES_BASE_PATH__ === prefix && document.querySelector("#root")?.childElementCount > 0, instance.prefix);
      await ping(page, instance.prefix);
      if (instance.pluginSHA && index === 0) await page.getByTestId("cetirun-qualification-plugin").waitFor();
      assert.ok([...assets].some(asset => asset.startsWith(instance.prefix + "/")), "no real bundle loaded");
    }
    for (const [index, page] of pages.entries()) {
      const instance = input.instances[index];
      const invalid = await page.evaluate(async ({ prefix, password }) => {
        const response = await fetch(prefix + "/auth/password-login", { method: "POST",
          headers: { "Content-Type": "application/json" }, body: JSON.stringify({ provider: "basic", username: "cetirun", password }) });
        return response.status;
      }, { prefix: instance.prefix, password: input.instances[1 - index].password });
      assert.equal(invalid, 401, "another instance's password was accepted");
      await page.getByText("Scheduled jobs", { exact: true }).click();
      await page.waitForURL(url => url.hash.length > 1);
      assert.ok(page.url().startsWith(origin.origin + instance.prefix + "/"));
      await page.waitForFunction(prefix => window.__HERMES_BASE_PATH__ === prefix && document.querySelector("#root")?.childElementCount > 0, instance.prefix);
      await page.reload({ waitUntil: "domcontentloaded" });
      await page.getByRole("button", { name: "New cron", exact: true }).click({ trial: true });
      await ping(page, instance.prefix);
      await page.screenshot({ path: path.join(input.outputDir, `deep-link-${index + 1}.png`), fullPage: true });
    }
    assert.deepEqual(escaped, [], "a request escaped the instance prefixes");
    assert.deepEqual(errors, [], "the real browser encountered JavaScript errors");
    const result = { passed: true, instances: 2, assets: assets.size, login: true, cookiePaths: true,
      separatePasswords: true, reload: true, deepLinks: true, websocket: true, escapedRequests: escaped.length,
      optionalExternalFontsBlocked: blockedFonts.length, optionalPluginCatalogsBlocked: blockedCatalogs.length,
      artifact: Boolean(input.artifact), plugin: Boolean(input.instances[0].pluginSHA),
      pluginDecisionsIsolated: Boolean(input.instances[0].pluginSHA), files: Boolean(input.files), sse: Boolean(input.sse) };
    fs.writeFileSync(path.join(input.outputDir, "browser-result.json"), JSON.stringify(result, null, 2) + "\n", { mode: 0o600 });
    console.log(JSON.stringify(result));
  } catch (error) {
    const state = [];
    for (const [index, page] of pages.entries()) {
      state.push({ path: new URL(page.url()).pathname, body: redact(await page.locator("body").innerText()) });
      await page.screenshot({ path: path.join(input.outputDir, `failure-${index + 1}.png`), fullPage: true });
    }
    fs.writeFileSync(path.join(input.outputDir, "browser-failure.json"), JSON.stringify({ escaped, errors, failedResponses, state }, null, 2), { mode: 0o600 });
    throw error;
  } finally {
    await context.close();
    await browser.close();
  }
}

async function ping(page, prefix) {
  const result = await page.evaluate(async prefix => {
    const response = await fetch(prefix + "/api/auth/ws-ticket", { method: "POST" });
    if (response.status !== 200) return false;
    const ticket = (await response.json()).ticket;
    return new Promise(resolve => {
      const socket = new WebSocket("wss://" + location.host + prefix + "/api/ws?ticket=" + encodeURIComponent(ticket));
      const timer = setTimeout(() => { socket.close(); resolve(false); }, 5000);
      socket.onopen = () => socket.send(JSON.stringify({ jsonrpc: "2.0", id: "browser-ping", method: "gateway.ping" }));
      socket.onmessage = event => {
        const message = JSON.parse(event.data);
        if (message.id === "browser-ping") { clearTimeout(timer); socket.close(); resolve(message.result?.ok === true); }
      };
      socket.onerror = () => { clearTimeout(timer); resolve(false); };
    });
  }, prefix);
  assert.equal(result, true, "browser WebSocket roundtrip failed");
}

async function files(page, prefix, index) {
  const result = await page.evaluate(async ({ prefix, index }) => {
    const path = "/home/vibe/browser-prefix-file.txt";
    const content = "Synthetic file for instance " + index;
    const uploaded = await fetch(prefix + "/api/files/upload", { method: "POST",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ path, data_url: "data:text/plain;base64," + btoa(content) }) });
    if (uploaded.status !== 200) return { upload: uploaded.status };
    const minted = await fetch(prefix + "/api/files/ticket", { method: "POST",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify({ route: "download", path }) });
    if (minted.status !== 200) return { ticket: minted.status };
    const { ticket } = await minted.json();
    const downloaded = await fetch(prefix + "/api/fs/download?" + new URLSearchParams({ path, ticket }));
    return { status: downloaded.status, matches: await downloaded.text() === content };
  }, { prefix, index });
  assert.deepEqual(result, { status: 200, matches: true }, "prefixed file upload/download failed");
}

async function stream(page, instance) {
  const result = await page.evaluate(async ({ prefix, password }) => {
    const response = await fetch(prefix + "/v1/chat/completions", { method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + password },
      body: JSON.stringify({ model: "synthetic-model", messages: [{ role: "user", content: "Answer the synthetic API request." }], stream: true }),
      signal: AbortSignal.timeout(60000) });
    const content = await response.text();
    return { status: response.status, sse: response.headers.get("content-type")?.includes("text/event-stream"),
      data: content.includes("data: "), done: content.includes("[DONE]") };
  }, instance);
  assert.deepEqual(result, { status: 200, sse: true, data: true, done: true }, "prefixed native API stream failed");
}

async function artifactFrame(page) {
  for (let attempt = 0; attempt < 100; attempt++) {
    for (const frame of page.frames()) {
      if (frame !== page.mainFrame() && await frame.getByRole("heading", { name: "Hermes fixture artifact" }).isVisible()) return frame;
    }
    await page.waitForTimeout(100);
  }
  throw new Error("the nested sandbox preview did not render the artifact");
}

async function rpc(page, prefix, method, params) {
  return page.evaluate(async ({ prefix, method, params }) => {
    const response = await fetch(prefix + "/api/auth/ws-ticket", { method: "POST" });
    if (response.status !== 200) throw new Error("browser RPC ticket refused");
    const { ticket } = await response.json();
    return new Promise((resolve, reject) => {
      const socket = new WebSocket("wss://" + location.host + prefix + "/api/ws?ticket=" + encodeURIComponent(ticket));
      const timer = setTimeout(() => { socket.close(); reject(new Error("browser RPC deadline exceeded")); }, 90000);
      socket.onopen = () => socket.send(JSON.stringify({ jsonrpc: "2.0", id: "browser-qualification", method, params }));
      socket.onmessage = event => {
        const message = JSON.parse(event.data);
        if (message.id !== "browser-qualification") return;
        clearTimeout(timer);
        socket.close();
        if (message.error) reject(new Error("browser RPC failed: " + message.error.code));
        else resolve(message.result);
      };
      socket.onerror = () => { clearTimeout(timer); reject(new Error("browser RPC socket failed")); };
    });
  }, { prefix, method, params });
}

main().catch(error => { console.error(error.stack); process.exitCode = 1; });
