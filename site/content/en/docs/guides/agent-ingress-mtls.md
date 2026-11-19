---
title: "Managed Agent ingress mTLS"
description: "Encrypt the hop from the shared ingress gateway to an Agent Pod with per-Agent certificates."
weight: 45
---

Ingress mTLS encrypts and authenticates the connection from the shared Istio
ingress gateway to the Pod of an Agent. Without it, the ingress gateway sends
the traffic to the Agent over plaintext HTTP inside the cluster. Use ingress mTLS
when your rules require encryption on each network hop.

With ingress mTLS, an Envoy endpoint **inside the Agent Pod** terminates TLS.
The ingress gateway authenticates that endpoint, and it presents the approved
client certificate of the Agent. These applications of the Agent then receive
traffic only on loopback:

- the OpenClaw API and Control UI
- the Hermes API and Webapp
- the optional ttyd terminal
- the registered noVNC desktop

Ingress mTLS has these costs:

- Each Agent needs two certificates that you issue. CetiRun does not issue or
  renew them.
- A certificate rotation stops the Agent and starts it again.
- Only an admission administrator can enable it, with `kubectl`. The Portal
  shows the setting, but you cannot change it there.

Ingress mTLS is not browser client-certificate authentication. Browser HTTPS,
OIDC, ownership authorization, and the authentication of the application stay
in place. Ingress mTLS does not examine outbound HTTPS, and it does not block
traffic by FQDN. Cilium is still the network boundary outside the workload.
The TLS helper in the Pod does not replace that boundary, and it is not
evidence of runtime enforcement.

Shared-only, Copilot-only, and mixed model selections keep the same AI Gateway
routing. Only the Agent Service targets the TLS ports. The private Copilot
companion Service keeps its own selector and port.

## Prerequisites and supported surfaces

- Use a Sandbox Agent with Sandbox execution enabled, in a Kata or Container
  Sandbox. Its agent type must be OpenClaw, Hermes, or an approved Custom
  Agent with an HTTP interface that follows the contract below. Sandbox Agents
  that an earlier version of Sandbox execution started do not support ingress
  mTLS.
- Before you enable it, upgrade the operator, the admission process, and the
  agent chart together. An older chart without the protected `ingressMTLS`
  schema field fails closed. It does not render plaintext.
- Register `Capabilities.mutualTLS: true` and a digest-pinned endpoint image
  under `platforms.kata.ingressMTLS` or `platforms.container.ingressMTLS` in
  the platform registration. Use the key that matches the isolation type of
  the Sandbox. A capability flag alone is not sufficient.
- Use the pinned `Gateway` resource of the ingress gateway, which is the
  operator's `istioGateway`, and its protected Pod selector in
  `topology.ingress`. Continue to list the
  approved **application** ports there. The compiler substitutes the TLS
  listener ports and denies the plaintext application ports, also against
  additive network allows.
- To keep the enabled desktop default, register `"desktop": true` and the
  application port 6080. The registration must match the rendered desktop
  selection. noVNC is never left as a plaintext route.
- Disable Tailscale, arbitrary sidecars and initializers, and extra mounted
  volumes. Unsupported surfaces cause a render failure. They are never left
  outside TLS. The logger and the ordinary initialization of the platform are
  still supported.
- Keep the ingress mode `authenticated`. `disabled` together with
  `requireMTLS: true` is not valid. `requireMTLS: false` keeps the ordinary
  render and routing path.

The endpoint listens on these TCP ports:

| Port | Use |
| --- | --- |
| 15443 | The primary API and UI |
| 15444 | The Hermes Webapp |
| 15445 | The enabled terminal |
| 15446 | The registered noVNC desktop |
| 15440 | The admin interface, on loopback only. It is never a Service or a Cilium ingress grant |

The primary application port is the approved Agent port. The Webapp stays on
loopback port 9120, and the terminal stays on loopback port 7681. The managed
desktop uses loopback port 6080 for noVNC, 5900 for VNC, and 18800 for Chrome
CDP. Only noVNC gets an external TLS route.

