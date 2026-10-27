# Native Sandbox first run

This example exercises one OpenClaw Agent with a Kata Sandbox and a
shared AI Gateway model. New Agents use the explicitly enabled common executor,
not a production failover or complete AgentPolicy conformance test.

## Prepare one authorized target

Start with the [development checks](../../DEVELOPMENT.md). Installation,
image/chart publication and live requests require authorization for the actual
target; these examples do not provision a cluster or grant credentials.

Use matching CRDs, Operator/admission binaries, runtime chart and Gateway chart
from the same integration build. The platform must already have:

- The selected Kata runtime, compatible CSI/RWOP storage and baseline Cilium
  networking. A RuntimeClass object alone is not runtime qualification.
- An accurate, administrator-maintained Sandbox platform configuration.
  Do not invent qualification flags, object UIDs or topology to pass admission.
- Enforced Sandbox configuration and Pod admission with working TLS.
- A configured AI Gateway model, its real provider credentials in the approved
  secret mechanism, and `operatorIntegration: true`. Model IDs and access
  groups must match the chart-derived policy bindings.
- The existing native ingress/authentication deployment and an authorized
  owner who can sign in. An HTTPS redirect alone does not prove owner access.

For the bounded native profile, the platform registration explicitly selects
`capabilities.profile: native-single-start` (the JSON field is
`registrations.kata.Capabilities.profile`). It does not claim an independent
enforcement observer or physical failover fencing. The runtime, host resource
limits, baseline network controls, storage and selected ingress mode still need
their actual deployment checks; do not enable this profile to bypass them.

