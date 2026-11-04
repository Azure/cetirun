---
title: "Managed Agent ingress mTLS"
description: "Opt-in TLS for common-executor Kata and container managed or approved Custom HTTP workloads."
weight: 45
---

`AgentPolicy.spec.network.ingress.requireMTLS: true` enables an explicitly
registered Envoy endpoint **inside the Agent Pod**. The managed Istio Gateway
authenticates that endpoint and presents its approved client certificate.
OpenClaw API/Control UI, Hermes API/dashboard, the optional ttyd terminal and
registered noVNC desktop then receive traffic over loopback only. No plaintext
network hop follows an external Team gateway.

This is not browser client-certificate authentication. Shared browser HTTPS,
OIDC, ownership authorization, and native application authentication stay in
place. It does not inspect outbound HTTPS or block traffic by FQDN. Cilium
is still the workload-external network boundary. The in-Pod TLS helper is not a
replacement for that boundary or independent runtime-enforcement evidence.

Shared-only, Copilot-only and mixed model selections keep the same Gateway
routing. Only the Agent Service targets the managed TLS ports. The separately
owned, private Copilot companion Service keeps its own selector and port.

## Prerequisites and supported surfaces

- Use the common executor, a Kata or container Sandbox, and OpenClaw, Hermes,
  or an approved Custom HTTP harness that follows the contract below.
  Retained single-start lifetimes do not support this opt-in.
- Before you enable it, upgrade the Operator, the independent admission
  process, and the runtime chart. An older chart without the protected
  `ingressMTLS` schema field fails closed. It does not silently render
  plaintext.
- Register `Capabilities.mutualTLS: true` and a digest-pinned endpoint image
  under `platforms.kata.ingressMTLS` or `platforms.container.ingressMTLS`.
  Use the key that matches the selected runtime in the protected platform
  ConfigMap. A capability flag alone is insufficient.
- Use one explicitly pinned shared Gateway and its protected Pod selector that
  already exists in `topology.ingress`. Continue to list the approved
  **application** ports there. The compiler substitutes TLS listener ports and
  denies the plaintext application ports. This denial also applies against
  additive network allows.
- To keep the enabled desktop default, register `"desktop": true` and
  application port 6080. The registration must match the rendered desktop
  selection. noVNC is not silently left as a plaintext route.
- For this opt-in, disable Tailscale, arbitrary sidecars/initializers, and
  extra mounted volumes. Unsupported surfaces cause a render failure. They are
  not silently left outside TLS. Logger and ordinary platform-owned
  initialization are still supported.
- Keep ingress mode `authenticated`. `disabled` plus `requireMTLS: true` is
  invalid. `requireMTLS: false` keeps the ordinary render and routing path.

The endpoint listens on TCP 15443 (primary API/UI), 15444 (Hermes dashboard),
15445 (enabled terminal), and 15446 (registered noVNC desktop). Its admin
interface is loopback-only on 15440 and is never a Service or Cilium ingress
grant. The application primary port is the approved Agent port. The dashboard
stays on loopback 9119, and the terminal stays on loopback 7681. The
managed desktop fixes noVNC to loopback port 6080, VNC to loopback port 5900
and Chrome CDP to loopback port 18800. Only noVNC gets an external TLS route.

The local readiness probe of the desktop examines HTTP assets, the RFB listener
and CDP availability without Gateway client credentials. A custom terminal port
or an unexpected Service port is rejected.

The optional `desktop` registration field is omitted/false for older
approvals. It never grants desktop ingress implicitly. Before you add it,
upgrade strict registration readers. Explicit desktop-disabled mTLS
configurations that already exist stay valid. Ordinary non-mTLS desktop
behavior is unchanged.

The helper has the selected Sandbox's helper requests and limits, an
unprivileged identity, dropped capabilities, and a read-only root filesystem.
Its resources are counted with the other resources of the Pod. It receives
neither HOME nor application/model credentials. Istio injection stays
disabled.

## Custom HTTP and terminal

A Custom registration uses `"harness": "custom"`, the exact declared
application port, `"desktop": false`, and the actual terminal selection. The
image must honor `CETIRUN_BIND_HOST=127.0.0.1`. CetiRun does not replace its
ENTRYPOINT/CMD. The platform's static probe examines the unchanged `/healthz`
contract. It also examines current public certificate validity and the active
Envoy leaf.

TLS probes pass an explicit `--mtls-server-name`. Older probe binaries reject
that option. They do not silently report HTTP-only readiness. Upgrade the
terminal/probe image together with the Operator, admission and runtime Chart.
The updated Pi image implements the loopback contract.

The application Service keeps its declared port but targets Envoy on 15443.
The optional separate terminal keeps port 7681 and targets its own Envoy on
port 15445. Its process listens only on loopback. Each helper receives only the
approved TLS/config mounts and Sandbox helper resources. The terminal does not
receive HOME, Entra/Connections proofs or model credentials. Its DNS/Kubernetes
API egress and CONNECT authorization, which already exist, stay separate.

