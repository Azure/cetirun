// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { randomUUID } from "node:crypto";
import { createHTTPServer, readJSON, writeJSON } from "./http.mjs";

const CHAT_TIMEOUT_MS = 120_000;
const OUTPUT_LIMIT = 4 * 1024 * 1024;
const STREAM_BUFFER_LIMIT = 256 * 1024;

/**
 * The CetiRun HTTP contract. The supplied runtime implements models(signal)
 * and run({ model, messages, signal, onText }); this module owns HTTP and SSE.
 * Login operations use auth-api.mjs; the platform supplies the browser terminal.
 */
export function createAgentAPI(runtime, activity) {
  const runs = new Set();
  const server = createHTTPServer(async (request, response) => {
    try {
      switch (`${request.method} ${request.url}`) {
        case "GET /healthz":
          return writeJSON(response, 200, { status: "ok" });
        case "GET /v1/models": {
          const models = await runtime.models(AbortSignal.timeout(10_000));
          return writeJSON(response, 200, {
            object: "list",
            data: models.map((id) => ({ id, object: "model" })),
          });
        }
        case "POST /v1/chat/completions":
          return await handleChat(request, response, runtime, activity, runs);
        default:
          return writeJSON(response, 404, { error: { message: "Not found" } });
      }
    } catch {
      if (response.headersSent) response.destroy();
      else writeJSON(response, 503, { error: { message: "Pi service is unavailable" } });
    }
  });
  return {
    server,
    abort() {
      for (const controller of runs) controller.abort();
    },
  };
}

export function validateChat(value) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).some((key) => !["model", "messages", "stream"].includes(key)) ||
    typeof value.model !== "string" || !value.model ||
    (value.stream !== undefined && typeof value.stream !== "boolean") ||
    !Array.isArray(value.messages) || value.messages.length === 0 || value.messages.length > 100 ||
    value.messages.at(-1)?.role !== "user") {
    throw new Error("Invalid chat request");
  }
  for (const message of value.messages) {
    if (!message || typeof message !== "object" ||
      Object.keys(message).some((key) => !["role", "content"].includes(key)) ||
      !["system", "user", "assistant"].includes(message.role) ||
      typeof message.content !== "string") {
      throw new Error("Invalid text message");
    }
  }
  return value;
}

async function handleChat(request, response, runtime, activity, runs) {
  const rejectBusy = () => writeJSON(response, 429, {
    error: { message: "Agent or login is busy" },
  });
  if (activity.chat || activity.login) return rejectBusy();

  let input;
  try {
    if (![undefined, "off", "no-capture"].includes(request.headers["x-cetirun-memory-mode"])) throw new Error("Invalid Memory restriction");
    input = validateChat(await readJSON(request));
  } catch {
    return writeJSON(response, 400, { error: { message: "Invalid text chat request" } });
  }
  // Another request or login can start while the request body is being read.
  if (activity.chat || activity.login) return rejectBusy();

  activity.chat = true;
  const controller = new AbortController();
  const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(CHAT_TIMEOUT_MS)]);
  runs.add(controller);
  const disconnected = () => {
    if (!response.writableEnded) controller.abort();
  };
  response.on("close", disconnected);
  const completion = {
    id: `chatcmpl-${randomUUID()}`,
    created: Math.floor(Date.now() / 1000),
    model: input.model,
  };
  try {
    const stream = input.stream ? startStream(response, completion, controller) : undefined;
    const result = await runtime.run({
      ...input,
      memory: { noMemory: request.headers["x-cetirun-memory-mode"] === "off", noCapture: ["off", "no-capture"].includes(request.headers["x-cetirun-memory-mode"]) },
      signal,
      onText: stream ? (text) => stream.chunk({ content: text }) : undefined,
    });
    signal.throwIfAborted();
    if (stream) {
      stream.chunk({}, "stop");
      signal.throwIfAborted();
      response.end("data: [DONE]\n\n");
    } else {
      if (Buffer.byteLength(result.text) > OUTPUT_LIMIT) throw new Error("Output limit exceeded");
      writeJSON(response, 200, {
        ...completion,
        object: "chat.completion",
        choices: [{
          index: 0,
          message: { role: "assistant", content: result.text },
          finish_reason: "stop",
        }],
      });
    }
  } catch {
    if (response.headersSent) response.destroy();
    else writeJSON(response, 502, { error: { message: "Pi run failed or model access is unavailable" } });
  } finally {
    activity.chat = false;
    runs.delete(controller);
    response.off("close", disconnected);
  }
}

function startStream(response, completion, controller) {
  let outputBytes = 0;
  const chunk = (delta, finishReason = null) => {
    if (response.destroyed) return;
    const data = JSON.stringify({
      ...completion,
      object: "chat.completion.chunk",
      choices: [{ index: 0, delta, finish_reason: finishReason }],
    });
    outputBytes += Buffer.byteLength(data);
    if (outputBytes > OUTPUT_LIMIT || response.writableLength > STREAM_BUFFER_LIMIT) {
      controller.abort();
      return;
    }
    response.write(`data: ${data}\n\n`);
  };
  response.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-store",
    Connection: "close",
  });
  chunk({ role: "assistant" });
  return { chunk };
}
