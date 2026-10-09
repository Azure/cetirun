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
> Private images use Portal-managed Registry credentials and the Operator's
> credential synchronizer. Custom supports non-default usernames; built-in
> profiles still require `vibe`. Portal self-service is also opt-in;
> a default installation does not enable Custom creation or provisioning.

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
| `port` | HTTP port, 1–65535. Custom serves its Web UI when `webUI` is true, otherwise the Chat API; omitted means headless. Built-ins retain qualified native defaults. Platform desktop and terminal access are independent. |
| `webUI` | Boolean, default false. When true, Custom must set `port` and serve a browser application there instead of the Chat API. Published at `/ui/<instance>/` through platform authentication. |

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
Helm values. `runtime`, `agentRequest` and managed `imagePullSecrets` are reserved platform inputs.
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
these references against the actual objects. The Kubernetes provider reuses
the platform Gateway controller to provision each approved Agent's Virtual Key;
it does not create another Gateway or database, publish skills, or invent a
storage handoff.

Without the provider, an Agent reports `CustomBindingsPending`. Missing,
changed or foreign objects report `CustomBindingRejected`. Registry references
use the separate credential synchronizer; native integrations remain rejected. No placeholder credentials
or native-secret fallback can start the workload.

Administrators can explicitly enable `customRuntime` in the Operator chart for
qualified image digests. This
Kubernetes provider prepares the namespace, HOME claim template and canonical
context, and coordinates Pod replacements, including approved image updates.
The runtime Chart retains and reuses the same HOME claim. The `gatewayImages`
setting is a subset of `approvedImages` qualified for platform model access.
Those images receive only their own Virtual Key and the Gateway API base.
Other approved images require `agentManagedModel: true` and retain image-owned
authentication. Enabling the Gateway does not migrate them automatically, and
missing Gateway bindings never select native login as a fallback.

For Gateway-backed Custom Agents, the existing Portal Overview provides the
platform Copilot sign-in. A compatible runtime chart marked
`cetirun.microsoft.com/custom-copilot: v1` deploys the separate no-database proxy.
Only Gateway Pods can reach it; OAuth and proxy credentials never enter the
Agent container or HOME. Shared models and that Agent's private Copilot models
are authorized by its Virtual Key. The [Pi example]({{< repo "examples/pi-agent/README.md" >}})
supports this path and discovers models through the Gateway. Portal Chat still
gets its model choices from the Agent, not directly from the Gateway catalog.

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
Agent UI. Desktop and Terminal can be enabled independently.

Custom uses a separate `cetirun-terminal` Deployment and Service from the
runtime Chart. The dedicated image is built with
`app/cetirun-operator/Dockerfile.terminal`; it does not reuse the Operator image.
The Service uses `ttyd.port` (7681 by default), while the browser keeps the
authenticated `/<instance>/term/` path. There is no terminal port field in the
Agent CRD. Set `spec.helm.values.ttyd.enabled: false` to disable an instance's
terminal. Charts from before this terminal integration keep their previous
Custom behavior.

The terminal opens a Kubernetes exec session in the Agent's main container.
Its entrypoint checks that container for `zsh`, then `bash`, then `sh`; it
reports a missing terminal and fails if none can run. It repeats discovery
after container replacement. The Agent image retains its own ENTRYPOINT/CMD
and does not need to start ttyd. The terminal Pod does not mount Agent HOME,
share its process namespace or receive model credentials.

The Operator prepares instance-scoped read roles; the Chart binds the terminal
ServiceAccount and deploys its workload. A fixed exec role and the instance's
admission policy restrict requests to the named Pod, main container and
approved shell/probe commands. Gateway authentication, instance ownership,
Agent UID and suspension/deletion checks remain required. Terminal ingress
also requires the configured gateway identity through Istio; its separate
NetworkPolicy limits outbound access to discovered API server addresses,
DNS and the mesh control plane.

