---
title: "Managed Agent ingress mTLS"
description: "Opt-in TLS for common-executor Kata and container managed or approved Custom HTTP workloads."
weight: 45
---

`AgentPolicy.spec.network.ingress.requireMTLS: true` enables an explicitly
registered Envoy endpoint **inside the Agent Pod**. The managed Istio Gateway
authenticates that endpoint and presents its approved client certificate.
OpenClaw API/Control UI, Hermes API/dashboard, the optional ttyd terminal and
registered noVNC desktop then receive traffic over loopback only. No plaintext network hop follows an
external Team gateway.

This is not browser client-certificate authentication. Shared browser HTTPS,
OIDC, ownership authorization, and native application authentication remain in
place. It does not inspect outbound HTTPS or implement FQDN blocking.
Cilium remains the workload-external network boundary; the in-Pod TLS helper is not
a replacement for that boundary or independent runtime-enforcement evidence.

Shared-only, Copilot-only and mixed model selections retain the same Gateway
routing. Only the Agent Service targets the managed TLS ports; the separately
owned, private Copilot companion Service keeps its own selector and port.

## Prerequisites and supported surfaces

- Use the common executor, a Kata or container Sandbox, and OpenClaw, Hermes,
  or an approved Custom HTTP harness following the contract below.
  Retained single-start lifetimes do not support this opt-in.
- Upgrade the Operator, independent admission process, and runtime chart before
  enabling it. An older chart without the protected `ingressMTLS` schema field
  fails closed rather than silently rendering plaintext.
- Register `Capabilities.mutualTLS: true` and a digest-pinned endpoint image
  under `platforms.kata.ingressMTLS` or `platforms.container.ingressMTLS`,
  matching the selected runtime in the protected platform ConfigMap.
  A capability flag alone is insufficient.
- Use one explicitly pinned shared Gateway and its existing protected Pod
  selector in `topology.ingress`. Continue listing the approved **application**
  ports there. The compiler substitutes TLS listener ports and denies the
  plaintext application ports, including against additive network allows.
- Register `"desktop": true` and application port 6080 to retain the existing
  enabled desktop default. The registration must match the rendered desktop
  selection; noVNC is not silently left as a plaintext route.
- Disable Tailscale, Teams webhooks, arbitrary sidecars/initializers,
  and extra mounted volumes for this opt-in. Unsupported surfaces fail rendering;
  they are not silently left outside TLS. Logger and ordinary platform-owned
  initialization remain supported.
- Keep ingress mode `authenticated`. `disabled` plus `requireMTLS: true` is
  invalid. `requireMTLS: false` retains the ordinary rendering and routing path.

The endpoint listens on TCP 15443 (primary API/UI), 15444 (Hermes dashboard),
15445 (enabled terminal), and 15446 (registered noVNC desktop). Its admin interface is loopback-only on 15440
and is never a Service or Cilium ingress grant. The application primary port is
the approved Agent port; dashboard and terminal remain on loopback 9119 and
7681. The managed desktop fixes noVNC, VNC and Chrome CDP to loopback ports
6080, 5900 and 18800. Only noVNC gets an external TLS route. Its local readiness
probe checks HTTP assets, the RFB listener and CDP availability without Gateway
client credentials. A custom terminal port or unexpected Service port is rejected.

The optional `desktop` registration field is omitted/false for older approvals;
it never grants desktop ingress implicitly. Upgrade strict registration readers
before adding it. Existing explicit desktop-disabled mTLS configurations remain
valid; ordinary non-mTLS desktop behavior is unchanged.

The helper has the selected Sandbox's helper requests and limits, an
unprivileged identity, dropped capabilities, and a read-only root filesystem.
Its resources are included in the existing Pod accounting. It receives neither
HOME nor application/model credentials. Istio injection remains disabled.

## Custom HTTP and terminal

A Custom registration uses `"harness": "custom"`, the exact declared application
port, `"desktop": false`, and the actual terminal selection. The image must
honor `CETIRUN_BIND_HOST=127.0.0.1`; CetiRun does not replace its ENTRYPOINT/CMD.
The unchanged `/healthz` contract is checked by the platform's static probe,
including current public certificate validity and the active Envoy leaf.
TLS probes pass an explicit `--mtls-server-name`; older probe binaries reject
that option rather than silently reporting HTTP-only readiness.
Upgrade the terminal/probe image together with Operator, admission and runtime
Chart. The updated Pi image implements the loopback contract.

The application Service retains its declared port but targets Envoy on 15443.
The optional separate terminal retains port 7681 and targets its own Envoy on
15445; its process listens only on loopback. Each helper receives only the
approved TLS/config mounts and Sandbox helper resources. The terminal does not
receive HOME, Entra/Connections proofs or model credentials. Its existing
DNS/Kubernetes API egress and CONNECT authorization remain separate.
Headless images have no application HTTP endpoint and cannot request this
opt-in; no HTTP port is synthesized. Custom desktop remains unsupported.

For private Portal Chat, approve one additional client per Portal namespace.
Add `chatClients` to the Custom binding; Web UI-only bindings leave it empty:

