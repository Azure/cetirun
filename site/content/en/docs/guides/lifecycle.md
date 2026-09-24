---
title: "Manage instance lifecycle"
description: "Check instance health, update a chart, and suspend, resume, or delete an instance."
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

These are pod probes, not checks of Entra login, model entitlement, Teams
delivery, or tailnet reachability. See the
[OpenClaw]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}})
probe templates.

## Suspend and resume

To suspend an instance, set `spec.suspended` to `true`. The operator scales the
workload to zero and removes its ingress route while retaining persistent data.

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

HOME seeding is separate from an image or configuration update. Once
`.cetirun-home-seeded` exists on the home PVC, a new image does not recopy its
HOME or update dependencies previously copied there. Managed configuration
continues to apply during Pod initialization.

Do not remove initialization records to force an upgrade. A nonempty home
without a recognized record fails safely; interrupted seeding requires the
same immutable source image. Review the recorded state, existing files and
recovery copies before performing explicit maintenance.

## Delete only with a recovery plan

Deleting an `Agent` invokes finalizer cleanup: it attempts Teams
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
