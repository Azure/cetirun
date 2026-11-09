---
title: "Troubleshooting"
description: "Find if reconciliation, storage, routing, or an external integration blocks an instance."
weight: 40
---

Start with the status and the events. Then examine only the layer that fails.
These commands connect to Kubernetes, but they do not change it. Replace the
example values with the actual instance name and its reported namespace. The
`alice-example` name is synthetic.

```bash
kubectl get agents.cetirun.microsoft.com alice-example
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.namespace}{"\n"}{.status.helmReleaseName}{"\n"}{.status.conditions}{"\n"}'
kubectl get statefulset,pods,pvc,service -n cetirun-alice-example
kubectl get events -n cetirun-alice-example --sort-by=.lastTimestamp
```

Resource names use `metadata.name`, not `spec.user.alias`. With the controller
and chart defaults, the namespace of this example is `cetirun-alice-example`.
Its Helm release is `openclaw-alice-example`, and its StatefulSet and Service
are both `openclaw-alice-example-cetirun`. You cannot use one of these API
names in place of another. We recommend that you use the status and the actual
resource lists, not guesses.

Sources: [name and routing helpers]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [reconciler]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}).

## Read governance status in the Portal

The **My Agents** and administrator **All agents** lists show a governance
cause next to the lifecycle phase. The Agent detail page shows the same cause
and a suggested next step on every tab. For example, an Agent can remain
`Pending` while the Portal shows **Blocked by policy** with `PolicyDenied` and
the policy's reason.

| Portal status | Next step |
| --- | --- |
| Blocked by policy | Review the Agent configuration against the Team's Agent Policy. Read the reported reason. |
| Identity owner binding required | Ask an administrator to verify the trusted owner binding for Agent Identity. |
| Binding or identity approval required | Ask an administrator to verify the Team binding and the Agent's identity or role approvals. |
| Identity not ready | Examine Agent Identity setup and the reported reason. |
| Platform support required | Verify platform support for the requested Sandbox controls. |
| Governance dependency unavailable | Examine the required policy, Sandbox, and platform dependencies. |
| Waiting for governance | Wait for the Operator to verify prerequisites. If the condition persists, examine the detailed conditions. |

`UnauthorizedBinding` can mean a binding or identity grant is not authorized.
It does not identify Entra approval as the only possible cause. The Portal
keeps that distinction and does not infer a specific identity failure from a
generic enforcement wait.

The summary requires an observation for the current Agent generation. Some
policy and Agent Identity conditions do not have their own observed generation.
For those conditions, the Portal checks the generation and matching state in
the corresponding policy or identity status. It omits older observations after
a configuration change and does not override deletion or suspension. Specific
causes take precedence over a generic enforcement wait. Full reported
conditions remain available under **Status**. The summary describes the latest
reported observation; it does not approve a grant or verify live enforcement.

## Follow the first failure

| Observation | Inspect next |
| --- | --- |
| No status or repeated reconciliation errors | Operator rollout, logs, CRD version, and RBAC |
| `HelmDeployed` is false | OCI chart and version, operator registry credentials, values validation, admission or RBAC failures, and upgrade hooks |
| PVC pending or mount failure | StorageClass, Blob CSI, subnet rules, and mount events |
| Pod cannot be scheduled | RuntimeClass existence and scheduling, node taints, and total resource requests and capacity |
| Image pull failure | Actual image reference or digest, node registry permissions, and the namespace-local pull Secret |
| Secret key does not exist, or container configuration error | Gateway Secret key names, agent type, and controller enrichment. Do not print the values. |
| Init container fails | `init-home-data` permissions and mounts, OpenClaw `doctor`, or npm `init-skills` registry and package access |
| `HomeSeedStateUnknown` or `HomeSeedImageUnconfirmed` | The HOME and initialization records that exist, and the source image of the interrupted copy. Do not clear the PVC or delete records to retry. |
| Native configuration apply fails | JSON/YAML or environment-file syntax, destination permissions and symlinks. The initializer keeps the files that failed. |
| `PodReady` is false | StatefulSet rollout generation, ready and updated replicas, probes, and desktop dependencies |
| `IngressReady` is false | Instance VirtualService and shared gateway |
| Login loop or 403 | OAuth callback and TLS, groups, sidecar injection, and ownership |
| Agent opens but model fails | AI Gateway conditions, Portal sign-in, selected model entitlement, DNS, and egress. Readiness does not test inference. |

The controller can mark the overall instance `Ready` even when the
VirtualService reconciliation failed. Read `IngressReady`. Then do a separate
check to make sure that access works. Also, `HelmDeployed=True` means that the
chart submission succeeded, but it does not wait for pod readiness. A suspended
instance intentionally has zero desired replicas and no VirtualService. Before
you decide that absent pods are a failure, examine `spec.suspended` and the
phase.

After a workload apply, readiness uses a current API read of the StatefulSet.
It does not use a pre-apply informer snapshot, which can be stale. The
generation, replica, restart, and migration checks that already exist use that
current object. If the current read fails, the failure is reported. A cached
Ready status is not used instead.

