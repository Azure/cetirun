---
title: "Helm configuration"
description: "Values and defaults for the agent, operator, and portal Helm charts."
weight: 20
---

There are separate charts for the agent, operator, portal, and shared Istio
infrastructure. Chart rendering does not prove that their images, storage,
RuntimeClass, or identity integrations are available.

## Offline rendering

From the repository root, with Helm already installed, this checks both agent
templates using synthetic models, credentials, and image repositories:

```bash
for agent in openclaw hermes; do
  helm template example-runtime app/cetirun-chart \
    --namespace cetirun-example --set-string agentType="$agent" \
    --set-string secrets.model=example-model \
    --set-string secrets.openclawGatewayToken=example-token \
    --set-string openclaw.image.repository=registry.example.com/openclaw-gateway \
    --set-string hermes.image.repository=registry.example.com/hermes-gateway \
    --set-string desktop.image.repository=registry.example.com/cetirun-desktop \
    > /dev/null
done
```

Do not deploy these placeholders. Local rendering does not pull images, query
existing Secrets, execute init containers or hooks, or verify cluster APIs.
Chart metadata `version: 0.1.0` and `appVersion: 0.1.0` are not evidence of
published artifacts. Connected admission/registry checks and mutating Helm
installation are separate steps in [platform setup](../../getting-started/platform/).

## Agent chart

Source: [app/cetirun-chart/values.yaml]({{< repo "app/cetirun-chart/values.yaml" >}}).
The following tables cover the main configuration surfaces. For exact nested
settings and validation, inspect that file and
[values.schema.json]({{< repo "app/cetirun-chart/values.schema.json" >}}).

### Runtime and images

| Value | Source default / meaning |
| --- | --- |
| `agentType` | `hermes`; also accepts explicit `openclaw` |
| `replicaCount` | `1`; schema accepts only `0` or `1` |
| `runtimeClassName` | `kata-vm-isolation` |
| `openclaw.image`, `hermes.image`, `desktop.image` | Repository, pull policy, tag, and digest per image |
| `imagePullSecrets` | `[]`; existing namespace-local Secrets for standalone Agent releases only |
| `nameOverride`, `fullnameOverride` | Empty |
| `serviceAccount.create` | `false` |
| `serviceAccount.name`, `serviceAccount.role` | Empty; review any token mount or ClusterRole binding |
| `azureWorkloadIdentity.enabled` | `false`; requires cluster and federated trust configuration |
| `azureWorkloadIdentity.clientId` | Empty |

For standalone Helm, Agent image pull policy is `Always`; empty tags resolve
from chart `appVersion`. Prefer the per-agent image objects over the generic
OpenClaw `image` alias, whose non-empty fields take precedence.

For operator-managed Agents, use a qualified `spec.agent.image` override or
leave it unset for the selected chart profile's default. Generic `image` and
selected-native image objects in deployment or instance Helm overrides are
rejected. The operator derives default Agent repositories from its chart
registry host and pins the resolved image on the Agent.

The desktop image remains a separate Helm setting. Its repository defaults
to `<chart-registry>/cetirun-desktop`; per-instance `desktop.image` overrides
remain available. Configure accessible artifacts and pin their versions:
repository defaults are not a public release guarantee.

