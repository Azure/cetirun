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
> Custom has a separate runtime implementation, but the default operator waits
> for platform bindings; no production binding provider is wired in.
> Registry references remain unsupported. Custom supports non-default usernames;
> built-in profiles still require `vibe`. The BYO requirements are not a
> self-service installation procedure.

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
| `port` | Optional Agent HTTP port, 1–65535. Without it, Custom has no Agent HTTP endpoint, Chat UI or HTTP probe. Platform desktop access is independent. Built-ins retain qualified native defaults. |

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

## Custom runtime

Custom uses its own Go adapter and chart branch. It does not require a built-in
profile, native provider, configuration template or preferred model name.
The main container preserves the image's ENTRYPOINT/CMD. Gateway-backed instances
receive `HOME`, `OPENAI_BASE_URL`, a Secret-backed `OPENAI_API_KEY`, and `PORT`
when the Agent declares an HTTP port. Init does not receive the model credential.

The operator consumes an internal `CustomBindingProvider`, supplied by the
platform capability owners. Its receipts identify the exact Agent UID,
generation and image, namespace, model Secret, retained HOME PVC, immutable
context snapshot, network peers and their revisions. The controller checks
these references against the actual objects. It does not create a Gateway,
issue Virtual Keys, publish approved skills or invent a storage handoff.

Without the provider, an Agent reports `CustomBindingsPending`. Missing,
changed or foreign objects report `CustomBindingRejected`; unsupported registry
references and native integrations remain rejected. No placeholder credentials
or native-secret fallback can start the workload.

Administrators can explicitly enable `customRuntime` in the Operator chart for
qualified image digests that manage their own model authentication. This
Kubernetes provider provisions the namespace, HOME and canonical context and
coordinates same-image Pod replacements. It does not inject Gateway variables
or issue model credentials. Missing Gateway bindings never select this mode
automatically. The [Pi example]({{< repo "examples/pi-agent/README.md" >}}) uses
native GitHub Copilot login through this opt-in path.

The approved context snapshot is mounted read-only at `$HOME/.agents`, including
`skills/cetirun-memory/SKILL.md` and canonical `AGENTS.md`. A same-named image
directory cannot make that view writable. Private memory stays elsewhere in
the instance HOME. The initial snapshot format is an immutable ConfigMap with
explicit key-to-path mappings; it is not a package installer or skill marketplace.

With platform `skills.shared` or Team `skillStorage`, Custom uses the same
Blob CSI PV/PVC templates as the built-in runtimes. Init reads its claim without
writing it and combines its skills with the approved ConfigMap in a per-Pod
temporary volume. The Agent receives only the completed, read-only view at
`$HOME/.agents`. Shared skills cannot replace canonical skills such as
`cetirun-memory`, and copying does not alter the user's HOME.

The view is rebuilt when a new Pod starts, not when shared files change.
Publish approved content before a controlled replacement and allow enough
ephemeral storage for the copy. Concurrent changes to the shared source are
not a transactional snapshot. Custom keeps its canonical discovery path
instead of inheriting the native `skills.mountPath`.
Nonempty npm/ClawHub package selections require prior materialization and
report `SkillsMaterializationPending`; Custom does not run package installers.

Custom keeps the shared platform desktop, NetworkPolicy, exec restriction,
PodDisruptionBudget and Istio discovery scope under their platform settings.
The desktop is enabled by default, including when the Agent has no HTTP port.
Its gateway route uses the existing platform authentication and instance
authorization; it does not expose the Custom chat endpoint or invent a native
Agent UI. Suspension removes the desktop route.

ttyd is temporarily disabled for Custom. Enabling it requires a defined way to
supply its tools and start it inside the Agent's own execution environment
without replacing ENTRYPOINT/CMD. A terminal in another container that only
shares HOME is not equivalent.

Custom does not run a native authentication helper, doctor, session logger,
Teams, Tailscale or the native upgrade hook. The Agent and HOME init containers
inherit the image's numeric non-root identity, with the approved volume group
applied by Kubernetes. The shared desktop keeps its platform image, UID 1024
and `/home/vibe` mount, which exposes the same PVC as the Agent's resolved HOME.
Image/filesystem compatibility,
including access by both identities, still requires qualification.