Native OpenClaw runs without a same-Pod Istio proxy. Its proxy trust is therefore
derived from the registered ingress peers, not the legacy `127.0.0.6` sidecar
address. Exact peer IPs remain the default; managed gateways can opt into
[rollout-stable ingress](#rollout-stable-managed-ingress) below. Do not use a
wildcard proxy range or a per-Agent Helm override.

The packaged native OpenClaw init containers use `/usr/bin/tini -g --` to
supervise the existing HOME, doctor and credential-retirement commands.
Use the matching OpenClaw image, which includes this init process; an older
image without it cannot run the updated initializer commands. Helper failures
keep their exit status, and resource limits, authentication cleanup and the
execution hold are unchanged. Standalone OpenClaw entrypoints are unchanged.
Existing prepared templates retain their exact recorded command expectations;
this does not replace or restart an existing native lifetime.

This minimal example selects no skills. With no skills catalog configured, the
native profile supplies an empty read-only skills directory and needs no shared
skills PVC or NFS publication. When any skill is selected, configure the existing
validated publication/catalog instead; requested skills are never silently
dropped. A configured catalog still uses its normal validation and storage path.

In the Operator values, explicitly select:

```yaml
sandboxSources:
  enabled: true
  nativeExecution: true
  bootstrapOwnedPrerequisites: true
  platformConfigMapName: sandbox-platform
  homeClaimName: home
  skillsClaimName: skills
aiGateway:
  enabled: true
sandboxAdmission:
  enabled: true
  enforce: true
  podsEnabled: true
```

The legacy `sandboxSources.helpers` flags do not control reconciled (`v2`)
native lifetimes. Those use the runtime chart's helper settings: OpenClaw
doctor runs on each Pod initialization by default, even if
`sandboxSources.helpers.doctor` is `false`.

This is an **overlay**, not complete installation configuration. Supply the
target's admission TLS/administrator/coordination settings, domain, registry,
Gateway configuration and source registries through your normal private
deployment configuration. Do not commit that configuration.

On AKS, the control plane can append its system-namespace exclusions to admission
webhooks. Deployment preflight remains strict by default. An administrator may
explicitly pass `--allow-aks-system-exclusions` to `deployment-preflight` to accept
only the two exact `NotIn` clauses for `control-plane: ["true"]` and
`kubernetes.azure.com/managedby: ["aks"]`, in addition to the original protected
namespace selector. Missing, changed, duplicated or additional clauses remain
blocked. This option does not alter webhooks or their failure policy; Agent
namespaces must not carry those system labels. Preflight still does not qualify
the runtime, test actual network enforcement or establish application readiness.

Copy [configuration.yaml](configuration.yaml) and [agent.yaml](agent.yaml) to a
private run directory. Replace `native-home` with the registered CSI storage
class, `example-model` with the exact authorized Gateway catalog ID, the image
and chart pins with the candidate artifacts, and the synthetic owner fields.
Use a fresh Agent name and matching user alias appropriate for the authenticated
owner. The example registry is deliberately nonfunctional.

An administrator creates the reusable configuration through supported APIs.
The Agent creator uses an authorized owner context; do not supply internal
authorization records, lifecycle finalizers or manually edited status.
Do not pre-create the Agent namespace or substitute unowned HOME/Secret objects.

## Helper memory

The Portal's new-Sandbox form and [configuration.yaml](configuration.yaml)
recommend `spec.resources.helpers.limits.memory: 2Gi`, with the memory request
remaining `128Mi`. OpenClaw 2026.9.6's doctor was observed to be OOMKilled at the
previous `512Mi` limit; `2Gi` allowed initialization to complete. This is a
starting recommendation, not a measured minimum or a guarantee for every runtime
and workload. All init containers, including `doctor`, `retire-copilot-sign-in`
and `retire-direct-model-auth`, and any Agent sidecars share the Sandbox's
per-helper resource settings. Include them and RuntimeClass overhead when
planning the aggregate Pod resource budget.

Existing Sandboxes retain their stored limits. To raise them, use the supported
[coordinated configuration update](../../app/cetirun-operator/README.md#coordinated-configuration-updates)
flow; changing the Portal default does not resize existing Sandboxes or bypass
lifecycle and admission checks.

## HOME access mode

This example and Portal default to `ReadWriteOncePod` (RWOP). New common-executor
Kata lifetimes also support `ReadWriteOnce` (RWO): select it in
`Sandbox.spec.storage.home.accessMode` and explicitly register it in
`registrations.kata.Capabilities.homeAccessModes`. Do not change an existing
HOME claim's mode or remove its ownership records to convert a running Agent.
Retained single-start lifetimes remain RWOP-only.

Both modes use the same retained restart, stop/resume and separately enabled
healthy-source migration flow. RWO permits multiple mounts on one node; it
does not replace private HOME ownership, single-replica workload admission or
acknowledged source termination and CSI detach before migration.
The local platform probe tests RWOP contention, not RWO lifecycle behavior.
Qualify the selected Kata/CSI combination separately before declaring RWO
support; an RWOP probe result alone does not justify adding it to the registration.

## Scoped internal egress

New common-executor Kata OpenClaw/Hermes lifetimes can use
`AgentPolicy.spec.network.allowedInternalCIDRs` with either registered HOME mode.
For example, this synthetic **policy fragment** grants TCP `443` throughout one
private subnet and adds UDP `8080` only in its overlapping subrange:

```yaml
network:
  allowedInternalCIDRs:
    - cidr: 10.20.0.0/16
      ports:
        - {port: 443, protocol: TCP}
    - cidr: 10.20.1.0/24
      ports:
        - {port: 8080, protocol: UDP}
```

Replace these ranges with approved internal destinations; they are not deployed
test endpoints. Omitted protocols default to TCP; the requested protocol and IP
family must be explicitly registered. SCTP has the additional
[direct-endpoint limits](#governed-sctp-egress) below.
Node, management, metadata and mandatory excluded ranges cannot
be granted. Explicit CIDR/port denies still win, and TCP/UDP `53` remains limited
to approved DNS peers even if an internal rule includes that port.

For Cilium-managed Pods, CIDR matching alone is insufficient: the platform must
register the exact namespace, protected peer-UID label and addresses in
`topology.peers`, with a complete managed-peer inventory. All addresses of a
registered peer must receive the same effective port permissions. This does not
discover changing Pod IPs or translate a Service's port to its backend port.
Do not grant node ranges, broaden selectors or alter the Cilium deployment to
work around an incomplete registration.

When configuration coordination is enabled, suspend all consumers and complete
their normal shutdown before updating reserved policy or platform sources.
Resume through the ordinary lifecycle after the coordinated configuration
change; do not remove reservations or edit status. Retained version-1
single-start lifetimes still reject internal grants.

Offline compiler, chart and API-server checks do not qualify real traffic.
On an explicitly authorized target, use owner-authenticated Agent requests and
independent task-owned witnesses to verify the permitted address/port/protocol,
wrong ports/protocols, outside ranges, overlapping grants, explicit denies,
managed-peer identities, DNS/node/metadata isolation and unchanged peer Agents.
Repeat the witness after grant removal through the supported lifecycle and
confirm access is denied. Record the exact artifacts and registration; this
field does not complete the Istio handover or enable FQDN/mTLS controls.

## Governed SCTP egress

New common-executor Kata OpenClaw/Hermes lifetimes can request SCTP in both
`network.blockedPorts` and `network.allowedInternalCIDRs`. They use the existing
Cilium compiler and owned CiliumNetworkPolicy, not a new proxy or dataplane.
Retained version-1 single-start lifetimes still reject SCTP port requests.

The existing administrator-controlled
`registrations.kata.Capabilities.protocols` list is the protocol opt-in.
Registration examples and the local platform probe still default to TCP/UDP.
Neither an API value of `SCTP` nor a successful TCP/UDP probe qualifies SCTP.
Only after separately authorized qualification of the exact Kata, kernel,
Cilium and address-family combination, add SCTP to that list. Port denies
also require `blockedPorts: true`. This **capability fragment** is not evidence
or a complete platform configuration:

```json
{
  "protocols": ["TCP", "UDP", "SCTP"],
  "blockedPorts": true
}
```

Keep the native adapter's `qualified: false`; this increment supplies no
independent enforcement observation or physical fencing. Do not enable SCTP
in Cilium or change host/kernel settings as part of an offline check.

This synthetic **policy fragment** grants SCTP `3868` to approved direct
internal endpoints and denies SCTP `3869` at every destination:

```yaml
network:
  allowedInternalCIDRs:
    - cidr: 10.20.0.0/16
      ports:
        - {port: 3868, protocol: SCTP}
  blockedPorts:
    - {port: 3869, protocol: SCTP}
```

The internal grant does not permit TCP/UDP `3868` or other SCTP ports.
Overlapping grants combine exact protocol/port pairs; explicit CIDR and port
denies still take precedence. TCP/UDP DNS `53` and the registered Gateway
ports remain required; blocking those pairs rejects the configuration.
Blocking SCTP on the same numeric port does not block TCP/UDP. Ports must be
`1`–`65535`; an omitted protocol still means TCP, not SCTP. IPv6 needs an
explicit `IPv6` registration and corresponding topology.
The existing public-egress baseline is unchanged: public addresses remain
permitted except for exclusions and explicit denies. Protocol registration is
not a public-egress allow-list.

SCTP internal grants cannot overlap registered Service CIDRs or Kubernetes API
destinations, in addition to the existing node, management, metadata and
mandatory exclusions. Use direct endpoints and the existing protected
namespace/peer-UID registration for managed Pods; do not treat CIDRs as
Service discovery. All addresses of a peer need identical effective port
permissions. Cilium's [SCTP limitations](https://docs.cilium.io/en/stable/configuration/sctp/)
include unsupported pod-to-VIP policies, multihoming, port rewriting, BPF
masquerading and egress gateway. This feature does not add those capabilities;
the administrator must qualify the actual direct path, including any NAT.

The existing [compiler budgets](../../app/cetirun-operator/internal/sandboxpolicy/limits.go)
and 40-entry port groups apply without truncating controls. An unregistered
protocol, unsupported family, malformed rule, protected destination or excessive
expansion rejects the request. Admission checks a policy against its selected
Sandbox/platform; storing an unused AgentPolicy alone is not authorization.

Use the ordinary stop/change/resume lifecycle for policy or registration changes.
Active policy reservations remain enforced. Administrator changes to platform
inventory invalidate old Pod authority; they are not an in-place capability
upgrade for running Pods. The compiler and controller bind both the requested
rules and registration into the existing revisions and source checks.

The served-API and compiler tests exercise these contracts with synthetic
inventory. They do not run SCTP traffic through Kata/Cilium or qualify a target.
On a separately authorized target, verify allowed SCTP, wrong ports and
TCP/UDP on the same port, overlapping grants, explicit denies, required DNS
and Gateway access, protected destinations, peer isolation and grant removal.
Record direct endpoint paths and each registered address family, not merely
application success. FQDN blocking and ingress mTLS remain unsupported.

## Rollout-stable managed ingress

For an AKS-managed Istio gateway, register the stable labels already supplied by
the gateway Deployment instead of manually labelling its individual Pods.
The native single-start profile accepts the following **one-entry fragment**
under `platforms.kata.topology.ingress` in the administrator's `platform.json`:

```json
[
  {
    "peer": {
      "name": "aks-istio-ingressgateway-external",
      "namespace": "aks-istio-ingress",
      "uid": "example-ingress-deployment-uid",
      "podSelector": {
        "istio": "aks-istio-ingressgateway-external"
      }
    },
    "trustedProxyCIDRs": ["10.244.0.0/16"],
    "ports": [{"port": 18789, "protocol": "TCP"}]
  }
]
```

Replace the synthetic UID, namespace, labels, Pod CIDRs and workload ports with
the approved target inventory. Use a stable gateway identity, such as its
Deployment UID, not a Pod UID. The registration declares that identity; it does
not discover or independently verify the Deployment. For native Hermes, register
its API port (`8642`) and Dashboard port (`9119`) instead of the OpenClaw port
above; see [Create, connect and use](#create-connect-and-use).

This mode requires matching Operator and runtime chart support, and:

- At least one valid Pod label, with all labels matched within the exact
  registered namespace. Do not include `peer.ips`: selection and proxy trust
  must not depend on the current Pods. The `peer-uid` Pod label is not required
  for this ingress entry. DNS, AI Gateway and other peers retain their existing
  IP/peer-UID contract.
- Explicit `trustedProxyCIDRs` contained in the registered `topology.podCIDRs`.
  The compiler rejects wildcard ranges and ranges overlapping Service, Node,
  management, metadata, loopback or link-local networks. Register the actual
  gateway source Pod networks, including each required IP family. Host-networked
  gateways or paths that SNAT to Node addresses are not supported by this mode.
- A platform-controlled namespace and labels, an authenticating gateway that
  still enforces owner access, and forwarded-client headers overwritten or
  safely rebuilt by that gateway. Tenants must not be able to create or relabel
  Pods in the selected gateway namespace.

Cilium still admits only the selected gateway Pods on the registered workload
ports and explicitly denies other sources and ports. Trusting the bounded Pod
CIDRs in OpenClaw does **not** admit arbitrary Pods from those networks. Gateway
Pod replacement within these networks needs no relabelling, registration update
or native Agent restart. OpenClaw's token and device authentication remain
enabled; the legacy `openclaw.portalSignIn` mode is not enabled for native
execution by this change.

This is an opt-in registration change, not an automatic migration of running
Agents. Existing lifetimes retain their recorded configuration. Arrange the
normal lifecycle transition before adopting a changed platform registration;
do not edit their expectations, authorization or start markers. Exact-IP
registrations remain supported, but their protected labels and proxy IP list
must still be maintained whenever the gateway Pods change.

Deployment preflight and offline rendering do not qualify gateway rollouts.
With separate authorization for the target, verify owner UI access before and
after a gateway Pod replacement, confirm the native Agent Pod and registration
are unchanged, and check that unauthenticated, non-owner and non-gateway access
remain denied. Record any source-IP translation or network enforcement failure
rather than widening proxy trust.

## Create, connect and use

Set an absolute `KUBECONFIG`, explicit `OWNER_CONTEXT` and `ADMIN_CONTEXT`, and
the expected `CLUSTER_UID` obtained from the approved target inventory.
Verify both contexts refer to that target before any write:

```bash
set -euo pipefail
for context in "$OWNER_CONTEXT" "$ADMIN_CONTEXT"; do
  test "$(kubectl --kubeconfig "$KUBECONFIG" --context "$context" \
    --request-timeout=15s get namespace kube-system -o jsonpath='{.metadata.uid}')" \
    = "$CLUSTER_UID"
done
```

Create the edited configuration with the administrator context using
`kubectl create -f configuration.yaml`, with the same explicit kubeconfig and
context arguments. If any named configuration already exists, stop and choose
new names or arrange deliberate reuse; do not overwrite it.

For the Agent, use the owner context and retain the creation receipt outside
the repository:

```bash
k() {
  kubectl --kubeconfig "$KUBECONFIG" --context "$OWNER_CONTEXT" \
    --request-timeout=15s "$@"
}
AGENT_NAME=native-example
STATE_DIR="$(mktemp -d -t cetirun-native.XXXXXXXX)"
test -z "$(k get agent "$AGENT_NAME" --ignore-not-found -o name)"
test -z "$(k get namespace "cetirun-$AGENT_NAME" --ignore-not-found -o name)"
k create -f agent.yaml -o json > "$STATE_DIR/created.json"
jq -e --arg name "$AGENT_NAME" \
  '.metadata.name == $name and (.metadata.uid | type == "string" and length > 0)' \
  "$STATE_DIR/created.json" >/dev/null
k wait --for=condition=Ready "agent/$AGENT_NAME" --timeout=10m
k get agent "$AGENT_NAME" -o jsonpath='{.status.accessUrl}'
```

If creation's outcome is uncertain, inspect it through authoritative reads
before taking further action. Do not retry creation or delete by name.

1. Verify the actual Pod is running the selected image and RuntimeClass, with
   the expected volumes and current application readiness. Record failures
   rather than modifying replicas, expectations or finalizers.
2. Open the returned native URL as the owner and sign in normally. The existing
   Portal Overview provides **Copy credential** for the Gateway token after
   the controller publishes the verified workload namespace. Paste that token
   into the native UI, or use OpenClaw's browser-only `#token=` handoff; never
   put it in a query string, log, example file or shared receipt.
   A new browser still needs normal device pairing. If OpenClaw asks for
   approval, use the existing owner-authenticated web terminal and run
   `openclaw devices approve <exact-current-request-id>` after checking the
   request. The registered ingress destinations must include TCP `7681` for
   that terminal as well as the actual OpenClaw listener, from the approved
   ingress peers only. For a native Hermes Agent they must include its API port,
   `8642`, and its Dashboard port, `9119`, which the Portal routes `/ui/<agent>/`
   to; without them the Hermes UI times out at the ingress. This is an explicit owner approval, not Kubernetes Pod
   exec or disabled device authentication. Verify an authenticated native
   WebSocket session; a handshake challenge alone is insufficient. Confirm
   unauthenticated and nonowner access cannot use the Agent.
3. Select the permitted real model and obtain a response through the Gateway.
4. Ask the Agent to make a harmless request carrying a fresh random nonce to
   a task-owned synthetic witness endpoint permitted by baseline networking.
   Verify that endpoint's receipt independently. Model prose claiming success
   is not evidence that the tool ran. Send no credentials or user data to the
   witness; do not use protected-Pod exec/attach/port-forward as an access bypass.

The legacy full `scripts/e2e-test.sh` flow is not this profile's acceptance run:
its suspend/resume and legacy integration checks are outside this profile.
Reuse its explicit-target/UID ownership principles, not its broader assertions.
FQDN rules, unregistered SCTP port requests, Copilot sidecars,
identity grants and live reconfiguration are unsupported here and must not be
silently ignored.

### Optional CIDR and TCP/UDP denies

The policy can include `network.blockedCIDRs` for registered address families
and `network.blockedPorts` for TCP/UDP. For example, these synthetic values deny
one documentation subnet and two destination ports:

```yaml
network:
  blockedCIDRs:
    - 198.51.100.0/24
  blockedPorts:
    - {port: 18080, protocol: TCP}
    - {port: 18081, protocol: UDP}
  ingress:
    mode: authenticated
```

This is a policy fragment, not a complete AgentPolicy or a reachable test
endpoint. Port denies require verified platform support recorded as
`registrations.kata.Capabilities.blockedPorts: true`; the baseline example does
not set that flag. Rules cannot block required DNS or Gateway traffic.
The existing public-egress baseline remains; these fields are not a public
destination allowlist or FQDN policy.

On an authorized target, use separate Team/Policy bindings for a restricted
Agent and a control Agent. Compare both against the same independently observed
task-owned endpoint, including a known-working allowed port for each protocol.
Verify DNS and a real permitted model request still work. Service failures and
generic transport errors must not count as policy denials. Do not use the
documentation addresses above as though a listener exists there.

Apply Team/Sandbox/AgentPolicy changes only through the supported coordination
path after their consumers have completed cleanup. Keep the approved platform
inventory fixed during the run. A fresh Agent can test a changed policy;
this is not resume, live rollout or transfer of an existing HOME.

### Repeat the network comparison

[native-network-check.py](../../scripts/native-network-check.py) checks the
network portion of this first run. It makes only explicit-target Kubernetes
reads and credential-free requests to your witnesses. It does not create or
delete Agents, change policies, execute in protected Pods, or establish owner
authentication, real model/tool use or runtime qualification.

Use three fresh OpenClaw Agents from this example, with distinct Team and
AgentPolicy objects and the same approved Sandbox/artifact configuration:

| Role | Policy controls |
| --- | --- |
| `control` | No `blockedCIDRs` or `blockedPorts` |
| `cidr` | One CIDR covering the restricted witness, but not the outside witness; no port denies |
| `ports` | TCP `18080` and UDP `18081` denied; no CIDR denies |

The Agents must be Ready before preparing a run. Supply the exact names rather
than assuming these role names are their resource names.

Deploy [network-witness.py](network-witness.py) on **two separate task-owned
IP addresses outside the platform's internal/management ranges**. It needs
Python 3 and TCP `18080`/`18082`, UDP `18081`/`18083`; the latter port of each
protocol is the allowed control. Restrict access to the validation participants.
It defaults to loopback; non-loopback binding needs explicit permission:

```bash
python3 network-witness.py --listen-address 0.0.0.0 --allow-public-listen \
  --allow-peer "$VALIDATION_CLIENT_CIDR" --allow-peer "$CLUSTER_EGRESS_CIDR"
```

Include every verified egress address used by the test Agents, plus the
validation host. Peer ranges are required beyond loopback and unrestricted
`/0` ranges are rejected. No credential is used for witness access.

This fixture stores only generated synthetic run/nonce pairs in memory. Do not
send credentials or other data to it. Replacing a witness or overflowing its
bounded event log invalidates a prepared run. Do not use a customer service,
an arbitrary public endpoint or the documentation IP ranges as a live witness.

On the validation host, create a private plan after checking the target and
the Agents' current configuration:

```bash
python3 -B scripts/native-network-check.py prepare \
  --kubeconfig "$KUBECONFIG" --context "$ADMIN_CONTEXT" --cluster-uid "$CLUSTER_UID" \
  --control-agent "$CONTROL_AGENT" --cidr-agent "$CIDR_AGENT" --ports-agent "$PORT_AGENT" \
  --restricted-witness "$RESTRICTED_WITNESS_IP" --outside-witness "$OUTSIDE_WITNESS_IP" \
  --dns-name "$APPROVED_GATEWAY_DNS_NAME" --output "$STATE_DIR/network-plan.json"
```

The file is created exclusively with owner-only permissions. It contains
private resource identities and endpoints, not credentials; do not commit or
publish it. The checker pins Agent/configuration/namespace/Pod/runtime/image
identities and its own source. Kubernetes client authentication must use the
explicit verified-HTTPS context without plugins or impersonation.

Generate each phase's readable probe command:

```bash
python3 -B scripts/native-network-check.py command \
  --plan "$STATE_DIR/network-plan.json" --phase control-before
```

Run that command through the **normal owner-authenticated terminal of the
control Agent**, not Kubernetes exec/attach/port-forward. Save its single
`CETIRUN_NETWORK_RESULT=` output line privately as `control-before.txt`.
Repeat for `cidr` in the CIDR Agent and `ports` in the port-restricted Agent,
then `control-after` in the control Agent. Do not replay a phase: duplicate
nonces fail validation. Probes have a 60-second deadline and check the recorded
hostname, DNS resolution, and all eight destination/protocol/port combinations.
The command supervises the complete probe in a separate process, including
native DNS resolution. On timeout or cancellation, it terminates and reaps the
worker before failing without a result line; partial observations are not accepted.

Verify within 30 minutes, using the same checker source and unchanged Agents:

```bash
python3 -B scripts/native-network-check.py verify \
  --kubeconfig "$KUBECONFIG" --context "$ADMIN_CONTEXT" --cluster-uid "$CLUSTER_UID" \
  --plan "$STATE_DIR/network-plan.json" \
  --result "$STATE_DIR/control-before.txt" --result "$STATE_DIR/cidr.txt" \
  --result "$STATE_DIR/ports.txt" --result "$STATE_DIR/control-after.txt"
```

Verification independently reads both witnesses and rechecks the source pins
before and after collecting evidence. Positive controls must bracket the
restricted probes. An allowed packet needs its matching witness receipt; a
denied packet must have no receipt and the expected timeout type. Refused
connections, malformed replies, post-connect TCP timeouts, missing controls,
stale/replayed results, source drift and unknown observations fail the check.
Each witness request made by `prepare` or `verify` has a five-second total
deadline covering connection, response headers and body collection. Continued
partial responses do not reset that deadline; an overrun closes the connection
and fails with `witness-deadline-exceeded`.
No polling-to-success or request retry turns those failures into a pass.

A passing summary covers 32 network checks, including eight expected denials.
It deliberately leaves `qualifiesExecution` false and owner access, model/tool
use and cleanup as `not-tested`. Continue the separate first-run steps above
and below on the same build; do not promote this network result to an E2E pass.
Keep initial failures, and remove the two task witnesses after validation.

## Delete and verify cleanup

Recheck the target UID. Compare the current Agent with the creation receipt,
then issue a UID/resourceVersion-conditional deletion:

```bash
test "$(k get namespace kube-system -o jsonpath='{.metadata.uid}')" = "$CLUSTER_UID"
expected_uid="$(jq -er '.metadata.uid' "$STATE_DIR/created.json")"
k get agent "$AGENT_NAME" -o json > "$STATE_DIR/current.json"
jq -e --arg uid "$expected_uid" '.metadata.uid == $uid' \
  "$STATE_DIR/current.json" >/dev/null
jq '{apiVersion:"v1",kind:"DeleteOptions",
  preconditions:{uid:.metadata.uid,resourceVersion:.metadata.resourceVersion},
  propagationPolicy:"Background"}' "$STATE_DIR/current.json" |
  k delete --raw "/apis/cetirun.microsoft.com/v1alpha1/agents/$AGENT_NAME" \
    -f - > "$STATE_DIR/delete.json"
k wait --for=delete "agent/$AGENT_NAME" --timeout=10m
k wait --for=delete "namespace/cetirun-$AGENT_NAME" --timeout=10m
```

A conflict or unknown delete outcome requires observation, not automatic replay.
Never force-delete the namespace, Pod, storage or lifecycle finalizers.
Verify route withdrawal, Gateway grant revocation and owned resource cleanup.
Respect storage reclaim policy; report any retained PVs and ongoing cost.
When a skills publication is configured, its read-only shared source is not
owned by this Agent and must survive. The no-skills example has no shared
skills storage to retain or delete.

Reusable Sandbox/AgentPolicy/Team configuration is separate from Agent cleanup.
If created solely for this run, remove it with the existing
[coordinated configuration deletion procedure](../../app/cetirun-operator/README.md)
after confirming no consumers remain. Disclose deliberately retained
configuration and any target infrastructure; unknown cleanup is not a pass.

Keep a sanitized receipt tying the source/image/chart pins and the five results
to the same run: **provisioned, Ready, owner connected, real model/tool action,
cleaned up**. Keep private target IDs and credentials out of PRs, and remove
task-local receipt files when no longer needed.
