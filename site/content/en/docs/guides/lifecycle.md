---
title: "Manage instance lifecycle"
description: "Examine instance health, request a retained restart, and update, suspend, resume, or delete an instance."
weight: 40
---

## Manage agents as an administrator

In Portal, open **Admin → All agents**. Search by Agent name or owner, or
filter by Team, Agent type, and phase. To open the management page of an
Agent, select its name or **View details**. To get access, a user must be a
member of the Portal's configured administrator allowlist. Team membership
alone does not grant this access.

The **Overview** tab shows ownership, configuration, integration states, and
the desired and observed configuration generations. **Status** shows Conditions
and Pod health. **Events** shows Agent and namespace events. An unavailable
diagnostic source is reported separately from an empty result. Configured
registry authentication means that a credential reference exists. It does not
prove that an image can be pulled.

This page does not expose credentials. It does not grant access to the owner's
chat, terminal, or native Agent workspace. The page does not include arbitrary
Helm values and annotations. The ordinary owner APIs keep their ownership
checks. This is also true when the caller is an administrator.

Select **Edit** to open the configuration dialog. In this dialog, you can
change the display name, image, model override, or supported integration
settings. Save and Cancel stay below the scrollable fields. You can edit the
port only for Custom Agents, as in the creation form. The platform runtime
manages the OpenClaw and Hermes ports, and the dialog shows them as read-only.
Web UI applies only to Custom Agents with a port.

If you clear the model, the platform's default resolution is selected. Agent
type, runtime username, resource name, owner, Team, Sandbox binding, and Chart
version are read-only here. You cannot clear a pinned image. An image change
keeps any private registry credential reference that exists. Administrators
cannot replace credentials through this page. Platform type restrictions and
admission still apply.

When you save, only the changed fields are sent. It is possible that the
Operator did not apply a saved configuration yet. Before you consider the
Agent ready, review Conditions and the observed generation.

A background refresh does not overwrite the draft of your changes. If another
operation changes the Agent, a version conflict keeps the draft. Before you
save again, reload and review the current configuration. The Portal does not
automatically retry a write that conflicts. If a refresh fails, the last loaded
details and the draft of your changes stay visible. The failure is reported so
that the conflict review can be retried.

Select **Delete**. Review the owner and Team. To confirm, enter the complete
Agent name. Deletion can remove persistent data, for example PVC objects
during namespace cleanup. There is no recycle bin or data-retention option in
this workflow.

After the request is accepted, the page shows **Deleting** and continues to
show status and events. When the Agent resource disappears, the page returns
to the list. This does not prove that all external resources or storage
finished cleanup. You must investigate a stuck deletion. The page cannot
remove finalizers or force cleanup. See [deletion behavior](#delete-only-with-a-recovery-plan).

If a version conflict occurs during deletion, select **Reload and review**.
Examine the current owner and Team. Then enter the complete Agent name again.
A reload never automatically retries deletion.

The administrative API uses `/api/v1/admin/instances` for the list operation.
It uses `/api/v1/admin/instances/{name}` for `GET`, `PATCH`, and `DELETE`.
`GET /api/v1/admin/instances/{name}/status` returns diagnostics and any errors
for each source. Both mutation bodies must contain the `uid` and
`resourceVersion` from the current detail response. The API returns these
status codes:

- `428` if the body does not contain the versions.
- `409` if the Agent changed or was replaced.
- `202` for an accepted deletion.

Together with those version fields, `PATCH` accepts only `displayName`,
`model`, and the nested `agent.image`, `agent.port`, and `agent.webUI` fields.
`agent.port` and `agent.webUI` are accepted only for Custom Agents. If a
request sends one of these fields for a built-in Agent, the API returns `400`.
This also applies to a request that tries to clear the field. Logs of
administrative mutations record the actor, the target, the names of changed
fields, and the HTTP result. They do not record configuration values or
request bodies.

## Inspect before changing

Microsoft Teams integration was removed. Before you upgrade an older
Teams-enabled deployment, use the previous version to retire its Azure Bot,
application registration, and federated credentials. Keep the resource
inventory. The new Operator removes the Teams API fields and no longer does
cloud cleanup. New runtime initializers remove saved Teams channel and plugin
references from HOME, but keep other configuration. They do not delete cloud
resources or conversation files.

Replace `alice` with your instance name. Replace `cetirun-alice` with the
reported namespace of the instance. These commands examine cluster resources
but do not change them:

```bash
kubectl get agents.cetirun.microsoft.com alice
kubectl get agents.cetirun.microsoft.com alice -o jsonpath='{.status.conditions}{"\n"}'
kubectl get pods,pvc -n cetirun-alice
kubectl get statefulset openclaw-alice-cetirun -n cetirun-alice
kubectl get virtualservice cetirun-alice -n cetirun-alice
```

Before the controller sets overall `Ready`, it waits for the StatefulSet's
updated replicas, ready replicas, and observed generation. Ingress creation is
best-effort, so also examine `IngressReady`. A displayed access URL or overall
`Ready` is not the same as a successful request through the gateway.

The probe paths are different for each runtime:

| Runtime | Startup | Liveness and readiness |
| --- | --- | --- |
| OpenClaw | `/health` on the gateway port, 18789 by default | `/healthz` on the gateway port |
| Hermes | `/health` on the API port, 8642 by default | `/health` on the API port |
| Custom with a port | `/healthz` on the declared port | Readiness uses `/healthz`. There is no liveness probe |
| Headless Custom | None | No HTTP probes |

These are pod probes. They do not examine Entra login, model entitlement, or
tailnet reachability. See the
[OpenClaw]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}})
probe templates.