Standalone Agent releases only reference `imagePullSecrets`; the chart does not
create or copy them. Operator-managed Agents instead use
[Portal-managed registry credentials](../agent-harness/#private-registries).
Raw `imagePullSecrets` in deployment or instance Helm overrides are rejected,
including empty lists; they are not appended to the managed pull binding. See
the [registry upgrade steps](../../getting-started/platform/#upgrading-runtime-registry-configuration)
before upgrading an installation that used this override. The Operator's
`acrDockerSecret` is for OCI chart pulls, not a runtime image pull Secret.

For operator-managed instances, leave chart naming overrides alone unless the
controller is changed with them: its readiness checks and routes expect the
standard StatefulSet/Service names. A standalone chart's `fullnameOverride`
is not an operator-wide naming override.

`runtimeClassName` selects an existing RuntimeClass; the chart creates no
sandbox runtime. Workload Identity requires a chart-created service account or
a pre-annotated existing one plus federated trust. A role, explicit service
account name, or Workload Identity enables service-account token automount in
the StatefulSet. Do not assume a named service account is token-free.

### Services and interfaces

| Value | Source default / meaning |
| --- | --- |
| `service.type` | `ClusterIP` |
| `service.port`, `service.targetPort` | `18789` for OpenClaw |
| `service.nodePort`, `service.annotations` | `null`, `{}` |
| `ingress.enabled` | `false`; operator creates Istio routes separately |
| `ingress.className`, `ingress.tls` | Empty, `[]` |
| `ingress.hosts`, `ingress.annotations` | Example host and WebSocket timeout annotations; set for your ingress controller |
| `ttyd.enabled`, `ttyd.port` | `true`, `7681` |
| `ttyd.shell`, `ttyd.basePath` | `/bin/zsh`, `/term` |
| `desktop.enabled` | `true`; Chrome debugging `18800`, noVNC `6080` |
| `desktop.screenResolution` | `1920x1080x24` |
| `desktop.logging` | `false` |
| `hermes.env.API_SERVER_PORT` | `8642` |
| `hermes.gateway.enabled` | `true` |

Both agents expose the CetiRun authentication helper on `18790`. Hermes also
exposes its native dashboard on `9119`; the operator routes native UI requests
to that dashboard, not the Hermes API. These ports are defined in
[agent helpers]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}})
and [controller routing]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}).
For operator-managed Agents, use `spec.agent.port`: the operator applies the
resolved primary port to the runtime and its primary Istio routes. Primary-port
overrides through `service.port`, `service.targetPort` or native Helm settings
are rejected. Standalone Helm still uses the settings in this table. Native
helper and dashboard ports remain part of the qualified integration profile.

The desktop runs Chrome, Xvfb, Fluxbox, x11vnc, and websockify in a separate
container. OpenClaw attaches to Chrome; the configured browser is headed and
no-sandbox. The gateway and desktop share the persistent home.

ttyd uses an origin check. Template validation rejects known port collisions;
the entrypoint validates the port and shell. Neither is a replacement for
authenticated ingress. Do not expose terminal, noVNC, or Chrome debugging ports
directly to an untrusted network.
Disabling the desktop also requires disabling OpenClaw browser `attachOnly`
or configuring a compatible browser path; template validation rejects
`desktop.enabled=false` with `attachOnly=true`.

### Resources and availability

| Value | Source default |
| --- | --- |
| `resources.requests` | CPU `4000m`, memory `8Gi` |
| `resources.limits` | CPU `8000m`, memory `16Gi` |
| `desktop.resources.requests` | CPU `500m`, memory `1Gi` |
| `desktop.resources.limits` | CPU `2000m`, memory `4Gi` |
| `nodeSelector`, `affinity` | `{}` |
| `tolerations`, `topologySpreadConstraints` | `[]` |
| `autoscaling.enabled` | `false` |
| `autoscaling.minReplicas`, `maxReplicas` | `1`, `3` |
| `autoscaling.targetCPUUtilizationPercentage` | `80` |
| `autoscaling.targetMemoryUtilizationPercentage` | `80` |
| `podDisruptionBudget.enabled`, `minAvailable` | `false`, `1` |

Although HPA defaults list `maxReplicas: 3`, enabling autoscaling with that value
fails rendering: [validation helpers]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}})
require **both** `minReplicas` and `maxReplicas` to be `1`. The schema rejects
`replicaCount` above `1`. These fields do not provide horizontal agent scaling.
Review desktop, logger, injected proxy, and init-container resources in addition
to the main agent; the default agent request alone is 4 CPU and 8 GiB.

### Persistence

| Value | Standalone chart default |
| --- | --- |
| `persistence.enabled` | `true` |
| `persistence.storageClass` | `default` (Azure Disk on AKS) |
| `persistence.accessMode` | `ReadWriteOnce` |
| `persistence.size` | `100Gi` |
| `persistence.existingClaim` | Empty |
| `persistence.annotations` | `{}` |
| `persistence.retainPolicy` | `Retain` |
| `persistence.useStatefulSetVolumeClaim` | `true` |

The operator and opt-in Custom provider use the same home defaults:
`default`, `ReadWriteOnce`, and `100Gi`. An empty `persistence.storageClass`
selects the cluster's default class instead of the class named `default`.
See [storage](../../operations/storage/) for shared skills and recovery.
Custom uses its approved HOME claim template rather than `persistence.*`
overrides. For standalone and built-in agents, an existing claim must be in
the runtime namespace.
Disabling persistence uses `emptyDir` unless an existing claim is supplied.
`Retain` controls StatefulSet claim retention, not namespace deletion or the
underlying PV's reclaim policy.