Headless images have no application HTTP endpoint and cannot request this
opt-in. No HTTP port is synthesized. Custom desktop is still not supported.

For private Portal Chat, approve one more client for each Portal namespace.
Add `chatClients` to the Custom binding. Web UI-only bindings leave it empty:

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
distinct client certificate/key and a CA that trusts the exact server identity
of the Agent. The client trust of the Agent server Secret must cover both
approved client identities. Never copy the Gateway client key into the Portal.

The Portal must use the Istio sidecar that it already has and its configured
SDS credential authorization. Istio 1.30 examines if the Portal ServiceAccount
has the namespace-wide `list` permission on Secrets. Permission to `get` an
individual Secret is not sufficient. Give that authority explicitly in a Portal
credential namespace that is correctly isolated. The CetiRun chart does not
grant it.

A per-Agent, workload-selected DestinationRule in the Portal namespace
originates mTLS for its private Chat route, which already exists. It is
exported only locally. The Portal process neither reads the key nor changes
its HTTP protocol. A mesh or credentials that are not available cannot cause a
fallback to reachable plaintext. The compiler restricts Cilium to the TLS
listener for that route.

Chat readiness examines that TLS listener, not the loopback port of the
application. The original HTTP Service port and the private route do not
change. The Portal compares its current DestinationRule, which it owns, with
the approved execution expectation. For this comparison, it uses the
namespace-local `get` permission. It does not read certificate Secrets. If an
approval, a caller selector, a TLS control, or a Service target port is absent
or changed, Chat stays unavailable.

These other Secret sources use the namespace-scoped source-read grants and
journal that already exist. The protected execution expectation, which already
exists, carries a typed Custom TLS plan. With this plan, no unrelated
cross-namespace control resource can be installed or adopted. Before you
enable it, upgrade all strict expectation readers. The stop/change/resume
rules below also apply to Portal credentials, selectors and approvals. No new
CA issuer or hot-rotation control plane is introduced.

## Externally provisioned certificates

CetiRun does **not** create a CA, install an issuer, issue certificates, or
renew them. An administrator supplies two distinct, per-Agent Secrets:

| Secret | Namespace | Required keys |
| --- | --- | --- |
| Agent server | The Agent namespace | `tls.crt`, `tls.key`, `ca.crt` that trusts Gateway clients |
| Gateway client | The shared Gateway namespace | `tls.crt`, `tls.key`, `ca.crt` that trusts Agent servers |

The server Secret must also be distinct from application, model and image-pull
credentials. It does not authorize `extraEnv`, `envFrom`, initializer access or
more mount aliases.

For the client credential, use the Secret format that Istio Gateway SDS
supports. The Gateway's TLS Secret for browsers is a different credential, and
it does not change. No private CA key belongs in either Secret.

The server certificate must have server-auth usage and the exact DNS SAN:

```text
agent-<first 32 lowercase hex characters of SHA256(Agent UID)>.<Agent Service FQDN>
```

For example, the Service FQDN is
`openclaw-example-cetirun.cetirun-example.svc.cluster.local` when that is the
Agent's actual Service name. Use the actual controller-resolved Service name,
not a guessed harness prefix.

The client certificate must have client-auth usage and the exact configured
SPIFFE URI SAN. Its path must end in `/<Agent UID>`. This makes sure that a
recreated Agent cannot inherit an old name-scoped identity. No wildcard SAN
authorization is supported. Both sides verify the configured CA. The Gateway
also verifies the exact server SAN and SNI.

The example that follows is a **synthetic registration fragment**, not a
deployable credential configuration. In the authorized environment, replace
every identity/version with the values of the approved live object:

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
The image above is a public, digest-pinned Envoy 1.36.3 multi-platform index.
A digest pin is not target runtime/image qualification.

During a pending certificate approval, the bootstrap that already exists can
create the Agent namespace and the HOME that is kept. For a new Agent that
waits for its first approval, supply its normal explicit image, port and
chart-version pins. This makes sure that the prerequisite bootstrap does not
depend on the runtime to run first.

No TLS workload is authorized until both Secrets and the Gateway match their
registered UID/resourceVersion pins. The operator reads Secret metadata for
these approvals, not certificate or key contents. The key is mounted only by
Envoy. The application receives only the public certificate and trust bundle
for readiness checks.

Hermes reserves API listener authority during HOME initialization and before
every start of a supervised Gateway process. In mTLS mode only, it removes
these items that compete for that authority:

- The `API_SERVER_ENABLED`, `API_SERVER_HOST` and `API_SERVER_PORT` HOME
  environment assignments
- The enabled/host/port overrides of the API platform
- Both flat and `extra` host/port spellings

This removal covers `platforms.api_server`, `gateway.platforms.api_server`,
`gateway.api_server`, and the legacy `gateway.json` base layer that already
exists. An invalid legacy file fails closed. It does not enable a fallback
listener again.

