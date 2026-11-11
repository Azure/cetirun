---
title: "Agent Harness"
description: "The spec.agent fields, the OpenClaw and Hermes runtime profiles, and the contract of a bring-your-own Custom image for files, ports, probes, environment, and HTTP."
weight: 15
---

Agent Harness is the runtime configuration of an Agent: Hermes, OpenClaw, or
a bring-your-own (BYO) Custom image. Each runtime keeps its own reasoning,
tools, conversations, and context.

> **Availability:** The CRD accepts the configuration below.
> Hermes and OpenClaw run through the shared Harness interface. Operator pins
> their image and port from the selected runtime chart before it provisions
> them. Custom has a typed adapter for the common Sandbox executor on Kata and
> the explicit ordinary-container backend. It must have platform registration,
> current Team/AgentPolicy authorization and a Gateway-qualified image. Custom
> without a Sandbox uses the `customRuntime` binding provider, which the
> Operator chart enables by default.
>
> Private images use Portal-managed Registry credentials and the Operator's
> credential synchronizer. Custom supports non-default usernames. Built-in
> profiles still must use `vibe`. A default installation allows Custom Agents
> in the Portal and the Operator, but it approves no Custom image. A Custom
> Agent starts only after an administrator approves its image digest. Refer to
> [Custom runtime](#custom-runtime).

## Configuration

The cluster-scoped [Agent](../crds/#agent) resource embeds its configuration in
`spec.agent`. This fragment shows a Custom configuration, not a runnable
deployment:

```yaml
spec:
  agent:
    type: custom
    image: registry.example.com/team/agent@sha256:<digest>
    registrySecretRef:
      name: registry-example-agent
    username: vibe
    port: 8080
```

| Field | Contract |
| --- | --- |
| `type` | `hermes` (default), `openclaw` or `custom`. It is immutable after creation. |
| `image` | Necessary for Custom. Use the exact `<repository>@sha256:<digest>` reference that an administrator lists in the Operator's `customRuntime.approvedImages`. The Operator compares the two strings exactly, so it never approves a tag reference. Built-ins use a pinned platform default or a qualified override. After the value is set, it cannot be cleared. |
| `registrySecretRef` | An optional reference to a Portal-managed source Secret. The reference contains only `name`. |
| `username` | A non-root user that exists in the image. The default is `vibe`, and HOME is at `/home/<username>`. It is immutable. |
| `port` | HTTP port, 1–65535. When `webUI` is true, Custom serves its Web UI on this port. Otherwise, Custom serves the Chat API. If the port is omitted, the Agent is headless. Built-ins keep qualified native defaults. Platform desktop and terminal access are independent of this field. |
| `webUI` | Boolean, default false. When it is true, Custom must set `port` and serve a browser application on that port instead of the Chat API. The application is published at `/ui/<instance>/` through platform authentication. |

`spec.agent` is still necessary. `agent: {}` selects Hermes. `spec.model`,
ownership, Team, skills, storage, resources, Helm settings and lifecycle stay
outside it. `gatewayTokenSecretRef` is necessary for built-ins and not allowed
for Custom. It is not a model or registry credential. There is no legacy
`spec.agentType` fallback or migration tool.

AgentPolicy limits the Harness types of a Team with `harnesses.allowed`. Image
approval is deployment-wide: a built-in image override must match its runtime
profile, and a Custom image must be in the Operator's
`customRuntime.approvedImages`. There is no per-Team image allowlist. Other
user images use Custom. An arbitrary image cannot claim built-in
compatibility. The API exposes no UID/GID, command, environment or volume
overrides.

## Built-in runtime profiles

Each runtime chart includes a versioned profile and native configuration assets
under `agents/hermes/` and `agents/openclaw/`. Operator resolves omitted images
and ports from that chart and writes them to the Agent. Then Operator
reconciles the saved configuration. Chart version and image version stay
separate.

An image override must match the profile's default image or an exact entry in
its `qualifiedImages` list. This compatibility check does not replace image
authorization policy. Port overrides must not use a native helper, dashboard,
desktop or terminal port.

Use `spec.agent.image` and `spec.agent.port`. Do not use image or primary-port
Helm values. The operator rejects these values and other reserved platform
inputs, for example `runtime`, `agentRequest`, and `imagePullSecrets`. For the
complete list on each execution path, see
[Helm-value admission](../crds/#helm-value-admission).

The Go adapters return configuration and endpoint descriptions. They do not
operate Kubernetes, run Agent code or manage conversations. Standalone Helm
uses the same native configuration assets. Native authentication, user files
and resource names keep the same roles.

## Custom runtime

The Portal and Operator charts enable Custom support by default. The Operator's
`customRuntime.approvedImages` list starts empty: the provider can start, but it
rejects every image until an administrator approves its immutable digest. No
image is approved by default. The list holds at most 64 entries, and each entry
must have the form `<repository>@sha256:<64 hex digits>`. An Agent's
`spec.agent.image` must exactly match an entry. Numeric `imageUsers` entries are
required when the selected policy enables FQDN capture; other images keep their
image-owned identity and must be qualified for the configured HOME storage. See
[Governed observed-SNI capture](#governed-observed-sni-capture).

The Operator publishes approved image references in its catalog. The Portal
shows those references when creating a Custom Agent or editing its image. Both
owner and administrator image updates must select an approved digest. An
unavailable or empty list blocks Custom creation and image changes, not other
Agent types or unrelated edits. The Operator still checks the current approval
before provisioning. A non-Sandbox Custom Agent submitted directly, or whose
approval changed after creation, reports `CustomImageNotApproved` and waits for
administrator approval. Approval allows that Agent to retry without being
recreated. For policy-governed Sandbox Custom Agents, a disabled provider or
an unapproved image instead reports `PolicyResolved=False` with reason
`PolicyDenied`, because the common executor requires a Gateway-qualified image.
After the first catalog is published, image approvals refresh even when the
Gateway is unavailable; the catalog retains its last known model availability.

Custom uses its own Go adapter and chart branch. For Custom, a built-in profile,
native provider, configuration template or preferred model name is not
necessary. The main container keeps the image's ENTRYPOINT/CMD. Gateway-backed
instances receive `HOME`, `OPENAI_BASE_URL`, a Secret-backed `OPENAI_API_KEY`,
and `CETIRUN_GATEWAY_MODEL_LIMITS`. When the Agent declares an HTTP port, they
also receive `PORT`. Init does not receive the model credential.

### Policy-governed Custom execution

The common executor is the operator flow that runs Sandbox Agents when
Sandbox execution is enabled. Enable it through the Operator's `sandboxSources`
configuration. This configuration includes `enabled`,
`bootstrapOwnedPrerequisites` and `nativeExecution`. Use the same registered
Sandbox, platform inventory and coordination authority that managed Agents use.

Allow `custom` in `AgentPolicy.spec.harnesses.allowed`. Select that policy and
an allowed Sandbox through the Team. Portal's policy editor supports Custom
explicitly. Portal creation also requires `custom` in
`features.allowedAgentTypes`, which includes it by default.

The Operator's `customRuntime.enabled` and `approvedImages` settings control
image approval. The selected digest must be approved, and every Custom image
must use the platform Gateway for model access. The image must use a numeric,
non-root UID/GID and support its declared HOME and the
[initialization contract](#initialization). The common Pod keeps the image user
instead of the managed harness UID. The registered volume group and filesystem
permissions still must be qualified.

Sandbox-bound Custom Agents must have an explicit model-source selection. Portal
creation records the sources available under the current Team policy and
Gateway catalog, or validates the owner's requested `modelAccess` selection.
When you create an Agent manifest directly, set `spec.modelAccess`, for example
`sharedModels: true` and `githubCopilot: false` for an allowed shared model. If
you omit both switches, the Agent does not inherit legacy model access. It
stays held without a model entitlement. Custom without a Sandbox keeps its
model-selection behavior.

Creation, restart, retained suspension/resume, healthy-source migration and
ordinary deletion use the common execution state, owned HOME and grant journal.
They do not use the separate Custom apply-completion engine. Kata and ordinary
containers share this contract. Only Kata claims VM-level isolation. Resources,
scratch bounds, security, DNS, approved peers and the Cilium policy come from
the selected Sandbox and AgentPolicy. Per-instance resource or scheduling values
cannot override those controls.

Headless Agents have a portless governing Service and no application HTTP
probes. HTTP Chat and Web UI keep their explicit port and `/healthz` contract.
The platform's static loopback probe works without curl, Node or Python in the
main image. This helper is packaged with `cetirun-terminal`, and the image must
be available to the target nodes.

The terminal and the probe helper take the `cetirun-terminal` image only from
the Operator's `cetirun.helmValues`, with an immutable digest. The terminal is
on by default, so each Custom Agent in a Sandbox needs this image, also a
headless Agent. Without it, the Agent reports `PolicyResolved=False` with the
reason `PolicyDenied`. With `ttyd.enabled: false`, only HTTP probes and
observed-SNI capture need the image. `make deploy` does not set it. See
[Custom terminal image](../helm/#custom-terminal-image).

The optional terminal uses the same pinned image and the registered
`topology.kubernetesAPI` destinations. Its separate Cilium selector excludes it
from the Agent policy. It receives only DNS/API egress and approved gateway
ingress, not Agent internet/model access. Its own API projection has no explicit
audience override. Protected Pod admission allows only this owned terminal's
current named target, main container and shell protocol. Admission also
examines the execution sources and the non-stopping reservation again.

The terminal does not mount HOME or model credentials. Suspension stops both
workloads before it acknowledges completion. For bound workloads, admission
accepts Kubernetes' copied region/zone labels only when they match a fresh read
of the assigned Node. This rule applies to terminal CONNECT and ordinary Pod
updates. Other labels and template inputs still must match exactly.

Custom receives an immutable, Agent-owned `AGENTS.md` snapshot and a read-only
`$HOME/.agents` view. Optional Memory/shared skills compose into that view. They
do not change the native entrypoint or replace user files that the instance
keeps. The controller-only `custom-runtime` authentication Secret is
identity-pinned, but it is not mounted into the Custom workload.

#### Governed identity and transport

Custom can use the same approved Entra workload projection as managed harnesses.
For `AgentPolicy.spec.entraID.enabled`, the platform capability and an exact
per-Agent identity approval are still necessary. Only the main application
receives the exchange token, which rotates, and the Azure SDK environment.
Initializers and the separate terminal do not inherit it. The application must
implement its own SDK use. Federation and Azure resource permissions are
external prerequisites.

Governed Connections are a separate opt-in. Include `custom` explicitly in the
Operator's `connectionsMCP.runtimeTypes`, Broker's `toolService.runtimeTypes`,
and Portal's `agentTools.runtimeTypes`. Also enable the native Connections
configuration that already exists. The defaults are still OpenClaw/Hermes only.

The main application receives `CETIRUN_CONNECTIONS_MCP_URL` for the HTTPS
endpoint, `CETIRUN_CONNECTIONS_MCP_TOKEN_FILE` for the Pod-bound proof, and
`CETIRUN_CONNECTIONS_MCP_CA_FILE` for the public CA. The proof rotates. Its MCP
client must read the proof and trust again for new requests. It must verify TLS
and must not replay a request that has an unknown outcome. The Pi example
implements this contract for both HTTP and native TUI sessions.

Agent Identity uses the directory lifecycle and credential-policy readback that
already exist. For Custom, Agent Identity must have the governed Connections
opt-in. Personal GitHub/Google connections work without Agent Identity or
generic Entra projection. Every provider connection must have an explicit owner
binding. Provider credentials stay inside Broker/MCP and never enter the Custom
workload.

Custom HTTP Chat and Web UI can use approved ingress mTLS on either backend.
An approved image must obey `CETIRUN_BIND_HOST=127.0.0.1` without a change to
its entrypoint. Its declared HTTP port and health contract do not change. The
static probe examines public certificate validity and the active Envoy leaf.

An enabled terminal uses its own TLS helper and keeps only the DNS/API egress
that it already had. Private Portal Chat must have its own approved client
credential. It must not use the Gateway private key. Refer to
[Custom mTLS setup](../../guides/agent-ingress-mtls/#custom-http-and-terminal).

#### Governed observed-SNI capture

After the platform registers the common capture adapter, Custom Chat, Web UI,
and headless workloads can use `blockedFQDNs` on Kata or on container. Gateway
image approval alone is not sufficient. The administrator must also approve
the numeric user and group of the immutable image in the Operator chart. This
example uses a synthetic image digest:

```yaml
customRuntime:
  imageUsers:
    "registry.example.com/pi@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":
      uid: 1024
      gid: 1024
```

The image must already be in `approvedImages`. Before you add it, make sure
that the UID and GID are the actual values that the image is intended to use.
Both must be positive 32-bit integers. UID 1337 is reserved for the proxy.

The Pod runs the original entrypoint of the image with this explicitly approved
identity. As a result, the workload cannot use the capture bypass of the
proxy. An absent approval fails closed. Images without FQDN capture keep their
image-owned identity.

Capture uses the exact managed proxy and the trusted initializer before the
normal initialization of the application. For HTTP probes, both the
application and the proxy must be ready. A headless capture workload examines
only the managed proxy. It does not get an application port. Pin `ttyd.image`
for that static probe, even when the optional terminal is disabled.

FQDN capture works together with Entra, Connections, and HTTP mTLS when each of
them has its separate approval. An enabled terminal keeps its separate TLS
configuration and DNS/API network policy. It does not get the capture
permissions or the model permissions of the Agent. The capture matches the
visible SNI exactly. It does not decrypt HTTPS, and it gives no guarantee for
ECH.

**Current boundaries:** this common Custom path does not support desktop.
Headless images have no application HTTP endpoint and cannot request ingress
mTLS. If no access surface is necessary, keep ingress disabled. Explicit
unsupported requests fail closed. A disabled policy is not a workaround.

For scoped Kubernetes access, its separate common-adapter authorization is
still necessary. Do not treat template, controller or local API-server tests
as live image, model/tool, Cilium, CSI, identity-provider or fencing
qualification. Before you enable the path, deploy Operator, admission, Portal
and runtime chart artifacts that match each other.

### Custom without Sandbox selection

The operator uses an internal `CustomBindingProvider`. The platform capability
owners supply this provider. Its receipts identify the exact Agent UID,
generation and image, namespace, model Secret, kept HOME PVC, immutable context
snapshot, network peers and their revisions. The controller compares these
references with the actual objects. The Kubernetes provider reuses the platform
Gateway controller to provision the Virtual Key of each approved Agent. It does
not create another Gateway or database, publish skills, or invent a storage
handoff.

Without the provider, a non-Sandbox Custom Agent reports `CustomRuntimeDisabled`.
Other binding dependencies can report `CustomBindingsPending`. Objects that are
absent, changed or foreign report `CustomBindingRejected`. Registry references
use the separate credential synchronizer. Native integrations are still
rejected. No placeholder credentials or native-secret fallback can start the
workload.

The Operator chart enables the `customRuntime` Kubernetes provider by default.
It runs only the image digests that an administrator approves in
`customRuntime.approvedImages`.

This provider prepares the namespace, HOME claim template and canonical
context. It also coordinates Pod replacements, for
example for approved image updates. The runtime Chart keeps and reuses the same
HOME claim. Every approved Custom image receives only its own Virtual Key and
the Gateway API base for model access that the platform supplies. This does not
restrict arbitrary outbound connections from user code in the image.

For Gateway-backed Custom Agents, the Portal Overview that already exists
supplies the platform Copilot sign-in. A compatible runtime chart marked
`cetirun.microsoft.com/custom-copilot: v1` deploys the separate no-database
proxy. Only Gateway Pods can reach it, and OAuth and proxy credentials never
enter the Agent container or HOME. The Virtual Key of the Agent authorizes the
shared models and the private Copilot models of that Agent. The [Pi example]({{< repo "examples/pi-agent/README.md" >}})
supports this path and discovers models through the Gateway. Portal Chat still
gets its model choices from the Agent, not directly from the Gateway catalog.

The approved context snapshot is mounted read-only at `$HOME/.agents`. This view
contains, for example, `skills/cetirun-memory/SKILL.md` and the canonical
`AGENTS.md`. An image directory with the same name cannot make that view
writable. Private memory stays elsewhere in the instance HOME. The initial
snapshot format is an immutable ConfigMap that sets an explicit path for each
key. It is not a package installer or skill marketplace.

With platform `skills.shared` or Team `skillStorage`, Custom uses the same Blob
CSI PV/PVC templates as the built-in runtimes. Init reads its claim but does not
write to it. Init combines its skills with the approved ConfigMap in a temporary
volume for each Pod. The Agent receives only the completed, read-only view at
`$HOME/.agents`. Shared skills cannot replace canonical skills such as
`cetirun-memory`. The copy does not change the user's HOME.

The view is rebuilt when a new Pod starts, not when shared files change.
Publish the approved content before a controlled replacement. Allow enough
ephemeral storage for the copy. Concurrent changes to the shared source are not
a transactional snapshot.

Custom keeps its canonical discovery path and does not inherit the native
`skills.mountPath`. Nonempty npm/ClawHub package selections must be
materialized in advance. They report `SkillsMaterializationPending`. Custom
does not run package installers.

Custom keeps the shared platform desktop, NetworkPolicy, exec restriction,
PodDisruptionBudget and Istio discovery scope under their platform settings.
The desktop is enabled by default, also when the Agent has no HTTP port. Its
gateway route uses the platform authentication and instance authorization that
already exist. It does not expose the Custom chat endpoint or invent a native
Agent UI. Desktop and Terminal can be enabled independently.

Custom uses a separate `cetirun-terminal` Deployment and Service from the
runtime Chart. The dedicated image is built with
`app/cetirun-operator/Dockerfile.terminal`. It does not reuse the Operator
image. The Service uses `ttyd.port` (7681 by default). But the browser keeps the
authenticated `/<instance>/term/` path.

There is no terminal port field in the Agent CRD. To disable the terminal of an
instance, set `spec.helm.values.ttyd.enabled: false`. Charts from before this
terminal integration keep their previous Custom behavior.

The terminal opens a Kubernetes exec session in the main container of the
Agent. Its entrypoint searches that container for `zsh`, then `bash`, then
`sh`. If none of them can run, it reports that no terminal exists and fails.
It repeats discovery after a container replacement. The Agent image keeps its
own ENTRYPOINT/CMD and does not have to start ttyd. The terminal Pod does not
mount Agent HOME, share its process namespace or receive model credentials.

The Operator prepares instance-scoped read roles. The Chart binds the terminal
ServiceAccount and deploys its workload. A fixed exec role and the instance's
admission policy restrict requests to the named Pod, main container and
approved shell/probe commands. Gateway authentication, instance ownership, Agent
UID and suspension/deletion checks are still necessary. Terminal ingress also
must have the configured gateway identity through Istio. Its separate
NetworkPolicy limits outbound access to discovered API server addresses, DNS
and the mesh control plane.

The Portal enables Terminal only after its owned Deployment becomes ready.
Suspension, deletion and a Sandbox hold stop the terminal and withdraw its
route. Pod replacement waits and opens a new exec session without a replay of
queued input. A normal shell exit and an authorization failure stop
reconnection.

An ambiguous connection failure against the same running container ends the
session and does not silently duplicate it. Browser reconnection cannot restore
a stopped process. Where available, use the Agent's own session persistence.
These behaviors must be qualified with the target CNI, mesh, runtime and image.
A configured route is not an interactive acceptance test.

Custom does not run a native authentication helper, doctor, session logger,
Tailscale or the native upgrade hook. The Agent and HOME init containers inherit
the numeric non-root identity of the image. Kubernetes applies the approved
volume group. The shared desktop keeps its platform image, UID 1024 and
`/home/vibe` mount, which exposes the same PVC as the Agent's resolved HOME.
Image/filesystem compatibility still must be qualified. This qualification
includes access by both identities.

The platform configures shared controls, desktop settings, resources, node
selection, affinity, tolerations, topology spread and RuntimeClass. Per-Agent
Helm overrides accept resource requests and limits, `ttyd.enabled`, and desktop
enablement, logging, resolution and resources. Terminal and desktop images,
terminal ports, gateway identity and URL configuration stay platform-owned.
Raw commands, containers, envFrom, identity, volumes, Secret references and
native feature settings are rejected. The final manifest check covers every
rendered object, not only the main container.

Preflight runs in the actual Helm action context. The controller seals
validated objects as canonical JSON before handoff. It applies only that
snapshot, with hooks and CRDs disabled. The strict decoder rejects unknown,
duplicate and ambiguously converted fields.

Server-side dry-run examines the exact objects before a workload is stopped.
This examination includes API validation and access permissions. Local checks
also cover resources, PDB values, CIDRs, annotations and scheduling. An Agent
HTTP endpoint must have NetworkPolicy enabled because it has no
application-level Chat token. A disabled policy is not silently overridden.

The [official VS Code Server example]({{< repo "examples/vscode-server/README.md" >}})
supplies a Web IDE with native GitHub Copilot sign-in. Its offline tests cover
prefix routing, browser storage and HOME preparation without a model call. Real
Copilot use, browser compatibility, network isolation and storage behavior must
be validated separately.

## Initialization

The init container uses the selected Agent image, with its image HOME visible
and the home PVC mounted at `/home-data`. It copies image HOME one time, then
writes `.cetirun-home-seeded` on the PVC. Later Pod initializations skip that
copy but still apply Go-generated configuration and run the selected Agent's
preparation. The main container mounts the same PVC at HOME and keeps its
native entrypoint. Custom uses the same fixed initialization stages, with an
empty native configuration stage and shared-context preparation when enabled.
It never loads the stages of another Harness.

Nonempty, unrecognized homes fail and are not overwritten. An interrupted seed
is tracked separately and can resume only with the same immutable image.
Native initialization records that already exist prevent a reseed of
established homes. These records are not authorization or ownership evidence.

Errors in necessary configuration keep the affected files and stop startup.
Routine initialization does not import old OAuth credentials or migrate between
Agent types. The credentials that already exist and the native authentication
helpers stay available.

## Private Memory Context

Memory Context is enabled by default for OpenClaw, Hermes and Custom Agents.
This includes the Sandbox Agents that the common executor runs. The only
workloads without Memory assets are Sandbox workloads that the common executor
does not reconcile, for example a retained version-1 native single-start
lifetime. Memory
Context uses a separate image, not a separate Helm release or service.

Operator's `memoryContext.image` is an optional full image reference. When it
is empty, Operator selects `cetirun-memory-context:<appVersion>` from each
Agent's pinned runtime chart and its publication registry. A change to the
platform's default Chart version does not change assets for an Agent that is
already pinned. Charts without Memory support keep their previous behavior
until the owner upgrades the Agent's Chart. To override the image for all
Agents whose Charts support Memory, set `global.memoryContext.image` in the
platform chart. You can set the more specific
`cetirun-operator.memoryContext.image` instead.

The standalone runtime chart uses `memoryContext.image` with the same release
defaults. Semantic-version tags (`X.Y.Z`, with an optional suffix such as
`-rc.1`) and explicit SHA-256 digests are accepted. Other tags, for example
`latest`, are not. A release tag selects a version, not immutable bytes. If you
must pin the exact image, supply a digest. The Memory image that matches the release
must be published with the release before you deploy it.

To disable Memory, set `memoryContext.enabled: false` on the Operator or on the
standalone runtime chart. An empty image value does not disable it. When Memory
is disabled, OpenClaw and Hermes keep the read-only shared skills mount at
`skills.mountPath`. Updates of the shared skills appear there without a new
context snapshot.

### Common initialization and discovery

The `memory-assets` init container installs the helper, adapters and source
instructions into an `emptyDir`, mounted read-only at `/opt/cetirun-memory`.
The HOME initializer uses the Agent's own image to prepare its managed
instructions. Custom receives a composed context view:

```text
$HOME/.agents/AGENTS.md
$HOME/.agents/skills/cetirun-memory/SKILL.md
$HOME/.agents/skills/cetirun-memory/references/requests.md
$HOME/.agents/skills/<shared-skill>/...
```

For Custom, the composer builds one complete view and mounts it read-only at
`$HOME/.agents`. During init, it reads shared skills from a separate read-only
source mount. The main container never mounts the shared PVC over
`.agents/skills`. A shared `cetirun-memory` directory or another collision with
canonical skills fails initialization and does not replace a platform skill.
For a Team of the [Skill Marketplace](../../operations/skill-marketplace/),
the source mount is the subtree of the Team. Init waits for the release of the
Team's selection and copies only its skills into `.agents/skills`.

The sources and the PVC's `.agents` files that already exist are preserved.
This does not change how shared storage is provisioned. For Custom, a
non-default `skills.mountPath` also exposes a read-only alias of the composed
skills view. It is not a second source over the canonical path.

The composed view is stable for the Pod's lifetime and is rebuilt for a new Pod.
Publish shared content before a replacement. A copy of a shared source that
changes concurrently is not a transactional snapshot.

Hermes and OpenClaw mount managed instructions only at
`$HOME/.agents/AGENTS.md`. Personal files and skills elsewhere in `$HOME/.agents`
remain visible and writable.
The shared Skills PVC is mounted directly, read-only, at `skills.mountPath`,
so updates remain visible without rebuilding a context snapshot. If that path
is explicitly `$HOME/.agents/skills`, the shared mount occupies that directory.
The platform Memory skill is loaded separately from
`/opt/cetirun-memory/skills/cetirun-memory/SKILL.md`. Both runtimes reserve that
skill name for the installed asset and ignore conflicting personal or shared
copies. Ordinary native instances limit the platform context volume to `40Mi`;
reconciled Sandboxes keep their configured scratch limit. Existing HOME files
and shared storage contents are not moved or deleted.

The binary and adapters stay available read-only at `/opt/cetirun-memory`.
Private notes stay in `$HOME/.cetirun/memory` on the HOME volume that already
exists, outside the instruction view.

The HOME context files and older note directories that already exist are
preserved. They are not imported automatically. An owner can import selected
legacy files with `import plan` and `import apply`. The helper keeps those
source files and marks imported claims for review.

Import refuses files that usually hold credentials, keys or shell history.
Examples are files under `.ssh`, `.aws`, `.azure` or `.kube`, `.env` files,
`.git-credentials` and the harnesses' configuration. The helper also refuses
text that looks like a token or key.

When you revise a note, its source records can stay as they are. Thus, you can
correct the body and the status without a deletion of the source lineage. New
or changed sources and other revised text must pass the current checks.

Directory placement and host discovery are separate:

- OpenClaw loads workspace `AGENTS.md`, not `$HOME/.agents/AGENTS.md`
  automatically. Its Memory plugin explicitly loads the common file. CetiRun
  also configures `skills.load.extraDirs` with `$HOME/.agents/skills`, even for
  non-default OpenClaw state directories.
- Hermes discovers project `AGENTS.md` from its working directory and Git-root
  chain. Its Memory plugin explicitly loads the common file, and
  `skills.external_dirs` preserves configured directories and includes personal
  `$HOME/.agents/skills`, the shared mount when enabled, and the platform's
  `/opt/cetirun-memory/skills` directory.
- Custom images must load the common context and skills. They must also
  connect the helper through a native adapter or MCP. The Pi example does this
  through its SDK. It does not assemble the view, and it does not replace
  skills from another asset directory. Mounted files alone do not register
  tools or lifecycle hooks in an arbitrary Custom application.

These discovery rules are documented by [OpenClaw Skills](https://docs.openclaw.ai/tools/skills),
[OpenClaw workspace files](https://docs.openclaw.ai/concepts/agent-workspace),
[Hermes Skills](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills#external-skill-directories)
and [Hermes context files](https://hermes-agent.nousresearch.com/docs/user-guide/features/context-files).

### Runtime behavior

Reads, writes and bounded method learning are enabled by default. Both installed
profiles, `memory-retrieval-v1` and `artifact-contract-v1`, are available.
Model-based fixture evaluation still must have a supported native route. It is
limited to four extra model requests per store in a rolling 24-hour window. It
does not run without an active request or change tool permissions.

OpenClaw and Hermes enable Memory by default for sessions admitted by the host
on entrypoints that run the Memory adapter. This includes interactive sessions,
message channels, scheduled tasks and native descendants of an admitted parent;
OpenClaw heartbeats can also use Memory. Each host still controls access to its
entrypoints. These sessions share the same Agent-private store, not separate
stores per sender or channel. Replies in a group or channel can disclose
recalled material to its participants.

Owners can narrow access with `owner_access` in the host's plugin settings or
`CETIRUN_MEMORY_OWNER_ACCESS`. These controls can restrict platforms, senders,
conversations, sub-agents, cron and ACP. Profile and environment restrictions
both apply, and native descendants inherit their parent's scope and
restrictions. `CETIRUN_MEMORY_NO_CAPTURE=true` disables writes while allowing
reads; `CETIRUN_MEMORY_NO_MEMORY=true` disables both. See
[Memory access for OpenClaw and Hermes]({{< repo "app/cetirun-memory-context/OWNER_ACCESS.md" >}})
for each host's configuration format and descendant-lifetime rules. Child
access requires native lineage and a current parent grant; a parent ID in a
prompt does not authorize it. A child cannot relax inherited restrictions or
attest an owner preference. Messaging tools still require confirmation before
sending recalled material outside the session.

CetiRun has no central Memory policy. Each Agent keeps its own store on its
HOME volume, and the platform enforces its user access rules. In OpenClaw,
owners control Memory for each conversation with `/memory off`,
`/memory no-capture`, and `/memory on`. These commands do not delete notes or
disable the history of the Harness.

The commands work only in OpenClaw. Hermes has its own `/memory` command, which
approves or rejects changes to its native memory files. In Hermes, these
commands do not reach CetiRun Memory, and Memory stays on. In Hermes, use
`owner_access` instead.

To limit the channels and senders that can use Memory, configure
`owner_access`. The helper does not bind
stored notes to an Agent or namespace UID. Private methods are not published as
Team skills.

## Private registries

Portal accepts a write-only Registry username and password or token. It creates
the Agent before it saves a `kubernetes.io/dockerconfigjson` source Secret. The
actual Agent UID owns this Secret. Operator validates that owner, the single
registry host derived from the image, and workload permissions before it copies
the Secret into the runtime namespace. The local name is
`registry-pull-<uid-without-hyphens>`. Users do not pre-create runtime
namespaces or Secrets.

### Installation and API

The Operator chart owns the separate `cetirun-registry-credentials` namespace
and keeps it when the chart is removed. The namespace must not be shared with
the Agent Identity Broker or runtime workloads. Both components accept
`REGISTRY_CREDENTIAL_NAMESPACE`. The umbrella chart sets
`global.registryCredentialNamespace` one time. Standalone installations must
set `registryCredentials.namespace` values that match. Only the Operator chart
creates the namespace.

If an Agent name uses the default or configured credential namespace, the name
is rejected before any lifecycle change. This rule also applies to deletion. A
collision that already exists keeps its finalizers, and administrator recovery
is necessary. Before you remove such an Agent, examine the namespace ownership.
Runtime charts must advertise the
`cetirun.microsoft.com/registry-credentials: v1` annotation. Older pinned charts
must be upgraded before credentials are attached.

Raw deployment or instance `imagePullSecrets` overrides are not merged with the
managed binding. Before you upgrade a deployment that used them, follow the
[registry upgrade steps](../../getting-started/platform/#upgrading-runtime-registry-configuration).
Standalone Agent Helm releases keep their separate pull-Secret setting.

| Operation | Contract |
| --- | --- |
| `POST /api/v1/instances` | Nested `agent` input and optional `registryAuth: {username, password}`. Private creation must have an explicit qualified image and a stable `Idempotency-Key` header. |
| `PUT /api/v1/instances/{name}/registry-auth` | Replace with a complete username and password/token. Use `If-Match: "<uid>/<resourceVersion>"` from a fresh instance response. |
| `DELETE /api/v1/instances/{name}/registry-auth` | Use the same precondition. Detach the reference. Operator removes the future Pod binding before it deletes this UID's copies. |
| `GET /api/v1/instances/{name}` | Return `agent`, `uid`, `resourceVersion`, `registryAuthConfigured` and conditions, never Registry credential data. |

Create requests reject the old `agentType` field and user-selected Secret names,
namespaces or registry login URLs. Ordinary instance PATCH uses `uid` and
`resourceVersion` in its JSON body. Instance deletion and upgrade also use
them. This management precondition is separate from the Registry endpoint's
`If-Match`.

PATCH accepts image and port updates inside `agent`. Omitted fields are
kept, and a null port clears the override. A pinned image cannot be
cleared, and type and username are immutable. Custom image updates replace the
Pod and keep HOME. The platform must approve the new image, and the image must
be compatible with the data and image user that already exist. Credentials and
references cannot change through ordinary PATCH.

The same credential controls work during Custom self-service creation and on
Custom instances that already exist. Overview shows the Registry credentials
section only when private-registry authentication is enabled. In that case,
Overview also shows the section when the save of the source credentials is
still not complete.

Removal of the credentials hides the whole section. Synchronization
diagnostics stay available in Status. The deployment's allowed types and
image approval still apply. Registry credentials do not enable native
integrations or supply model authentication for a Custom image.

If source persistence fails after Agent creation, the response identifies the
instance that already exists. Retry the same creation request with its request
ID and settings, or manage credentials on that instance. The retry is
recognized before the instance quota check and never replaces a saved source.
Creation failure does not delete the Agent, namespace or HOME.

A concurrent mutation returns a conflict. Refresh before you retry. Never submit
a masked or empty password.

Credentials stay out of CRDs, responses, Helm values, logs, Agent environments
and HOME. Portal clears credential fields after submission or cancellation and
does not keep them in mutation caches. Source persistence, synchronization and
a successful image pull are separate states. `RegistryCredentialsReady` records
copy readiness. A bad password, an unavailable registry or an image that does
not exist can still leave the Pod unready. An image cached on a node is not an
authentication test.

### Rotation and recovery

Content rotation updates the same local Secret without a rollout. Addition or
removal of the reference updates future Pod templates and does not replace a
healthy Pod. Native StatefulSets use an `OnDelete` update for that limited case.
A later chart, image or runtime configuration change explicitly restores
`RollingUpdate` and clears the credential-only readiness marker. A chart change
is never classified as credential-only, even when its values are unchanged.

An initial image-pull failure is different. A Pod that never ran an Agent and
does not have the new pull reference is replaced with UID and resource-version
preconditions. Its StatefulSet and PVCs are kept. This recovery path does not
replace a Pod that is healthy or that already ran a workload. Identical retries
of an applied native release skip upgrade hooks.

New built-in namespaces record their Agent UID at creation, before Pod
readiness. An unowned namespace that already exists is not adopted on the basis
of its name alone. It must have a trusted prior namespace/release status
binding. Custom keeps the writer-coordination rules that it already has. When
credentials cannot be synchronized, suspension stays available. Suspension
does not pull a chart or start an upgrade hook.

Native ingress cleanup validates the UID-owned namespace and managed route
labels. Custom-only ownership annotations are not necessary for this cleanup.
Deletion and same-name recreation use actual Agent UIDs and do not reuse
another instance's source. These operations do not revoke the external Registry
account or change PVC retention policy.

### Permissions

The permission gate examines effective ServiceAccount authorization. This
examination includes named Role/ClusterRole grants, indirect workload creation
and service-account token issuance. Incompatible access or failed authorization
checks block the save or copy of credentials. The platform does not silently
rewrite grants that already exist.

The own-namespace exec permission of the platform Custom Terminal is accepted
only after its Agent UID ownership is examined. The exact fail-closed admission
rule and Deny binding are also examined before acceptance. That rule and binding
restrict exec to the Agent's main container and approved shell commands. This
exception never applies to an Agent runtime identity, cross-namespace exec,
Secret access, token issuance or admission-policy changes.

Portal and Operator must have the packaged RBAC inspection and
SubjectAccessReview permissions. These permissions include reads of the terminal
ServiceAccount and admission objects. Reviews are deduplicated within each check
and run with at most eight requests in flight. Authorization results are not
cached across checks. Each check inventories workloads one time and tests their
identities against the current Agent's credentials. It also inspects named RBAC
grants across namespaces.

Unchanged synchronization only rereads the source and local Secret and examines
their owner, contents, revisions, and saved receipt. It does not repeat the
cluster-wide permission audit. Completed creation retries are also read-only.
Credential writes and new workload applications always run a fresh audit before
they continue. As a result, initial saves and replacements still cause
synchronous authorization work proportional to the runtime identities. There is
no cached authorization result that can allow a later write.

Permission changes do not revoke an already-distributed credential or stop a
running Agent automatically. This feature does not narrow broad control-plane
grants that already exist. Operator also must have Pod deletion permission for
the bounded initial-pull recovery above, but no such permission is granted to
Agent workloads. Privileged containers and unqualified added capabilities are
rejected. Container setuid support alone is not treated as host privilege. API
authorization and credential mounts are examined separately.

Administrators must also do these tasks:

- Keep tenant Kubernetes identities out of both credential locations.
- Protect node access.
- Configure Secret encryption at rest and backups.
- Prevent request-body logging at proxies or tracing collectors.

Registry mutations do live authorization reviews. API capacity and latency at
the deployment's instance count must be qualified for the environment.

## BYO image contract

A Custom image must meet these requirements. The sections that follow give the
details.

| Area | Contract |
| --- | --- |
| Image | An exact `repository@sha256:<digest>` reference that the Operator's `customRuntime.approvedImages` lists. The Pod uses `imagePullPolicy: IfNotPresent`. The image keeps its own ENTRYPOINT and CMD, which must run in the foreground. |
| User | A numeric non-root `USER <uid>:<gid>` that matches `username`. HOME is `/home/<username>`. |
| Security | The main container and the HOME initializer run with `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, all capabilities dropped, and the `RuntimeDefault` seccomp profile. Images that need setuid programs, for example `sudo`, do not work. |
| Root filesystem | Read-only. Without a Sandbox, this is fixed. In a Sandbox, the AgentPolicy setting `security.readOnlyRootFilesystem` controls it, and its default is `true`. Do not write to `/etc`, `/var`, `/opt`, or other image paths. |
| Writable paths | `$HOME`, which is the retained HOME volume, and `/tmp`, which is an `emptyDir`. In a Sandbox, the scratch limit of the Sandbox applies to `/tmp`. |
| Read-only paths | `$HOME/.agents`, which holds `AGENTS.md` and the approved skills, and `/opt/cetirun-memory` when Memory is enabled. |
| Kubernetes identity | Without a Sandbox, the Pod does not mount a ServiceAccount token, and its volume group is the Operator's `customRuntime.fsGroup` (`1024` by default). |
| Ports and routes | With `port`, the image serves the [Chat](#chat) routes, or the [Web UI](#custom-web-ui) when `webUI` is `true`. Without `port`, the Agent is headless. |
| Probes | `GET /healthz` on the HTTP port for startup and readiness. Startup runs every 5 seconds and allows 60 failures, so the image must be ready in about 300 seconds. Readiness runs every 10 seconds. In a Sandbox, the timeout of each probe is 3 seconds. Without a Sandbox, it is the Kubernetes default of 1 second. There is no liveness probe, so the platform does not restart a process that hangs. A headless Agent has no HTTP probes. |
| Environment | Always `HOME`, `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and `CETIRUN_GATEWAY_MODEL_LIMITS`. With a port, `PORT`. With a Web UI, `CETIRUN_WEB_BASE_PATH` and `CETIRUN_WEB_INSTANCE`. With Memory, `CETIRUN_MEMORY_ASSETS=/opt/cetirun-memory` and `CETIRUN_MEMORY_ROOT=$HOME/.cetirun/memory`. With ingress mTLS, `CETIRUN_BIND_HOST=127.0.0.1`. With governed Connections, `CETIRUN_CONNECTIONS_MCP_URL`, `CETIRUN_CONNECTIONS_MCP_TOKEN_FILE`, and `CETIRUN_CONNECTIONS_MCP_CA_FILE`. |
| Initialization tools | `sh`, `cp`, `mkdir`, `mv`, `rm`, `chmod`, `mktemp`, `find`, and `cmp`. For a Team of the Skill Marketplace, also `sleep`. See [Initialization](#initialization). |

## Image, storage and model access

BYO images must have a foreground entrypoint and a numeric non-root
`USER <uid>:<gid>` that matches `username`. In the runtime contract, HOME must
be on a writable PVC that is kept independently. Image HOME can contain
configuration and dependencies for the first copy. An image update does not
refresh files that were already copied into the PVC. Plan dependency or data
migrations separately.

`sh`, `cp` and the basic tools that the common initializer uses are necessary
for the initial BYO contract. These tools include `mkdir`, `mv`, `rm`, `chmod`,
`mktemp`, `find` and `cmp`. For a Team of the Skill Marketplace, `sleep` is
also necessary: init waits for the Team's release with it. The copy must keep
hidden files, executable bits and symlinks. It must not follow links outside
HOME.

If a necessary tool is not available, initialization fails. The platform does
not install the tool or elevate privileges. Custom does not have to supply
Python, jq, npm, kubectl or a platform-specific hook.

The BYO contract reserves `$HOME/.agents/skills` for read-only approved skills.
`cetirun-memory` and the canonical `$HOME/.agents/AGENTS.md` are necessary,
independently of Team selection. Images load them through native discovery or
prompt/bootstrap adaptation. Private memory and native sessions stay
instance-local. The built-in skill locations that already exist are described
in [Configuration and skills](../../concepts/configuration/).

The contract uses `OPENAI_BASE_URL` and the Agent's Virtual Key as
`OPENAI_API_KEY` for model discovery and calls through the shared AI Gateway.
The Operator sets both variables for an approved Custom image when the
platform AI Gateway is enabled. `OPENAI_BASE_URL` is the in-cluster address of
the Gateway with `/v1`. `OPENAI_API_KEY` comes from the Agent's own Secret
`ai-gateway-virtual-key`. The GitHub Copilot models of the Agent use the same
address and key, through the separate Copilot proxy that
[Custom runtime](#custom-runtime) describes.

The platform does not write a model list or a default model for a Custom
image. The image gets the models from `GET $OPENAI_BASE_URL/models`.

The Operator also sets `CETIRUN_GATEWAY_MODEL_LIMITS` for a Custom image. This
JSON value has the context window and the output limit of each model that the
Agent can use. Its `version` is `1`, and each entry of `models` has `id`,
`contextWindow`, and `maxOutputTokens`. A model with several Gateway
deployments has their smallest limits. A router has the smallest limits of the
models that it can select.

A model without known limits is not in the value, and the image should refuse
it instead of guessing its limits. The variable does not grant access to a
model. For the model configuration of the built-in runtimes, refer to
[Choose a model](../../guides/models/).

## Chat

Custom images with `port` and `webUI` false or omitted receive `PORT`. They
serve these routes on that port:

| Route | Purpose |
| --- | --- |
| `POST /v1/chat/completions` | OpenAI-compatible text chat, also with streaming replies. |
| `GET /v1/models` | Model identifiers accepted by this Agent's chat endpoint, not the Gateway catalog. |
| `GET /healthz` | HTTP startup/readiness: `200` with `{"status":"ok"}`, without model or tool calls. |

The Portal Chat path is browser → authenticated Portal backend → private
Agent Service. The optional feature serves only Custom with `port` and `webUI`
false. It is disabled by default. Compatible Portal, Operator and runtime charts
are necessary for it. Hermes and OpenClaw continue to use their native
interfaces.

An application-level Chat token is not necessary for Custom endpoints. The
platform enforces user authorization and network isolation. Built-ins keep
native API authentication. Never forward the outbound `OPENAI_API_KEY` to a chat
endpoint.

Portal Chat owns its session list and history in the current browser's
IndexedDB, and it sends the necessary `messages`. The Portal server has no
history database or PVC. Native history is optional, and no synchronization is
necessary for it. Interrupted requests are not automatically replayed. Browser
history does not sync across devices or supply a backup. For information about
how to enable the feature, and about privacy, limits and recovery behavior, read
[Custom Chat](../../guides/custom-chat/).

## Custom Web UI

A Custom image sets `spec.agent.webUI: true` to serve a browser application on
`spec.agent.port` instead of the Chat API. When the flag is enabled, the port is
necessary. Both modes use the fixed `/healthz` startup/readiness endpoint. This
endpoint must work without a model call or a user's service login.

```yaml
spec:
  agent:
    type: custom
    image: registry.example.com/team/editor@sha256:<digest>
    port: 8080
    webUI: true
```

The platform supplies `PORT`, `CETIRUN_WEB_BASE_PATH` and
`CETIRUN_WEB_INSTANCE`. The base path is `/ui/<instance>/`. The instance value
is the Agent UID, which is stable across Pod replacements. The application must
keep this prefix in assets, WebSockets and callbacks. It must also scope browser
storage and cookies per instance. Paths on one origin are not a browser security
boundary.

The Operator publishes the route only through the platform's authenticated
gateway and instance-ownership check. NetworkPolicy must stay enabled. When
the Agent is Ready and the route is configured, the Portal shows the declared
Web UI and enables its link. Suspension removes the route. Desktop and Terminal
stay separate optional platform services. Web UI readiness does not establish
successful Copilot authentication or model execution.

The Portal creation and edit forms have a **Serve Web UI** switch. They use the
same port field for both modes. The API accepts a boolean `agent.webUI`. If a
PATCH omits it, the saved flag is kept. A false or null value resets it to false
and does not clear `agent.port`.

To make an instance headless, disable Web UI and clear the port together.
Changes must match the protocol that the image supports. Instance UID and
resource-version preconditions still apply.

## Lifecycle

Pin runtime settings per Agent and keep chart and image versions separate. In
the contract, upgrades must keep UID/GID, HOME and data compatibility, stop old
writers, and examine the storage handover. Private PVCs must survive compute
replacement. Before you change or delete an instance that already exists, read
[Lifecycle](../../guides/lifecycle/). Namespace deletion removes its PVCs.

No new Harness, Run or Session CRD is introduced. Unsupported runtime features
fail with `AgentRuntimeNotImplemented`. Invalid profiles, images and port
combinations fail with `AgentConfigInvalid` before workload deployment.

The shared network rules keep the platform's configured external-egress and
internal-CIDR settings, DNS and Istio control-plane access. Custom adds the
approved Gateway peer and internal Chat API callers. Desktop and declared Web UI
ingress are allowed through the platform gateway. The private Chat port is not
added to that public route. NetworkPolicy configuration alone does not prove
Gateway-only model egress or live mesh enforcement.

Governed Custom uses the common `RollingUpdate` workload, and it keeps the HOME
claim that already exists. The common executor coordinates restart, suspension
and healthy-source migration without a reset of Agent, HOME or Gateway
accounting identity. Unavailable-node fencing is not supplied.

Without Sandbox selection, Custom uses `OnDelete` and excludes the built-in
orphan-delete hook. A change to an image, model/context projection or Pod
configuration waits for `CustomMaintenancePending`. The change waits until the
platform authorizes handoff, the old StatefulSet is stopped and no active Pod
still references HOME. The controller does not force-delete Pods, detach volumes
or reset initialization records. Writer handoff is necessary only for changes
to the actual Pod/model/context inputs.

When platform-object changes such as NetworkPolicy, PDB or exec controls are
applied, they do not stop an unchanged Agent Pod. OnDelete readiness uses
observed generation and updated/ready replica counts, not convergence of
currentRevision. Identical inputs that produce different rendered output report
`RendererDrift`. Suspension can request zero replicas even when bindings are
unavailable.

For a Custom Agent without a Sandbox, the operator applies a change only
after the platform approves the handoff. The approval has a validity period and
completion checks, and it is bound to the digests of both the inputs and the
rendered output. Before the apply, the controller reads the generation and the
suspension and deletion state of the Agent again. If Helm succeeds but the
status write fails, the controller restores the status from the matching Helm
record and does not stop the Agent again.

The operator records each apply operation before the approval and keeps its
outcome until the binding provider acknowledges the completion. The operator
can restart, or the desired configuration can change. Then the operator gives
the recorded outcome to the provider again with the same operation ID. It
does not run Helm again only to release the coordination. The provider must
support a durable completion that it can repeat for that ID.

A pending completion does not prevent a request for suspension. Cleanup waits
for the completion to finish.

RuntimeClass names and node-selector label syntax are examined before handoff.
This includes values inherited from Chart defaults. Their existence and actual
runtime compatibility still must have platform qualification.

Custom ownership receipts live in the operator namespace and keep the approved
Agent, namespace and HOME UIDs. Cleanup uses that receipt and a namespace UID
precondition, not user annotations or a derived name alone. Ownership that is
absent or in conflict blocks cleanup. Never-bound instances do not delete
foreign namespaces. Deletion of an owned namespace still deletes its PVCs. The
receipt does not replace a data backup.

For a headless Custom Agent, `Ready` describes the running process, not HTTP
health, a successful model call or recovered sessions. With a port, probes use
`/healthz`. See [BYO image contract](#byo-image-contract) for the probe timing. Protocol qualification examines the response separately from
Kubernetes probes.

When Web UI is enabled and its route is configured, the Operator sets the Agent
CR's `status.accessUrl`, and the Portal returns it as `webUIUrl`. Chat-only and
headless Custom Agents leave `status.accessUrl` empty. After their routes are
configured, Portal derives the independent Desktop and Terminal entries from the
platform gateway domain and instance name. Portal does not expect the Portal and
gateway to share an origin.

## Portal self-service

The Portal creates and edits generic Custom Agents. An image-specific login
helper is not necessary for it. The Portal does not change the image's
entrypoint or manage its own model credentials. Custom Terminal and Desktop use
the platform access boundary that already exists. They stay independent of
the optional Agent HTTP port.

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

Custom must have an image that the configured runtime accepts. If `username` is
omitted, `vibe` is selected. If `port` is omitted, a headless Agent is created.

`model` stays independent of `agent` in the API. The current image-owned
Custom runtime does not use it to select a model. As a result, Custom forms
leave model setup and login to the image. Custom requests must omit Gateway
Token, Tailscale and Agent Identity settings, also when their values are empty
or false. No built-in credential Secret is created for Custom.

The instance heading shows its nonblank Display Name. If the Display Name is
blank, the heading shows the Agent's name. An owner can clear Display Name with
an empty string.

### Configuring Custom self-service

Custom is included in the default Portal type list. To disable Custom
self-service, remove it from the [Portal chart](../helm/#portal-chart) list:

```yaml
features:
  allowedAgentTypes: [hermes, openclaw]
```

Custom uses the Portal login and instance-ownership checks that already exist.
Set `customRuntime.enabled: false` in the Operator chart to disable its Custom
provider separately. Update the Operator image and the chart together. An
Operator image that is older than the chart can reject an enabled provider with
an empty approval list.
These opt-outs apply to direct Helm chart installations. The `make deploy`
entry point requires the complete platform configuration, including enabled
Custom support and a nonempty approval list. An empty `allowedAgentTypes: []`
uses the default type list; it does not disable all types.

When upgrading an installation that previously omitted `customRuntime.enabled`,
review its retained Custom values before applying the new chart. The provider
now starts by default and validates that configuration at Operator startup.
Remove retired `agentManagedModel` and `gatewayImages` fields, replace tag
approvals with digests, and check storage and image-user entries. Invalid
configuration stops the Operator, including reconciliation of other types.
An explicit `customRuntime.enabled: false` keeps the provider disabled.

Enablement of the type does not enable Agent Identity or make a separate Portal
admission grant necessary. The backend still examines the type flag, instance
limits and request fields. This includes immutable fields and concurrent-update
preconditions.

AgentPolicy limits the Harness types of a Team. There is no per-user or
per-Team image allowlist. The Operator's runtime image approvals, profile
compatibility and reserved-port checks stay in effect. Portal creation does not bypass those checks or
release a Sandbox hold.

### Editing and concurrent changes

GET responses include `uid`, `resourceVersion`, `generation` and
`observedGeneration`. PATCH, DELETE and upgrade requests must include the UID
and resource version from the object that the user viewed. A stale version or
same-name replacement returns a conflict. Before you retry, read the current
object again and review it. The Portal does not replay a stale edit
automatically.

An omitted field leaves its value unchanged. `agent.port: null` removes a
Custom HTTP endpoint or restores a built-in version's qualified default.
`agent.type` and the effective `agent.username` cannot change. A pinned image
cannot be cleared. Owners can edit the Custom image in Portal or PATCH
`agent.image`.

An approved image update replaces a running Pod and reuses its HOME PVC without
a reseed. A suspended Agent stays stopped until it is resumed. Image changes do
not change the pinned runtime Chart version. The image provider and owner are
responsible for compatibility with files, native sessions and HOME dependencies
that already exist. CetiRun does not migrate their formats.

For Team reassignment, an administrator and a suspended Agent are necessary.
Ordinary edits keep the effective Team and its governance bindings.

The displayed configuration is the requested configuration. A saved request
does not establish new runtime readiness.

Registry credentials and `registrySecretRef` mutations are not part of this
management flow. The optional Custom Chat feature has its own access checks.
Image-specific authentication stays with the image. The Portal does not show
a Custom native Agent UI or Copilot helper merely because an HTTP port is
configured.
