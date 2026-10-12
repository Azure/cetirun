# Native Sandbox first run

This example exercises one normal OpenClaw Agent with a Kata Sandbox and a
shared AI Gateway model. It is an explicitly enabled, single-start profile,
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
derived from the registered ingress peer IPs, not the legacy `127.0.0.6` sidecar
address. The controller passes only those exact addresses through its protected
render plan. Do not use a wildcard proxy range or a per-Agent Helm override.

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
  helpers:
    doctor: false
    logger: false
    desktop: false
aiGateway:
  enabled: true
sandboxAdmission:
  enabled: true
  enforce: true
  podsEnabled: true
```

This is an **overlay**, not complete installation configuration. Supply the
target's admission TLS/administrator/coordination settings, domain, registry,
Gateway configuration and source registries through your normal private
deployment configuration. Do not commit that configuration.

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
   ingress peers only. This is an explicit owner approval, not Kubernetes Pod
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
Custom network rules, Copilot sidecars, identity grants and live reconfiguration
are unsupported here and must not be silently ignored.

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
