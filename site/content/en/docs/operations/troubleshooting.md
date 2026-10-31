---
title: "Troubleshooting"
description: "Find whether an instance is blocked by reconciliation, storage, routing, or an external integration."
weight: 40
---

Start with status and events, then inspect only the failing layer. These
commands contact Kubernetes but do not change it. Substitute the actual
instance name and its reported namespace; `alice-example` is synthetic.

```bash
kubectl get agents.cetirun.microsoft.com alice-example
kubectl get agents.cetirun.microsoft.com alice-example -o jsonpath='{.status.namespace}{"\n"}{.status.helmReleaseName}{"\n"}{.status.conditions}{"\n"}'
kubectl get statefulset,pods,pvc,service -n cetirun-alice-example
kubectl get events -n cetirun-alice-example --sort-by=.lastTimestamp
```

Resource naming uses `metadata.name`, not `spec.user.alias`. With the
controller/chart defaults, this example's namespace is `cetirun-alice-example`,
Helm release is `openclaw-alice-example`, and StatefulSet/Service is
`openclaw-alice-example-cetirun`. These API names are not interchangeable.
Prefer status and actual resource listings to guessing.

Sources: [naming and routing helpers]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [reconciler]({{< repo "app/cetirun-operator/internal/controller/agent_controller.go" >}}).

## Follow the first failure

| Observation | Inspect next |
| --- | --- |
| No status or repeated reconciliation errors | Operator rollout, logs, CRD version, and RBAC |
| `HelmDeployed` is false | OCI chart/version, operator registry credentials, values validation, admission/RBAC failures, upgrade hooks |
| PVC pending or mount failure | StorageClass, Blob CSI, subnet rules, mount events |
| Pod cannot schedule | RuntimeClass existence/scheduling, node taints, total resource requests and capacity |
| Image pull failure | Actual image reference/digest, node registry permissions, namespace-local pull Secret |
| Missing Secret key / container configuration error | Gateway Secret key names, agent type, controller enrichment; do not dump values |
| Init container fails | `init-home-data` permissions/mounts, OpenClaw `doctor`, or npm `init-skills` registry/package access |
| `HomeSeedStateUnknown` or `HomeSeedImageUnconfirmed` | Existing HOME and initialization records, interrupted-copy source image; do not clear the PVC or delete records to retry |
| Native configuration apply fails | JSON/YAML or environment-file syntax, destination permissions and symlinks; the initializer preserves failed files |
| `PodReady` is false | StatefulSet rollout generation, ready/updated replicas, probes, desktop dependencies |
| `IngressReady` is false | Instance VirtualService and shared gateway |
| Login loop or 403 | OAuth callback/TLS, groups, sidecar injection, ownership |
| Agent opens but model fails | AI Gateway conditions, Portal sign-in, selected model entitlement, DNS and egress; readiness does not test inference |

The controller can mark the overall instance `Ready` even when
VirtualService reconciliation failed. Read `IngressReady` and verify access
separately. Conversely, `HelmDeployed=True` means chart submission succeeded;
it does not wait for pod readiness. A suspended instance intentionally has
zero desired replicas and no VirtualService; inspect `spec.suspended` and
phase before treating absent pods as a failure.

For a missing `ValidatingAdmissionPolicy` API or a forbidden admission-policy
write, check the default `execRestriction` prerequisites and operator
ClusterRole. The chart does not conditionally omit that policy for older
clusters. Do not change cluster access policy just to make a render install.

For opt-in Sandbox reported-status alerts and offline `promtool` tests, see the
[standalone examples]({{< repo "app/cetirun-operator/observability/README.md" >}}).

## Routing

```bash
kubectl get virtualservice cetirun-alice-example -n cetirun-alice-example
kubectl get gateway.networking.istio.io cetirun-gateway -n aks-istio-ingress
kubectl get pods -n cetirun-system
kubectl get pods -n cetirun-istio
```

These commands use the umbrella's `cetirun-system` namespace. For a standalone
portal installation, check its configured namespace (`cetirun-portal` by default).

The shared gateway namespace and the per-instance VirtualService namespace are
different. The portal’s internal ownership service is part of the access path.
Do not disable OAuth2 Proxy, ownership validation, or network policy as a
diagnostic shortcut.

For login loops, confirm that Entra's redirect URI matches the rendered
`https://<domain>/oauth2/callback`, not an OAuth subdomain. Check `cetirun-tls`
coverage/expiry through the approved certificate workflow, federated trust,
allowed groups, and the `oauth2-proxy` extension provider in the active Istio
revision's shared ConfigMap. A Helm render cannot validate those dependencies.

For ownership 403s, check the instance owner, portal service address, actual
portal sidecar injection, and gateway-to-portal NetworkPolicy. The ownership
call goes to port 8081 with mTLS disabled; port 8080 uses strict mTLS. An admin
alias is not an exception to native instance ownership. Consult
[Istio operations](../istio/) for the distinct authentication and ownership
exemptions.

## Agent services

| Interface | Default |
| --- | --- |
| OpenClaw gateway | `18789` |
| Hermes API / native dashboard | `8642` / `9119` |
| Desktop noVNC / Chrome debugging | `6080` / `18800` |
| ttyd terminal | `7681` |

The chart defaults desktop, ttyd, and session logging to enabled. Keep debugging
and terminal endpoints behind the intended authenticated path.

OpenClaw startup probes use `/health`; liveness and readiness both use
`/healthz` on the gateway port. Hermes uses `/health` for all three on the API
port, not its dashboard. Inspect the actual StatefulSet because overrides and
sidecar probe handling can affect the effective pod configuration.
Sources: [OpenClaw probes]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}})
and [Hermes probes]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}).

Inspect only the failing container's logs privately. `logger` can contain
session content, not just diagnostic messages. The default admission policy
denies `kubectl exec` in the instance namespace except for a fixed service
account; do not propose exec or port-forward as a way around the deployment's
access controls. The portal terminal is the intended authenticated user path
when enabled and healthy.

## Before changing lifecycle state

A restart, Helm upgrade/rollback, suspension, or deletion changes the running
environment and needs authorization; none belongs in the read-only checks
above. Use [lifecycle guidance](../../guides/lifecycle/) for deliberate changes.
The runtime pre-upgrade/pre-rollback hook can recreate a StatefulSet when
immutable fields differ; it does not migrate PVC data. Deleting the CR deletes
its namespace, so a StatefulSet `Retain` value is not sufficient protection.
Do not delete PVCs or remove finalizers to conceal a failed reconciliation.

## External integrations

- [Copilot catalog](../../guides/models/#when-discovery-fails)
- [Tailscale](../../guides/tailscale/#if-it-does-not-connect)

## Share a useful report

Include the source revision, agent type, chart version, expected behavior,
redacted condition reasons, and a minimal reproduction. Exclude full session
logs, credentials, cloud IDs, user prompts, and persistent browser state.
For suspected vulnerabilities, use the repository’s existing
[security reporting instructions]({{< repo "SECURITY.md" >}}).
