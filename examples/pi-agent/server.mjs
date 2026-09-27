// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import http from "node:http";
import { randomUUID } from "node:crypto";
import { pathToFileURL } from "node:url";

const BODY_LIMIT = 1024 * 1024;
const OUTPUT_LIMIT = 4 * 1024 * 1024;

function json(res, status, body) {
  if (res.destroyed) return;
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(body));
}

async function body(req) {
  let size = 0;
  const chunks = [];
  for await (const chunk of req) {
    size += chunk.length;
    if (size > BODY_LIMIT) throw new Error("Request too large");
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
}

export function validateChat(value) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).some((key) => !["model", "messages", "stream"].includes(key)) ||
    typeof value.model !== "string" || !value.model ||
    (value.stream !== undefined && typeof value.stream !== "boolean") ||
    !Array.isArray(value.messages) || value.messages.length === 0 || value.messages.length > 100 ||
    value.messages.at(-1)?.role !== "user") throw new Error("Invalid chat request");
  for (const message of value.messages) {
    if (!message || typeof message !== "object" ||
      Object.keys(message).some((key) => !["role", "content"].includes(key)) ||
      !["system", "user", "assistant"].includes(message.role) ||
      typeof message.content !== "string") throw new Error("Invalid text message");
  }
  return value;
}

export function createServers(runtime) {
  let busy = false;
  let login = { state: "idle" };
  let loginAbort;
  const runs = new Set();
  const api = http.createServer(async (req, res) => {
    try {
      if (req.method === "GET" && req.url === "/healthz") return json(res, 200, { status: "ok" });
      if (req.method === "GET" && req.url === "/v1/models") {
        const models = await runtime.models(AbortSignal.timeout(10_000));
        return json(res, 200, { object: "list", data: models.map((id) => ({ id, object: "model", owned_by: "github-copilot" })) });
      }
      if (req.method !== "POST" || req.url !== "/v1/chat/completions") return json(res, 404, { error: { message: "Not found" } });
      if (busy || loginAbort) return json(res, 429, { error: { message: "Agent or login is busy" } });
      let input;
      try { input = validateChat(await body(req)); } catch { return json(res, 400, { error: { message: "Invalid text chat request" } }); }
      if (busy || loginAbort) return json(res, 429, { error: { message: "Agent or login is busy" } });
      busy = true;
      const abort = new AbortController();
      runs.add(abort);
      const signal = AbortSignal.any([abort.signal, AbortSignal.timeout(120_000)]);
      const closed = () => { if (!res.writableEnded) abort.abort(); };
      res.on("close", closed);
      const id = `chatcmpl-${randomUUID()}`;
      const created = Math.floor(Date.now() / 1000);
      let outputBytes = 0;
      const chunk = (delta, finish_reason = null) => {
        if (res.destroyed) return;
        const data = JSON.stringify({ id, object: "chat.completion.chunk", created, model: input.model,
          choices: [{ index: 0, delta, finish_reason }] });
        outputBytes += Buffer.byteLength(data);
        if (outputBytes > OUTPUT_LIMIT || res.writableLength > 256 * 1024) {
          abort.abort();
          return;
        }
        res.write(`data: ${data}\n\n`);
      };
      try {
        if (input.stream) {
          res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-store", Connection: "close" });
          chunk({ role: "assistant" });
        }
        const result = await runtime.run({ ...input, signal, onText: input.stream ? (text) => chunk({ content: text }) : undefined });
        signal.throwIfAborted();
        if (input.stream) {
          chunk({}, "stop");
          signal.throwIfAborted();
          res.end("data: [DONE]\n\n");
        } else {
          if (Buffer.byteLength(result.text) > OUTPUT_LIMIT) throw new Error("Output limit exceeded");
          json(res, 200, { id, object: "chat.completion", created, model: input.model,
            choices: [{ index: 0, message: { role: "assistant", content: result.text }, finish_reason: "stop" }] });
        }
      } catch {
        if (res.headersSent) res.destroy();
        else json(res, 502, { error: { message: "Pi run failed or Copilot login is required" } });
      } finally {
        busy = false;
        runs.delete(abort);
        res.off("close", closed);
      }
    } catch {
      if (res.headersSent) res.destroy();
      else json(res, 503, { error: { message: "Pi service is unavailable" } });
    }
  });
  const auth = http.createServer(async (req, res) => {
    try {
      const host = new URL(`http://${req.headers.host}`).hostname;
      if (!["localhost", "127.0.0.1", "[::1]"].includes(host) || req.headers.origin ||
        req.headers["x-cetirun-pi-auth"] !== "1") return json(res, 403, { error: "Local auth client required" });
      if (req.method === "GET" && req.url === "/v1/status") {
        return json(res, 200, { ...login, authenticated: await runtime.authenticated(), skills: runtime.skills() });
      }
      if (req.method === "POST" && req.url === "/v1/login/start") {
        const request = await body(req);
        if (request.allowModelPolicyUpdates !== true) return json(res, 400, { error: "Explicit model-policy consent required" });
        if (loginAbort) return json(res, 409, { error: "Login is already active" });
        if (busy) return json(res, 409, { error: "Agent is busy" });
        loginAbort = new AbortController();
        const controller = loginAbort;
        login = { state: "starting" };
        void runtime.login(AbortSignal.any([controller.signal, AbortSignal.timeout(15 * 60_000)]), (event) => {
          login = event;
        }).then(() => { login = { state: "authenticated" }; }).catch(() => {
          login = { state: controller.signal.aborted ? "cancelled" : "failed" };
        }).finally(() => { loginAbort = undefined; });
        return json(res, 202, { state: "starting" });
      }
      if (req.method === "POST" && req.url === "/v1/login/cancel") {
        loginAbort?.abort();
        return json(res, 200, { state: "cancelling" });
      }
      json(res, 404, { error: "Not found" });
    } catch {
      json(res, 400, { error: "Invalid authentication request" });
    }
  });
  for (const server of [api, auth]) {
    server.requestTimeout = 30_000;
    server.headersTimeout = 10_000;
    server.keepAliveTimeout = 1000;
    server.maxConnections = 32;
  }
  return {
    api, auth,
    async close() {
      loginAbort?.abort();
      for (const abort of runs) abort.abort();
      await Promise.all([api, auth].map((server) => new Promise((resolve) => {
        server.close(resolve);
        server.closeAllConnections();
      })));
    },
  };
}