### Models and Secrets

| Value | Source default / meaning |
| --- | --- |
| `secrets.existingSecret` | Empty; otherwise use an existing Secret |
| `secrets.annotations` | `{}` |
| `secrets.provider` | `github_copilot`; also `openai`, `anthropic` |
| `secrets.model` | Required; no standalone default |
| `secrets.apiKey`, `secrets.apiBase`, `secrets.authMode` | Provider configuration; empty by default |
| `secrets.openclawGatewayToken` | Empty; required for standalone OpenClaw without `existingSecret` |
| `secrets.braveApiKey`, `secrets.perplexityApiKey` | Optional web search provider credentials |
| `secrets.msteamsAppId`, `msteamsAppPassword`, `msteamsTenantId` | Lower-level Teams chart settings |
| `msteamsWebhookPort`, `msteamsAuthType` | Top-level keys: `0` (off), empty |
| `msteamsAllowedUsers` | Top-level key: optional permitted Teams user IDs |

The three Teams credential keys are under `secrets`; the port, authentication
type, and permitted users are not. Use approved secret delivery, not real keys
in CLI flags. The operator manages an existing gateway Secret for its instances.

`existingSecret` skips [chart-managed Secret creation]({{< repo "app/cetirun-chart/templates/secret.yaml" >}}).
Values such as `secrets.apiKey` do not populate that external Secret. OpenClaw
needs `OPENCLAW_GATEWAY_TOKEN` and `CETIRUN_AUTH_KEY`; Hermes needs
`API_SERVER_KEY` and `CETIRUN_AUTH_KEY` plus its initialization environment.
The operator creates/enriches these runtime keys and Hermes `hermes.env`, but
does not authenticate the user to a model provider. With a standalone existing
Secret, provide the required keys yourself. A successful render does not check
their presence.

Hermes 0.21.5 requires an authentication provider when its Dashboard binds to
a non-loopback address. CetiRun enables Hermes' built-in password provider:
the username is `cetirun`, `API_SERVER_KEY` is the Dashboard password, and
`CETIRUN_AUTH_KEY` signs Dashboard sessions. The outer Istio OAuth2 Proxy still
controls access to the instance route; the Hermes login is an additional
application-level gate.

Chart-generated auth keys use live Secret lookup for reuse on upgrade. Offline
rendering cannot preserve live keys and must not be used to rotate them.
[Model selection](../../guides/models/) explains provider-specific precedence;
for OpenAI/Anthropic, configure both the provider's model and credentials.

### Agent configuration and health

`openclaw.config` supplies the native configuration. Browser support is enabled,
`headless` is false, the gateway attaches to the desktop CDP profile, and logging is `info`.
The chart enables the OpenClaw doctor init step through `openclaw.doctor.enabled`.
It runs `openclaw doctor --fix --non-interactive --yes` on the persistent home;
this is a startup mutation, not a read-only diagnostic. The `doctor.enabled`
alias must also be true for it to run. Hermes does not run this init container.
`openclaw.acpx` configures the installed agent communication plugin; review its
permissions, allowed adapters, and optional command overrides before use.

`hermes.config`, `hermes.env`, and `hermes.gateway` configure Hermes.
Model/provider values are rendered into the selected agent’s native format.

| Probe | Enabled | Initial delay | Period | Timeout | Failure threshold |
| --- | --- | --- | --- | --- | --- |
| Liveness | `true` | 30s | 180s | 180s | 3 |
| Readiness | `true` | 30s | 30s | 10s | 6 |
| Startup | `true` | 10s | 10s | 5s | 30 |

OpenClaw uses `/startupz`, `/healthz`, and `/readyz` for startup, liveness, and
readiness respectively, on the gateway port. Hermes uses `/health` for all three
probes on the API port. Neither verifies model entitlement or the native dashboard.
Sources: [OpenClaw helpers]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}}),
[Hermes helpers]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}), and
[StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).

### Network, logging, and extensions

