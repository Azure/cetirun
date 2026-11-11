---
title: "Architecture"
description: "How the portal, operator, and agent runtime work together on AKS."
weight: 10
---

CetiRun combines a self-service portal, a Kubernetes operator, and agent
runtimes on AKS. The portal handles user access. The operator manages instance
resources. Each runtime supplies its native agent interface.

## Control and access paths

```text
User → Istio gateway → OAuth2 Proxy / Entra ID → ownership check
                                                │
                     Portal UI + API ←───────────┘
                           │
                  Agent / Team
                           │
                     CetiRun operator
                           │
       instance namespace: Helm release + StatefulSet + PVC + routes
                           │
           OpenClaw or Hermes → configured model / tool / channel
```

The portal’s Go backend and React frontend supply self-service operations.
The operator watches cluster-scoped instance resources and referenced team
resources, and then reconciles Kubernetes objects. It manages instance
lifecycle. OpenClaw and Hermes supply their own agent loops and native
interfaces. Models and connected tools are separate services with their own
access rules.

Deployed Istio, OAuth2 Proxy, Entra configuration, DNS, and TLS are necessary
for this access path. The creation of an instance does not establish those
services. OAuth2 Proxy uses `https://<domain>/oauth2/callback` on the
deployment apex.

The gateway's ownership filter takes the instance name from `/ui/<instance>`
or the first path segment. Then it calls the portal's internal `/internal/auth`
listener. It does not compare a subdomain alias with the user. Public instance
APIs also do an ownership check. Keep the internal listener and backend
Services private.

The filter does not use `authPolicy.adminAliases`. For each prefix in
`authPolicy.aliasExemptPrefixes`, it skips the ownership check on the host
`<prefix>.<domain>`. Refer to [Exemptions](../../operations/istio/#exemptions).

Sources: [OAuth2 deployment]({{< repo "infra/istio/templates/ingress/oauth2-proxy-deployment.yaml" >}}),
[ownership filter]({{< repo "infra/istio/templates/ingress/envoy-filter-alias.yaml" >}}),
and [portal handlers]({{< repo "app/cetirun-portal/internal/handler/instance.go" >}}).

## Components and ownership

| Component | Source | Responsibility |
| --- | --- | --- |
| Platform chart | [app/cetirun-platform]({{< repo "app/cetirun-platform" "tree" >}}) | Install the operator and portal together. Shared Istio, credentials, agents, and agent data stay separate |
| Agent chart | [app/cetirun-chart]({{< repo "app/cetirun-chart" "tree" >}}) | StatefulSet, config, Services, PVCs, network policy, desktop and logger |
| Operator | [app/cetirun-operator]({{< repo "app/cetirun-operator" "tree" >}}) | Instance reconciliation, team-change propagation, instance Secrets, Helm, routes |
| Portal | [app/cetirun-portal]({{< repo "app/cetirun-portal" "tree" >}}) | User and admin UI, ownership enforcement in the backend |
| Shared Istio chart | [infra/istio]({{< repo "infra/istio" "tree" >}}) | Gateway, OAuth2 Proxy, authorization filters, egress logging/policy |
| Infrastructure | [infra]({{< repo "infra" "tree" >}}) | AKS and Azure Blob NFS Bicep |
| CetiAdmin | [Operator CetiAdmin module]({{< repo "app/cetirun-operator/internal/cetiadmin" "tree" >}}) | Hermes administration Agent, operations skills, and helpers deployed by the operator. Enabled by default. Refer to [CetiAdmin](../../operations/cetiadmin/) for its access |

## Instance names and routes

For a resource named `alice`, the controller uses the namespace
`cetirun-alice`, the Helm release `openclaw-alice`, and the chart-generated
Service/StatefulSet name `openclaw-alice-cetirun`. These names come from
`metadata.name`, not necessarily from the owner’s alias. Keep names short. Do
not override chart resource names, because the controller expects its
calculated Service and StatefulSet names.

The portal and instance routes share the deployment apex. For either agent,
`status.accessUrl` is `https://<domain>/ui/<instance>/`. It is a configured URL,
not proof that ingress works.

| External path | Destination at default ports |
| --- | --- |
| `/ui/<instance>/` | OpenClaw Control UI/gateway on 18789, or Hermes Webapp on 9120 |
| `/ui/<instance>/v1...` and `/ui/<instance>/health...` for Hermes | Hermes API on 8642, with the UI prefix removed |
| `/<instance>/term/` | ttyd on 7681 |
| `/<instance>/desktop/` | noVNC on 6080 |

OpenClaw keeps `/ui/<instance>` in its gateway requests. An exact WebSocket
upgrade at that path is routed without an HTTP redirect. Hermes Webapp
receives an `x-forwarded-prefix` header. After the platform sign-in, the Hermes
application login is also necessary. For this login, use `cetirun` as the
username and the instance's Hermes API / Webapp key as the password.
Neither routing rule establishes compatibility with every OpenAI-style
inference endpoint.

The instance VirtualService is in the instance namespace. It refers to the
operator's `istioGateway`, which is `aks-istio-ingress/cetirun-gateway` by
default. Configure the namespace of the gateway workload separately in
`cetirun.helmValues.networkPolicy.istioIngressNamespace`. The controller's
route ports do not come from arbitrary Helm port overrides. Keep the defaults
unless you validated the complete routing contract.

Sources: [route builder]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [route tests]({{< repo "app/cetirun-operator/internal/controller/istio_test.go" >}}).

## Runtime isolation

By default, the runtime chart selects `kata-vm-isolation`. A RuntimeClass that
works and compatible nodes are necessary for this selection. A rendered field
is not evidence of hardware isolation.

`execRestriction.enabled` is `true` by default and renders
`admissionregistration.k8s.io/v1` ValidatingAdmissionPolicy resources. The cluster must support and enforce that
API. This policy targets `pods/exec`, not the separate browser terminal. It
does not cover `pods/attach`, `pods/portforward`, or ephemeral containers.
Kubernetes RBAC alone controls these operations. Refer to
[Kubernetes exec restriction](../../security/#kubernetes-exec-restriction).

The chart allows zero or one runtime replica. If you enable its HPA, both
`minReplicas` and `maxReplicas` must be 1. This HPA configuration is not
multi-replica agent scaling. The operator supplies explicit suspension, not an
automatic idle/hibernation policy. Model selection does not implement a
centralized model gateway or provider entitlement enforcement.

Sources: [chart values]({{< repo "app/cetirun-chart/values.yaml" >}}),
[values schema]({{< repo "app/cetirun-chart/values.schema.json" >}}),
[chart validation]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
and [exec policy]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}}).

## A persistent home is not a backup

With persistence enabled, the runtime mounts its home volume at `/home/vibe`
for files, agent configuration, and credentials. The operator, standalone
chart, and Custom provider use these defaults: `100Gi`,
`ReadWriteOnce`, and the StorageClass named `default` (Azure Disk on AKS).
Shared skills can be mounted separately and read-only. Pod restarts can keep
that home. Namespace deletion removes PVC objects. Recoverability depends on
the storage reclaim behavior and tested backups.

Sources: [operator defaults]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}})
and [StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).

NetworkPolicy, Istio policy, and the configured RuntimeClass are separate
controls. A namespace alone is not a complete security boundary. Before you
select workload privileges or storage access, read [security](../../security/).
