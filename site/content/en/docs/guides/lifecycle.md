---
title: "Manage instance lifecycle"
description: "Check instance health, request a retained restart, update, suspend, resume, or delete an instance."
weight: 40
---

## Inspect before changing

Replace `alice` and `cetirun-alice` with your instance name and its reported
namespace. These commands inspect cluster resources without changing them:

```bash
kubectl get agents.cetirun.microsoft.com alice
kubectl get agents.cetirun.microsoft.com alice -o jsonpath='{.status.conditions}{"\n"}'
kubectl get pods,pvc -n cetirun-alice
kubectl get statefulset openclaw-alice-cetirun -n cetirun-alice
kubectl get virtualservice cetirun-alice -n cetirun-alice
```

The controller waits for the StatefulSet's updated replicas, ready replicas,
and observed generation before setting overall `Ready`. Ingress creation is
best-effort and Teams errors are non-blocking, so also check `IngressReady`
and, if enabled, `TeamsReady`. A displayed access URL or overall `Ready` is
not a successful request through the gateway.

Probe paths differ by runtime:

| Runtime | Startup | Liveness and readiness |
| --- | --- | --- |
| OpenClaw | `/health` on the gateway port, 18789 by default | `/healthz` on the gateway port |
| Hermes | `/health` on the API port, 8642 by default | `/health` on the API port |
| Custom with a port | `/healthz` on the declared port | Readiness uses `/healthz`; no liveness probe |
| Headless Custom | None | No HTTP probes |

These are pod probes, not checks of Entra login, model entitlement, Teams
delivery, or tailnet reachability. See the
[OpenClaw]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}})
probe templates.

## Suspend and resume

To suspend an instance, set `spec.suspended` to `true`. The operator scales the
workload, and the instance's GitHub Copilot proxy if it has one, to zero and
removes its ingress route while retaining persistent data.

```bash
kubectl patch agents.cetirun.microsoft.com alice --type=merge \
  -p '{"spec":{"suspended":true}}'
```

Verify `Suspended`, the StatefulSet's actual zero replicas, no remaining
workload pods, and removal of the VirtualService. The suspension branch does
not wait for pod termination. The value builder requests zero before merging
instance overrides; inspect the effective Helm configuration and keep
autoscaling disabled for this lifecycle. `Suspended` alone is not proof that
all access paths have stopped.

The runtime chart permits only `replicaCount: 0` or `1`. Its HPA validation
requires `minReplicas=maxReplicas=1`, so it cannot scale an agent horizontally
and is not a substitute for explicit suspension. There is no automatic
idle-suspend/resume policy in this controller.

Resume by setting `suspended` to `false`. Reconciliation must restore both the
workload and routing before handing access back to the user.
Suspension does not delete Teams cloud resources or revoke a tailnet device.

## Restart while retaining HOME

For a current, ready Agent, choose **Restart Agent** in Portal and confirm the
interruption. This requests a replacement Pod through the existing controller;
it does not delete the Agent, namespace or HOME PVC. Persistent files and the
existing Gateway key identity and spending history are retained. Active process
sessions are interrupted, and Pod-local temporary files are not retained.
Closing the confirmation dialog before submitting discards that draft; reopening
uses the currently displayed Agent version. After a request was sent with an
uncertain result, closing the dialog retains its ID and version for a safe retry.
For an explicit `instance_changed` version conflict after a status update,
close the dialog and confirm again using the current version. Other errors,
including suspension after an earlier attempt, a network failure or an unreadable
response, do not establish whether the original request was accepted and retain
it for retry. An already observed accepted request is never discarded merely
because a later retry is rejected.
Portal refreshes the displayed Agent after each outcome without automatically
submitting another restart.

The owner API is `POST /api/v1/instances/{name}/restart`. Send the current
`uid` and `resourceVersion` in the JSON body and an `Idempotency-Key` header
containing a fresh 16-128 character ID made of letters, digits, `_` or `-`.
Reuse the same ID and body when retrying an uncertain response. The accepted
ID is persisted as `spec.restartRequestID`; a repeated current ID does not
request another restart, including after a Portal restart. A stale request
cannot replace a newer request or a same-name replacement Agent.