The local readiness probe of the desktop examines the HTTP assets, the RFB
listener, and CDP without the client credentials of the ingress gateway. A custom terminal port
or an unexpected Service port is refused.

The optional `desktop` registration field is empty or false for earlier
approvals. It never grants desktop ingress implicitly. Before you add it,
upgrade the strict readers of the registration. mTLS configurations with an
explicitly disabled desktop stay valid. The ordinary desktop behavior without
mTLS does not change.

The helper uses the helper requests and limits of the Sandbox, an
unprivileged identity, dropped capabilities, and a read-only root filesystem.
Its resources count with the other resources of the Pod. It receives neither
HOME nor application or model credentials. Istio injection stays disabled.

## Enable ingress mTLS for an Agent

Do these steps as an admission administrator. In the commands, `<agent>` is
the name of the Agent. The `Gateway` resource of the ingress gateway is
`aks-istio-ingress/cetirun-gateway`, the default `istioGateway` of the
operator.

1. In `platform.json`, set `mutualTLS: true` in the registration of the
   isolation type, `kata` or `container`. Add `ingressMTLS` to the platform of
   the same isolation type, with the endpoint image and no bindings yet:

   ```json
   "ingressMTLS": {
     "image": "docker.io/envoyproxy/envoy:v1.36.3@sha256:d68fc27a15a17ae3b13df8c8631523a2fe1192b39ec867dffe870b666c55c2dd",
     "bindings": []
   }
   ```

   Then replace the ConfigMap, as
   [The platform registration](../../operations/policy-teams/#the-platform-registration)
   shows.
2. Select an Agent Policy for the mTLS Agents, and set `requireMTLS` on it:

   ```bash
   kubectl patch agentpolicy <policy> --type merge \
     -p '{"spec":{"network":{"ingress":{"requireMTLS":true}}}}'
   ```

   > **Warning:** `requireMTLS` applies to every Agent of the Agent Policy. An
   > Agent of the Agent Policy without a certificate approval does not run. Use
   > an Agent Policy that has no other Agents.
3. Create the Agent in a Team of this Agent Policy. Give it explicit
   `spec.agent.image`, `spec.agent.port`, and `spec.helm.version` values. The
   Agent waits for its certificate approval. During this wait, the bootstrap
   can create the namespace and the HOME of the Agent.
4. Read the UID of the Agent, and calculate the DNS name of its server
   certificate. The Service of an Agent is `cetirun-<agent>` for
   every agent type, at most 63 characters. Its namespace is
   `cetirun-<agent>`:

   ```bash
   AGENT=<agent>
   AGENT_UID=$(kubectl get agent "$AGENT" -o jsonpath='{.metadata.uid}')
   SERVICE=$(printf 'cetirun-%s' "$AGENT" | cut -c1-63 | sed 's/-*$//')
   HASH=$(printf '%s' "$AGENT_UID" | sha256sum | cut -c1-32)
   echo "agent-${HASH}.${SERVICE}.cetirun-${AGENT}.svc.cluster.local"
   ```

5. Issue the server certificate and the client certificate, as
   [Externally provisioned certificates](#externally-provisioned-certificates)
   shows. Create the server Secret in the namespace of the Agent. Create the
   client Secret in the namespace of the `Gateway` resource.
6. Make both Secrets immutable. Then read the UID of both Secrets and of the
   `Gateway` resource:

   ```bash
   kubectl patch secret <server-secret> -n "cetirun-$AGENT" --type merge -p '{"immutable":true}'
   kubectl patch secret <client-secret> -n aks-istio-ingress --type merge -p '{"immutable":true}'
   kubectl get secret <server-secret> -n "cetirun-$AGENT" -o jsonpath='{.metadata.uid}{"\n"}'
   kubectl get secret <client-secret> -n aks-istio-ingress -o jsonpath='{.metadata.uid}{"\n"}'
   kubectl get gateways.networking.istio.io cetirun-gateway -n aks-istio-ingress -o jsonpath='{.metadata.uid}{"\n"}'
   ```

   These commands give UID-only pins. For the other pin form, refer to
   [Pin forms](#pin-forms).

7. Add a binding for the Agent to `ingressMTLS.bindings` in `platform.json`.
   The binding has the UID, the namespace, and the port of the Agent. It also
   has the agent type (`harness`), the client URI, and the pins of step 6.
   Refer to the fragment in
   [Externally provisioned certificates](#externally-provisioned-certificates).
   Then replace the ConfigMap again.
8. Make sure that the Agent becomes ready. Open its UI through the Portal.
   Before you treat the setup as complete, examine the TLS handshake through
   the ingress gateway.

A registration has at most 128 bindings. These steps are not verified on a
cluster.

## Externally provisioned certificates

CetiRun does **not** create a CA, install an issuer, issue certificates, or
renew them. A platform administrator supplies two distinct Secrets for each
Agent:

| Secret | Namespace | Necessary keys |
| --- | --- | --- |
| Agent server | The namespace of the Agent | `tls.crt`, `tls.key`, and a `ca.crt` that trusts the ingress gateway clients |
| Ingress gateway client | The namespace of the `Gateway` resource | `tls.crt`, `tls.key`, and a `ca.crt` that trusts the Agent servers |

The server Secret must also be distinct from the application, model, and
image-pull credentials. It does not authorize `extraEnv`, `envFrom`, access by
initializers, or more mount aliases.

For the client credential, use the Secret format that Istio supports for the
SDS of the ingress gateway. The browser TLS Secret of the ingress gateway is a
different credential, and it does not change. No private CA key belongs in either
Secret.

The server certificate must have server-auth usage and this exact DNS SAN:

```text
agent-<first 32 lowercase hex characters of SHA256(Agent UID)>.<Agent Service FQDN>
```

For example, for the Agent `example`, the Service FQDN is
`cetirun-example.cetirun-example.svc.cluster.local`.

The client certificate must have client-auth usage and the exact configured
SPIFFE URI SAN. Its path must end in `/<Agent UID>`. As a result, a recreated
Agent cannot inherit an old identity that was bound to its name. Wildcard SANs
are not supported. Both sides verify the configured CA. The ingress gateway
also verifies the exact server SAN and SNI.

The example that follows is a **synthetic registration fragment**, not a
deployable credential configuration. In your environment, replace each
identity with the values of the approved live object:

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
      "uid": "example-server-secret-uid"
    },
    "clientSecret": {
      "namespace": "managed-ingress",
      "name": "gateway-example-client",
      "uid": "example-client-secret-uid"
    },
    "gateway": {
      "namespace": "managed-ingress",
      "name": "shared-ingress",
      "uid": "example-gateway-uid"
    }
  }]
}
```

This fragment is the value of `platforms.kata.ingressMTLS`, or
`platforms.container.ingressMTLS` for ordinary containers. Hermes uses
`"harness": "hermes"` and its approved primary port, normally 8642. The image
above is a public, digest-pinned Envoy 1.36.3 multi-platform index. A digest
pin does not qualify the image for the target runtime.

No TLS workload is authorized until both Secrets and the `Gateway` resource
match their registered pins. The operator does not read the certificate or key
contents for these approvals. Only Envoy mounts the key. The application
receives only the public certificate and the trust bundle for its readiness
checks.

Hermes validates the authority of its API and Webapp listeners before each
supervised start of the Hermes gateway process, with and without mTLS. Platform values supply the
API binding. The read-only `HERMES_MANAGED_DIR` overlay supplies the
`enabled`, `host`, `port`, and full HTTPS `public_url` of the Webapp. In mTLS
mode, both listeners bind only to loopback. The launcher uses the upstream
configuration loaders to refuse competing profile or configuration sources.

The launcher removes platform-reserved assignments in the HOME environment and
API listener aliases, and it keeps unrelated configuration. These problems
stop the startup: an overlay that is not valid, a missing independent signing
key, a wrong runtime identity, or a missing Webapp bundle. The Hermes gateway
process does not select a backup port or an older UI. The same persistent
process serves the Webapp and the API. There is no separate Dashboard process.

The usual Sandbox controls also cover the mTLS resources. These controls are
the execution expectation, the source-read grants, the coordinated source
reservation, and the final-Pod admission. The mTLS resources are the helper,
the Secret references, the ConfigMap, the Cilium policy, the Service targets,
and the Istio DestinationRule of each Service. The read grant for the client
Secret of the ingress gateway is limited to its name, and the grant journal
records it.

### Pin forms

Each `serverSecret`, `clientSecret`, `gateway`, and Chat client `secret`
reference has one of two forms:

| Form | What it pins | What stops the Agent |
| --- | --- | --- |
| UID only (no `resourceVersion`), recommended | The object UID. A pinned Secret must be immutable. | A replacement or a deletion of the object, a Secret that is not immutable, or a change to the servers or the selector of the `Gateway` resource |
| UID and `resourceVersion` | The exact object version | Every change to the object, including a label or an annotation change |

With the UID-only form:

- Label and annotation changes do not stop the Agent. This includes the
  `helm.sh/chart` label that a `cetirun-istio` chart upgrade writes on the
  `Gateway` resource.
- The operator reads each pinned Secret only to check its `immutable` field.
  It does not log, hash, or copy the Secret data. The operator ServiceAccount
  already has `get` permission on Secrets.
- The execution pins the configuration of the `Gateway` resource with a
  content digest, and the operator checks its HTTPS server and its selector.
  A change to them stops the Agent.

To create a Secret that is immutable from the start:

```bash
kubectl create secret generic agent-ingress-tls -n cetirun-example \
  --from-file=tls.crt=server.crt --from-file=tls.key=server.key \
  --from-file=ca.crt=client-ca.crt --dry-run=client -o yaml \
  | kubectl patch --local -f - --type merge -p '{"immutable":true}' -o yaml \
  | kubectl apply -f -