## Suspend and resume

In Portal, owners can select **Suspend Agent** or **Resume Agent**. These
controls change only the `spec.suspended` intent. They do not delete the
Agent, HOME, or Gateway key. They do not reset the spend history or create
another lifecycle operation.

| Current Agent | Suspend | Resume |
| --- | --- | --- |
| Ordinary OpenClaw, Hermes, or Custom Agents, and common-executor Sandbox Agents | Allowed when the current owned workload has a confirmed PVC-backed HOME, also during provisioning or recovery | Allowed when suspension is requested |
| Ephemeral HOME, or workload storage that is unverified or absent | Unavailable. No retained suspension is accepted | A resume intent that already exists stays available |
| Pending restart or administrator migration | Suspension takes precedence | Continues the original operation and keeps its request ID and controller safeguards |
| Recorded legacy single-start Sandbox lifetime | Rejected. The lifetime is not converted | Rejected |
| Agent in deletion | Rejected. Deletion takes precedence | Rejected |
| Same requested intent on the same Agent UID | Observe without another write | Observe without another write |

The owner endpoints are `POST /api/v1/instances/{name}/suspend` and
`POST /api/v1/instances/{name}/resume`. The equivalent endpoints for the
default instance use `/api/v1/instance/suspend` and `/api/v1/instance/resume`.
For both actions, the JSON body must contain the current `uid` and
`resourceVersion`. The caller must be the owner, and the request must pass the
normal management origin checks.

They return HTTP `202` with the requested `suspended` value of the Agent, the
`suspendAvailable`/`resumeAvailable` capabilities, and the current controller
status. These capabilities show if the intent can be changed, not if compute
stopped or recovered. Restart stays unavailable during suspension or during a
pending restart or migration.

Portal examines the effective HOME mount of the StatefulSet, not only the
Agent overrides. This is because chart and deployment defaults can select
`emptyDir`. If its Pod stops, this type of HOME is lost. As a result, the
owner action returns `409 suspension_home_required` and does not write the
intent. If the workload storage is absent, unreadable, or not owned, the
action returns `503 suspension_home_unverified`.

The disabled control gives the reason through `suspensionUnavailableReason`.
No automatic storage conversion or data-loss confirmation is offered. Resume
and observation of an already accepted intent do not depend on a new storage
check.

After status updates, a repeated intent can be observed without another change
to the Agent. A stale version cannot reverse a newer intent or target a
replacement with the same name. After a lost or unreadable reply, retry with
the **original action, UID and version**, or observe the current Agent. Portal
refreshes after every outcome and does not automatically resubmit. A delayed
acknowledgement does not replace an observation that was already refreshed at
the same generation. This is also true when the only changes are status
changes from the controller.

If you close a dialog that you did not send, its draft is discarded. If you
close the dialog after an uncertain submission, the draft is kept for a retry.
This does not cancel accepted work. After a definite rejection, a new
confirmation against the current Agent is necessary. The rejection does not
prove that an earlier uncertain try never took effect.

Portal intentionally shows **Suspension requested**. It does not say that
shutdown is complete. Its controller phase is not an observation of Pod
termination or access removal. In the same way, HTTP acceptance of a resume is
not readiness. Wait for the Ready condition of the current generation and for
the restart and migration completion checks.

A failed recovery stays visible in Status, and suspension stays available when
persistent HOME can be confirmed. Portal does not advance migration phases or
give migration controls to owners. Use supported Portal, Operator, and CRD
versions that match.