The Custom terminal also waits until its one current Deployment replica is
updated, ready, and available, and until no older surge replica is left. If
only the previous terminal Pod is available, the rollout is not complete.
These observations do not replace model/tool or provider acceptance.

The `ValidatingAdmissionPolicy` API can be absent, or an admission-policy write
can be forbidden. In both cases, examine the default `execRestriction`
prerequisites and the operator ClusterRole. The chart does not have a condition
that omits that policy for older clusters. Do not change the cluster access
policy only to make a render install.

For opt-in alerts about the reported Sandbox status and for offline `promtool`
tests, refer to the
[standalone examples]({{< repo "app/cetirun-operator/observability/README.md" >}}).

## Routing

```bash
kubectl get virtualservice cetirun-alice-example -n cetirun-alice-example
kubectl get gateway.networking.istio.io cetirun-gateway -n aks-istio-ingress
kubectl get pods -n cetirun-system
kubectl get pods -n cetirun-istio
```

These commands use the umbrella's `cetirun-system` namespace. For a standalone
portal installation, examine its configured namespace (`cetirun-portal` by
default).

The shared gateway namespace and the VirtualService namespace of each instance
are different. The internal ownership service of the portal is part of the
access path. Do not disable OAuth2 Proxy, ownership validation, or network
policy as a diagnostic shortcut.

For login loops, make sure that the Entra redirect URI matches the rendered
`https://<domain>/oauth2/callback`, not an OAuth subdomain. Also examine these
items:

- The `cetirun-tls` coverage and expiry, through the approved certificate
  workflow
- The federated trust
- The allowed groups
- The `oauth2-proxy` extension provider in the shared ConfigMap of the active
  Istio revision

A Helm render cannot validate those dependencies.

For ownership 403 errors, examine the instance owner and the portal service
address. Also examine the actual portal sidecar injection and the
gateway-to-portal NetworkPolicy. The ownership call goes to port 8081 with mTLS
disabled. Port 8080 uses strict mTLS. An admin alias is not an exception to
native instance ownership. For the separate authentication and ownership
exemptions, refer to [Istio operations](../istio/).

## Agent services

| Interface | Default |
| --- | --- |
| OpenClaw gateway | `18789` |
| Hermes API / native Webapp | `8642` / `9120` |
| Desktop noVNC / Chrome debugging | `6080` / `18800` |
| ttyd terminal | `7681` |

By default, the chart enables the desktop, ttyd, and session logging. Keep the
debug and terminal endpoints behind the intended authenticated path.

OpenClaw uses `/startupz`, `/healthz`, and `/readyz` for startup, liveness, and
readiness on its gateway port. Hermes checks `/health` on its API port; startup
and readiness also check Webapp health and built assets. Examine the actual StatefulSet,
because overrides and the way that sidecars handle probes can affect the
effective pod configuration.
Sources: [OpenClaw probes]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes probes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}).

Examine only the logs of the container that fails, and examine them privately.
`logger` can contain session content, not only diagnostic messages. The default
admission policy denies `kubectl exec` in the instance namespace, except for a
fixed service account. Do not propose exec or port-forward as a way around the
access controls of the deployment. When the portal terminal is enabled and
healthy, it is the intended authenticated user path.

### Hermes tools stop at 420 seconds

The pinned Hermes release has an outer tool deadline, in addition to the
timeout that the terminal tool requests. A larger tool request alone cannot
make an eight-minute foreground command fit in that default deadline. Use a
deliberately bounded per-command override, or the native timeout configuration
that already exists. The
[Hermes long-task guidance]({{< repo "app/cetirun-chart/agents/hermes/README.md" >}})
describes both. Keep the normal approval controls and a finite overall run
budget. An output file that is written later, a detached child, or a
successful CLI exit does not alone show a successful foreground-tool result.

## Before changing lifecycle state

A restart, a Helm upgrade or rollback, a suspension, or a deletion changes the
running environment. Authorization is necessary for each of these changes.
None of them belongs in the read-only checks above. For deliberate changes, use
the [lifecycle guidance](../../guides/lifecycle/).

If immutable fields are different, the runtime pre-upgrade/pre-rollback hook
can recreate a StatefulSet. The hook does not migrate PVC data. Deletion of the
CR also deletes its namespace. As a result, a StatefulSet `Retain` value is not
sufficient protection. Do not delete PVCs or remove finalizers to hide a failed
reconciliation.

## External integrations

- [Copilot catalog](../../guides/models/#when-discovery-fails)
- [Tailscale](../../guides/tailscale/#if-it-does-not-connect)

## Share a useful report

Include the source revision, agent type, chart version, expected behavior,
redacted condition reasons, and a minimal reproduction. Do not include full
session logs, credentials, cloud IDs, user prompts, or persistent browser
state. For suspected vulnerabilities, use the repository’s
[instructions for security reports]({{< repo "SECURITY.md" >}}).
