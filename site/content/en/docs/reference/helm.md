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

On AKS Pod Sandboxing (`runtimeClassName: kata-vm-isolation`, the default),
`kubectl port-forward` cannot reach the agent's Pod, so the install notes print
no port-forward commands for that runtime class.

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

Agents that run the in-agent sign-in helper expose it on `18790`: Hermes off
the AI Gateway, and OpenClaw on charts before its per-instance Copilot proxy.
Agents on the Gateway sign in through the portal and expose no helper. Hermes
also
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
Setting `desktop.enabled=false` automatically defaults OpenClaw browser
`attachOnly` to `false` and `headless` to `true`, removing the dependency on the
desktop's CDP endpoint. With the desktop enabled, these defaults are `true` and `false`.
Explicit boolean values under `openclaw.config.browser` override these defaults;
omitted or null values use the desktop-dependent defaults. Template validation
still rejects an enabled browser with explicit `attachOnly=true` when the desktop
is disabled. Set `openclaw.config.browser.enabled=false` to disable browser use.
Browser automation without the desktop requires a compatible Chrome/Chromium
executable in the Agent image; disabling the desktop does not install one.

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

`openclaw.config` supplies the native configuration. Browser support is enabled
and logging is `info`. With the default desktop enabled, the gateway attaches to
its headed CDP browser; without the desktop, it defaults to headless browser mode.
The chart enables the OpenClaw doctor init step through `openclaw.doctor.enabled`.
It runs `openclaw doctor --fix --non-interactive --yes` on the persistent home;
this is a startup mutation, not a read-only diagnostic. The `doctor.enabled`
alias must also be true for it to run. Hermes does not run this init container.
For an OpenClaw instance on the AI Gateway, the `retire-copilot-sign-in` init
step follows doctor: once per HOME, it removes the GitHub Copilot sign-in that
the earlier in-agent helper left there, as [Model selection](../../guides/models/)
describes. A Hermes instance on the Gateway does the same in its
`init-home-data` step.
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
| `skills.protocol`, `skills.mountPath` | `nfs`, `/home/vibe/.aks/skills`; ordinary Agents expose this as a read-only alias to the composed view |
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
OpenClaw as packaged in the agent image keeps its sessions in SQLite and writes
no such files, so for OpenClaw the logger currently exports nothing.
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
| `cetirun-ai-gateway.operatorIntegration` | `true`; required while the Gateway is installed. Reserves `copilot/` model names, the form Team routers name GitHub Copilot models in, and checks router fallback targets against the caller's key. Set it on a separately installed Gateway too |
| `cetirun-ai-gateway.tpmReservation` | `false`; tokens-per-minute limits, such as an AgentPolicy's `rateLimits.tpm`, count the tokens of finished requests. `true` reserves each request's input plus its maximum output first, which refuses long OpenClaw conversations under a small limit. See [AgentPolicy rate limits](../crds/#agentpolicy-rate-limits) |
| `cetirun-ai-gateway.models` | `[]`; shared models for OpenClaw and Hermes Agents, each with a price while the Gateway blocks unpriced models. The chart gives each a deployment ID and an access group derived from its name: Agents without a policy are offered all of them, policy-bound Agents those their AgentPolicy grants. Editing a model's settings keeps its access group. Empty is valid: OpenClaw and Hermes Agents then use their owners' GitHub Copilot |
| `cetirun-ai-gateway.priceTable` | `remote`; LiteLLM downloads the current price table from GitHub when a Gateway pod starts and falls back to the copy in the image. `bundled` uses only the image's copy, for a cluster without internet access or prices pinned to the image. New models and price changes then arrive only with a new image: while the Gateway blocks unpriced models, a shared model missing from that copy needs a declared price, and a GitHub Copilot model newer than the image is not offered. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#known-gaps) |
| `cetirun-ai-gateway.requestLog.enabled` | `true`; one JSON line per model call on the Gateway's stdout, with the caller, model, tokens, spend and timing, for the cluster's log collection. It carries no request or response content |
| `cetirun-ai-gateway.spendLogs.enabled`, `spendLogs.retention` | `true` and `7d`; LiteLLM writes a row per call to `LiteLLM_SpendLogs`, which the LiteLLM console's request log pages read, without prompts or responses. Each Gateway pod deletes rows older than the retention every hour, the first time an hour after it starts, with the stored health check results. Spend, budgets and daily usage totals are kept beyond it. Turned off only together with the request log |
| `cetirun-ai-gateway.metrics.enabled` | `true` in the platform (the Gateway chart defaults to `false`); serves LiteLLM's Prometheus metrics on port 9090 of each Gateway pod (9091 when the Gateway itself listens on 9090), without authentication. `metrics.podMonitor.enabled`, also `true` in the platform, adds a PodMonitor for Azure Monitor managed Prometheus when the cluster serves its API |
| `cetirun-ai-gateway.router.routingStrategy` | `simple-shuffle`; how one of a model's deployments is chosen: also `least-busy`, `latency-based-routing`, `usage-based-routing-v2` or `cost-based-routing`. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#choosing-a-deployment) |
| `cetirun-ai-gateway.router.failover`, `router.continuationAffinity` | `true`; with several deployments behind one model name, a request that failed on one is sent once more to another (not a retry; `simple-shuffle` only), and a Responses API continuation goes to the deployment that stored the earlier response. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#routing) |
| `cetirun-ai-gateway.router.encryptedContentAffinity`, `router.deploymentAffinity` | `true`; keeps each Responses API conversation on the deployment that encrypted its reasoning, and each key on the deployment that served it last, for the provider's prompt cache. When that deployment fails, the conversation's requests fail with it until it is in cooldown or marked unhealthy by the health checks; only then does the conversation move, without its encrypted reasoning. Keep health checks on: a cooldown lasts five seconds. Deployment affinity is off for models whose deployments set `order`. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#encrypted-reasoning-stays-with-its-resource) |
| `cetirun-ai-gateway.router.healthChecks.enabled`, `router.healthChecks.intervalSeconds`, `router.healthChecks.concurrency` | `true`, `300` and `8`; probes every deployment, at most eight at a time, and routes only to those that answered. With Redis, one replica probes for all. Each Agent's GitHub Copilot routes are probed only with `cetirun-operator.aiGateway.copilotProxy.healthChecks.enabled` |
| `cetirun-ai-gateway.budgetExceededStatusCode` | `402`; the status of a request refused for an exhausted key or team budget. OpenClaw and Hermes stop and report exhausted credits; `422` makes them report a malformed request and `429` a rate limit they retry. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#when-a-budget-runs-out) |
| `cetirun-ai-gateway.podDisruptionBudget.enabled` | `true`; above one Gateway replica, a PodDisruptionBudget with `maxUnavailable: 1` so a node drain evicts one replica at a time |
| `cetirun-ai-gateway.router.fallbacks`, `router.contextWindowFallbacks` | `[]`; models to try when a model fails, or when a request is too long for it. Each target is checked against the Agent's key |
| `cetirun-operator.aiGateway.enabled`, `cetirun-portal.features.aiGatewayEnabled` | `true`; required, the chart refuses to install with either off |
| `cetirun-operator.aiGateway.copilotProxy.healthChecks.enabled` | `false`; the Gateway's health checks skip each Agent's GitHub Copilot routes, since every probe is a real call on the owner's Copilot allowance. `true` probes them like the other deployments, without retries: a route that shares a model's name with shared deployments then leaves rotation at the next probe when its proxy cannot be reached. A change registers each Agent's routes again within about ten minutes, without restarting Agents. See [Choose a model](../../guides/models/#models-through-the-ai-gateway) |
| `cetirun-operator.domainSuffix`, `cetirun-portal.istio.host` | Required deployment domain for enabled components |
| `cetirun-operator.istioGateway`, `cetirun-portal.istio.gateway` | Matching Gateway resource in `namespace/name` form |
| `cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` | Namespace containing the gateway workload allowed to reach agents |
| `cetirun-portal.istio.gatewayNamespace`, `gatewayServiceAccount` | Gateway workload identity used by portal access controls |
| `cetirun-operator.sandboxAdmission.portalServiceAccount` | `cetirun-portal` in the platform (the operator chart defaults to empty); with Sandbox admission enabled, adds the Portal's service account in the release namespace to the admission administrators, which the Portal's writes to policy-bound Agents need. Keep it equal to `cetirun-portal.serviceAccount.name`; clear it when the Portal is disabled. See the [platform chart README]({{< repo "app/cetirun-platform/README.md" >}}) |

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
| `sandboxAdmission.enabled`, `sandboxAdmission.podsEnabled` | `false`; run the Sandbox admission webhook for configuration and Agent writes, and for Sandbox Pods. Needs Kubernetes 1.37 or later, `tlsSecretName` and an administrator. See [Policy-bound Teams](../../operations/policy-teams/) |
| `sandboxAdmission.tlsSecretName` | Empty; an existing `kubernetes.io/tls` Secret for `cetirun-sandbox-admission.<namespace>.svc`. The chart issues no certificate |
| `sandboxAdmission.enforce`, `sandboxAdmission.caBundle` | `false` and empty; make the webhook's refusals binding, with the base64 PEM bundle of the CA that issued its certificate. Not on the first install: turn it on once the webhook answers |
| `sandboxAdmission.adminUsers`, `sandboxAdmission.adminGroups` | `[]`; the identities that may write policy-bound configuration directly and reauthorize Agents. At least one is required; blanket groups such as `system:authenticated` are refused |
| `sandboxSources.enabled` | `false`; opt in to Kubernetes capability declarations for configuration admission and sources for held native Sandbox preparation |
| `sandboxSources.nativeExecution` | `false`; select common OpenClaw/Hermes reconciliation for new lifetimes with owned bootstrap, enforced admission and the AI Gateway |
| `sandboxSources.plannedMigration` | `false`; permit new administrator-requested healthy-source moves with one CSI/RWOP HOME. Requires `nativeExecution` and upgraded CRD/Operator/admission/Portal readers; existing checkpoints and cleanup permissions remain when disabled |
| `sandboxSources.bootstrapOwnedPrerequisites` | `false`; additionally bootstrap owned namespace, operator access, HOME and native authentication for new namespaces only; requires explicit Agent chart/image/port pins |
| `sandboxSources.platformConfigMapName` | Empty; existing operator-namespace ConfigMap containing versioned `platform.json`, shared by configuration admission and the controller |
| `sandboxSources.homeClaimName`, `sandboxSources.skillsClaimName` | `home`, `skills`; existing claims stay read-only; bootstrap can create owned HOME only in new namespaces, while published skills stay external |
| `sandboxSources.skillsCatalogConfigMapName` | Empty; opt in to a separate operator-owned `skills.json` catalog binding exact Team identities to already-published Blob NFS content; requires owned bootstrap |
| `sandboxSources.helpers` | `doctor`, `logger`, `desktop` default to `false`; legacy held-preparation helper selection, not reconciled (`v2`) helper enablement |

Reconciled (`v2`) native lifetimes use the runtime chart's helper settings, not
`sandboxSources.helpers`. The chart defaults both `openclaw.doctor.enabled` and
its `doctor.enabled` alias to `true`, so OpenClaw runs doctor on each Pod
initialization even with `sandboxSources.helpers.doctor: false`. Hermes does not
run doctor.

For these lifetimes, `Sandbox.spec.resources.helpers` supplies the resources of
every init container and Agent sidecar, including doctor and the credential
retirement steps. The Portal's new-Sandbox form and the
[native example]({{< repo "examples/native-sandbox/README.md" >}}#helper-memory)
recommend a `2Gi` helper memory limit: `512Mi` can OOMKill OpenClaw initialization.
This is a starting recommendation, not an enforced minimum. Existing Sandboxes
keep their stored resource settings and require an explicit coordinated update.

Sandbox source wiring requires enabled, enforced configuration and Pod
admission. The chart uses the same `sandboxSources.platformConfigMapName` and
operator `namespace` for both processes, with no separate admission-source
values. `CETIRUN_SANDBOX_SOURCES` remains operator-only; admission receives the
selected platform namespace and ConfigMap name in `CETIRUN_SANDBOX_ADMISSION`.
These settings are omitted when sources are disabled. The dedicated admission
ServiceAccount reuses its existing namespace-scoped ConfigMap `get` Role and
RuntimeClass/StorageClass `get` permissions. Enabled sources grant the controller
`get`, `list`, `watch`, `create`, `update`, `patch` and `delete` on namespaced
`ciliumnetworkpolicies.cilium.io` for guarded staging of compiled policies.
These controller permissions are cluster-wide; staging still verifies exact
Agent ownership and namespace identities. Admission remains read-only, with no
cluster-wide Cilium policy or endpoint write permissions added.
Sources additionally grant controller-only `get` on Istio Gateways for exact
native-access input pinning. This is read-only access to shared routing
infrastructure, not Gateway mutation or automatic route/runtime activation.
The optional skills catalog adds only PersistentVolume `get` for its dependency
metadata; it adds no Secret lists or cluster-wide ConfigMap access.

Use one release version across the Operator, Sandbox admission, Portal and
configuration-change tools. Native execution selects the common flow without a
second rollout gate. The retired `sandboxSources.reconciledExecution` value is
rejected and must be removed. Persisted mode and ownership still govern existing
lifetimes; configuration changes do not migrate namespace or HOME state.
Mixed-version rollouts and pre-upgrade bootstrap-only recovery are not promised.
See [common Sandbox reconciliation]({{< repo "app/cetirun-operator/README.md" >}}#common-sandbox-reconciliation-explicit-opt-in).

Sources are read-only by default and do not create capability registrations. With
`bootstrapOwnedPrerequisites: true`, the operator creates an owned protected
namespace, its operator Role/RoleBinding, HOME from the selected Sandbox and a
native authentication Secret. Durable receipts bind those resources to their
UIDs; existing namespace inputs stay read-only and are never adopted, resized or
replaced. The Agent must explicitly set the approved chart version, image and
port. Published skills and model credentials remain external prerequisites;
bootstrap does not substitute empty skills storage or authorize execution or
fencing. Tenant Helm values cannot select the source ConfigMap or promote a
different chart version.

With `skillsCatalogConfigMapName` set, new catalog-backed bootstrap can create
owned static PV/PVC bindings to already-published read-only skills. The approval
must match the current Team name and UID; editable Team storage settings are not
authority. The operator records ownership before effects and never uploads
content, resolves storage credentials or changes the backing publication.
Bindings use `Retain` and exact UID/resourceVersion cleanup; existing external
claims and older bootstrap receipts remain in their existing mode. See the
[catalog and recovery contract]({{< repo "app/cetirun-operator/README.md" >}}#published-blob-nfs-skills-catalog-separate-opt-in).

These sources verify explicit platform declarations and existing resource
ownership through authoritative Kubernetes reads. Each relevant configuration
admission request checks the current inventory without a cross-request cache.
Missing, inaccessible, malformed or drifted inventory blocks governed
configuration writes that require capability checks, not authorized
cleanup/status operations, suspension-only updates or unrelated legacy writes.
An authorized suspension may only change `spec.suspended` from false (or
omitted) to true, preserving other spec fields and caller-controlled metadata.
Creation, resume, binding changes and reauthorization still require valid
inventory. Webhook matching and
outage behavior are unchanged: other matched writes still fail closed if the
admission service is unavailable. Source wiring does not grant Portal callers
Kubernetes admission authority or replace trusted Team assignments.

**These declarations do not qualify a platform or enable
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
`sandboxAdmission.placementConfigMapName` defaults to empty. Selecting an
operator-namespace placement registry requires enabled configuration and Pod
admission. The server serves the placement endpoint during bootstrap;
`enforce: true` also installs its cluster-wide checks for Pod writes, scheduling
bindings, and reserved Node updates/deletion. There is no namespace or Pod-label
exemption. Only read permissions for Nodes, DaemonSets and explicit platform
PriorityClasses are added; state reads reuse the operator-namespace ConfigMap
role. PriorityClass-derived Pod fields are verified against current class state.
The legacy core `bindings` endpoint is denied, and binding annotation overrides
cannot mutate a Pod while bypassing its normal validation.

This is a dedicated-pool pilot control, not an execution switch. Enable it only
after registering the complete current Node identity inventory and permitted
platform workloads. Unknown or replaced nodes cannot receive a binding, and
reserved-node DaemonSet replacements need explicit Pod-UID enrollment. An
unavailable registry or webhook blocks matching writes across the cluster;
ordinary Pod status and deletion remain outside those rules. Keep serving and
registry-repair resources on separately registered unreserved nodes. New serving
Pods are also gated; maintain working replicas and a stopped maintenance
recovery procedure rather than assuming a serving-workload bypass.

`sandboxSources.placementConfigMapName` separately opts the controller into
durable placement binding and must match the admission registry. The default
is empty; selecting it adds only Node `get` permission to the controller.
After zero-replica staging, the current reservation is bound to the exact
namespace/HOME/workload identities in the existing preparation receipt before
the held expectation is published. Missing enrollment leaves preparation
incomplete; changed identities require a separately qualified handoff, not
automatic reassignment. Cleanup retains the historical Node UID/provider-ID
tuple without depending on the live registry. This upgrades selected internal
receipts to `v2`, so keep compatible readers until their cleanup is complete.
It neither qualifies fencing nor enables execution.

`sandboxAdmission.validationMode` defaults to `compatible`; `strict` is an
explicit option that requires ordered schema/binary upgrades. Neither mode
disables authorization or changes the webhook failure policy. Review schema
and existing-resource ownership compatibility separately from the Helm upgrade.

`sandboxAdmission.coordinationConfigMapName` and `coordinationConfigMapUID`
default to empty, including when absent from older `--reuse-values` releases.
Explicit non-string values still fail validation. Set both to opt in to
coordinated Team, Sandbox and AgentPolicy updates/deletions against one existing, protected
authority in the operator namespace.
This requires `validationMode: strict` and enabled, enforced configuration and
Pod admission. The chart neither creates authority state nor grants write access
to admission; its existing namespace-scoped ConfigMap `get` permission is reused.
The state must be separate from platform, placement and skills ConfigMaps.

When selected, a primary-resource update needs an exact submitted coordination
reservation as well as normal configuration authorization. Administrator or
controller identity alone is not a bypass. Use the operator's explicit
`sandbox-config-change` command with pinned before/after inputs to submit or
observe an update. Admission performs only reads, including for dry runs;
missing, corrupt or replaced authority blocks matching updates.

Primary-resource deletion also requires the exact submitted reservation, original
UID/resourceVersion preconditions and background propagation, even for
unreferenced configurations and legacy Teams. Use `sandbox-config-change
--action delete --before FILE`, without `--after`; recover uncertain outcomes
with `--action observe-delete` and the unchanged original file. The reservation
covers the whole source, not only a Team class. Update tickets never authorize
deletion. Missing authority is not a deletion exemption. Upgrade operational
clients before enabling this option.

Deletion acceptance is observed when the exact original UID is terminating at
a new version or is absent. Delayed conditional requests cannot delete a
replacement. Explicit `delete` can then release the recorded reservation without
resubmission; read-only observation retains it. `deletion-accepted` does not
mean finalizers, dependents or physical cleanup have completed.
If that UID remains live at a newer resourceVersion, the old conditional Delete
cannot apply. `observe-delete` reports `deletion-superseded-reservation-retained`;
explicit recovery reports `deletion-superseded` without replaying the Delete.
Portal deletion can then authorize a new request using the administrator's
fresh UID/resourceVersion. An unchanged uncertain operation remains held.

An already-terminating configuration can remove existing finalizers through its
normal authorized update path without acquiring a new change ticket. This
read-only cleanup check requires the exact current object, no other content
changes, and no execution or change reservation using that configuration.
It cannot add finalizers, alter policy, release an outstanding operation or
proceed with missing authority. Dry runs leave both source and authority
unchanged. The ordinary updater continues to reject terminating objects.

This option covers configuration updates/deletions, not creation, Agent changes,
storage, Secrets or the other execution inputs. Existing status and narrow
garbage-collector cleanup exclusions remain unchanged. It is not the complete
source-write boundary needed for runtime activation and does not enable a
governed Agent. Preserve the execution hold until all participants and the
independently qualified runtime provider are integrated.

The Portal's [Upgrade action](../../guides/lifecycle/#pin-and-update-a-chart) still
reads `cetirun-system/cetirun-operator`; a non-default operator namespace
requires reviewing that additional dependency.

Keep `istioGateway` aligned with the portal chart's `istio.gateway`. The
Gateway resource namespace can differ from its workload namespace: set
`cetirun.helmValues.networkPolicy.istioIngressNamespace` to the workload
namespace so agent NetworkPolicies allow gateway traffic.
The governed native-access component instead resolves consumer namespaces from
the trusted platform ingress topology and pins their identities independently of
the Gateway resource. Its VirtualService keeps the configured Gateway reference
but exports to those enrolled ingress workload namespaces, not implicitly to the
Gateway resource namespace. These inputs do not qualify gateway authentication
or enable governed execution.

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
| `sandboxRuntimeProfiles` | Approved chart/image/port pins for built-in Sandbox creation; missing profiles reject creation unless controller defaults are explicitly enabled |
| `sandboxRuntimeDefaultsFromController` | `false`; permit missing profiles only after the Operator's common Sandbox executor is enabled |
| `noticeBanner` | Set your deployment notice explicitly or clear it |
| `image`, `imagePullSecrets` | Portal image pin and pull access |

The backend enforces `features.allowedAgentTypes`, the Tailscale feature flag
and the per-user instance limit. Custom uses the existing Portal login and
instance-ownership checks; it does not require a separate admission grant.
Per-user and Team-specific type and image policy is deferred to AgentPolicy.
See [Portal self-service](../agent-harness/#portal-self-service) for the request
contract and configuration. Operator runtime qualifications and Sandbox
admission remain separate checks.

Enable `sandboxRuntimeDefaultsFromController` only when the Operator uses
`sandboxSources.nativeExecution`; the platform chart rejects a mismatch.
Disable this Portal setting before disabling native execution.
Configured profiles and explicit image/port requests remain subject
to their existing validation and admission checks.

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
