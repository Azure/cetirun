// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";

const fs = require("node:fs");
const http = require("node:http");
const https = require("node:https");
const net = require("node:net");
const { spawn, execFileSync } = require("node:child_process");
const python = "/usr/local/lib/hermes-agent/venv/bin/python";
const home = "/home/vibe";
const configPath = home + "/.hermes/config.yaml";
const envPath = home + "/.hermes/.env";
const legacyPath = home + "/.hermes/gateway.json";
const children = [];
const results = [];
let stopping = false;
const deadline = setTimeout(() => finish(false, "native check deadline exceeded"), 150000);

function finish(ok, message) {
  if (stopping) return;
  stopping = true;
  clearTimeout(deadline);
  for (const child of children) child.process.kill("SIGTERM");
  const result = { ok, message, results };
  if (!ok) result.logs = children.map((child) => ({ command: child.command, tail: fs.readFileSync(child.log, "utf8").slice(-2000) }));
  console.log(JSON.stringify(result));
  setTimeout(() => {
    for (const child of children) child.process.kill("SIGKILL");
    process.exit(ok ? 0 : 1);
  }, 750);
}
process.on("SIGTERM", () => finish(false, "native check stopped"));

function start(command, args) {
  const log = "run/child-" + children.length + ".log";
  const fd = fs.openSync(log, "a", 0o600);
  const process = spawn(command, args, { stdio: ["ignore", fd, fd] });
  fs.closeSync(fd);
  children.push({ command, process, log });
  process.on("error", (error) => finish(false, "cannot start native process: " + error.code));
  process.on("exit", (code) => { if (!stopping) finish(false, command + " exited " + code); });
}

function request(port, path, tls, headers = {}) {
  return new Promise((resolve, reject) => {
    const req = (tls ? https : http).get({ hostname: "127.0.0.1", port, path, agent: false, headers, ...(tls || {}) }, (res) => {
      let body = "";
      res.on("data", (chunk) => { body = (body + chunk).slice(0, 65536); });
      res.on("end", () => resolve({ status: res.statusCode, html: /<html|<!doctype/i.test(body) }));
    });
    req.setTimeout(1500, () => req.destroy(new Error("request timed out")));
    req.on("error", reject);
  });
}

