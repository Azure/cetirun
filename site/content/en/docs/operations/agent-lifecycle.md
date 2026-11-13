---
title: "Administer Agents"
description: "Create, inspect, suspend, restart, migrate, update, and delete Agents through the Portal administration pages and the Kubernetes API."
weight: 38
---

This page is for platform administrators. Its tasks need the Portal
administrator role, or permission to read and change Agent resources in the
cluster. Agent owners manage their own Agents in the Portal. For the owner
tasks, refer to [Manage your Agent](../../guides/lifecycle/).

## Manage Agents in the Portal

In the Portal, open **Admin** → **Agents**. The page title is **All agents**.
Search by Agent name or owner, or filter by Team, Agent type, and phase. To
open the management page of an Agent, select its name or **View details**. To
get access, a user must be a member of the configured Portal administrator
allowlist. Team membership alone does not grant this access.

For this role and the other Admin pages, refer to
[Portal administration](../portal-administration/).

The **Overview** tab shows ownership, configuration, integration states, and
the desired and observed configuration generations. **Status** shows Conditions
and Pod health. **Events** shows Agent and namespace events. An unavailable
diagnostic source is reported separately from an empty result. Configured
registry authentication means that a credential reference exists. It does not
prove that an image can be pulled.

This page does not expose credentials. It does not grant access to the owner's
chat, terminal, or Agent UI. The page does not include arbitrary
Helm values and annotations. The ordinary owner APIs keep their ownership
checks. This is also true when the caller is a Portal administrator.

Select **Edit** to open the configuration dialog. In this dialog, you can
change the display name, image, model override, or supported integration
settings. Save and Cancel stay below the scrollable fields. You can edit the
port only for Custom Agents, as in the creation form. The platform runtime
manages the OpenClaw and Hermes ports, and the dialog shows them as read-only.
Web UI applies only to Custom Agents with a port.

If you clear the model, the platform's default resolution is selected. Agent
type, runtime username, resource name, owner, Team, Sandbox binding, and Chart
version are read-only here. You cannot clear a pinned image. An image change
keeps any private registry credential reference that exists. Portal
administrators cannot replace credentials through this page. Platform type
restrictions and admission still apply.

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

### Administration API

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

## Create an Agent with Kubernetes

You must have permission to create cluster-scoped resources on a configured
cluster. The Portal limit of Agents for each user is a Portal creation limit.
It is not a Kubernetes resource quota, and it does not apply to Agents that
you create with this procedure. Save this manifest as `instance.yaml` outside
the repository.
Replace `alice-example` with the Agent name and `spec.user.alias` with the
identity of its owner. When you apply the manifest, the Agent and its
resources are created.

```yaml
apiVersion: cetirun.microsoft.com/v1alpha1
kind: Agent
metadata:
  name: alice-example
spec:
  team: example-team
  sandboxRef: standard
  agent:
    type: hermes
  modelAccess:
    sharedModels: true
  gatewayTokenSecretRef: gateway-token
  user:
    alias: alice
    displayName: Alice
```

Keep `metadata.name` and `spec.user.alias` stable. The Agent namespace, Helm
release, and URL path come from the name. The ownership comes from the alias.
Do not use `ui`, because the controller reserves it.

`spec.team` and `spec.sandboxRef` select the Agent Policy and the isolation
settings that an administrator defines. You must set `spec.agent`. If you do
not set its `type`, Hermes is the default. The effective type and runtime
username cannot change after creation.

An Agent with `spec.sandboxRef` must request its model sources. Without
`modelAccess.sharedModels: true` or `modelAccess.githubCopilot: true`, it gets
no model. The Agent Policy of the Team must also allow each source that the
Agent requests.

Validate the manifest against the cluster's installed CRD. This command does
not save the manifest:

```bash
kubectl apply --dry-run=server -f instance.yaml
```

Server-side validation examines admission and the schema. It connects to the
cluster, but it does not test image pulls, scheduling, storage, routing, or
identity.

Create the Agent:

```bash
kubectl apply -f instance.yaml
```

Examine the Agent status:

```bash
kubectl get agents.cetirun.microsoft.com alice-example
kubectl wait --for=condition=Ready agents.cetirun.microsoft.com/alice-example --timeout=10m
kubectl wait --for=condition=IngressReady agents.cetirun.microsoft.com/alice-example --timeout=2m
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.namespace}{"\n"}{.status.accessUrl}{"\n"}'
```