```

Registrations that already have a `resourceVersion` keep the strict behavior.
To change to the UID-only form, remove the `resourceVersion` fields and make
the Secrets immutable. This changes the execution revision, so the Agent
restarts one time.

If an approval is stale and the Agent is not suspended, it stays `Pending`.
Its `Ready` and `SandboxReady` conditions have the reason `IngressMTLSApprovalStale`. The
condition message names the kind, the namespace, and the name of the stale
object.

A missing first certificate binding reports `IngressMTLSApprovalPending`.
An authorized caller can request suspension through the Agent API while this
binding is missing or stale, including after namespace and HOME bootstrap but
before the first workload exists.
The Operator acknowledges `Suspended` and the current generation only after
verifying shutdown and access revocation. When no execution expectation exists,
it also requires the complete owned bootstrap inventory, no preparation or
grant history, no execution reservation, and no workloads or Pods. Missing or
conflicting evidence keeps suspension `Pending`; workload absence alone is not
proof that a previous submission stopped.

After confirmed suspension, `Ready` and `PodReady` report `Suspended`, while
`SandboxReady` and `IngressReady` retain the missing or stale approval diagnosis.
Repeated checks do not rewrite unchanged status. Resume retains the same HOME
but does not bypass enrollment: execution stays held until the administrator
registers a current approval.

The Portal's existing HOME check is unchanged. This status fix does not add
Portal suspension for a bootstrap-completed Agent that has no workload yet.

An authorized Agent DELETE also removes this first-approval hold. The Operator
uses the recorded bootstrap ownership even when only the bootstrap and hold
finalizers exist. It waits for namespace deletion before releasing those
records and finalizers; certificate enrollment is not required for cleanup.
An already-terminating Agent in this state resumes cleanup after the Operator
is upgraded. Do not add or remove finalizers manually. Ownership conflicts,
remaining Pods and uncertain execution history still block cleanup.

## OpenClaw Control UI Portal sign-in

With the registered mTLS endpoint above, an OpenClaw Sandbox Agent can use
Portal sign-in:

```yaml
spec:
  helm:
    values:
      openclaw:
        portalSignIn:
          enabled: true