HTTP `202` means the request was accepted, not that the replacement is ready.
Portal reports the desired `restartRequestId` and
`observedRestartRequestId` separately. The Operator records
`status.observedRestartRequestID` only after observing the requested template
on the current ready Pod. It does not count the old Pod's Ready status or a
successful Helm apply as completion. A pending request prevents another restart.
During an Operator rolling upgrade, an older leader may observe the new Agent
generation without applying the restart marker. A restart-aware controller binds
both reconciliation caches to the request ID, so takeover applies the pending
restart even if that generation was already observed. Waiting for the replacement
Pod or recording its readiness does not request another apply.

Suspension and deletion take precedence; restart cannot resume a suspended
Agent. Current policy and runtime checks still apply. Earlier single-start
Sandbox lifetimes reject this operation rather than converting their records.
Custom Agents use their existing approved shutdown and apply path, not a new
executor. The Operator renders their restart marker through the existing
`podAnnotations` chart contract, so approved older Custom chart pins do not need
an upgrade to restart, stop or resume. The marker remains controller-owned;
caller-supplied values are rejected. Use Operator, Portal and CRD versions that
support the restart API. A chart that omits the required annotation contract
cannot produce a completed restart.

This operation is a normal retained restart. It does not provide node migration,
force deletion, physical writer fencing, or recovery from an unreachable source.
Completion is a Kubernetes workload observation, not a new model-inference or
storage-failover qualification.

## Planned node migration

Platform administrators can opt in to planned cold migration for common-executor
OpenClaw/Hermes Agents with one filesystem CSI/RWOP HOME. This is a Kubernetes
API, not a Portal migration button. The deployment must enable
`sandboxSources.plannedMigration` after all readers have upgraded. Qualify the
actual AKS, runtime and CSI artifacts in an authorized test environment first.
Custom, retained single-start, additional PVCs, unavailable sources and
cross-cluster moves are outside this initial scope.

Read the current Agent UID/resourceVersion and target Node UID from the
authorized context. The following values are illustrative and must be replaced:

```bash
kubectl --context=test-context patch agents.cetirun.microsoft.com alice --type=json \
  -p '[{"op":"test","path":"/metadata/uid","value":"current-agent-uid"},
       {"op":"test","path":"/metadata/resourceVersion","value":"current-version"},
       {"op":"add","path":"/spec/plannedMigration","value":{
         "requestID":"migration-example-0001",
         "target":{"name":"worker-b","uid":"current-target-node-uid"}}}]'
```

Observe `status.plannedMigration.phase` and `PlannedMigrationReady`. An accepted
request, absent Pod or changed placement alone is not completion. The controller
retains the source Pod's termination and CSI detach observations before allowing
the destination to start. Completion requires the current Ready replacement and
reattached original HOME; Agent/namespace/PVC/PV and Gateway spending identities
remain unchanged. Process sessions and Pod-local scratch do not survive.

An uncertain response must be recovered by reading the current request, not
issuing another ID. In-progress requests cannot be overwritten; same-ID retries
do not repeat a move. After completion, a new ID can request the next move.
Suspension retains the request without starting its destination; resume
continues it. Deletion destroys the Agent through normal cleanup, never migration
failover. Portal disables retained restart while migration is pending.

Before a request is captured, a transient authorization outage can stop the
source. Once authorization recovers, normal reconciliation restores the current
running configuration before retrying capture; it does not apply the new target
early or discard a previous completed destination pin. After capture, source
observations must be protected before any hold stops compute. Suspension remains
pending if a source read or protection write fails during `Protecting`, and
retries continue the same request when those operations recover.