async function ready(tls, phase) {
  for (let attempt = 0; attempt < 80 && !stopping; attempt++) {
    try {
      const api = await request(15443, "/health", tls);
      const dashboard = await request(15444, "/login", tls);
      const terminal = await request(15445, "/term/", tls);
      if (api.status === 200 && dashboard.status === 200 && dashboard.html && terminal.status === 200 && terminal.html) {
        results.push({ phase, api: api.status, dashboard: dashboard.status, terminal: terminal.status });
        return;
      }
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("native API/dashboard/terminal did not become ready");
}

function refuses(port) {
  return new Promise((resolve, reject) => {
    const socket = net.connect({ host: "127.0.0.2", port });
    socket.setTimeout(1000, () => { socket.destroy(); resolve(); });
    socket.on("error", () => resolve());
    socket.on("connect", () => { socket.destroy(); reject(new Error("backend bound beyond loopback: " + port)); });
  });
}

function readConfig() {
  return JSON.parse(execFileSync(python, ["-c", "import json,sys,yaml; print(json.dumps(yaml.safe_load(open(sys.argv[1]))))", configPath], { encoding: "utf8", timeout: 5000 }));
}

function seedOverrides() {
  const config = fs.existsSync(configPath) ? readConfig() : {};
  config.user_preference = { retained: true, nested: ["one", "two"] };
  config.platforms = { ...(config.platforms || {}), api_server: {
    enabled: false, host: "0.0.0.0", port: 18080,
    extra: { host: "::", port: 18081, model_name: "retained-model-name" }, reply_to_mode: "all"
  } };
  config.dashboard = { host: "0.0.0.0", port: 18082, theme: "retained" };
  config.gateway = { ...(config.gateway || {}),
    platforms: { api_server: { enabled: false, host: "0.0.0.0", port: 18087, extra: { host: "::", port: 18088 } } },
    api_server: { enabled: false, host: "0.0.0.0", port: 18089, extra: { host: "::", port: 18090 }, max_concurrent_runs: 7 }
  };
  fs.writeFileSync(configPath, JSON.stringify(config), { mode: 0o600 });
  fs.writeFileSync(legacyPath, JSON.stringify({ platforms: { api_server: {
    enabled: false, host: "0.0.0.0", port: 18091, extra: { host: "::", port: 18092, _enabled_explicit: true }
  } }, legacy_preference: "retained" }), { mode: 0o600 });
  fs.writeFileSync(envPath, [
    "API_SERVER_KEY=synthetic-api-key", "API_SERVER_ENABLED=false", "API_SERVER_HOST=0.0.0.0",
    "API_SERVER_PORT=18083", "HERMES_DASHBOARD_HOST=0.0.0.0", "HERMES_DASHBOARD_PORT=18084",
    "TTYD_PORT=18085", "TTYD_BIND_ARGS=--interface 0.0.0.0", "USER_SETTING=retained", ""
  ].join("\n"), { mode: 0o600 });
}

function assertRetained() {
  const config = readConfig();
  const api = config.platforms.api_server;
  if (["enabled", "host", "port"].some((name) => name in api) || ["host", "port"].some((name) => name in api.extra)) {
    throw new Error("persisted API listener override survived authority enforcement");
  }
  if (!config.user_preference.retained || config.dashboard.theme !== "retained" ||
    api.reply_to_mode !== "all" || api.extra.model_name !== "retained-model-name") throw new Error("unrelated HOME configuration was lost");
  const legacy = JSON.parse(fs.readFileSync(legacyPath, "utf8"));
  for (const entry of [config.gateway.platforms.api_server, config.gateway.api_server, legacy.platforms.api_server]) {
    if (["enabled", "host", "port"].some((key) => key in entry) ||
      ["host", "port", "_enabled_explicit"].some((key) => key in entry.extra)) throw new Error("an API listener alias survived authority enforcement");
  }
  if (config.gateway.api_server.max_concurrent_runs !== 7 || legacy.legacy_preference !== "retained") throw new Error("unrelated gateway configuration was lost");
  const env = fs.readFileSync(envPath, "utf8");
  if (/^API_SERVER_(HOST|PORT|ENABLED)=/m.test(env) || !env.includes("USER_SETTING=retained") ||
    !env.includes("HERMES_DASHBOARD_PORT=18084") || !env.includes("TTYD_PORT=18085")) throw new Error("HOME environment authority or retention failed");
}

async function bindings(phase) {
  for (const port of [8642, 9119, 7681]) {
    await refuses(port);
    results.push({ phase, loopbackOnly: port });
  }
}

async function run(input) {
  fs.mkdirSync(home + "/.hermes", { recursive: true });
  fs.mkdirSync(home + "/.aks/skills", { recursive: true });
  fs.mkdirSync(home + "/.cache", { recursive: true });
  fs.mkdirSync("run", { recursive: true });
  fs.mkdirSync("tls", { recursive: true, mode: 0o700 });
  if (input.retained) seedOverrides();
  fs.writeFileSync("generated.env", "API_SERVER_KEY=synthetic-api-key\nAPI_SERVER_CORS_ORIGINS=https://example.com\n", { mode: 0o600 });
  execFileSync(python, ["/etc/cetirun/hermes-apply-config.py", "--home", home, "--config", "/candidate/config.yaml",
    "--env", "/work/generated.env", "--gateway-key-env", "CETIRUN_GATEWAY_API_KEY", "--ingress-mtls"], { timeout: 10000 });
  if (input.retained) assertRetained();
  for (const [file, contents] of Object.entries(input.material)) fs.writeFileSync("tls/" + file, contents, { mode: 0o600 });
  fs.writeFileSync("envoy.json", JSON.stringify(input.bootstrap));
  // Only scratch paths are relocated; source remains read-only.
  fs.writeFileSync("entrypoint.sh", fs.readFileSync("/candidate/entrypoint.sh", "utf8").replaceAll("/tmp/", "/work/run/"), { mode: 0o500 });
  start("/bin/bash", ["/work/entrypoint.sh"]);
  start("/opt/envoy", ["--disable-hot-restart", "--concurrency", "1", "--log-level", "error", "-c", "/work/envoy.json"]);
  const tls = { ca: input.material["ca.crt"], cert: input.clientCert, key: input.clientKey, servername: input.serverName, minVersion: "TLSv1.3" };
  await ready(tls, input.retained ? "retained" : "fresh");
  await bindings("initial");
  if ((await request(15443, "/v1/models", tls, { Authorization: "Bearer synthetic-api-key" })).status !== 200) throw new Error("native API models failed");
  if ((await request(15444, "/", tls, { Authorization: "Basic " + Buffer.from("cetirun:synthetic-api-key").toString("base64") })).status !== 200) throw new Error("native dashboard failed");
  if (input.retained) {
    seedOverrides();
    execFileSync("supervisorctl", ["-c", "/work/run/supervisord.conf", "restart", "all"], { timeout: 30000 });
    await ready(tls, "supervised-restart");
    await bindings("supervised-restart");
    assertRetained();
  }
  for (const port of [15443, 15444, 15445]) {
    for (const kind of ["missing-client", "wrong-client-uri", "wrong-server-san", "plaintext"]) {
      let options = { ...tls };
      if (kind === "missing-client") { delete options.cert; delete options.key; }
      if (kind === "wrong-client-uri") { options.cert = input.wrongCert; options.key = input.wrongKey; }
      if (kind === "wrong-server-san") options.servername = "other.example.com";
      if (kind === "plaintext") options = null;
      let rejected = false;
      try { await request(port, "/", options); } catch { rejected = true; }
      if (!rejected) throw new Error(kind + " accepted on " + port);
      results.push({ rejection: kind, port });
    }
  }
  const certificateReady = await new Promise((resolve) => require("/candidate/ingress-health.cjs")(resolve, "/work/tls/", 15440, input.serverName));
  if (!certificateReady) throw new Error("candidate certificate readiness failed");
  finish(true, "native Hermes listener authority, HOME retention and TLS traversal verified");
}

let input = "";
process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => { input += chunk; });
process.stdin.on("end", () => run(JSON.parse(input)).catch((error) => finish(false, error.message)));
