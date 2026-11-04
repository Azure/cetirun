// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";
const fs = require("node:fs"), path = require("node:path"), http = require("node:http"), https = require("node:https");
const directory = process.argv[2];
function target(request) {
  const backends = JSON.parse(fs.readFileSync(path.join(directory, "backends.json"), "utf8"));
  const prefix = Object.keys(backends).find(prefix => request.url.startsWith(prefix + "/"));
  if (!prefix) return null;
  return { ...backends[prefix], path: request.url.slice(prefix.length), method: request.method,
    headers: { ...request.headers, "x-forwarded-prefix": prefix, "x-forwarded-proto": "https" } };
}
const server = https.createServer({ key: fs.readFileSync(path.join(directory, "key.pem")), cert: fs.readFileSync(path.join(directory, "cert.pem")) }, (request, response) => {
  const options = target(request);
  if (!options) { response.writeHead(404).end(); return; }
  const upstream = http.request(options, incoming => { response.writeHead(incoming.statusCode, incoming.headers); incoming.pipe(response); });
  upstream.on("error", () => response.writeHead(502).end());
  request.pipe(upstream);
});
server.on("upgrade", (request, socket, head) => {
  const options = target(request);
  if (!options) { socket.destroy(); return; }
  const upstream = http.request(options);
  upstream.on("upgrade", (response, remote, initial) => {
    socket.write(`HTTP/1.1 ${response.statusCode} ${response.statusMessage}\r\n` + Object.entries(response.headers).map(([key, value]) => `${key}: ${value}\r\n`).join("") + "\r\n");
    if (head.length) remote.write(head);
    if (initial.length) socket.write(initial);
    socket.pipe(remote).pipe(socket);
    socket.on("error", () => remote.destroy());
    remote.on("error", () => socket.destroy());
  });
  upstream.on("response", response => { socket.end(`HTTP/1.1 ${response.statusCode} Rejected\r\nConnection: close\r\n\r\n`); response.resume(); });
  upstream.on("error", () => socket.destroy());
  upstream.end();
});
server.listen(0, "127.0.0.1", () => console.log(server.address().port));