async function main() {
  if (process.argv.includes("--help")) {
    console.log("Pi Custom example: HOME and PORT are required. API binds 0.0.0.0:PORT; auth binds 127.0.0.1:9099.");
    return;
  }
  const port = Number(process.env.PORT);
  if (!process.env.HOME || !Number.isInteger(port) || port < 1 || port > 65535 || port === 9099) {
    throw new Error("HOME and a valid PORT other than 9099 are required");
  }
  process.umask(0o077);
  process.env.PI_TELEMETRY = "0";
  const { createPiRuntime } = await import("./pi-runtime.mjs");
  const servers = createServers(await createPiRuntime(process.env.HOME));
  try {
    for (const [server, portNumber, host] of [[servers.api, port, "0.0.0.0"], [servers.auth, 9099, "127.0.0.1"]]) {
      await new Promise((resolve, reject) => {
        server.once("error", reject);
        server.listen(portNumber, host, resolve);
      });
    }
  } catch (error) {
    await servers.close();
    throw error;
  }
  const stop = () => { void servers.close(); };
  process.once("SIGTERM", stop);
  process.once("SIGINT", stop);
  console.log("Pi example ready; no credential or prompt logging is enabled.");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(() => { console.error("Pi example startup failed; check HOME, context and PORT."); process.exitCode = 1; });
}