```json
{
  "chatClients": [{
    "secret": {
      "namespace": "portal-system",
      "name": "custom-chat-client",
      "uid": "example-chat-secret-uid",
      "resourceVersion": "100"
    },
    "uri": "spiffe://example.com/portal/example-agent-uid",
    "podLabels": {"app": "cetirun-portal"}
  }]
}
```

This is a synthetic fragment, not a credential or deployment command. The
namespace/labels must exactly match the Operator's configured Chat callers.
The URI must end with the actual immutable Agent UID. The Secret contains a
distinct client certificate/key and a CA trusting the Agent's exact serving
identity; the Agent server Secret's client trust must cover both approved
client identities. Never copy the Gateway client key into the Portal.

The Portal must use its existing Istio sidecar and configured SDS credential
authorization. A per-Agent, workload-selected DestinationRule in the Portal
namespace originates mTLS for its existing private Chat route. It is exported
only locally; the Portal process neither reads the key nor changes its HTTP
protocol. Missing mesh/credentials cannot fall back to reachable plaintext.
The compiler restricts Cilium to the corresponding TLS listener.

These additional Secret sources use the existing namespace-scoped source-read
grants and journal. The existing protected execution expectation carries a
typed Custom TLS plan so that no unrelated cross-namespace control resource
can be installed or adopted. Upgrade all strict expectation readers before
enabling it. The stop/change/resume rules below also apply to Portal
credentials, selectors and approvals. No new CA issuer or hot-rotation control
plane is introduced.

## Externally provisioned certificates

CetiRun does **not** create a CA, install an issuer, issue certificates, or
renew them. An administrator supplies two distinct, per-Agent Secrets:

| Secret | Namespace | Required keys |
| --- | --- | --- |
| Agent server | The Agent namespace | `tls.crt`, `tls.key`, `ca.crt` trusting Gateway clients |
| Gateway client | The shared Gateway namespace | `tls.crt`, `tls.key`, `ca.crt` trusting Agent servers |

The server Secret must also be distinct from application, model and image-pull
credentials. It does not authorize `extraEnv`, `envFrom`, initializer access or
additional mount aliases.

Use the Secret format supported by Istio Gateway SDS for the client credential.
The Gateway's browser-facing TLS Secret is a different credential and remains
unchanged. No private CA key belongs in either Secret.

The server certificate must have server-auth usage and the exact DNS SAN:

```text
agent-<first 32 lowercase hex characters of SHA256(Agent UID)>.<Agent Service FQDN>
```

For example, the Service FQDN is
`openclaw-example-cetirun.cetirun-example.svc.cluster.local` when that is the
Agent's actual Service name. Use the actual controller-resolved Service name,
not an assumed harness prefix. The client certificate must have client-auth
usage and the exact configured SPIFFE URI SAN. Its path must end in
`/<Agent UID>` so a recreated Agent cannot inherit an old name-scoped identity.
No wildcard SAN authorization is supported. Both sides verify the configured
CA; the Gateway also verifies the exact server SAN and SNI.

The following is a **synthetic registration fragment**, not a deployable
credential configuration. Replace every identity/version with the approved
live object's values in the authorized environment:

```json
{
  "image": "docker.io/envoyproxy/envoy:v1.36.3@sha256:d68fc27a15a17ae3b13df8c8631523a2fe1192b39ec867dffe870b666c55c2dd",
  "bindings": [{
    "agentUID": "example-agent-uid",
    "namespace": "cetirun-example",
    "harness": "openclaw",
    "port": 18789,
    "terminal": true,
    "desktop": true,
    "clientURI": "spiffe://example.com/gateway/example-agent-uid",
    "serverSecret": {
      "namespace": "cetirun-example",
      "name": "agent-ingress-tls",
      "uid": "example-server-secret-uid",
      "resourceVersion": "100"
    },
    "clientSecret": {
      "namespace": "managed-ingress",
      "name": "gateway-example-client",
      "uid": "example-client-secret-uid",
      "resourceVersion": "101"
    },
    "gateway": {
      "namespace": "managed-ingress",
      "name": "shared-ingress",
      "uid": "example-gateway-uid",
      "resourceVersion": "102"
    }
  }]
}
```

This fragment is the value of `platforms.kata.ingressMTLS`, or
`platforms.container.ingressMTLS` for ordinary containers. Hermes uses
`"harness": "hermes"` and its approved primary port (normally 8642).
The image above is a public, digest-pinned Envoy 1.36.3 multi-platform index;
pinning is not target runtime/image qualification.

The existing bootstrap can create the Agent namespace and retained HOME while
certificate approval is pending. For a new Agent awaiting its first approval,
provide its normal explicit image, port and chart-version pins so prerequisite
bootstrap does not depend on running the runtime first. No TLS workload is authorized until both
Secrets and the Gateway match their registered UID/resourceVersion pins.
The operator reads Secret metadata for these approvals, not certificate or key
contents. The key is mounted only by Envoy. The application receives only the
public certificate and trust bundle for readiness checks.

