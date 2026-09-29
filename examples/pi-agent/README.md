# Pi Custom Agent

Run [Pi](https://github.com/earendil-works/pi/tree/v0.87.1/packages/coding-agent)
in CetiRun, with its native terminal UI and an OpenAI-compatible HTTP adapter.
The example pins `@earendil-works/pi-coding-agent` to **0.87.1**. It uses Pi's
real tools; it does not provide a Web Chat UI.

## HTTP interfaces

Set `spec.agent.port: 8080`. The platform passes that value as `PORT`; the image
starts its HTTP server on `0.0.0.0:PORT`.

| Interface | Implementation |
| --- | --- |
| `GET /healthz` | Returns process readiness without calling a model. |
| `GET /v1/models` | Lists the Copilot models available to this instance. |
| `POST /v1/chat/completions` | Accepts text messages and optional SSE streaming; runs Pi's read, bash, edit, write, grep, find and ls tools. |

[agent-api.mjs](agent-api.mjs) contains all three routes. It accepts one HTTP
chat request at a time, with a two-minute deadline and an eight-tool-call
budget. HTTP history is reconstructed from each request in memory, separate
from native TUI history. Automatic model retries and compaction are disabled.
See the [Custom Agent contract](../../docs/agent-harness-contract.md) for the
platform requirements beyond this example.

## Code map

| File | Responsibility |
| --- | --- |
| [server.mjs](server.mjs) | Image entrypoint: start the API and private auth listener; stop them on shutdown. |
| [agent-api.mjs](agent-api.mjs) | HTTP contract, input validation, streaming, limits and cancellation. |
| [pi-runtime.mjs](pi-runtime.mjs) | Pi SDK sessions, model discovery and tool execution. |
| [pi-resources.mjs](pi-resources.mjs) | Load approved context and skills without global extensions or runtime package installation. |
| [pi-tui.mjs](pi-tui.mjs) | Start Pi's native interactive mode with the same context and private auth directory. |
| [auth-api.mjs](auth-api.mjs), [copilot-auth.mjs](copilot-auth.mjs) | Loopback-only login/status/cancel API and its operations CLI. |
| [http.mjs](http.mjs) | Shared HTTP I/O and server lifecycle helpers. |
| [Dockerfile](Dockerfile) | Install pinned dependencies and tools, expose `pi-tui`, and run the HTTP entrypoint as a non-root user. |

The auth listener is private to the Pod at `127.0.0.1:9099`; never expose it
through a Service or ingress. From an approved operations channel inside the
Agent container, `node /opt/cetirun-pi/copilot-auth.mjs --help` describes its CLI.
Credentials stay in `$HOME/.pi/agent/auth.json` and are not returned by the API
or copied from another Agent. Do not disable exec restrictions to reach it.

## Build the image

Run from `examples/pi-agent`:

```bash
npm ci --ignore-scripts --no-audit --no-fund
npm test
npm run check
docker buildx build --platform linux/amd64 --load -t pi-example:local .
```

The Dockerfile uses the CetiRun base image for common HOME-initialization tools,
installs the locked SDK plus checksum-pinned `fd`, and verifies required tools
at build time. It declares `USER 1024:1024`, `HOME=/home/vibe`, and keeps the
application outside the HOME mount at `/opt/cetirun-pi`. Telemetry and automatic
Pi catalog/update checks are disabled; explicit OAuth and model calls still work.

To adapt this example, keep that image layout, the three HTTP routes and a
runnable shell/TUI command. Do not add ttyd or Kubernetes credentials to the
Agent image. A different base image must satisfy the initializer contract and
the Dockerfile's dependency checks. Publish under a new tag, record the source
and image digest, and use that digest below; publishing is a separate operation.

## Deploy an approved instance

Use an Operator/runtime Chart release that includes the Custom terminal and
its published `cetirun-terminal` image. The Operator provisions the terminal
ServiceAccount, restricted exec access, routing and storage. Keep the existing
gateway configuration; `ttyd.gatewayServiceAccount` must match its identity.

An administrator must enable Custom provisioning and approve the exact image.
This example explicitly uses Pi-owned model authentication while the platform
Gateway is unavailable; missing Gateway credentials never enable it implicitly.
Merge these settings into the platform values:

```yaml
cetirun-operator:
  customRuntime:
    enabled: true
    agentManagedModel: true
    approvedImages:
      - registry.example.com/pi-example@sha256:<published-digest>
    storageClass: default
    storageSize: 100Gi
    accessMode: ReadWriteOnce
    fsGroup: 1024
```

The Operator prepares the namespace, canonical context and HOME claim template
in one Chart installation. The StatefulSet creates the PVC and Pod; a
pre-existing PVC or UID is not required. The default context includes
`cetirun-memory`, alongside approved platform and Team shared skills.

On AKS, `default` uses Azure Disk with `WaitForFirstConsumer`: scheduling the
Pod triggers binding. Ready requires both a Bound HOME and a ready workload.
The claim must match the configured storage profile; changing that profile
does not migrate or resize an existing HOME. Storage handoff and CSI/mesh
qualification follow the [Custom example](../custom-agent/README.md#platform-integration).

Create a new Agent, replacing the owner, digest and Chart version:

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: pi-example
spec:
  user:
    alias: example-user
    displayName: Example User
  agent:
    type: custom
    image: registry.example.com/pi-example@sha256:<published-digest>
    username: vibe
    port: 8080
  helm:
    version: "<terminal-enabled-chart-version>"
    values:
      ttyd:
        enabled: true
```

The platform creates the namespace and PVCs. Do not change an existing Agent's
type or manually create its storage. Image changes on an existing HOME require
separate compatibility qualification. Deleting an Agent deletes its namespace
and PVCs; it is not a restart procedure.

Only `ttyd.enabled` is an instance override. Terminal image, port and gateway
identity belong in the Operator's platform values, not `spec.helm.values`.
Desktop is independent: `spec.helm.values.desktop.enabled: false` disables
Desktop without disabling Terminal.

## Open Pi from the Portal

Open the instance, select **Terminal**, and run:

```bash
pi-tui          # Continue the most recent saved native session
pi-tui --new    # Or start a new session
```

The platform's `cetirun-terminal` service opens a shell in the Agent container,
trying `zsh`, `bash`, then `sh`. This image does not install or start ttyd.
The browser uses `/<instance>/term/`; the terminal Service defaults to port 7681,
separate from the Agent's HTTP port.

For model access, approve Pi's Copilot model-policy updates before using
`/login github-copilot`, then select a model with `/model`. Complete the GitHub
device flow with your own approved account. Terminal access does not log you in
to Copilot; this example has no Portal model-login action.

Pi works in `$HOME/workspace`. Credentials and saved sessions stay in the
private HOME PVC under `.pi/agent`; context and skills come from read-only
`.agents`. Pi saves a new native session after its first assistant response,
so an empty or shell-only session is not a persistence test. Pod replacement
opens a new shell; run `pi-tui` again to reopen a saved session.

## Validate

The Node tests cover the HTTP adapter, CLI entrypoint and real SDK resource
discovery without OAuth or model calls. In a separately authorized environment,
verify Portal → Terminal → `pi-tui`, execute a synthetic `!printf` command, then
check terminal reconnection and a synthetic HOME file after Pod replacement.

Copilot login, model calls, streaming/cancellation and saved model-session
recovery need separate live checks with an approved account and request budget.
Ready status, a working terminal or passing offline tests do not establish them.
