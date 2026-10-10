// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";

const http = require("node:http");
const args = process.argv.slice(2);
const [rawPort, path, rawTimeout] = args;
const port = Number(rawPort);
const timeoutMs = Number(rawTimeout);
if (
  args.length !== 3 ||
  !/^[0-9]+$/.test(rawPort) || !Number.isInteger(port) || port < 1 || port > 65535 ||
  !["/healthz", "/readyz", "/startupz"].includes(path) ||
  !/^[0-9]+$/.test(rawTimeout) || !Number.isInteger(timeoutMs) ||
  timeoutMs < 1 || timeoutMs > 2147483647
) {
  console.error("native health probe: invalid probe arguments");
  process.exit(1);
}

let finished = false;
let request;
const deadline = setTimeout(() => finish(1, "request timed out"), timeoutMs);

function finish(code, message) {
  if (finished) return;
  finished = true;
  clearTimeout(deadline);
  if (request) request.destroy();
  if (message) console.error(`native health probe: ${message}`);
  process.exit(code);
}

// Never use a Pod IP, the global proxy agent, or model credentials for probes.
request = http.get({ hostname: "127.0.0.1", port, path, agent: false }, (response) => {
  response.resume();
  if (response.statusCode >= 200 && response.statusCode < 400) {
    finish(0);
  } else {
    finish(1, `HTTP ${response.statusCode}`);
  }
});
request.on("error", () => finish(1, "connection failed"));
