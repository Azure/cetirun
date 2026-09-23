---
title: "Agent Harness"
description: "Agent configuration, Hermes defaults, and the BYO image and HTTP contract."
weight: 15
---

Agent Harness defines configuration for Hermes, OpenClaw and bring-your-own
(BYO) images without replacing native reasoning, tools, conversations or context.

> **Availability:** The CRD accepts the configuration below.
> Hermes and OpenClaw run through the shared Harness interface. Operator pins
> their image and port from the selected runtime chart before provisioning.
> Custom, registry references and non-default usernames remain unsupported.
> The BYO requirements describe the image contract, not an installation procedure.

## Configuration

The cluster-scoped [Agent](../crds/#agent) resource embeds its configuration in
`spec.agent`. This fragment shows a Custom configuration, not a runnable
deployment:

```yaml
spec:
  agent:
    type: custom
    image: registry.example.com/team/agent:1.0.0
    registrySecretRef:
      name: registry-example-agent
    username: vibe
    port: 8080
```

| Field | Contract |
| --- | --- |
| `type` | `hermes` (default), `openclaw` or `custom`; immutable after creation. |
| `image` | Required for Custom. Built-ins use a pinned platform default or a qualified override. Once set, it cannot be cleared. Prefer production digests. |
| `registrySecretRef` | Optional Portal-managed source Secret reference containing only `name`. |
| `username` | Existing non-root image user, default `vibe`, with HOME at `/home/<username>`; immutable. |
| `port` | Optional HTTP port, 1–65535. Without it, Custom has no platform HTTP service, Chat UI or HTTP probe. Built-ins retain qualified native defaults. |

`spec.agent` remains required; `agent: {}` selects Hermes. `spec.model`,
ownership, Team, skills, storage, resources, Helm settings and lifecycle stay
outside it. `gatewayTokenSecretRef` is required for built-ins and forbidden for
Custom; it is not a model or registry credential. There is no legacy
`spec.agentType` fallback or migration tool.

Policy controls allowed types and images separately. Other user images use
Custom; an arbitrary image cannot claim built-in compatibility. The API exposes
no UID/GID, command, environment or volume overrides.

## Built-in runtime profiles

Each runtime chart includes a versioned profile and native configuration assets
under `agents/hermes/` and `agents/openclaw/`. Operator resolves omitted images
and ports from that chart, writes them to the Agent, then reconciles the saved
configuration. Chart version and image version remain separate.

An image override must match the profile's default image or an exact entry in
its `qualifiedImages` list. This compatibility check does not replace image
authorization policy. Port overrides must not collide with native helper,
dashboard, desktop, terminal or enabled Teams ports.

Use `spec.agent.image` and `spec.agent.port`, not the old image or primary-port
Helm values. `runtime` and `agentRequest` are reserved platform inputs.
Other supported deployment values keep their existing scope.

The Go adapters return configuration and endpoint descriptions; they do not
operate Kubernetes, execute Agent code, or manage conversations. Standalone
Helm uses the same native configuration assets. Native authentication, user
files and resource names retain their existing roles.

## Initialization

The init container uses the selected Agent image, with its image HOME visible
and the home PVC mounted at `/home-data`. It copies image HOME once, then writes
`.cetirun-home-seeded` on the PVC. Later Pod initializations skip that copy but
still apply Go-generated configuration and run the Agent's native preparation.
The main container mounts the same PVC at HOME and keeps its native entrypoint.

Nonempty, unrecognized homes fail rather than being overwritten. An interrupted
seed is tracked separately and can resume only with the same immutable image.
Existing native initialization records prevent reseeding established homes.
These records are not authorization or ownership evidence.

Required configuration errors preserve the affected files and stop startup.
Routine initialization does not import old OAuth credentials or migrate between
Agent types. Existing credentials and the native authentication helpers remain
available.

## Private registries

The registry contract assigns write-only credential input and source Secret
storage to Portal. Operator verifies the Agent UID and copies the credentials
to a local pull Secret for Pod `imagePullSecrets`. Users do not pre-create
runtime namespaces or Secrets. Credential input and synchronization are not
enabled by the CRD reference alone.

Credentials stay out of CRDs, responses, Helm values, logs and Agent containers.
Rotation synchronizes subsequent pulls without restarting the Agent solely for
that change. Retries and cleanup remain tied to the Agent UID; synchronization
does not prove a pull succeeds.

## Image, storage and model access

BYO images must provide a foreground entrypoint and numeric non-root
`USER <uid>:<gid>` matching `username`. The runtime contract requires HOME on an
independently retained, writable PVC. Image HOME may contain configuration and
dependencies for the first copy. Updating the image does not refresh files
already copied into the PVC; plan dependency or data migrations separately.

The initial BYO contract requires `sh`, `cp` and the basic tools used by the
common initializer, including `mkdir`, `mv`, `rm`, `chmod`, `mktemp`, `find` and
`cmp`. Copying must preserve hidden files, executable bits and symlinks without
following links outside HOME. Missing tools fail initialization; the platform
does not install them or elevate privileges. Custom is not required to provide
Python, jq, npm, kubectl or a platform-specific hook.

The BYO contract reserves `$HOME/.agents/skills` for read-only approved skills.
It requires `cetirun-memory` and canonical `AGENTS.md` context independently of
Team selection. Images load them through native discovery or prompt/bootstrap
adaptation; private memory and native sessions stay instance-local. Existing
built-in skill locations are described in [Configuration and skills](../../concepts/configuration/).

The contract uses `OPENAI_BASE_URL` and the Agent's Virtual Key as
`OPENAI_API_KEY` for shared AI Gateway model discovery and calls. That Gateway
and a separate Copilot authorization proxy are [Roadmap](../../overview/roadmap/)
capabilities, not prerequisites implemented by the schema. See
[Choose a model](../../guides/models/) for supported built-in model configuration.

## Chat

Custom images with `port` receive `PORT` and serve these routes on that port:

| Route | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI-compatible text chat, including streaming replies. |
| `GET /v1/models` | Model identifiers accepted by this Agent's chat endpoint, not the Gateway catalog. |
| `GET /healthz` | HTTP startup/readiness: `200` with `{"status":"ok"}`, without model or tool calls. |

Requests follow browser → authenticated Chat UI backend → private Agent Service.
Custom endpoints need no application-level Chat token; the platform enforces
user authorization and network isolation. Built-ins retain native API
authentication. Never forward the outbound `OPENAI_API_KEY` to a chat endpoint.

Chat UI owns its session list and history and sends the needed `messages`.
Native history is optional and requires no synchronization. Chat UI storage
is a separate choice; interrupted requests are not automatically replayed.

## Lifecycle

Pin runtime settings per Agent and keep chart and image versions separate.
The contract requires upgrades to preserve UID/GID, HOME and data compatibility,
stop old writers, and verify storage handover. Private PVCs must survive compute
replacement. Read [Lifecycle](../../guides/lifecycle/) before changing or deleting
an existing instance; namespace deletion removes its PVCs.

No new Harness, Run or Session CRD is introduced. Unsupported runtime features
fail with `AgentRuntimeNotImplemented`; invalid profiles, images and port
combinations fail with `AgentConfigInvalid` before workload deployment.