| Value | Source default / meaning |
| --- | --- |
| `networkPolicy.enabled` | `true` |
| `networkPolicy.istioNamespace` | `istio-system`; operator sets `aks-istio-system` |
| `networkPolicy.istioIngressNamespace` | Falls back to Istio namespace; operator sets `aks-istio-ingress` |
| `networkPolicy.dnsNamespace` | `kube-system` |
| `networkPolicy.blockedCIDRs` | RFC 1918 ranges and Azure IMDS |
| `networkPolicy.allowedInternalCIDRs` | Deployment-specific exception list; review/replace before installation |
| `sidecarScope.enabled` | `true`; limits service discovery scope |
| `logger.enabled`, `logger.scanInterval` | `true`, 30 seconds |
| `skills.shared` | `false`; optional static Blob CSI PV/PVC |
| `skills.protocol`, `skills.mountPath` | `nfs`, `/home/vibe/.aks/skills` |
| `skills.secretName`, `skills.mountOptions` | Empty, `[]`; non-NFS protocols require a namespace-local staging Secret |
| `skills.npm`, `skills.clawHub` | Empty lists; ClawHub installer is not implemented |
| `skills.npmRegistry` | Environment-specific feed; explicitly select your approved reachable registry |
| `execRestriction.enabled` | `true`; renders a cluster-scoped policy denying `pods/exec` in this namespace |
| `tailscale.hostname`, `tailscale.serve` | `cetirun`, `false`; operator enables Serve for Tailscale instances |

NetworkPolicy allows gateway ingress, DNS, Istiod, approved internal exceptions,
and other external IPv4 traffic. Gateway ingress is selected by namespace, not
by an individual gateway pod. The configurable CIDR exceptions are additive
allow rules. It is not an HTTPS-only policy or an immutable block on every
private address.

`execRestriction` renders `admissionregistration.k8s.io/v1` policy and binding
resources without checking cluster API availability. Inspect its
[fixed service-account exception]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}})
and administrator access requirements before installation; it is not a
general cluster-admin exception. Do not disable it as a troubleshooting shortcut.

The [logger script]({{< repo "app/cetirun-chart/configs/session-logger.sh" >}})
tails recent OpenClaw session JSONL files or the Hermes gateway log to stdout.
It starts at the end of each file and does not replay history. Collection
requires a separate log pipeline; this sidecar is not a complete audit archive.

Extension surfaces include `extraEnv`, `extraEnvFrom`, `extraVolumeMounts`,
`extraVolumes`, `initContainers`, `sidecars`, `podAnnotations`, and security
contexts. Changes can expand privileges and data access. See
[security](../../security/) before using them.
In particular, the main container defaults to `readOnlyRootFilesystem: false`
and `allowPrivilegeEscalation: true`; do not describe all containers as
read-only or privilege-escalation-disabled.

## Platform chart

Source: [platform values]({{< repo "app/cetirun-platform/values.yaml" >}}).
The umbrella packages the operator, the portal and the AI Gateway. Their values
are nested under `cetirun-operator`, `cetirun-portal` and `cetirun-ai-gateway`;
runtime agent values remain under `cetirun-operator.cetirun.helmValues`.

| Value | Purpose / default |
| --- | --- |
| `istio.enabled` | `false`; must be `true` to acknowledge the separately installed Istio prerequisites, not to install Istio |
| `istio.oauth2Proxy.name`, `namespace` | `oauth2-proxy`, `cetirun-istio`; checked by the deployment helper |
| `cetirun-operator.enabled`, `cetirun-portal.enabled` | `true`; each component can be disabled |
| `cetirun-operator.namespace` | `cetirun-system`; align with the Helm release namespace, which the bundled AI Gateway requires |
| `cetirun-portal.aiGateway.catalogNamespace` | `cetirun-system`; must equal `cetirun-operator.namespace`, where the operator publishes the shared models. The deployment helper sets both from `NAMESPACE`; a standalone portal keeps its own value |
| `cetirun-ai-gateway.enabled` | `true`; turn it off only when a Gateway is installed separately in the operator namespace |
| `cetirun-ai-gateway.operatorIntegration` | `true`; required while the Gateway is installed. Reserves `copilot/` model names for the Agents' GitHub Copilot routes and checks router fallback targets against the caller's key. Set it on a separately installed Gateway too |
| `cetirun-ai-gateway.models` | `[]`; shared models every OpenClaw Agent can use, each with `model_info.access_groups: ["cetirun-shared"]` and, while the Gateway blocks unpriced models, a price. Empty is valid: OpenClaw Agents then use their owners' GitHub Copilot. Hermes is not routed through the Gateway |
| `cetirun-operator.aiGateway.enabled`, `cetirun-portal.features.aiGatewayEnabled` | `true`; required, the chart refuses to install with either off |
| `cetirun-operator.domainSuffix`, `cetirun-portal.istio.host` | Required deployment domain for enabled components |
| `cetirun-operator.istioGateway`, `cetirun-portal.istio.gateway` | Matching Gateway resource in `namespace/name` form |
| `cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` | Namespace containing the gateway workload allowed to reach agents |
| `cetirun-portal.istio.gatewayNamespace`, `gatewayServiceAccount` | Gateway workload identity used by portal access controls |