The Portal enables Terminal only after its owned Deployment becomes ready.
Suspension, deletion and a Sandbox hold stop the terminal and withdraw its route.
Pod replacement waits and opens a new exec session without replaying queued
input. Normal shell exit and authorization failure stop reconnecting. An
ambiguous connection failure against the same running container ends the
session rather than silently duplicating it. Browser reconnection cannot
restore a terminated process; use the Agent's own session persistence where
available. These behaviors require qualification with the target CNI, mesh,
runtime and image; a configured route is not an interactive acceptance test.

Custom does not run a native authentication helper, doctor, session logger,
Teams, Tailscale or the native upgrade hook. The Agent and HOME init containers
inherit the image's numeric non-root identity, with the approved volume group
applied by Kubernetes. The shared desktop keeps its platform image, UID 1024
and `/home/vibe` mount, which exposes the same PVC as the Agent's resolved HOME.
Image/filesystem compatibility,
including access by both identities, still requires qualification.

The platform configures shared controls, desktop settings, resources, node
selection, affinity, tolerations, topology spread and RuntimeClass. Per-Agent
Helm overrides accept resource requests and limits, `ttyd.enabled`, and desktop
enablement, logging, resolution and resources. Terminal and desktop images,
terminal ports, gateway identity and URL wiring remain platform-owned.
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

The [official VS Code Server example]({{< repo "examples/vscode-server/README.md" >}})
provides a Web IDE with native GitHub Copilot sign-in. Its offline tests exercise
prefix routing, browser storage and HOME preparation without a model call.
Real Copilot use, browser compatibility, network isolation and storage behavior
require separate validation.

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

Portal accepts a write-only Registry username and password or token. It creates
the Agent before saving a `kubernetes.io/dockerconfigjson` source Secret, owned
by the actual Agent UID. Operator validates that owner, the single registry host
derived from the image, and workload permissions before copying it into the
runtime namespace. The local name is `registry-pull-<uid-without-hyphens>`.
Users do not pre-create runtime namespaces or Secrets.

### Installation and API

The Operator chart owns the separate `cetirun-registry-credentials` namespace
and retains it on chart removal. It must not be shared with the Agent Identity
Broker or runtime workloads. Both components accept
`REGISTRY_CREDENTIAL_NAMESPACE`; the umbrella chart sets
`global.registryCredentialNamespace` once. Standalone installations must set
matching `registryCredentials.namespace` values. Only the Operator chart creates
the namespace. Agent names that would use the default or configured credential
namespace are rejected before any lifecycle changes, including deletion. A
pre-existing collision retains its finalizers and requires administrator recovery;
verify namespace ownership before removing such an Agent. Runtime charts must
advertise the
`cetirun.microsoft.com/registry-credentials: v1` annotation; older pinned charts
must be upgraded before attaching credentials.