The platform configures shared controls, desktop settings, resources, node
selection, affinity, tolerations, topology spread and RuntimeClass. Per-Agent Helm overrides accept resource requests
and limits plus desktop enablement, logging, resolution and resources; desktop
image selection and URL wiring remain platform-owned.
Raw commands, containers, envFrom, identity, volumes, Secret references and
native feature settings are rejected. The final manifest check covers every
rendered object, not just the main container.

Preflight runs in the actual Helm action context. The controller seals validated
objects as canonical JSON before handoff and applies only that snapshot, with
hooks and CRDs disabled. Strict decoding rejects unknown, duplicate and
ambiguously converted fields. Server-side dry-run checks the exact objects
before stopping a workload, including API validation and access permissions.
Local checks also cover resources, PDB values, CIDRs, annotations and scheduling.
An Agent HTTP endpoint requires NetworkPolicy to be enabled because it has no
application-level Chat token; a disabled policy is not silently overridden.

The [synthetic example and qualification commands]({{< repo "examples/custom-agent/README.md" >}})
exercise this path without a paid model. They do not establish production
Gateway availability, skill provenance, network isolation or storage compatibility.

## Initialization

The init container uses the selected Agent image, with its image HOME visible
and the home PVC mounted at `/home-data`. It copies image HOME once, then writes
`.cetirun-home-seeded` on the PVC. Later Pod initializations skip that copy but
still apply Go-generated configuration and run the selected Agent's preparation.
The main container mounts the same PVC at HOME and keeps its native entrypoint.
Custom uses the same fixed initialization stages, with an empty native
configuration stage and shared-context preparation when enabled. It never
loads another Harness's stages.

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
It requires `cetirun-memory` and canonical `$HOME/.agents/AGENTS.md` independently of
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

The intended Chat UI path is browser → authenticated Chat UI backend → private
Agent Service. This runtime does not implement Chat UI, chatproxy or history storage.
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

The shared network rules retain the platform's configured external-egress and
internal-CIDR settings, DNS and Istio control-plane access. Custom adds the
approved Gateway peer and internal Agent HTTP callers. Desktop ingress is
allowed through the platform gateway; the Agent HTTP port is not added to that
public route. NetworkPolicy configuration alone does not prove Gateway-only
model egress or live mesh enforcement.

Custom uses `OnDelete` and excludes the built-in orphan-delete hook.
Changing an image, model/context projection or Pod configuration waits for
`CustomMaintenancePending` until the platform authorizes handoff, the old
StatefulSet is stopped and no active Pod still references HOME. The controller
does not force-delete Pods, detach volumes or reset initialization records.
Only changes to the actual Pod/model/context inputs require writer handoff.
Platform-object changes such as NetworkPolicy, PDB or exec controls are applied
without stopping an unchanged Agent Pod. OnDelete readiness uses observed
generation and updated/ready replica counts, not convergence of currentRevision.
Identical inputs producing different rendered output report `RendererDrift`.
Suspension can request zero replicas even while bindings are unavailable.

Apply approval is a lease with validity and completion checks, bound to both
input and output digests. The controller rechecks the live Agent's generation
and pause/deletion state before applying. If Helm succeeds but status writing
fails, a matching successful Helm record restores status without another stop.

A separate operator-owned outbox records each apply operation before approval.
It retains the outcome until the provider acknowledges completion. Recovery
uses the same operation ID through `ResumeApply`, including after a controller
restart or a change to the desired configuration. It never repeats Helm merely
to release coordination. Providers must support durable, idempotent completion
for that ID. Pending completion does not prevent requesting suspension; cleanup
waits for it to finish.

RuntimeClass names and node-selector label syntax are checked before handoff,
including values inherited from Chart defaults. Their existence and actual
runtime compatibility still require platform qualification.

Custom ownership receipts live in the operator namespace and retain the
approved Agent, namespace and HOME UIDs. Cleanup uses that receipt and a
namespace UID precondition, not user annotations or a derived name alone.
Missing/conflicting ownership blocks cleanup. Never-bound instances do not
delete foreign namespaces. Deleting an owned namespace still deletes its PVCs;
the receipt does not replace a data backup.

With no port, `Ready` describes the running process, not HTTP health, a successful
model call or recovered sessions. With a port, probes use `/healthz`; protocol
qualification checks the exact JSON response separately from Kubernetes probes.
Neither mode publishes a native Agent UI URL in the Agent CR's `status.accessUrl`.
Portal derives the independent Desktop entry from the platform gateway domain
and instance name after the route is configured; it does not assume the Portal
and gateway share an origin.