The chart does not own managed Istio, the independent CetiRun Istio release,
certificates, credentials, agent instances, or retained agent data. Setting
`istio.enabled` does not prove those prerequisites are running. The
[deployment helper]({{< repo "scripts/cetirun-platform-deploy.sh" >}})
performs connected checks and labels the namespace before installation.

For direct Helm use, build the locked local dependencies and label the target
namespace for the installed Istio revision separately. Configure the independent
Istio chart's ownership endpoint for the portal's actual namespace. Do not
install the umbrella over resources owned by standalone releases.
See [Install CetiRun](../../getting-started/platform/#platform-chart).

## Operator chart

Source: [operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}).

| Value | Purpose / default |
| --- | --- |
| `replicaCount`, `leaderElection` | `2`, `true` |
| `domainSuffix` | Required deployment domain |
| `namespace` | `cetirun-system`; used directly by operator resource templates |
| `istioRevision` | Empty for operator auto-detection |
| `istioGateway` | `aks-istio-ingress/cetirun-gateway`; used by agent VirtualServices |
| `cetirun.chart.repoURL`, `cetirun.chart.version` | Accessible OCI agent chart and version |
| `cetirun.defaultModel` | `gpt-6-astra`; select a model available to your users' Copilot accounts |
| `cetirun.helmValues` | Deployment-wide agent values |
| `acrDockerSecret` | Operator-namespace registry Secret for its Helm client |
| `image` | Operator image pin; `IfNotPresent`, empty tag falls back to `appVersion` |
| `imagePullSecrets` | Declared in values, but not rendered into the operator Deployment |
| `azure.*` | Optional Teams provisioning configuration |
| `sandboxSources.enabled` | `false`; opt in to read-only Kubernetes sources for held native Sandbox preparation |
| `sandboxSources.bootstrapOwnedPrerequisites` | `false`; additionally bootstrap owned namespace, operator access, HOME and native authentication for new namespaces only; requires explicit Agent chart/image/port pins |
| `sandboxSources.platformConfigMapName` | Empty; name of an existing operator-namespace ConfigMap containing versioned `platform.json` |
| `sandboxSources.homeClaimName`, `sandboxSources.skillsClaimName` | `home`, `skills`; existing claims stay read-only; bootstrap can create owned HOME only in new namespaces, while published skills stay external |
| `sandboxSources.helpers` | `doctor`, `logger`, `desktop` default to `false`; approved native helper selection |

Sandbox source wiring requires enabled, enforced configuration and Pod
admission. It passes `CETIRUN_SANDBOX_SOURCES` only to the operator and uses the
dedicated admission ServiceAccount. Sources are read-only by default and add no
RBAC permissions or capability registrations. With
`bootstrapOwnedPrerequisites: true`, the operator creates an owned protected
namespace, its operator Role/RoleBinding, HOME from the selected Sandbox and a
native authentication Secret. Durable receipts bind those resources to their
UIDs; existing namespace inputs stay read-only and are never adopted, resized or
replaced. The Agent must explicitly set the approved chart version, image and
port. Published skills and model credentials remain external prerequisites;
bootstrap does not substitute empty skills storage or authorize execution or
fencing. Tenant Helm values cannot select the source ConfigMap or promote a
different chart version.