The validated Pod environment then supplies the listener settings. Unrelated
HOME configuration and environment data are kept. No loopback defaults are
left in HOME for a later non-mTLS start. Dashboard and ttyd use explicit,
protected command-line bind settings. Their preferences that are kept cannot
override those arguments.

The execution expectation, source-read grants, coordinated source reservation,
and final-Pod admission already exist. They cover the helper, Secret
references, ConfigMap, Cilium policy, Service targets, and per-Service Istio
DestinationRule. The added Gateway Secret read grant is name-scoped. It is
recorded in the grant journal that already exists. No second controller,
execution receipt, or journal is introduced.

## Rotation, loss and cleanup

For leaf rotation, CA changes, Gateway changes, or revocation, use the
**stop/change/resume** workflow that already exists:

1. Stop/suspend the affected Agent. Then make sure that the shutdown is
   acknowledged. The acknowledged shutdown includes the HOME/writer checks
   that already exist and the release of its source reservation.
2. Provision the replacement certificate material externally.
3. Update the approved Secret UID/resourceVersion pins and Gateway pin through
   the configuration process that the administrator already has.
4. Resume the Agent. Before you treat access as restored, examine the
   replacement Pod, readiness, route, and actual TLS handshake.

When a Custom Agent is suspended, its application and its separate terminal
stop. The approved terminal selection does not change. The suspension does not
authorize terminal access. The shutdown must still be acknowledged.

This implementation does not promise restart-free rotation. Unapproved Secret
changes, replacement, deletion, or unavailable pinned Gateway inputs stop the
workload and withdraw its route. They are not adopted by name. Secret metadata
events enqueue affected Agents. Active mTLS Agents also reconcile every five
seconds. These are control-plane reconciliation bounds, not instantaneous
dataplane revocation guarantees.

Read-only Helm configuration/history failures do not mark workload submission.
If TLS prerequisites later become unavailable before the first apply, the
pending checkpoint that already exists can be cancelled safely. This allows
normal retry, suspension and deletion. If reads of the same Secret are
restored, the Secret keeps its approval. A recreated Secret must get a new
UID/version approval. For a genuinely submitted or uncertain apply, shutdown
evidence is still necessary, even when no StatefulSet is now visible.

Each final apply first claims the expectation's resource version with an
`apply-attempt` identifier. First attempts stay pending through every
authority read. They change to submitted only by a conditional write
immediately before manifest delivery. A failed read or lost claim
acknowledgement is still recoverable. Claims that compete invalidate stale
writers. Lost submission acknowledgements and attempts that were submitted
before are never rolled back.

The platform ConfigMap is still a shared, versioned source. An update to its
certificate approvals can invalidate other Agents that use that same
authority. Coordinate all affected source reservations. Per-Agent certificates
and network selectors give identity isolation. They do not give independent
rollout authority or permission to ignore shared-source drift.

Mounted material that does not exist or is not valid prevents startup or
readiness. The public certificate probe examines validity and the active Envoy
certificate. Expiry cannot produce a ready endpoint indefinitely.

TLS session resumption is disabled. Authenticated downstream/upstream
connections are capped at five minutes. A shutdown is still necessary to
immediately revoke the connections that exist. The removal of a CA alone is
not an instantaneous disconnect.

Cleanup uses the ownership and journal rules that already exist. HOME stays
governed by the retention policy that it already has. Cleanup does not delete
or change the externally supplied Gateway client Secret. Its administrator
owns retirement. Agent namespace deletion removes its namespaced resources.
Never distribute the Gateway client key to the Agent application.

## Validation boundary

Offline tests exercise the compiler, the common render path, source pins,
final-Pod validation, rotation/holds and cleanup for both harnesses and both
HOME modes. Real localhost Envoy tests generate ephemeral certificates and
cover valid peers, absent/wrong/expired client certificates, wrong server
CA/SAN, plaintext, certificate loss/readiness, replacement and old-authority
retirement.

Optional network-disabled, read-only cached image tests exercise OpenClaw's
native API/Control UI and ttyd, and Hermes' API/dashboard and ttyd behind the
generated endpoint. The Hermes test covers fresh HOME, stale listener overrides
in a HOME that is kept, and reintroduced overrides before a supervised process
restart. The cached desktop test exercises actual websockify/noVNC HTTP and
WebSocket traffic through the TLS endpoint. This includes an RFB banner and
backend-loss readiness. Its RFB and CDP backends are synthetic. The test does
not run or qualify the graphical X/Chrome stack.

These are not a live Istio/Kata/Cilium deployment or general image
qualification. Before production opt-in, separately validate these items on
the exact target versions:

- SDS selection for the Gateway
- All routed native API/UI/WebSocket/terminal paths
- OIDC/owner controls
- Direct Pod-IP and Service bypass attempts from allowed and disallowed peers
- Both registered address families
- Source loss
- Certificate expiry
- Coordinated rotation
- Active-connection termination
- Readiness
- Cleanup

Do not treat synthetic controller observations as dataplane or
physical-fencing evidence.