To suspend an instance, set `spec.suspended` to `true`. The operator scales
the workload to zero. If the instance has a GitHub Copilot proxy, the operator
also scales this proxy to zero. The operator removes the ingress route of the
instance but keeps the persistent data.

```bash
kubectl patch agents.cetirun.microsoft.com alice --type=merge \
  -p '{"spec":{"suspended":true}}'
```

Make sure that the Agent shows `Suspended` and that the actual replica count of
the StatefulSet is zero. Also make sure that no workload pods are left and that
the VirtualService was removed. The suspension branch does not wait for pod
termination.

For ordinary OpenClaw and Hermes Agents, the controller enforces zero replicas
and `persistence.retainPolicy: Retain` after all Helm overrides. As a result,
a configured `Delete` policy cannot remove HOME during scale-down. The stored
Agent, deployment, and chart preferences do not change. Resume applies the
configured retention policy again. Older cache entries for the suspended state
are invalidated so that an Operator upgrade does not skip this retention
repair.

Keep autoscaling disabled for this lifecycle. `Suspended` alone does not prove
that all access paths stopped. During retained suspension, the ordinary
built-in apply path also rejects an effective ephemeral-HOME configuration.
This also applies to defaults that are resolved from the selected chart. A
configuration change must not turn an accepted retained stop into a
destructive scale-down.

The runtime chart allows only `replicaCount: 0` or `1`. Its HPA validation
accepts only `minReplicas=maxReplicas=1`. As a result, the HPA cannot scale an
agent horizontally, and it is not a substitute for explicit suspension. There
is no automatic idle-suspend/resume policy in this controller.

To resume, set `suspended` to `false`. Reconciliation must restore both the
workload and routing before it gives access back to the user. Suspension does
not revoke a tailnet device.

## Restart while retaining HOME

For a current, ready Agent, select **Restart Agent** in Portal. Then confirm
the interruption. This requests a replacement Pod through the controller. It
does not delete the Agent, namespace, or HOME PVC. Persistent files, the
current Gateway key identity, and the spend history are kept. Active process
sessions are interrupted, and Pod-local temporary files are not kept.

If you close the confirmation dialog before you submit, the draft is
discarded. If you open the dialog again, it uses the Agent version that is
displayed now. If you close the dialog after a sent request has an uncertain
result, the ID and version are kept for a safe retry. If an explicit
`instance_changed` version conflict occurs after a status update, close the
dialog. Then confirm again with the current version.

Other errors do not show if the original request was accepted. Examples are a
suspension after an earlier try, a network failure, or an unreadable response.
After these errors, the request is kept for a retry. An accepted request that
was already observed is never discarded only because a later retry is
rejected. After each outcome, Portal refreshes the displayed Agent. It does
not automatically submit another restart.

The owner API is `POST /api/v1/instances/{name}/restart`. Send the current
`uid` and `resourceVersion` in the JSON body. Also send an `Idempotency-Key`
header with a new ID of 16-128 characters. Use only letters, digits, `_`, or
`-` in the ID. When you retry after an uncertain response, use the same ID and
body again.

The accepted ID is persisted as `spec.restartRequestID`. A repeated current ID
does not request another restart. This is also true after a Portal restart. A
stale request cannot replace a newer request or a replacement Agent with the
same name.

HTTP `202` means that the request was accepted. It does not mean that the
replacement is ready. Portal reports the desired `restartRequestId` and
`observedRestartRequestId` separately. The Operator records
`status.observedRestartRequestID` only after it observes the requested
template on the current ready Pod. It does not count the Ready status of the
old Pod or a successful Helm apply as completion. A pending request prevents
another restart.

During an Operator rolling upgrade, it is possible that an older leader
observes the new Agent generation but does not apply the restart marker. A
restart-aware controller binds both reconciliation caches to the request ID.
As a result, takeover applies the pending restart even if that generation was
already observed. When the controller waits for the replacement Pod or records
its readiness, it does not request another apply.

Suspension and deletion take precedence. Restart cannot resume a suspended
Agent. Current policy and runtime checks still apply. Earlier single-start
Sandbox lifetimes reject this operation and do not convert their records.
Governed Custom uses the common Sandbox restart path. Custom without Sandbox
selection keeps its approved shutdown and apply path.

The Operator renders the Custom restart marker through the `podAnnotations`
chart contract that already exists. As a result, approved older Custom chart
pins can restart, stop, or resume without an upgrade. The marker stays
controller-owned. Values that a caller supplies are rejected. Use Operator,
Portal, and CRD versions that support the restart API. If a chart does not
have the necessary annotation contract, it cannot produce a completed restart.