These sources verify explicit platform declarations and existing resource
ownership through authoritative Kubernetes reads. **They do not enable
execution:** the stock binary still has no qualified writer-exclusion adapter,
so held staging stops at that gate. An empty Pod list or PVC access mode is not
fencing evidence. See the
[source and lifecycle contract]({{< repo "app/cetirun-operator/README.md" >}}#kubernetes-backed-sources-opt-in)
before enabling this option.

Node kubelet pull access and the operator’s OCI chart pull credentials are
different paths. One succeeding does not prove the other works. The
[operator Deployment]({{< repo "app/cetirun-operator/chart/templates/deployment.yaml" >}})
mounts `acrDockerSecret` for the Helm registry client but does not consume
`imagePullSecrets`; that value alone cannot fix an operator image pull failure.
Use a separately configured authorized image-pull path and verify the pod spec.

The deploy helper's `NAMESPACE` sets the Helm release namespace, whereas the
chart's `namespace` controls its workload, service account, and binding.
Keep them aligned; changing `--namespace` alone does not relocate those
resources. The operator chart manages all four CRDs under `templates/crds/`;
Helm upgrades them with the release. Their `helm.sh/resource-policy: keep`
annotation retains them on uninstall, not against schema downgrade.
`sandboxAdmission.validationMode` defaults to `compatible`; `strict` is an
explicit option that requires ordered schema/binary upgrades. Neither mode
disables authorization or changes the webhook failure policy. Review schema
and existing-resource ownership compatibility separately from the Helm upgrade.
The Portal's [Upgrade action](../../guides/lifecycle/#pin-and-update-a-chart) still
reads `cetirun-system/cetirun-operator`; a non-default operator namespace
requires reviewing that additional dependency.

Keep `istioGateway` aligned with the portal chart's `istio.gateway`. The
Gateway resource namespace can differ from its workload namespace: set
`cetirun.helmValues.networkPolicy.istioIngressNamespace` to the workload
namespace so agent NetworkPolicies allow gateway traffic.

`cetirun.helmValues` is a default layer, not an enforcement boundary. Team
settings and per-instance `spec.helm.values` can override it, and lists are
replaced rather than appended, except that platform-reserved inputs such as
`imagePullSecrets` are rejected at both levels. Copilot `spec.model` then takes precedence over
the deployment default. Each instance's `spec.helm.version` is pinned during
first reconciliation, so changing the operator's default chart version alone
does not upgrade existing instances.

## Portal chart

Source: [portal values]({{< repo "app/cetirun-portal/chart/values.yaml" >}}).

| Value | Purpose / default |
| --- | --- |
| `replicaCount` | `2` |
| `istio.host` | Deployment apex hostname |
| `istio.gateway` | `aks-istio-ingress/cetirun-gateway` |
| `istio.gatewayNamespace`, `istio.gatewayServiceAccount` | `aks-istio-ingress`, `aks-istio-ingressgateway-external`; used for access controls |
| `istio.sidecarInjection` | `true`; namespace must actually have injection enabled |
| `admins.aliases` | Explicit administrator aliases |
| `features.tailscaleEnabled` | `false` |
| `features.maxInstancesPerUser` | `1`; `0` means unlimited |
| `features.allowedAgentTypes` | Defaults to `hermes`, `openclaw`; also accepts opt-in `custom`. Checked by both the form and backend |
| `noticeBanner` | Set your deployment notice explicitly or clear it |
| `image`, `imagePullSecrets` | Portal image pin and pull access |

The backend enforces `features.allowedAgentTypes`, the Tailscale feature flag
and the per-user instance limit. Custom uses the existing Portal login and
instance-ownership checks; it does not require a separate admission grant.
Per-user and Team-specific type and image policy is deferred to AgentPolicy.
See [Portal self-service](../agent-harness/#portal-self-service) for the request
contract and configuration. Operator runtime qualifications and Sandbox
admission remain separate checks.

`istio.sidecarInjection: true` is only a chart validation assertion; it does
not label the namespace or inject a running pod. The deploy helper labels the
namespace; direct Helm users must arrange that separately. Port 8080 uses
strict mTLS, while internal ownership port 8081 disables it and depends on
the chart's NetworkPolicy. Both require the intended gateway namespace.

The portal service name comes from `fullnameOverride` or the chart name, not
the Helm release name. If you change that name or the namespace, align the
shared Istio chart's `portalAuth.clusterHost`. `admins.aliases` controls portal
administrative APIs, not access to every native instance UI. Set `noticeBanner`
to reviewed deployment text or clear it instead of inheriting the bundled copy.

For shared authentication settings, use [Istio operations](../../operations/istio/).
