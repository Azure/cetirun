// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

"use strict";
const http = require("node:http");
const server = http.createServer((request, response) => {
  const port = /^\/(?:v1|health)(?:\/|\?|$)/.test(request.url) ? 8642 : 9120;
  const upstream = http.request({ hostname: "127.0.0.1", port, path: request.url, method: request.method, headers: request.headers }, incoming => {
    response.writeHead(incoming.statusCode, incoming.headers); incoming.pipe(response);
  });
  upstream.on("error", () => response.writeHead(502).end());
  request.pipe(upstream);
});
server.on("upgrade", (request, socket, head) => {
  const upstream = http.request({ hostname: "127.0.0.1", port: 9120, path: request.url, headers: request.headers });
  upstream.on("upgrade", (response, remote, initial) => {
    socket.write(`HTTP/1.1 ${response.statusCode} ${response.statusMessage}\r\n` + Object.entries(response.headers).map(([key, value]) => `${key}: ${value}\r\n`).join("") + "\r\n");
    if (head.length) remote.write(head);
    if (initial.length) socket.write(initial);
    socket.pipe(remote).pipe(socket);
    socket.on("error", () => remote.destroy()); remote.on("error", () => socket.destroy());
  });
  upstream.on("response", response => { socket.end(`HTTP/1.1 ${response.statusCode} Rejected\r\nConnection: close\r\n\r\n`); response.resume(); });
  upstream.on("error", () => socket.destroy());
  upstream.end();
});
server.listen(19120, "0.0.0.0");