This operation is a normal retained restart. It does not do node migration,
force deletion, physical writer fencing, or recovery from an unreachable
source. Completion is an observation of the Kubernetes workload. It is not a
new qualification for model inference or storage failover.

## Planned node migration

Platform administrators can opt in to planned cold migration for Agents that
meet these conditions:

- The Agent is a common-executor OpenClaw, Hermes, or governed Custom Agent.
- The Agent has one filesystem CSI HOME that uses `ReadWriteOncePod` (RWOP) or
  `ReadWriteOnce` (RWO).
- The selected Kata or container platform registration explicitly supports
  that HOME mode.

This is a Kubernetes API, not a Portal migration button. The deployment must
enable `sandboxSources.plannedMigration` after all readers are upgraded. First,
qualify the actual AKS, runtime, and CSI artifacts in an authorized test
environment. Non-Sandbox Custom, retained single-start, other PVCs, unavailable
sources, and cross-cluster moves are outside this initial scope.

Both HOME modes use the same checkpoints for restart, retained stop/resume, and
migration. RWO is not a single-Pod lock. Current workload admission and
acknowledged source shutdown and detach stay mandatory. HOME claims that
already exist are not converted in place. Retained single-start lifetimes stay
RWOP-only.

Read the current Agent UID/resourceVersion and the target Node UID from the
authorized context. The values in the command that follows are examples. You
must replace them:

```bash
kubectl --context=test-context patch agents.cetirun.microsoft.com alice --type=json \
  -p '[{"op":"test","path":"/metadata/uid","value":"current-agent-uid"},
       {"op":"test","path":"/metadata/resourceVersion","value":"current-version"},
       {"op":"add","path":"/spec/plannedMigration","value":{
         "requestID":"migration-example-0001",
         "target":{"name":"worker-b","uid":"current-target-node-uid"}}}]'
```

Observe `status.plannedMigration.phase` and `PlannedMigrationReady`. An
accepted request, an absent Pod, or a changed placement alone is not
completion. Before the controller allows the destination to start, it keeps
the observations of the source Pod's termination and CSI detach. For
completion, the current Ready replacement and the reattached original HOME are
necessary. The Agent, namespace, PVC, PV, and Gateway spend identities do not
change. Process sessions and Pod-local scratch do not survive.

To recover from an uncertain response, you must read the current request. Do
not send another ID. In-progress requests cannot be overwritten. Retries with
the same ID do not repeat a move. After completion, a new ID can request the
next move.

Suspension keeps the request but does not start its destination. Resume
continues the request. Deletion destroys the Agent through normal cleanup,
never through migration failover. Portal disables retained restart during a
pending migration.

Before a request is captured, a transient authorization outage can stop the
source. When authorization recovers, normal reconciliation restores the
current running configuration before it retries the capture. It does not apply
the new target early or discard an earlier completed destination pin. If the
owned source StatefulSet is scaled down or its readiness is not converged, a
stale Agent `Ready` observation does not prevent this recovery.

The request stays uncaptured until the actual source is stable. Ownership,
authorization, suspension, and target checks still apply. After capture, the
source observations must be protected before any hold stops compute. If a
source read or a protection write fails during `Protecting`, suspension stays
pending. When those operations recover, retries continue the same request.

These retries still examine the current authorization. If model access is
withdrawn or unverified, the Gateway key that exists is blocked. This block
does not depend on the deferred source shutdown, and it does not reset the
accounting identity of the key. A failed key operation stays an explicit error
with retries, not a successful block.