Raw deployment or instance `imagePullSecrets` overrides are not merged with the
managed binding. Follow the [registry upgrade steps](../../getting-started/platform/#upgrading-runtime-registry-configuration)
before upgrading a deployment that used them; standalone Agent Helm releases
retain their separate pull-Secret setting.

| Operation | Contract |
| --- | --- |
| `POST /api/v1/instances` | Nested `agent` input and optional `registryAuth: {username, password}`. Private creation requires an explicit qualified image and a stable `Idempotency-Key` header. |
| `PUT /api/v1/instances/{name}/registry-auth` | Replace with a complete username and password/token. Use `If-Match: "<uid>/<resourceVersion>"` from a fresh instance response. |
| `DELETE /api/v1/instances/{name}/registry-auth` | Use the same precondition. Detach the reference; Operator removes the future Pod binding before deleting this UID's copies. |
| `GET /api/v1/instances/{name}` | Return `agent`, `uid`, `resourceVersion`, `registryAuthConfigured` and conditions, never Registry credential data. |

Create requests reject the old `agentType` field and user-selected Secret names,
namespaces or registry login URLs. Ordinary instance PATCH uses `uid` and
`resourceVersion` in its JSON body, as do instance deletion and upgrade. This
management precondition is separate from the Registry endpoint's `If-Match`.
PATCH accepts image and port updates inside `agent`: omitted fields are preserved
and a null port clears the override. A pinned image cannot be cleared; type and
username are immutable. Custom image updates replace the Pod and retain HOME;
the new image must be approved by the platform and compatible with the existing
data and image user. Credentials and references cannot change through ordinary
PATCH.

The same credential controls work during Custom self-service creation and on
existing Custom instances. Overview shows the Registry credentials section only
while private-registry authentication is enabled, including when saving the
source credentials is still pending. Removing the credentials hides the whole
section; synchronization diagnostics remain available in Status. The deployment's
allowed types and image approval still apply. Registry credentials do not enable
native integrations or provide model authentication for a Custom image.

If source persistence fails after Agent creation, the response identifies the
existing instance. Retry the same creation request, including its request ID and
settings, or manage credentials on that instance. The retry is recognized before
the instance quota check and never replaces a saved source. Creation failure does
not delete the Agent, namespace or HOME. A concurrent mutation returns a conflict;
refresh before retrying, and never submit a masked or empty password.

Credentials stay out of CRDs, responses, Helm values, logs, Agent environments
and HOME. Portal clears credential fields after submission or cancellation and
does not retain them in mutation caches. Source persistence, synchronization and
successful image pulling are separate states. `RegistryCredentialsReady` records
copy readiness; a bad password, unavailable registry or missing image can still
leave the Pod unready. An image cached on a node is not an authentication test.

### Rotation and recovery

Content rotation updates the same local Secret without entering a rollout.
Adding or removing the reference updates future Pod templates without replacing
a healthy Pod. Native StatefulSets use an `OnDelete` update for that limited
case; a later chart, image or runtime configuration change explicitly restores
`RollingUpdate` and clears the credential-only readiness marker. A chart change
is never classified as credential-only, even when its values are unchanged.
An initial image-pull failure is different: a Pod that never ran an Agent
and lacks the new pull reference is replaced using UID and resource-version
preconditions. Its StatefulSet and PVCs are retained. A Pod that is healthy or
has already run a workload is not replaced by this recovery path. Identical
retries of an applied native release skip upgrade hooks.

New built-in namespaces record their Agent UID at creation, before Pod readiness.
An existing unowned namespace is not adopted based on its name alone; it needs
a trusted prior namespace/release status binding. Custom retains its existing
writer-coordination rules. Suspension remains available when credentials cannot
be synchronized, without pulling a chart or starting an upgrade hook. Native
ingress cleanup validates the UID-owned namespace and managed route labels;
it does not require Custom-only ownership annotations. Deletion and same-name
recreation use actual Agent UIDs and do not reuse another instance's source.
These operations do not revoke the external Registry account or change PVC
retention policy.

### Permissions

The permission gate checks effective ServiceAccount authorization, including
named Role/ClusterRole grants, indirect workload creation and service-account
token issuance. Incompatible access or failed authorization checks block saving
or copying credentials; the platform does not silently rewrite existing grants.
The platform Custom Terminal's own-namespace exec permission is accepted only
after checking its Agent UID ownership and the exact fail-closed admission rule
and Deny binding that restrict exec to the Agent's main container and approved
shell commands. This exception never applies to an Agent runtime identity,
cross-namespace exec, Secret access, token issuance or admission-policy changes.
Portal and Operator need the packaged RBAC inspection and SubjectAccessReview
permissions, including reads of the terminal ServiceAccount and admission
objects. Reviews are deduplicated within each check and run with at most eight
requests in flight; authorization results are not cached across checks. Each
check inventories workloads once and tests their identities against the current
Agent's credentials, while also inspecting named RBAC grants across namespaces.
Unchanged synchronization only rereads the source and local Secret and verifies
their owner, contents, revisions, and saved receipt; it does not repeat the
cluster-wide permission audit. Completed creation retries are also read-only.
Credential writes and new workload applications always run a fresh audit before
proceeding. Initial saves and replacements therefore still incur synchronous
authorization work proportional to the runtime identities; there is no cached
authorization result that can allow a later write. Permission changes do not
revoke an already-distributed credential or stop a running Agent automatically.
Existing broad control-plane grants are not narrowed by this
feature. Operator also needs Pod deletion permission for the bounded initial-pull
recovery above; no such permission is granted to Agent workloads. Privileged
containers and unqualified added capabilities are rejected. Container setuid
support alone is not treated as host privilege; API authorization and credential
mounts are checked separately.
Administrators must also keep tenant Kubernetes identities out of both
credential locations, protect node access, configure Secret encryption at rest
and backups, and prevent request-body logging at proxies or tracing collectors.
Registry mutations perform live authorization reviews; API capacity and latency
at the deployment's instance count require environment qualification.

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

Custom images with `port` and `webUI` false or omitted receive `PORT` and serve
these routes on that port:

| Route | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI-compatible text chat, including streaming replies. |
| `GET /v1/models` | Model identifiers accepted by this Agent's chat endpoint, not the Gateway catalog. |
| `GET /healthz` | HTTP startup/readiness: `200` with `{"status":"ok"}`, without model or tool calls. |

The Portal Chat path is browser → authenticated Portal backend → private
Agent Service. The optional feature serves only Custom with `port` and `webUI`
false. It is disabled by default and requires compatible Portal, Operator and
runtime charts. Hermes and OpenClaw continue using their native interfaces.
Custom endpoints need no application-level Chat token; the platform enforces
user authorization and network isolation. Built-ins retain native API
authentication. Never forward the outbound `OPENAI_API_KEY` to a chat endpoint.

Portal Chat owns its session list and history in the current browser's IndexedDB
and sends the needed `messages`. The Portal server has no history database or
PVC. Native history is optional and requires no synchronization; interrupted
requests are not automatically replayed. Browser history does not sync across
devices or provide a backup. Read [Custom Chat](../../guides/custom-chat/) for
enabling the feature, privacy, limits and recovery behavior.

## Custom Web UI

A Custom image sets `spec.agent.webUI: true` to serve a browser application on
`spec.agent.port` instead of the Chat API. The port is required while the flag
is enabled. Both modes use the fixed `/healthz` startup/readiness endpoint,
which must work without a model call or a user's service login.

```yaml
spec:
  agent:
    type: custom
    image: registry.example.com/team/editor:1.0.0
    port: 8080
    webUI: true
```

The platform supplies `PORT`, `CETIRUN_WEB_BASE_PATH` and
`CETIRUN_WEB_INSTANCE`. The base path is `/ui/<instance>/`; the instance value is
the Agent UID, stable across Pod replacements. The application must preserve
this prefix in assets, WebSockets and callbacks and scope browser storage and
cookies per instance. Paths on one origin are not a browser security boundary.

The Operator publishes the route only through the platform's authenticated
gateway and instance-ownership check. NetworkPolicy must remain enabled. The
Portal shows the declared Web UI and enables its link when the Agent is Ready
and the route is configured; suspension removes the route. Desktop and Terminal
remain separate optional platform services. Web UI readiness
does not establish successful Copilot authentication or model execution.

The Portal creation and edit forms provide a **Serve Web UI** switch and use
the same port field for both modes. The API accepts a boolean `agent.webUI`.
Omitting it during a PATCH preserves the saved flag; false or null resets it
to false without clearing `agent.port`. To make an instance headless, disable
Web UI and clear the port together. Changes must match the image's supported
protocol. Instance UID and resource-version preconditions still apply.

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
approved Gateway peer and internal Chat API callers. Desktop and declared Web UI
ingress are allowed through the platform gateway; the private Chat port is not
added to that public route. NetworkPolicy configuration alone does not prove Gateway-only
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

For a headless Custom Agent, `Ready` describes the running process, not HTTP
health, a successful model call or recovered sessions. With a port, probes use
`/healthz`; protocol qualification checks the response separately from Kubernetes
probes. When Web UI is enabled and its route is configured, the Operator sets
the Agent CR's `status.accessUrl` and the Portal returns it as `webUIUrl`.
Chat-only and headless Custom Agents leave `status.accessUrl` empty. Portal
derives the independent Desktop and Terminal entries from the platform gateway
domain and instance name after their routes are configured; it does not assume
the Portal and gateway share an origin.

## Portal self-service

The Portal creates and edits generic Custom Agents. It does not require an
image-specific login helper, change the image's entrypoint, or manage its own
model credentials. Custom Terminal and Desktop use the existing platform access
boundary and remain independent of the optional Agent HTTP port.

Management requests and responses use a nested `agent` object. The old
`agentType` input, unknown fields and duplicate JSON fields are rejected on
both the `/api/v1/instance` and `/api/v1/instances` routes. For example:

```json
{
  "instanceName": "research",
  "displayName": "Research Assistant",
  "agent": {
    "type": "custom",
    "image": "registry.example.com/team/assistant@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "username": "vibe"
  }
}
```

Custom requires an image accepted by the configured runtime. Omitting
`username` selects `vibe`; omitting `port` creates a headless Agent. `model`
remains independent of `agent` in the API. The current image-owned Custom runtime
does not use it to select a model, so Custom forms leave model setup and login
to the image. Custom requests must omit
Gateway Token, Tailscale, Microsoft Teams and Agent Identity settings, including
empty or false values. No built-in credential Secret is created for Custom.

The instance heading displays its nonblank Display Name, falling back to the
Agent's name. An owner can clear Display Name with an empty string.

### Enabling Custom self-service

Custom is absent from the default Portal type list. An administrator enables
it in the [Portal chart](../helm/#portal-chart):

```yaml
features:
  allowedAgentTypes: [hermes, openclaw, custom]
```

Custom uses the existing Portal login and instance-ownership checks. Enabling
the type does not enable Agent Identity or require a separate Portal admission
grant. The backend still checks the type flag, instance limits and request
fields, including immutable fields and concurrent-update preconditions.

Per-user and Team-specific type and image restrictions are deferred to
AgentPolicy; this management flow does not implement them. The Operator's
existing runtime image approvals, profile compatibility and reserved-port
checks remain in effect. Portal creation does not bypass those checks or
release a Sandbox hold.

### Editing and concurrent changes

GET responses include `uid`, `resourceVersion`, `generation` and
`observedGeneration`. PATCH, DELETE and upgrade requests must include the UID
and resource version from the object the user viewed. A stale version or
same-name replacement returns a conflict; reread and review the current object
before retrying. The Portal does not replay a stale edit automatically.

An omitted field leaves its value unchanged. `agent.port: null` removes a
Custom HTTP endpoint or restores a built-in version's qualified default.
`agent.type` and the effective `agent.username` cannot change; a pinned image
cannot be cleared. Owners can edit the Custom image in Portal or PATCH
`agent.image`. An approved image update replaces a running Pod and reuses its
HOME PVC without reseeding it. A suspended Agent stays stopped until resumed.
Image changes do not change the pinned runtime Chart version. The image
provider and owner are responsible for compatibility with existing files,
native sessions and HOME dependencies; CetiRun does not migrate their formats.
Team reassignment requires an administrator and a suspended Agent; ordinary
edits preserve the effective Team and its policy bindings.

The displayed configuration is the requested configuration. A saved request
does not establish new runtime readiness.

Registry credentials and `registrySecretRef` mutations are not part of this
management flow. The optional Custom Chat feature has its own access checks;
image-specific authentication remains with the image. The Portal does not present a Custom native Agent UI or
Copilot helper merely because an HTTP port is configured.
