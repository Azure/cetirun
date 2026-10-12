# Local native Sandbox validation

This directory contains a bounded local platform probe, not a production
qualification service. It composes the existing network smoke tests with
actual Kata host/guest resource observations, read-only filesystem checks,
node/DNS traffic controls and ordinary CSI ReadWriteOncePod (RWOP) contention
and reopen checks.

Read the [native first-run guide](../README.md) and
[development checks](../../../DEVELOPMENT.md) first. The probe requires an
explicit local Kind name, node-container identity, kubeconfig, context and
expected cluster UID. It never selects a cluster from ambient configuration.
Run it only on a disposable local environment you own.

## Tested local stack

The local integration used:

- Kind 0.33.0 / Kubernetes 1.37.0, with the API and HTTPS ingress exposed on
  loopback only.
- Real Kata Containers 4.2.0 QEMU/KVM, not a renamed runc RuntimeClass.
- Cilium 1.20.2 and the upstream CSI host-path test driver, not Azure CNI or
  Azure Disk qualification.
- Istio 1.31.1, a real local Dex 2.46.0 OIDC issuer, and OAuth2 Proxy with a
  task-local CA. Local OIDC is an explicit chart opt-in; Entra defaults remain
  unchanged.
- The actual CetiRun Operator, admission server, Portal, runtime chart and
  OpenClaw 2026.9.6 image.
- The actual CetiRun AI Gateway/PostgreSQL/Valkey, backed by local
  Qwen3-4B inference using llama.cpp. A canned model response is not equivalent.

The tested Agent used 4 GiB requested / 8 GiB limited application memory.
This is a tested local sizing choice, not a minimum or a production capacity
recommendation. Diagnose VMM termination using actual host limits and OOM
counters rather than assuming that increasing a memory limit fixes it.

### Kind/Kata details that matter

Kata's QEMU shared memory must fit inside the **node container's private**
`/dev/shm`; Docker's usual 64 MiB default is insufficient. Verify the actual
mount and its ownership before sizing it. Do not remount the development
machine's shared-memory filesystem.

Use the upstream Kata `[runtime] sandbox_cgroup_only = true` setting when
required by the node's systemd hierarchy. Then create a fresh sandbox and
verify the VMM and all its threads inherit finite host CPU/memory limits
including RuntimeClass overhead. Configuration text alone is not evidence.
The probe reports the numeric limits, ancestry, guest kernel and guest limits.

Runtime images may use `imagePullPolicy: Always`; loading an image into Kind
alone is then insufficient. Supply the exact image through a local registry
that the node can resolve and trust, alongside the versioned runtime chart.
Keep TLS verification enabled. A local CA certificate must be readable and
its directories traversable by the nonroot processes that use it.

## Run the bounded probe

Install dependencies separately using the pinned tooling described in
[DEVELOPMENT.md](../../../DEVELOPMENT.md). Substitute your own task-local
paths and already-verified resource names:

```bash
python3 -B examples/native-sandbox/local/platform-probe.py \
  --cluster-name cetirun-local-example \
  --node-container cetirun-local-example-control-plane \
  --context kind-cetirun-local-example \
  --cluster-uid-file /absolute/private/cluster-uid \
  --kubeconfig /absolute/private/kubeconfig \
  --kubectl /absolute/tools/kubectl \
  --docker-config /absolute/private/docker \
  --probe-image python@sha256:8cdd496996889eef0a8a7921c152c0a4a9f18a26c7a5e7257d3c90b431891894 \
  --runtime-class kata-qemu \
  --runtime-handler kata-qemu \
  --storage-class local-native-home \
  --output /absolute/private/local-platform-probes.json \
  --allow-local-test-resources
```

`baselineRequiredSatisfied` is true only when all required probe checks passed.
Do not replace a false/unknown observation with a declaration of support.
The helper retains `qualified`, `qualifiesExecution`,
`enforcementObservation` and `restrictiveFailure` as false. It does not test
production failover or physical writer fencing.
Keep its receipt private: it includes invocation paths and deployment metadata.
Review and minimize any excerpt before sharing it.

Offline helper tests:

```bash
python3 -B -m unittest discover \
  -s examples/native-sandbox/local -p 'test_*.py'
```

## Finish the actual Agent run

Passing this probe is only the platform step:

1. Populate the administrator-owned native registration from current
   RuntimeClass/CSI identities, actual topology and the observed controls.
   Select `native-single-start` and the generic `cilium` adapter for this
   non-Azure environment. Bind approved service peers to their current backend
   Pods and preserve their protected peer labels in workload templates.
2. Install matching components with native execution and enforced Agent/Pod
   admission. Configure one real Gateway model and normal owner authentication.
   Do not copy provider credentials into AgentPolicy or public files.
3. Create Sandbox, AgentPolicy, Team and Agent through authorized APIs. Verify
   actual application readiness, not just a created Pod or declared capability.
4. Complete owner login and explicit browser-device pairing using the existing
   Portal credential and owner web-terminal flow. Check nonowner and anonymous
   denial and authenticated native WebSocket success.
5. Send a real task through the Agent. Correlate a harmless tool request with
   a fresh nonce at an independently observed task-owned endpoint.
6. Delete that same Agent with UID/resourceVersion preconditions; verify
   Gateway grant revocation, live-session closure and owned namespace/PV cleanup.

The deployment preflight still requires admission replicas on distinct Nodes.
A one-node local stack does not meet that availability requirement. Report it
separately; do not weaken the check or describe local lifecycle success as
multi-node admission availability or production qualification.