Do not clear recorded destination fields, edit checkpoints, force-delete Pods,
strip finalizers, or force-detach storage to make progress. Unknown source
state and incompatible or replaced Nodes stay blocked. Kubernetes and CSI
acknowledgements are not physical fencing or unavailable-node recovery.
See the [operator contract]({{< repo "app/cetirun-operator/README.md" >}}#planned-healthy-source-migration-separate-opt-in).

## Pin and update a chart

During the first reconciliation, the operator fills an empty
`spec.helm.version` with its configured agent chart version. Later operator
upgrades do not automatically advance that instance’s pinned chart. To update
it, set a reviewed version that is accessible in your configured OCI registry.

The portal's **Upgrade** action selects the chart version that is configured
on the operator Deployment. It does not query a registry for the newest
release. An update to that version is separate from a change to the operator
itself.

The lookup reads Deployment `cetirun-operator` in `cetirun-system` and the
`--chart-version` argument of its `manager` container. For an installation
with a different name or namespace, a reviewed workflow is necessary. If you
change only the Helm release namespace, this lookup does not become portable.
See the [portal Kubernetes client]({{< repo "app/cetirun-portal/internal/kube/client.go" >}}).

For an OpenClaw or Hermes Helm release that exists, the operator compares the
Agent's image with the deployed chart's default and resolved image. If both
match, a chart-version change selects the new chart's default image. The
operator saves that image before it deploys the new chart.

Other built-in image choices stay pinned and must satisfy the selected runtime
profile's qualification rules. Custom images stay pinned under their image
approval checks. Policy and lifecycle authorization still apply. An operator
upgrade alone does not change an Agent's pinned chart or image.

Treat model, chart, credential, and agent configuration changes as possibly
disruptive. Before you change users’ instances, review the backups and test
the selected configuration.

A change to a ConfigMap changes the pod-template checksum. On a full
reconcile, the operator also annotates the pod template with the resource
version of the gateway Secret. As a result, environment-backed credential
updates can restart the workload. It is possible that a direct Secret edit does
not trigger reconciliation. Use the supported update path.

## Update an Agent image

To select a new image, edit **Agent Image** in Portal or update
`spec.agent.image`. Platform image approvals still apply. The Agent type and
the image username cannot change. You cannot clear a pinned image. If you
change the image, `spec.helm.version` does not change.

A running Agent is replaced with the new image and the same HOME PVC. A
suspended Agent stays stopped. When the Agent is resumed, it uses the selected
image. Select an image that supports the data that already exists, the numeric
user identity, and the HOME permissions. CetiRun does not migrate native data
formats.

The process that seeds HOME is separate from an image or configuration update.
After `.cetirun-home-seeded` exists on the home PVC, a new image does not copy
its HOME again. It also does not update dependencies that were copied there
before. Managed configuration continues to apply during Pod initialization.

Do not remove initialization records to force an upgrade. A nonempty home
without a recognized record fails safely. If the seed operation was
interrupted, the same immutable source image is necessary. Before you do
explicit maintenance, review the recorded state, the files that exist, and the
recovery copies.

Custom without Sandbox selection uses a separate `OnDelete` update path. For
this path, approved model, HOME, and context bindings are necessary. An
explicit storage/lifecycle handoff is also necessary. Before the new plan is
applied, these conditions must be true:

- The old StatefulSet is stopped.
- No active Pod still refers to HOME.

The configured binding provider coordinates this handoff. The opt-in
Kubernetes provider scales the old StatefulSet to zero and waits for its HOME
consumers to exit. Then it allows the new image to be applied with one
replica. It does not use the built-in orphan-delete hook or replace the HOME
claim. Changes to the Model Key and to the context revision go through the
same gate. The process environment does not refresh in place.

The default installation does not enable Custom provisioning. The opt-in
non-Sandbox provider reports `CustomBindingsPending` until its prerequisites are
available. Governed Custom instead must have a current common-executor
registration and a Gateway-qualified image approval. Chat/headless instances
have no public Agent UI URL. A declared Web UI uses its authenticated route.
See [Agent Harness](../../reference/agent-harness/#custom-runtime) for the
implementation boundary and offline qualification tests.

## Delete only with a recovery plan

Governed Custom cleanup uses the common bootstrap ownership and grant journal.
Non-Sandbox Custom must have its separate durable ownership receipt. A
never-bound Custom does not delete resources only because their names match.

For a bound Custom, cleanup makes sure that the namespace has the recorded UID
before deletion. Then cleanup waits for the namespace to disappear. It does not
run Helm uninstall hooks. Absent records or changed ownership block cleanup. Do
not remove these finalizers to bypass the ownership check.

Deletion of a built-in `Agent` invokes finalizer cleanup. This cleanup tries a
Helm uninstall, then route and namespace deletion. Some cleanup errors are
logged but do not block finalizer removal. Namespace deletion is asynchronous.
As a result, a custom resource that disappeared does not prove that all
Kubernetes or Azure resources are gone. Examine these resources separately
under the same authorized scope.

> **Warning:** Before you delete an instance, back up the necessary data and
> test recovery. Namespace deletion removes PVC objects. This includes claims
> retained by a StatefulSet. To bind a retained volume again, a separate
> recovery procedure is necessary.

Before you authorize deletion, examine the reclaim policy of the storage
backend. Do not remove finalizers only to hide a blocked cleanup.

Sources: [instance controller]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[value builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
and [chart validation]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}).