Hermes reserves API listener authority during HOME initialization and before
every supervised Gateway process start. In mTLS mode only, it removes the
competing `API_SERVER_ENABLED`, `API_SERVER_HOST` and `API_SERVER_PORT` HOME
environment assignments, plus the API platform's enabled/host/port overrides
and both flat and `extra` host/port spellings. This covers `platforms.api_server`,
`gateway.platforms.api_server`, `gateway.api_server`, and the existing legacy
`gateway.json` base layer; an invalid legacy file fails closed rather than
re-enabling a fallback listener. The validated Pod environment then supplies
the listener settings. Unrelated HOME configuration and environment data are
retained. No loopback defaults are left in HOME for a later non-mTLS
start. Dashboard and ttyd use explicit, protected command-line bind settings;
their retained preferences cannot override those arguments.

The existing execution expectation, source-read grants, coordinated source
reservation, and final-Pod admission cover the helper, Secret references,
ConfigMap, Cilium policy, Service targets, and per-Service Istio DestinationRule.
The additional Gateway Secret read grant is name-scoped and recorded in the
existing grant journal. No second controller, execution receipt, or journal is
introduced.

## Rotation, loss and cleanup

Use the existing **stop/change/resume** workflow for leaf rotation, CA changes,
Gateway changes, or revocation:

1. Stop/suspend the affected Agent and observe acknowledged shutdown, including
   the existing HOME/writer checks and release of its source reservation.
2. Provision the replacement certificate material externally.
3. Update the approved Secret UID/resourceVersion pins and Gateway pin through
   the administrator's existing configuration process.
4. Resume and verify the replacement Pod, readiness, route, and actual TLS
   handshake before treating access as restored.

This implementation does not promise restart-free rotation. Unapproved Secret
changes, replacement, deletion, or unavailable pinned Gateway inputs stop the
workload and withdraw its route; they do not get adopted by name. Secret metadata
events enqueue affected Agents, and active mTLS Agents also reconcile every
five seconds. These are control-plane reconciliation bounds, not instantaneous
dataplane revocation guarantees.

Read-only Helm configuration/history failures do not mark workload submission.
If TLS prerequisites subsequently become unavailable before the first apply,
the existing pending checkpoint can be cancelled safely, permitting normal
retry, suspension and deletion. Restoring reads of the same Secret preserves
its approval; recreating a Secret requires new UID/version approval. A genuinely
submitted or uncertain apply still requires shutdown evidence, even when no
StatefulSet is currently visible.

Each final apply first claims the expectation's resource version with an
`apply-attempt` identifier. First attempts remain pending through every authority
read, and transition to submitted only by a conditional write immediately before
manifest delivery. A failed read or lost claim acknowledgement is still
recoverable. Competing claims invalidate stale writers; lost submission
acknowledgements and previously submitted attempts are never rolled back.

The platform ConfigMap remains a shared, versioned source. Updating its
certificate approvals can invalidate other Agents using that same authority;
coordinate all affected source reservations. Per-Agent certificates and network
selectors provide identity isolation, not independent rollout authority or
permission to ignore shared-source drift.

Missing/invalid mounted material prevents startup or readiness. The public
certificate probe checks validity and the active Envoy certificate; expiry
cannot produce a ready endpoint indefinitely. TLS session resumption is
disabled, and authenticated downstream/upstream connections are capped at
five minutes. Shutdown is still required to revoke existing connections
immediately; removing a CA alone is not an instantaneous disconnect.

Cleanup uses existing ownership and journal rules. HOME remains governed by its
existing retention policy. The externally supplied Gateway client Secret is not
deleted or modified by cleanup; its administrator owns retirement. Agent
namespace deletion removes its namespaced resources. Never distribute the
Gateway client key to the Agent application.

## Validation boundary

Offline tests exercise the compiler, common rendering, source pins, final-Pod
validation, rotation/holds and cleanup for both harnesses and both HOME modes.
Real localhost Envoy tests generate ephemeral certificates and cover valid peers,
missing/wrong/expired client certificates, wrong server CA/SAN, plaintext,
certificate loss/readiness, replacement and old-authority retirement.
Optional network-disabled, read-only cached image tests exercise OpenClaw's
native API/Control UI and ttyd, and Hermes' API/dashboard and ttyd behind the
generated endpoint. The Hermes test covers fresh HOME, stale listener overrides
in retained HOME, and reintroduced overrides before a supervised process restart.
The cached desktop test exercises actual websockify/noVNC HTTP and WebSocket
traffic through the TLS endpoint, including an RFB banner and backend-loss
readiness. Its RFB and CDP backends are synthetic; it does not run or qualify
the graphical X/Chrome stack.

These are not a live Istio/Kata/Cilium deployment or general image qualification.
Before production opt-in, separately verify SDS selection for the Gateway,
all routed native API/UI/WebSocket/terminal paths, OIDC/owner controls,
direct Pod-IP and Service bypass attempts from allowed and disallowed peers,
both registered address families, source loss, certificate expiry, coordinated
rotation, active-connection termination, readiness, and cleanup on the exact
target versions. Do not equate synthetic controller observations with dataplane
or physical-fencing evidence.