```

The owner opens the Control UI from the Portal without pasting the OpenClaw
gateway token or running `openclaw devices approve`. Before sign-in, the
ingress gateway still removes the identity headers that a client sends. It
also checks ownership before it forwards `x-auth-request-email`. The Envoy
endpoint in the Pod verifies the approved client certificate of the ingress
gateway for the Agent. OpenClaw listens on loopback and trusts exactly
`127.0.0.1`, not the registered Pod network.

The operator validates the rendered proxy list, the authentication settings,
and the OpenClaw gateway credential environment. The doctor initializer
receives no gateway credentials. The key of the OpenClaw gateway Secret does
not change. The Terminal CLI receives it as `OPENCLAW_GATEWAY_PASSWORD`
instead of `OPENCLAW_GATEWAY_TOKEN`, because OpenClaw does not accept a token
in trusted-proxy mode.

At each start, HOME initialization replaces the saved gateway authentication.
With Portal sign-in, rendering and HOME initialization remove both
`gateway.auth.token` and `gateway.remote.token`. OpenClaw can use the remote
token even when `gateway.mode` is `local`. Other remote settings stay. If you
disable Portal sign-in, the next start restores the token environment and
removes the managed trusted-proxy configuration.

Portal sign-in is false by default. Upgrade the operator and the agent chart
together before you enable it. The operator refuses an older chart that cannot
render the requested mTLS sign-in contract. The chart does not stay in token
mode without a message.

Sandbox ingress without mTLS does not enable Portal sign-in, with Pod IPs or
with a Pod selector and trusted CIDRs. Sandbox Agents that an earlier version
of Sandbox execution started also keep token authentication and device
approval. Native `gateway.auth` preferences cannot bypass this mTLS gate.
Tailscale stays unsupported for an mTLS Sandbox Agent. Standard Agents keep
their Istio-sidecar opt-in and token behavior with Tailscale.

## Custom HTTP and terminal

A Custom registration uses `"harness": "custom"`, the exact declared
application port, `"desktop": false`, and the actual terminal selection. The
image must honor `CETIRUN_BIND_HOST=127.0.0.1`. CetiRun does not replace its
ENTRYPOINT or CMD. The static probe of the platform examines the unchanged
`/healthz` contract. It also examines the validity of the current public
certificate and the active Envoy leaf certificate.

TLS probes pass an explicit `--mtls-server-name`. Older probe binaries refuse
that option. They do not report HTTP-only readiness. Upgrade the terminal and
probe image together with the operator, the admission process, and the agent
chart. The updated Pi image implements the loopback contract.

The application Service keeps its declared port, but it targets Envoy on port
15443. The optional separate terminal keeps port 7681 and targets its own
Envoy on port 15445. Its process listens only on loopback. Each helper
receives only the approved TLS and configuration mounts and the helper
resources of the Sandbox. The terminal does not receive HOME, Entra or
Connections proofs, or model credentials. Its DNS and Kubernetes API egress
and its CONNECT authorization stay separate.

Headless images have no application HTTP endpoint, and they cannot request
ingress mTLS. The platform does not create an HTTP port for them. Custom
desktop is not supported.

For private Portal Chat, approve one more client for each Portal namespace.
Add `chatClients` to the Custom binding. Bindings for only a Web UI leave it
empty:

```json
{
  "chatClients": [{
    "secret": {
      "namespace": "portal-system",
      "name": "custom-chat-client",
      "uid": "example-chat-secret-uid"
    },
    "uri": "spiffe://example.com/portal/example-agent-uid",
    "podLabels": {"app": "cetirun-portal"}
  }]
}
```

This is a synthetic fragment, not a credential or deployment command. The
namespace and the labels must exactly match the Chat callers that the operator
is configured with. The URI must end with the actual immutable Agent UID. The
Secret contains a distinct client certificate and key, and a CA that trusts
the exact server identity of the Agent. The client trust of the Agent server
Secret must cover both approved client identities. Never copy the client key
of the ingress gateway into the Portal.

The Portal must use its Istio sidecar and its configured SDS credential
authorization. Istio 1.30 examines if the Portal ServiceAccount has the
namespace-wide `list` permission on Secrets. Permission to `get` one Secret is
not sufficient. Give that permission explicitly, in a Portal credential
namespace that is correctly isolated. The CetiRun chart does not grant it.
The [standalone Portal profile]({{< repo "examples/native-sandbox/README.md" >}}#isolated-portal-chat-mtls)
provides namespace-local SDS RBAC and the matching Portal values.

A DestinationRule for each Agent in the Portal namespace selects the Portal
workload and originates mTLS for the private Chat route. It is exported only
locally. The Portal process does not read the key, and it does not change its
HTTP protocol. A mesh or credentials that are not available cannot cause a
fallback to reachable plaintext. The compiler limits Cilium to the TLS
listener for that route.

Chat readiness examines that TLS listener, not the loopback port of the
application. The original HTTP Service port and the private route do not
change. The Portal compares its current DestinationRule, which it owns, with
the approved execution expectation. For this comparison, it uses the `get`
permission of its namespace. It does not read certificate Secrets. If an
approval, a caller selector, a TLS control, or a Service target port is
absent or changed, Chat stays unavailable.

The other Secret sources use the namespace-scoped source-read grants and the
grant journal. The protected execution expectation carries a typed Custom TLS
plan. With this plan, no unrelated control resource in another namespace can
be installed or adopted. Before you enable it, upgrade all strict readers of
the expectation. The stop, change, and resume rules below also apply to Portal
credentials, selectors, and approvals.

## Rotation, loss and cleanup

For a leaf rotation, a CA change, a change to the `Gateway` resource, or a
revocation, use the **stop/change/resume** workflow. A CA or `Gateway` change
affects every Agent that uses it. Do each step for all affected Agents
together:

1. Suspend the affected Agents. Then make sure that each shutdown is
   acknowledged. The acknowledged shutdown includes the HOME and writer checks
   and the release of the source reservation of the Agent.
2. Provision the replacement certificates outside CetiRun. With UID-only pins,
   create new immutable Secrets. You cannot change the data of an immutable
   Secret, so a rotation always gives a new UID.
3. In one edit of the platform registration, update the pins of the Secrets
   and of the `Gateway` resource. Then replace the ConfigMap one time, as
   [The platform registration](../../operations/policy-teams/#the-platform-registration)
   shows.
4. Resume the Agents. For each Agent, examine the replacement Pod, its
   readiness, its route, and the actual TLS handshake. Then treat access as
   restored.

When a Custom Agent is suspended, its application and its separate terminal
stop. The approved terminal selection does not change. The suspension does
not authorize terminal access. The shutdown must still be acknowledged.

> **Warning:** A replacement or a deletion of a pinned Secret or of the pinned
> `Gateway` resource stops the mTLS Agents that use it. It also removes their
> route. A change to the servers or the selector of the `Gateway` resource
> does the same. With UID and `resourceVersion` pins, every change does the
> same: for example, a label change. Then update the pins of every affected
> Agent, as the steps above show. Refer to [Pin forms](#pin-forms).

CetiRun does not adopt a replaced Secret by its name. A recreated Secret must
get a new approval for its new UID. Secret metadata events put the affected
Agents in the reconcile queue. Active mTLS Agents also
reconcile every five seconds. These are the bounds of control-plane
reconciliation. They do not guarantee instantaneous revocation in the data
plane.

The platform ConfigMap is a shared, versioned source. An update to its
certificate approvals can affect other Agents that use the same registration.
Coordinate all affected source reservations. Per-Agent certificates and
network selectors isolate the identity of each Agent. They do not give each
Agent its own rollout authority, or permission to ignore drift of the shared
source.

Mounted material that is absent or not valid prevents startup or readiness.
The public certificate probe examines the validity and the active Envoy
certificate. An expired certificate cannot keep an endpoint ready.

TLS session resumption is disabled. Authenticated downstream and upstream
connections last at most five minutes (300 seconds). To revoke the open
connections immediately, a shutdown is still necessary. The removal of a CA
alone does not close them.

The connection limit also applies to the Terminal. The terminal server, ttyd,
starts one shell for each connection. After five minutes, the Terminal
connects to a new shell. The shell history and the commands that run in the
old shell are lost. The Control UI and the Hermes Webapp connect again without
a new sign-in. To keep a Terminal session, run this command after you open the
Terminal and again after each new connection:

```bash
tmux new -A -s main
```

The tmux session and its commands continue in the Agent container.

Cleanup uses the usual ownership and journal rules. The retention policy of
HOME does not change. Cleanup does not delete or change the client Secret of
the ingress gateway that you supplied. The platform administrator retires it.
The deletion of the namespace of an Agent removes its namespaced resources.
Never give the client key of the ingress gateway to the Agent application.

## Troubleshooting

The Agent reports these messages in its status conditions:

| Message | Cause | Action |
| --- | --- | --- |
| `mTLS has no current per-Agent certificate approval` | The registration has no binding for the UID and the namespace of the Agent | Add the binding. Refer to [Enable ingress mTLS for an Agent](#enable-ingress-mtls-for-an-agent) |
| `mTLS requires current common-executor certificate registration` | The platform of the isolation type has no `ingressMTLS` registration, or Sandbox execution does not run the Agent | Register `ingressMTLS`, and enable Sandbox execution |
| `mTLS approval differs from this Agent's actual harness, port or shared Gateway` | The agent type (`harness`), the `port`, or the `gateway` of the binding does not match the Agent, or the Agent has no `spec.agent.port` | Correct the binding or the Agent |
| `mTLS endpoint credentials must be distinct from application and model Secrets` | The server Secret is also an application or model Secret of the Agent | Use a separate Secret |
| `mTLS approved Secret is unavailable` | The operator cannot read a pinned Secret | Make sure that the operator can read the Secret, and that its name and namespace in the binding are correct |
| `The mTLS approval for Secret <namespace>/<name> is stale (...)`, reason `IngressMTLSApprovalStale` | The pinned Secret was replaced or deleted. Or it has a UID-only pin and is not immutable. Or it has a `resourceVersion` pin and changed | Register the current UID of the Secret. With a UID-only pin, make the Secret immutable |
| `mTLS shared Gateway is unavailable`, or `The mTLS approval for Gateway <namespace>/<name> is stale (...)` | The operator cannot read the pinned `Gateway` resource, or the resource was replaced or deleted. Or it has a `resourceVersion` pin and changed | Update the pin of the `Gateway` resource in the binding |
| `mTLS must retain the shared browser HTTPS Gateway` | The `Gateway` resource has no HTTPS server on port 443 for the domain of the installation | Restore the HTTPS server of the `Gateway` resource |
| `mTLS Gateway selector differs from the protected Cilium ingress peers` | The namespace or the selector of the `Gateway` resource is different from the `topology.ingress` peers | Correct `topology.ingress` |

## Validation

Offline tests use synthetic certificates and Envoy on localhost. They are not
a test of a live Istio, Kata, or Cilium deployment, and they do not qualify an
image. Before you rely on ingress mTLS in production, validate these items on
the exact target versions:

- SDS selection for the ingress gateway
- All routed native API, UI, WebSocket, and terminal paths
- OIDC and owner controls
- Bypass attempts to the Pod IP and the Service, from allowed and disallowed
  peers
- Both registered address families
- Source loss
- Certificate expiry
- Coordinated rotation
- The end of active connections
- Readiness
- Cleanup

Do not treat synthetic controller observations as evidence of the data plane
or of physical fencing. For the test coverage and the apply protocol, refer to
the operator README's
[Managed ingress mTLS internals]({{< repo "app/cetirun-operator/README.md" >}}#managed-ingress-mtls-internals).