Do not clear recorded destination fields, edit checkpoints, force-delete Pods,
strip finalizers or force-detach storage to make progress. Unknown source
state and incompatible or replaced Nodes remain blocked. Kubernetes and CSI
acknowledgements are not physical fencing or unavailable-node recovery.
See the [operator contract]({{< repo "app/cetirun-operator/README.md" >}}#planned-healthy-source-migration-separate-opt-in).

## Pin and update a chart

On first reconciliation the operator fills an empty `spec.helm.version` from
its configured agent chart version. Later operator upgrades do not automatically
advance that instance’s pinned chart. To update it, set a reviewed version
that is accessible in your configured OCI registry.

The portal's **Upgrade** action selects the chart version configured on the
operator Deployment; it does not query a registry for the newest release.
Updating to that version is separate from changing the operator itself.
The lookup reads Deployment `cetirun-operator` in `cetirun-system`
and its `manager` container's `--chart-version` argument. A differently named
or namespaced installation needs a reviewed workflow; changing only the Helm
release namespace does not make this lookup portable.
See the [portal Kubernetes client]({{< repo "app/cetirun-portal/internal/kube/client.go" >}}).

Treat model, chart, credential, and agent configuration changes as potentially
disruptive. Review backups and test the selected configuration before changing
users’ instances. ConfigMap changes alter the pod-template checksum. The
operator also annotates the pod template with the gateway Secret's resource
version on a full reconcile, so environment-backed credential updates can
restart the workload. Direct Secret edits might not trigger reconciliation;
use the supported update path.

## Update an Agent image

Edit **Agent Image** in Portal or update `spec.agent.image` to select a new
image. Platform image approvals still apply. The Agent type and image username
cannot change, and a pinned image cannot be cleared. Changing the image does
not change `spec.helm.version`.

A running Agent is replaced using the new image and the existing HOME PVC.
A suspended Agent stays stopped and uses the selected image when resumed.
Choose an image that supports the existing data, numeric user identity and
HOME permissions; CetiRun does not migrate native data formats.

HOME seeding is separate from an image or configuration update. Once
`.cetirun-home-seeded` exists on the home PVC, a new image does not recopy its
HOME or update dependencies previously copied there. Managed configuration
continues to apply during Pod initialization.

Do not remove initialization records to force an upgrade. A nonempty home
without a recognized record fails safely; interrupted seeding requires the
same immutable source image. Review the recorded state, existing files and
recovery copies before performing explicit maintenance.

Custom updates use a separate `OnDelete` path. They require approved model,
HOME and context bindings plus an explicit storage/lifecycle handoff. The old
StatefulSet must be stopped and no active Pod may still reference HOME before
the new plan is applied. The configured binding provider coordinates this
handoff. The opt-in Kubernetes provider scales the old StatefulSet to zero,
waits for its HOME consumers to exit, and allows the new image to be applied
with one replica. It does not use the built-in orphan-delete hook or replace
the HOME claim. Model Key and context revision changes go through the same
gate; process environment does not refresh in place.

The default operator has no production Custom binding provider and reports
`CustomBindingsPending`. A Custom process has no native UI URL, including when
its internal HTTP Service exists. See [Agent Harness](../../reference/agent-harness/#custom-runtime)
for the implementation boundary and synthetic qualification tests.

## Delete only with a recovery plan

Custom cleanup requires the Operator's durable ownership receipt. A never-bound
Custom does not delete resources merely because their names match. For a bound
Custom, cleanup verifies the recorded namespace UID before deletion and waits
for it to disappear; it does not execute Helm uninstall hooks. Missing records
or changed ownership block cleanup. Do not remove these finalizers to bypass
the ownership check.

Deleting a built-in `Agent` invokes finalizer cleanup: it attempts Teams
cleanup and Helm uninstall, then route and namespace deletion. Some cleanup
errors are logged without blocking finalizer removal, and namespace deletion
is asynchronous. A disappeared custom resource therefore does not prove that
all Kubernetes or Azure resources are gone. Verify them separately under the
same authorized scope.

> **Warning:** Namespace deletion removes PVC objects, including claims retained
> by a StatefulSet. Back up required data and test recovery before deleting an
> instance. Rebinding a retained volume requires a separate recovery procedure.

Check the storage backend's reclaim policy before authorizing deletion.
Do not remove finalizers merely to hide a blocked cleanup.

Sources: [instance controller]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[value builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
[chart validation]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
and [Teams cleanup]({{< repo "app/cetirun-operator/internal/controller/teams.go" >}}).
