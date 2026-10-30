# CetiRun Pi Agent

Run [Pi](https://github.com/earendil-works/pi/tree/v0.87.1/packages/coding-agent)
in CetiRun, with its native terminal UI and an OpenAI-compatible HTTP adapter.
The example pins `@earendil-works/pi-coding-agent` to **0.87.1**. It uses Pi's
real tools and works with Portal Chat when enabled; it does not bundle a Web UI.
The demo image and package are named `cetirun-pi-agent`.

## HTTP interfaces

Set `spec.agent.port: 8080`. The platform passes that value as `PORT`; the image
starts its HTTP server on `0.0.0.0:PORT`.

| Interface | Implementation |
| --- | --- |
| `GET /healthz` | Returns process readiness without calling a model. |
| `GET /v1/models` | Lists this Agent's available model identifiers from its selected model-access mode. |
| `POST /v1/chat/completions` | Accepts text messages and optional SSE streaming; runs Pi's read, bash, edit, write, grep, find and ls tools. |

[agent-api.mjs](agent-api.mjs) contains all three routes. It accepts one HTTP
chat request at a time, with a two-minute deadline and an eight-tool-call
budget. HTTP history is reconstructed from each request in memory, separate
from native TUI history. Automatic model retries and compaction are disabled.
Streaming responses send bounded SSE keepalives every 15 seconds during silent
model prefill or tool work. The two-minute task deadline, cancellation and output
limits still apply; keepalives do not extend a task or replay a request.
See the [Custom Agent contract](../../docs/agent-harness-contract.md) for the
platform requirements beyond this example.

## Code map

| File | Responsibility |
| --- | --- |
| [server.mjs](server.mjs) | Image entrypoint: start the API and private auth listener; stop them on shutdown. |
| [agent-api.mjs](agent-api.mjs) | HTTP contract, input validation, streaming, limits and cancellation. |
| [pi-runtime.mjs](pi-runtime.mjs) | Pi SDK sessions, model discovery and tool execution. |
| [gateway-runtime.mjs](gateway-runtime.mjs) | Model discovery and requests through the platform AI Gateway, with memory-only credentials and no automatic retries. |
| [pi-resources.mjs](pi-resources.mjs) | Load the common `$HOME/.agents` view and connect Memory through the Pi SDK. |
| [pi-tui.mjs](pi-tui.mjs) | Start Pi's native interactive mode with the same context and private auth directory. |
| [auth-api.mjs](auth-api.mjs), [copilot-auth.mjs](copilot-auth.mjs) | Loopback-only login/status/cancel API and its operations CLI. |
| [http.mjs](http.mjs) | Shared HTTP I/O and server lifecycle helpers. |
| [Dockerfile](Dockerfile) | Install pinned dependencies and tools, expose `pi-tui`, and run the HTTP entrypoint as a non-root user. |

In image-owned mode, the auth listener is private to the Pod at `127.0.0.1:9099`; never expose it
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
docker buildx build --platform linux/amd64 --load -t cetirun-pi-agent:local .
```

The root `make build-images` and `make publish-images` targets include this
demo. `scripts/pi-agent-docker-build.sh` builds it separately; use `BASE_IMAGE`
to select an existing CetiRun base and `PUSH=true` only to publish.

The Dockerfile uses the CetiRun base image for common HOME-initialization tools,
installs the locked SDK plus checksum-pinned `fd`, and verifies required tools
at build time. It declares `USER 1024:1024`, `HOME=/home/vibe`, and keeps the
application outside the HOME mount at `/opt/cetirun-pi`. Telemetry and automatic
Pi catalog/update checks are disabled; explicit OAuth and model calls still work.

The `runtime-source` build stage sets copied JavaScript modules to `0644` and
the application directory and `pi-tui.mjs` to `0755`, even when the build context
was created with `umask 077`. Both build stages check the entrypoints as
UID/GID `1024:1024`; the final stage also imports the installed Pi runtime.
These permission changes do not touch HOME or user credentials.

To adapt this example, keep that image layout, the three HTTP routes and a
runnable shell/TUI command. Do not add ttyd or Kubernetes credentials to the
Agent image. A different base image must satisfy the initializer contract and
the Dockerfile's dependency checks. Publish under a new tag, record the source
and image digest, and use that digest below; publishing is a separate operation.

## MCR publication

The release pipeline publishes
`mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-pi-agent` and the shared
`cetirun-base` after repository onboarding. Use an available release digest
approved by your platform administrator.

## Deploy an approved instance

Use an Operator/runtime Chart release that includes the Custom terminal and
its published `cetirun-terminal` image. The Operator provisions the terminal
ServiceAccount, restricted exec access, routing and storage. Keep the existing
gateway configuration; `ttyd.gatewayServiceAccount` must match its identity.

An administrator enables Custom provisioning and approves the exact image.
Custom platform model access always uses the AI Gateway.
The Operator provisions the Agent's Virtual Key and private Gateway access;
the image receives `OPENAI_BASE_URL` and a Secret-backed `OPENAI_API_KEY`.
Merge these settings into the platform values:

```yaml
cetirun-operator:
  customRuntime:
    enabled: true
    approvedImages:
      - mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-pi-agent@sha256:<published-digest>
    storageClass: default
    storageSize: 100Gi
    accessMode: ReadWriteOnce
    fsGroup: 1024
```

Gateway mode keeps its key in memory, ignores native `auth.json`, and disables
the image's local login listener. Pi discovers models through the Gateway using
its own key and runs its tools locally. The HTTP adapter still owns no history;
Portal Chat supplies each conversation's context. Automatic model and compaction
retries are disabled. The adapter uses a 32K context estimate and a 2K output
ceiling, not a claim about every model's actual limits; Gateway/provider limits
still apply. Pi's local cost display is not the Gateway's usage ledger.

Shared models appear only when the Gateway grants access. If Copilot is enabled,
use **Sign in with GitHub** on the Portal Overview. The platform deploys a
separate proxy using a chart marked `cetirun.microsoft.com/custom-copilot: v1`.
Its token and proxy key never reach the Agent container or HOME. Ready with an
empty model list means model access is still pending, not successful inference.

Missing or failed Gateway configuration never selects native login
automatically. Partial Gateway environment variables fail startup. For a
platform-managed Agent, all model access uses the Gateway.

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
  name: pi-agent
spec:
  team: example-team # An existing, approved Team.
  modelAccess:
    sharedModels: true
    githubCopilot: false
  user:
    alias: example-user
    displayName: Example User
  agent:
    type: custom
    image: mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-pi-agent@sha256:<published-digest>
    username: vibe
    port: 8080
  helm:
    version: "<chat-and-custom-copilot-enabled-chart-version>"
    values:
      ttyd:
        enabled: true
```

The platform creates the namespace and PVCs. Do not change an existing Agent's
type or manually create its storage. To upgrade, edit the image in Portal or
change `spec.agent.image` to another platform-approved digest. The Operator
replaces the running Pod and reuses its HOME PVC without reseeding it. Choose
an image compatible with the existing user, files and native sessions.
Deleting an Agent deletes its namespace and PVCs; it is not a restart procedure.

Only `ttyd.enabled` is an instance override. Terminal image, port and gateway
identity belong in the Operator's platform values, not `spec.helm.values`.
Desktop is independent: `spec.helm.values.desktop.enabled: false` disables
Desktop without disabling Terminal.

## Open Pi from the Portal

For web chat, enable [Portal Chat](../../site/content/en/docs/guides/custom-chat.md)
and select **Chat** in the instance. The Portal saves conversations in this
browser and sends their context with each request; Pi runs the model and tools.
This history is separate from Pi's native terminal sessions.

Open the instance, select **Terminal**, and run:

```bash
pi-tui          # Continue the most recent saved native session
pi-tui --new    # Or start a new session
```

The platform's `cetirun-terminal` service opens a shell in the Agent container,
trying `zsh`, `bash`, then `sh`. This image does not install or start ttyd.
The browser uses `/<instance>/term/`; the terminal Service defaults to port 7681,
separate from the Agent's HTTP port.

In Gateway mode, model login stays in Portal Overview. In image-owned mode,
approve Pi's Copilot model-policy updates before using
`/login github-copilot`, then select a model with `/model`. Complete the GitHub
device flow with your own approved account. Terminal access does not log you in
to Copilot; the image-owned mode has no Portal model-login action.

Pi works in `$HOME/workspace`. Native sessions, and image-owned credentials when
that mode is selected, stay in the private HOME PVC under `.pi/agent`;
context and skills come from read-only
`.agents`. Pi saves a new native session after its first assistant response,
so an empty or shell-only session is not a persistence test. Pod replacement
opens a new shell; run `pi-tui` again to reopen a saved session.

## Validate

### Source permissions

Select a cached image with Node.js 24 and a POSIX shell, then run from the
repository root:

```bash
CETIRUN_PERMISSIONS_TEST_IMAGE=local/node:24 \
  python3 -B -m unittest scripts.tests.test_example_image_permissions -v
```

Replace `local/node:24` with that image. The suite builds both Pi and VS Code
`runtime-source` stages with `umask 077`. It checks non-root reads, executable
scripts, inaccessible modules and nested extension paths, and unchanged HOME
permissions. Builds and containers have no network; containers also have a
read-only root filesystem and no host mounts or injected credentials.

An unset image variable skips the suite; a missing selected image fails without
pulling it. These source-stage checks do not validate complete Pi or VS Code
images, their installed SDK or distribution, or their production base image.

### Runtime checks

The Node tests cover the HTTP adapter, CLI entrypoint and real SDK resource
discovery without OAuth or model calls. In a separately authorized environment,
verify Portal → Terminal → `pi-tui`, execute a synthetic `!printf` command, then
check terminal reconnection and a synthetic HOME file after Pod replacement.

Copilot login, model calls, streaming/cancellation and saved model-session
recovery need separate live checks with an approved account and request budget.
Ready status, a working terminal or passing offline tests do not establish them.