If `gateway-token` does not exist in `cetirun-alice-example`, the
[controller]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}})
creates it. The Secret contains `OPENCLAW_GATEWAY_TOKEN` and the separate
`CETIRUN_AUTH_KEY`. Hermes also must have `API_SERVER_KEY` and `hermes.env`,
and the controller supplies them. Do not put tokens in the CR or print the
Secret for diagnosis. If you supply a Secret that already exists, make sure
that its OpenClaw token is valid. The name of a Secret alone does not validate
its contents.

`spec.model` is optional. It names the default model of the Agent, as the AI
Gateway offers it, for example `auto`. Without it, the operator supplies its
deployment default. For an Agent of a policy-bound Team, the Agent Policy must
allow the model. If it does not, the Agent does not run. Its `PolicyResolved`
condition is then `False` with the reason `PolicyDenied`, and the message names
the model.

The operator pins the agent chart version at the first reconciliation. Refer
to [Pin and update a chart](#pin-and-update-a-chart).

`status.accessUrl` is the address of the Agent UI. For the readiness
conditions, refer to [Inspect an Agent](#inspect-an-agent). To sign in to the
Agent UI, the owner follows
[Open your Agent](../../getting-started/first-instance/#open-your-agent).

## Inspect an Agent

Replace `alice` with the Agent name. Replace `cetirun-alice` with the reported
namespace of the Agent. These commands examine cluster resources but do not
change them:

```bash
kubectl get agents.cetirun.microsoft.com alice
kubectl get agents.cetirun.microsoft.com alice -o jsonpath='{.status.conditions}{"\n"}'
kubectl get pods,pvc -n cetirun-alice
kubectl get statefulset openclaw-alice-cetirun -n cetirun-alice
kubectl get virtualservice cetirun-alice -n cetirun-alice
```

Before the controller sets overall `Ready`, it waits for the StatefulSet's
updated replicas, ready replicas, and observed generation. `Ready` does not
include ingress. Ingress creation is best-effort, so also examine
`IngressReady`, which confirms VirtualService reconciliation. A displayed
access URL or overall `Ready` is not the same as a successful request through
the ingress gateway. Examine TLS, sign-in, and model-provider access
separately.

Each agent type has its own startup, liveness, and readiness probes. For the
probe paths and ports, refer to
[Agent services](../troubleshooting/#agent-services). These are pod probes.
They do not examine Entra login, model entitlement, or tailnet reachability.

### Separate provisioning from model readiness

The creation of the platform and the creation of one Agent are different
operations. For a slow Agent, inspect the current Conditions before you extend a
wait or change timeouts. `PolicyDenied` is a validation failure, not a slow
start of the workload. For `DependencyUnavailable`, find if the reported
dependency is not provisioned yet, temporarily unreachable, or misconfigured. A
longer Agent startup-probe window does not repair a failed Agent Policy
validation or a missing configuration. Examine scheduling, image pulls, init
containers, and HOME binding separately from application probes.

If `PolicyResolved` is `False`, the operator holds a Sandbox Agent. Then
`Ready` and `SandboxReady` have the reason `SandboxHeld`, and their message ends
with the message of `PolicyResolved`. This applies when Sandbox execution is
on. A suspended Agent and a retained single-start lifetime can have other
reasons.

For GitHub Copilot, also inspect `status.aiGateway.copilot.phase` and `reason`.
Successful owner authorization, a Ready Copilot proxy, registered model routes,
and successful inference are different stages. `ProxyStarting` means that the
proxy is not ready, including an outage after routes were registered. Existing
routes and credentials are retained. Check the proxy Deployment and its Pod
events for scheduling, image-pull, or startup failures. Readiness checks do not
recreate a deleted Deployment; an administrator must restore it from the Agent's
current Helm release. Do not repeat owner authorization to repair the workload.
`SignInCompleting` instead means that the Portal is still completing a sign-in.
Overall Agent `Ready` does not prove that these model-provider stages are complete.

## Suspend and resume an Agent

In the Portal, owners select **Suspend Agent** or **Resume Agent**. These
controls change only the `spec.suspended` intent. They do not delete the
Agent, HOME, or AI Gateway key. They do not reset the spend history or create
another lifecycle operation.

Suspension keeps HOME and the AI Gateway spend, but it does **not** keep the
Copilot sign-in. The Operator removes the per-Agent Copilot token and
proxy-key Secrets during explicit suspension. After resume, the owner must sign
in to GitHub again. Do not use Suspend/Resume in place of an ordinary image or
chart update, because it does not keep the sign-in. Use the supported update and
reconciliation path. If a coordinated configuration change makes a suspension
necessary, include owner reauthorization in the maintenance plan.

| Current Agent | Suspend | Resume |
| --- | --- | --- |
| Standard OpenClaw, Hermes, or Custom Agents, and Sandbox Agents | Allowed when the current owned workload has a confirmed PVC-backed HOME, also during provisioning or recovery | Allowed when suspension is requested |
| Sandbox Agent held before bootstrap | Allowed without HOME storage when read-only checks confirm that no runtime namespace, lifetime receipts, grant journal, or execution history exists. A missing workload alone is not sufficient. The prerequisite failure stays available for diagnosis, and no runtime resources are created | Continues bootstrap with the current prerequisites |
| Ephemeral HOME, or workload storage that is unverified or absent | Unavailable. No retained suspension is accepted | A resume intent that already exists stays available |
| Pending restart, or a migration that a platform administrator requested | Suspension takes precedence | Continues the original operation and keeps its request ID and controller safeguards |
| Recorded legacy single-start Sandbox lifetime | Rejected. The lifetime is not converted | Rejected |
| Agent in deletion | Rejected. Deletion takes precedence | Rejected |
| Same requested intent on the same Agent UID | Observe without another write | Observe without another write |

### Owner suspension API

The owner endpoints are `POST /api/v1/instances/{name}/suspend` and
`POST /api/v1/instances/{name}/resume`. The equivalent endpoints for the
default Agent use `/api/v1/instance/suspend` and `/api/v1/instance/resume`.
For both actions, the JSON body must contain the current `uid` and
`resourceVersion`. The caller must be the owner, and the request must pass the
normal management origin checks.

The Portal also sends the `generation` captured when the confirmation dialog
opens in the optional `X-CetiRun-Agent-Generation` header. With this positive
integer, status-only updates do not invalidate the confirmation. A
configuration change advances the generation and requires a new confirmation
before changing the suspension intent.

Requests that omit the header retain the strict resource-version check. For
requests with the header, the API uses a conditional write. After one write
conflict, it reads the Agent again, checks ownership, generation, and
suspension eligibility again, and retries. The JSON body stays compatible with
older Portal replicas, which ignore the header and apply their strict
resource-version check during a rolling update.

They return HTTP `202` with the requested `suspended` value of the Agent, the
`suspendAvailable`/`resumeAvailable` capabilities, and the current controller
status. These capabilities show if the intent can be changed, not if compute
stopped or recovered. Restart stays unavailable during suspension or during a
pending restart or migration.

The Portal examines the effective HOME mount of the StatefulSet, not only the
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
the **original action, UID and version**, or observe the current Agent. The
Portal refreshes after every outcome and does not automatically resubmit. A
delayed acknowledgement does not replace an observation that was already
refreshed at the same generation. This is also true when the only changes are
status changes from the controller.

The Portal shows **Suspension requested** after it accepts the intent. This is
not an observation of Pod termination or access removal. In the same way, HTTP
acceptance of a resume is not readiness. Wait for the Ready condition of the
current generation and for the restart and migration completion checks.

A failed recovery stays visible in Status, and suspension stays available when
persistent HOME can be confirmed. The Portal does not advance migration phases
or give migration controls to owners. Use supported Portal, Operator, and CRD
versions that match.

### Suspend with kubectl

To suspend an Agent, set `spec.suspended` to `true`. The operator scales
the workload to zero. If the Agent has a GitHub Copilot proxy, the operator
also scales this proxy to zero. The operator removes the ingress route of the
Agent but keeps the persistent data.

```bash
kubectl patch agents.cetirun.microsoft.com alice --type=merge \
  -p '{"spec":{"suspended":true}}'
```

Make sure that the Agent shows `Suspended` and that the actual replica count of
the StatefulSet is zero. Also make sure that no workload pods are left and that
the VirtualService was removed. The suspension branch does not wait for pod
termination.

For standard OpenClaw and Hermes Agents, the controller enforces zero replicas
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

The agent chart allows only `replicaCount: 0` or `1`. Its HPA validation
accepts only `minReplicas=maxReplicas=1`. As a result, the HPA cannot scale an
Agent horizontally, and it is not a substitute for explicit suspension. There
is no automatic idle-suspend/resume policy in this controller.

To resume, set `suspended` to `false`. Reconciliation must restore both the
workload and routing before it gives access back to the user. Suspension does
not revoke a tailnet device.

## Restart an Agent

For a current, ready Agent, the owner selects **Restart Agent** in the Portal
and confirms the interruption. This requests a replacement Pod through the
controller. It does not delete the Agent, namespace, or HOME PVC. Persistent
files, the current AI Gateway key identity, and the spend history are kept.
Active process sessions are interrupted, and Pod-local temporary files are not
kept.

Suspension and deletion take precedence. Restart cannot resume a suspended
Agent. Current policy and runtime checks still apply. Earlier single-start
Sandbox lifetimes reject this operation and do not convert their records. A
Custom Sandbox Agent uses the common Sandbox restart path. Custom without
Sandbox selection keeps its approved shutdown and apply path.

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

### Owner restart API

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
replacement is ready. The Portal reports the desired `restartRequestId` and
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

## Planned node migration

Platform administrators can opt in to planned cold migration for Agents that
meet these conditions:

- The Agent is a Sandbox Agent of the OpenClaw, Hermes, or Custom agent type.
- The Agent has one filesystem CSI HOME that uses `ReadWriteOncePod` (RWOP) or
  `ReadWriteOnce` (RWO).
- The selected Kata or container platform registration explicitly supports
  that HOME mode.

This is a Kubernetes API, not a Portal migration button. The deployment must
enable `sandboxSources.plannedMigration` after all readers are upgraded. First,
qualify the actual AKS, runtime, and CSI artifacts in an authorized test
environment. Non-Sandbox Custom, retained single-start, other PVCs, unavailable
sources, and cross-cluster moves are outside the supported scope.

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
necessary. The Agent, namespace, PVC, PV, and AI Gateway spend identities do
not change. Process sessions and Pod-local scratch do not survive.

To recover from an uncertain response, you must read the current request. Do
not send another ID. In-progress requests cannot be overwritten. Retries with
the same ID do not repeat a move. After completion, a new ID can request the
next move.

Suspension keeps the request but does not start its destination. Resume
continues the request. Deletion destroys the Agent through normal cleanup,
never through migration failover. The Portal disables retained restart during
a pending migration.

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
withdrawn or unverified, the AI Gateway key that exists is blocked. This block
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
upgrades do not automatically advance that Agent's pinned chart. To update
it, set a reviewed version that is accessible in your configured OCI registry.

The owner's **Upgrade to Latest** action in the Portal selects the chart
version that is configured on the operator Deployment. It does not query a
registry for the newest release. An update to that version is separate from a
change to the operator itself.

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
approval checks. Agent Policy and lifecycle authorization still apply. An
operator upgrade alone does not change an Agent's pinned chart or image.

Treat model, chart, credential, and agent configuration changes as possibly
disruptive. Before you change users' Agents, review the backups and test
the selected configuration.

A change to a ConfigMap changes the pod-template checksum. On a full
reconcile, the operator also annotates the pod template with the resource
version of the gateway Secret. As a result, environment-backed credential
updates can restart the workload. It is possible that a direct Secret edit does
not trigger reconciliation. Use the supported update path.

## Remove the Microsoft Teams integration before a platform upgrade

CetiRun no longer includes the Microsoft Teams integration. Before you upgrade
an older Teams-enabled deployment, use the previous version to retire its
Azure Bot, application registration, and federated credentials. Keep the
resource inventory. The new Operator removes the Teams API fields and does not
do cloud cleanup. New runtime initializers remove saved Teams channel and
plugin references from HOME, but keep other configuration. They do not delete
cloud resources or conversation files.

## Update an Agent image

To select a new image, edit **Agent Image** in the Portal or update
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

The configured binding provider coordinates this handoff. The
Kubernetes provider scales the old StatefulSet to zero and waits for its HOME
consumers to exit. Then it allows the new image to be applied with one
replica. It does not use the built-in orphan-delete hook or replace the HOME
claim. Changes to the Model Key and to the context revision go through the
same gate. The process environment does not refresh in place.

The Operator chart enables the Custom agent type by default, with no approved
images. For a standard Custom Agent, the operator reports
`CustomImageNotApproved` while image approval is missing and
`CustomBindingsPending` while other binding prerequisites are unavailable. A
Custom Sandbox Agent instead must have a current common-executor registration
and an AI Gateway-qualified image approval.
Chat/headless Agents have no public Agent UI URL. A declared Web UI uses its
authenticated route. See
[Agent Harness](../../reference/agent-harness/#custom-runtime) for the
implementation boundary and offline qualification tests.

## Delete only with a recovery plan

The cleanup of a Custom Sandbox Agent uses the common bootstrap ownership and
grant journal. Non-Sandbox Custom must have its separate durable ownership
receipt. A never-bound Custom does not delete resources only because their
names match.

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

> **Warning:** Before you delete an Agent, make a backup of the necessary data
> and test recovery. Namespace deletion removes PVC objects. This includes claims
> retained by a StatefulSet. To bind a retained volume again, a separate
> recovery procedure is necessary.

Before you authorize deletion, examine the reclaim policy of the storage
backend. Do not remove finalizers only to hide a blocked cleanup. `Ready` with
the reason `PodsTerminating` shows that the deletion of a Sandbox Agent waits
for its Pods to terminate.

Sources: [Agent controller]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}),
[value builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}),
and [chart validation]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}).
