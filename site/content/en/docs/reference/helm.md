---
title: "Helm configuration"
description: "This page gives the values and defaults for the agent, platform, AI Gateway, operator, portal, and Skill Marketplace Helm charts."
weight: 20
---

There are separate charts for the agent, the AI Gateway, the operator, the
portal, the Skill Marketplace, and shared Istio infrastructure. The platform
chart packages the operator, the portal, the AI Gateway, and the Skill
Marketplace. A chart render does not prove that their images, storage,
RuntimeClass, or identity integrations are available.

## Offline rendering

If Helm is already installed, this loop examines both agent templates from the
repository root. It uses synthetic models, credentials, and image repositories:

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

Do not deploy these placeholders. A local render does not pull images, query
Secrets that already exist, run init containers or hooks, or examine cluster
APIs. The chart metadata values `version: 0.1.0` and `appVersion: 0.1.0` are not
evidence of published artifacts. The connected admission and registry checks
and the Helm installation that mutates the cluster are separate steps in
[platform setup](../../getting-started/platform/).

## Agent chart

Source: [app/cetirun-chart/values.yaml]({{< repo "app/cetirun-chart/values.yaml" >}}).
The tables that follow cover the main configuration areas. For the exact nested
settings and validation, examine that file and
[values.schema.json]({{< repo "app/cetirun-chart/values.schema.json" >}}).

### Runtime and images

| Value | Source default / meaning |
| --- | --- |
| `agentType` | `hermes`. You can also set `openclaw` explicitly. |
| `replicaCount` | `1`. The schema accepts only `0` or `1`. |
| `runtimeClassName` | `kata-vm-isolation` |
| `openclaw.image`, `hermes.image`, `desktop.image` | The repository, pull policy, tag, and digest for each image |
| `imagePullSecrets` | `[]`. Namespace-local Secrets that already exist, for standalone Agent releases only |
| `nameOverride`, `fullnameOverride` | Empty |
| `serviceAccount.create` | `false` |
| `serviceAccount.name`, `serviceAccount.role` | Empty. Examine any token mount or ClusterRole binding. |
| `azureWorkloadIdentity.enabled` | `false`. Cluster and federated trust configuration is necessary. |
| `azureWorkloadIdentity.clientId` | Empty |

For standalone Helm, the Agent image pull policy is `Always`. Empty tags
resolve from the chart `appVersion`. We recommend that you use the per-agent
image objects, not the generic OpenClaw `image` alias. The non-empty fields of
this alias take precedence.

For operator-managed Agents, use a qualified `spec.agent.image` override, or
leave it unset. If it is unset, the default of the selected chart profile
applies. Generic `image` and selected-native image objects in deployment or
instance Helm overrides are rejected. The operator gets the default Agent
repositories from its chart registry host. It pins the resolved image on the
Agent.

The desktop image is still a separate Helm setting. Its repository defaults to
`<chart-registry>/cetirun-desktop`. Per-instance `desktop.image` overrides are
still available. Configure accessible artifacts and pin their versions. The
repository defaults are not a public release guarantee.

Standalone Agent releases only refer to `imagePullSecrets`. The chart does not
create or copy these Secrets. Operator-managed Agents use
[Portal-managed registry credentials](../agent-harness/#private-registries)
instead.

Raw `imagePullSecrets` in deployment or instance Helm overrides are rejected.
Even empty lists are rejected. These values are not appended to the managed
pull binding. Before you upgrade an installation that used this override, read
the [registry upgrade steps](../../getting-started/platform/#upgrading-runtime-registry-configuration).
The Operator's `acrDockerSecret` is for OCI chart pulls. It is not a pull Secret
for runtime images.

For operator-managed instances, do not change the chart name overrides unless
the controller is changed with them. The readiness checks and routes of the
controller expect the standard StatefulSet and Service names. The
`fullnameOverride` of a standalone chart is not an operator-wide name override.

`runtimeClassName` selects a RuntimeClass that already exists. The chart does
not create a sandbox runtime. Workload Identity must use federated trust. It
must also use a chart-created service account, or a pre-annotated service
account that already exists. A role, an explicit service account name, or
Workload Identity enables service-account token automount in the StatefulSet.
Do not expect a named service account to be token-free.

On AKS Pod Sandboxing (`runtimeClassName: kata-vm-isolation`, the default),
`kubectl port-forward` cannot reach the agent's Pod. As a result, the install
notes do not print port-forward commands for that runtime class.

### Services and interfaces

| Value | Source default / meaning |
| --- | --- |
| `service.type` | `ClusterIP` |
| `service.port`, `service.targetPort` | `18789` for OpenClaw |
| `service.nodePort`, `service.annotations` | `null`, `{}` |
| `ingress.enabled` | `false`. The operator creates Istio routes separately. |
| `ingress.className`, `ingress.tls` | Empty, `[]` |
| `ingress.hosts`, `ingress.annotations` | An example host and WebSocket timeout annotations. Set them for your ingress controller. |
| `ttyd.enabled`, `ttyd.port` | `true`, `7681` |
| `ttyd.shell`, `ttyd.basePath` | `/bin/zsh`, `/term` |
| `desktop.enabled` | `true`. The Chrome debug port is `18800`, and the noVNC port is `6080`. |
| `desktop.screenResolution` | `1920x1080x24` |
| `desktop.logging` | `false` |
| `hermes.env.API_SERVER_PORT` | `8642` |
| `hermes.gateway.enabled` | `true` |

Agents sign in to model sources through the Portal and the AI Gateway. Hermes
exposes its native Webapp on `9120`. The operator routes native UI requests
to the Webapp; native API paths use the configured API port, which defaults to
8642. These ports are defined in
[agent helpers]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}})
and [controller routing]({{< repo "app/cetirun-operator/internal/controller/helpers.go" >}}).

For operator-managed Agents, use `spec.agent.port`. The operator applies the
resolved primary port to the runtime and to its primary Istio routes.
Primary-port overrides through `service.port`, `service.targetPort`, or native
Helm settings are rejected. Standalone Helm still uses the settings in this
table. Native helper and Webapp ports are still part of the qualified
integration profile.

The desktop runs Chrome, Xvfb, Fluxbox, x11vnc, and websockify in a separate
container. OpenClaw attaches to Chrome. The configured browser is headed and
no-sandbox. The gateway and the desktop share the persistent home.

ttyd uses an origin check. Template validation rejects known port collisions.
The entrypoint validates the port and the shell. Neither is a replacement for
authenticated ingress. Do not expose terminal, noVNC, or Chrome debug ports
directly to an untrusted network.

If you set `desktop.enabled=false`, OpenClaw browser `attachOnly` automatically
defaults to `false`, and `headless` defaults to `true`. This removes the
dependency on the CDP endpoint of the desktop. When the desktop is enabled,
these defaults are `true` and `false`. Explicit boolean values under
`openclaw.config.browser` override these defaults. Omitted or null values use
the desktop-dependent defaults.

When the desktop is disabled, template validation still rejects an enabled
browser with an explicit `attachOnly=true`. To disable browser use, set
`openclaw.config.browser.enabled=false`. For browser automation without the
desktop, the Agent image must contain a compatible Chrome or Chromium
executable. If you disable the desktop, this does not install one.

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

The HPA defaults list `maxReplicas: 3`, but if you enable autoscaling with that
value, the render fails. In the
[validation helpers]({{< repo "app/cetirun-chart/templates/_helpers.tpl" >}}),
**both** `minReplicas` and `maxReplicas` must be `1`. The schema rejects
`replicaCount` above `1`. These fields do not give horizontal agent scaling.
Examine the desktop, logger, injected proxy, and init-container resources in
addition to the main agent. The default agent request alone is 4 CPU and 8 GiB.

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

The operator and the Custom provider use the same home defaults:
`default`, `ReadWriteOnce`, and `100Gi`. An empty `persistence.storageClass`
selects the default class of the cluster, not the class with the name
`default`. For shared skills and recovery, see [storage](../../operations/storage/).
Custom uses its approved HOME claim template, not `persistence.*` overrides.

For standalone and built-in agents, a claim that already exists must be in the
runtime namespace. If you disable persistence, `emptyDir` is used unless you
supply a claim that already exists. `Retain` controls StatefulSet claim
retention. It does not control namespace deletion or the reclaim policy of the
bound PV.

### Models and Secrets

| Value | Source default / meaning |
| --- | --- |
| `secrets.existingSecret` | Empty. Otherwise, use a Secret that already exists. |
| `secrets.annotations` | `{}` |
| `secrets.provider` | `openai`. This is the Gateway wire protocol. |
| `secrets.model` | Must be set. There is no standalone default. |
| `secrets.apiKey` | The removed direct credential input. It must stay empty. |
| `secrets.apiBase`, `secrets.authMode` | The Gateway endpoint and authentication that the operator selects |
| `aiGateway.enabled`, `aiGateway.keySecretName` | Gateway access is necessary. The Operator supplies the Agent virtual-key Secret. |
| `secrets.openclawGatewayToken` | Empty. Must be set for standalone OpenClaw without `existingSecret`. |
| `secrets.braveApiKey`, `secrets.perplexityApiKey` | Optional credentials for web search providers |

Use approved secret delivery. Do not put real keys in CLI flags. For its
instances, the operator manages a gateway Secret that already exists.

`existingSecret` skips [chart-managed Secret creation]({{< repo "app/cetirun-chart/templates/secret.yaml" >}}).
Direct provider keys are no longer accepted. OpenClaw must have
`OPENCLAW_GATEWAY_TOKEN` and `CETIRUN_AUTH_KEY`. Hermes must have
`API_SERVER_KEY` and `CETIRUN_AUTH_KEY`, and also its initialization
environment.

The operator creates these runtime keys and Hermes `hermes.env`, or adds to
them, but it does not authenticate the user to a model provider. If a
standalone release uses a Secret that already exists, supply the necessary keys
yourself. A successful render does not make sure that they are present.

Hermes 0.22.7 must have an authentication provider when its Webapp binds to
a non-loopback address. CetiRun enables Hermes' built-in password provider. The
username is `cetirun`, and `API_SERVER_KEY` is the Webapp password.
`CETIRUN_AUTH_KEY` independently signs Webapp sessions. Both keys are retained
across gateway restarts, Pod replacement and suspend/resume. The persistent
`$HERMES_HOME/webapp/session-token` is an internal browser credential and is
not the user login key. The outer Istio OAuth2 Proxy still
controls access to the instance route. The Hermes login is another gate, at the
application level.

Chart-generated auth keys use a live Secret lookup, so that an upgrade can
reuse them. An offline render cannot keep the live keys. Do not use an offline
render to rotate them. [Model selection](../../guides/models/) explains Gateway
model selection. Configure upstream providers and credentials in the AI
Gateway.

### Agent configuration and health

`openclaw.config` supplies the native configuration. Browser support is enabled,
and logging is `info`. When the desktop is enabled (the default), the gateway
attaches to its headed CDP browser. Without the desktop, the gateway defaults to
headless browser mode.

The chart enables the OpenClaw doctor init step through
`openclaw.doctor.enabled`. This step runs
`openclaw doctor --fix --non-interactive --yes` on the persistent home. This is
a startup mutation, not a read-only diagnostic. The `doctor.enabled` alias must
also be true for the step to run. Hermes does not run this init container.

`openclaw.acpx` configures the installed agent communication plugin. Before you
use it, examine its permissions, allowed adapters, and optional command
overrides.

`hermes.config`, `hermes.env`, and `hermes.gateway` configure Hermes. Model and
provider values are rendered into the native format of the selected agent.

| Probe | Enabled | Initial delay | Period | Timeout | Failure threshold |
| --- | --- | --- | --- | --- | --- |
| Liveness | `true` | 30s | 180s | 180s | 3 |
| Readiness | `true` | 30s | 30s | 10s | 6 |
| Startup | `true` | 10s | 10s | 5s | 30 |

OpenClaw uses these paths on the gateway port: `/startupz` for startup,
`/healthz` for liveness, and `/readyz` for readiness. Hermes checks `/health`
on the API port. Its startup and readiness probes also check the Webapp health
endpoint and built assets. These probes do not examine model entitlement or
complete a browser login.
Sources: [OpenClaw helpers]({{< repo "app/cetirun-chart/templates/agents/openclaw/_runtime.tpl" >}}),
[Hermes helpers]({{< repo "app/cetirun-chart/templates/agents/hermes/_runtime.tpl" >}}), and
[StatefulSet]({{< repo "app/cetirun-chart/templates/statefulset.yaml" >}}).

### Network, logging, and extensions

| Value | Source default / meaning |
| --- | --- |
| `networkPolicy.enabled` | `true` |
| `networkPolicy.istioNamespace` | `istio-system`. The operator sets `aks-istio-system`. |
| `networkPolicy.istioIngressNamespace` | Uses the Istio namespace as a fallback. The operator sets `aks-istio-ingress`. |
| `networkPolicy.dnsNamespace` | `kube-system` |
| `networkPolicy.blockedCIDRs` | RFC 1918 ranges and Azure IMDS |
| `networkPolicy.allowedInternalCIDRs` | A deployment-specific exception list. Examine or replace it before installation. |
| `sidecarScope.enabled` | `true`. It limits the scope of service discovery. |
| `logger.enabled`, `logger.scanInterval` | `true`, 30 seconds |
| `skills.shared` | `false`. An optional static Blob CSI PV and PVC. |
| `skills.protocol`, `skills.mountPath` | `nfs`, `/home/vibe/.aks/skills`. Native Agents mount shared skills directly, read-only. Custom exposes a read-only alias to its composed context view. |
| `skills.secretName`, `skills.mountOptions` | Empty, `[]`. Non-NFS protocols must have a namespace-local staging Secret. |
| `skills.npm`, `skills.clawHub` | Empty lists. The ClawHub installer is not implemented. |
| `skills.npmRegistry` | An environment-specific feed. Explicitly select your approved and reachable registry. |
| `execRestriction.enabled` | `true`. It renders a cluster-scoped policy that denies `pods/exec` in this namespace. |
| `tailscale.hostname`, `tailscale.serve` | `cetirun`, `false`. The operator enables Serve for Tailscale instances. |

NetworkPolicy allows gateway ingress, DNS, Istiod, approved internal exceptions,
and other external IPv4 traffic. Gateway ingress is selected by namespace, not
by an individual gateway pod. The configurable CIDR exceptions are additive
allow rules. It is not an HTTPS-only policy or an immutable block on every
private address.

`execRestriction` renders `admissionregistration.k8s.io/v1` policy and binding
resources. It does not examine the availability of the cluster API. Before
installation, examine its
[fixed service-account exception]({{< repo "app/cetirun-chart/templates/deny-exec-policy.yaml" >}})
and the necessary administrator access. This exception is not a general
cluster-admin exception. Do not disable the exec restriction as a
troubleshooting shortcut.

The [logger script]({{< repo "app/cetirun-chart/configs/session-logger.sh" >}})
tails recent OpenClaw session JSONL files or the Hermes gateway log to stdout.
In the agent image, OpenClaw keeps its sessions in SQLite and writes no such
files. As a result, the logger now exports nothing for OpenClaw. The logger
starts at the end of each file and does not replay history. For collection, a
separate log pipeline is necessary. This sidecar is not a complete audit
archive.

Extension surfaces include `extraEnv`, `extraEnvFrom`, `extraVolumeMounts`,
`extraVolumes`, `initContainers`, `sidecars`, `podAnnotations`, and security
contexts. Changes can expand privileges and data access. Before you use them,
see [security](../../security/). In particular, the main container defaults to
`readOnlyRootFilesystem: false` and `allowPrivilegeEscalation: true`. Do not
describe all containers as read-only or privilege-escalation-disabled.

## Platform chart

Source: [platform values]({{< repo "app/cetirun-platform/values.yaml" >}}).
The umbrella chart packages the operator, the portal, and the AI Gateway. Their
values are nested under `cetirun-operator`, `cetirun-portal`, and
`cetirun-ai-gateway`. The operator runs the optional Skill Marketplace. Runtime
agent values are still under `cetirun-operator.cetirun.helmValues`.

| Value | Purpose / default |
| --- | --- |
| `istio.enabled` | `false`. It must be `true` to acknowledge the separately installed Istio prerequisites. It does not install Istio. |
| `istio.oauth2Proxy.name`, `namespace` | `oauth2-proxy`, `cetirun-istio`. `make deploy` sets `namespace` to the namespace of the CetiRun Istio release. |
| `cetirun-operator.enabled`, `cetirun-portal.enabled` | `true`. You can disable each component. |
| `cetirun-operator.namespace` | `cetirun-system`. Align it with the Helm release namespace. This is necessary for the bundled AI Gateway. |
| `cetirun-portal.aiGateway.catalogNamespace` | `cetirun-system`. It must equal `cetirun-operator.namespace`, where the operator publishes the shared models. `make deploy` sets both from `platform.namespace`. A standalone portal keeps its own value. |
| `cetirun-ai-gateway.enabled` | `true`. Disable it only when a Gateway is installed separately in the operator namespace. `make deploy` refuses `false`. |
| `cetirun-ai-gateway.operatorIntegration` | `true`. It is necessary while the Gateway is installed. It reserves `copilot/` model names. Team routers use this name form for GitHub Copilot models. It also validates router fallback targets against the caller's key. Also set it on a Gateway that is installed separately for CetiRun. |
| `cetirun-ai-gateway.tpmReservation` | `false`. Tokens-per-minute limits, for example an AgentPolicy's `rateLimits.tpm`, count the tokens of finished requests. `true` first reserves the input of each request plus its maximum output. Under a small limit, this refuses long OpenClaw conversations. See [AgentPolicy rate limits](../crds/#agentpolicy-rate-limits). |
| `cetirun-ai-gateway.models` | `[]`. Shared models for OpenClaw and Hermes Agents. While the Gateway blocks unpriced models, each model has a price. The chart gives each model a deployment ID and an access group derived from its name. Agents without a policy are offered all of them. Policy-bound Agents are offered the models that their AgentPolicy grants. If you edit the settings of a model, it keeps its access group. An empty list is valid for a direct install of the chart. In that case, OpenClaw and Hermes Agents use their owners' GitHub Copilot. With `make deploy`, the models `auto` and `decision-1` are necessary. |
| `cetirun-ai-gateway.priceTable` | `remote`. When a Gateway pod starts, LiteLLM downloads the current price table from GitHub. It uses the copy in the image as a fallback. `bundled` uses only the copy in the image. This is for a cluster without internet access, or for prices pinned to the image. New models and price changes then arrive only with a new image. While the Gateway blocks unpriced models, these limits apply. A shared model that is not in that copy must have a declared price. A GitHub Copilot model that is newer than the image is not offered. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#known-gaps). |
| `cetirun-ai-gateway.requestLog.enabled` | `true`. One JSON line for each model call on the Gateway's stdout, for the cluster's log collection. The line has the caller, model, tokens, spend, and time data. It carries no request or response content. |
| `cetirun-ai-gateway.spendLogs.enabled`, `spendLogs.storePrompts`, `spendLogs.retention` | `true`, `true`, and `7d`. LiteLLM writes a row for each call to `LiteLLM_SpendLogs`. With `storePrompts`, the row also holds the prompt and the response of the call. Set `storePrompts: false` to keep only the metadata of the call. The request log pages of the LiteLLM console read this table. Every hour, each Gateway pod deletes rows older than the retention, together with the stored health check results. The first time is an hour after the pod starts. Spend, budgets, and daily usage totals are kept longer than the retention. It is disabled only together with the request log. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#litellms-spend-log). |
| `cetirun-ai-gateway.metrics.enabled` | `true` in the platform. The Gateway chart defaults to `false`. It serves LiteLLM's Prometheus metrics on port 9090 of each Gateway pod, without authentication. If the Gateway itself listens on 9090, it uses port 9091. `metrics.podMonitor.enabled` is also `true` in the platform. It adds a PodMonitor for Azure Monitor managed Prometheus when the cluster serves its API. |
| `cetirun-ai-gateway.router.routingStrategy` | `simple-shuffle`. This sets how one deployment of a model is selected. You can also use `least-busy`, `latency-based-routing`, `usage-based-routing-v2`, or `cost-based-routing`. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#choosing-a-deployment). |
| `cetirun-ai-gateway.router.failover`, `router.continuationAffinity` | `true`. If several deployments are behind one model name, a request that failed on one is sent one more time to another. This is not a retry, and it works with `simple-shuffle` only. In that case, a Responses API continuation goes to the deployment that stored the earlier response. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#routing). |
| `cetirun-ai-gateway.router.encryptedContentAffinity`, `router.deploymentAffinity` | `true`. It keeps each Responses API conversation on the deployment that encrypted its reasoning. It also keeps each key on the deployment that served it last. This is for the provider's prompt cache. When that deployment fails, the conversation's requests fail with it. This continues until the deployment is in cooldown or the health checks mark it unhealthy. The conversation moves only at that time, without its encrypted reasoning. Keep health checks enabled. A cooldown lasts five seconds. Deployment affinity is off for models with deployments that set `order`. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#encrypted-reasoning-stays-with-its-resource). |
| `cetirun-ai-gateway.router.healthChecks.enabled`, `router.healthChecks.intervalSeconds`, `router.healthChecks.concurrency` | `true`, `300`, and `8`. It probes every deployment, at most eight at a time, and routes only to the deployments that answered. With Redis, one replica probes for all. Each Agent's GitHub Copilot routes are probed only with `cetirun-operator.aiGateway.copilotProxy.healthChecks.enabled`. |
| `cetirun-ai-gateway.budgetExceededStatusCode` | `402`. The status of a request that is refused because a key or team budget is exhausted. With this status, OpenClaw and Hermes stop and report exhausted credits. `422` makes them report a malformed request. `429` makes them report a rate limit that they retry. See the [Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#when-a-budget-runs-out). |
| `cetirun-ai-gateway.podDisruptionBudget.enabled` | `true`. With more than one Gateway replica, a PodDisruptionBudget with `maxUnavailable: 1`, so that a node drain evicts one replica at a time. |
| `cetirun-ai-gateway.router.fallbacks`, `router.contextWindowFallbacks` | `[]`. The models to try when a model fails, or when a request is too long for it. Each target is validated against the Agent's key. |
| `cetirun-operator.aiGateway.enabled`, `cetirun-portal.features.aiGatewayEnabled` | `true`. Necessary. The chart refuses to install if either value is off. |
| `cetirun-operator.aiGateway.copilotProxy.healthChecks.enabled` | `false`. The Gateway's health checks skip each Agent's GitHub Copilot routes, because every probe is a real call on the owner's Copilot allowance. `true` probes them like the other deployments, without retries. Then, a route that shares a model's name with shared deployments leaves rotation at the next probe when its proxy cannot be reached. A change registers each Agent's routes again within about ten minutes, without a restart of the Agents. See [Choose a model](../../guides/models/#models-through-the-ai-gateway). |
| `cetirun-operator.domainSuffix`, `cetirun-portal.istio.host` | The deployment domain for enabled components. You must set it. |
| `cetirun-operator.istioGateway`, `cetirun-portal.istio.gateway` | The Gateway resource that matches, in `namespace/name` form |
| `cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` | The namespace that contains the gateway workload that is allowed to reach agents |
| `cetirun-portal.istio.gatewayNamespace`, `gatewayServiceAccount` | The Gateway workload identity that portal access controls use |
| `cetirun-operator.sandboxAdmission.portalServiceAccount` | `cetirun-portal` in the platform. The operator chart defaults to empty. When Sandbox admission is enabled, this value adds the Portal's service account in the release namespace to the admission administrators. The Portal must have this access to write to policy-bound Agents. Keep it equal to `cetirun-portal.serviceAccount.name`. If the Portal is disabled, clear it. See the [platform chart README]({{< repo "app/cetirun-platform/README.md" >}}). |
| `cetirun-operator.skillMarketplace.enabled`, `cetirun-portal.skillMarketplace.enabled` | `false`. Turn on both for the optional [Skill Marketplace](#skill-marketplace-values). The chart refuses one without the other while the bundled Operator and Portal are installed, and a Portal `skillMarketplace.catalogNamespace` other than `cetirun-operator.namespace`. `make deploy` sets these values from the `skillMarketplace` block of its environment file. |

The chart does not own managed Istio, the independent CetiRun Istio release,
certificates, credentials, agent instances, or agent data that is kept. If you
set `istio.enabled`, this does not prove that those prerequisites run. Before
it writes, `make deploy` does connected checks. It also creates and labels the
namespaces, and it installs the CetiRun Istio and Broker releases with the
platform. See [Install CetiRun](../../getting-started/platform/#platform-chart).

For direct Helm use, build the locked local dependencies. Also label the target
namespace for the installed Istio revision separately. Configure the ownership
endpoint of the independent Istio chart for the actual namespace of the portal.
Do not install the umbrella chart over resources that standalone releases own.
See the platform chart README's
[Use the component chart directly]({{< repo "app/cetirun-platform/README.md" >}}#use-the-component-chart-directly).

## AI Gateway chart

Source: [Gateway values]({{< repo "app/cetirun-ai-gateway/chart/values.yaml" >}}).
You can install the AI Gateway chart without the rest of CetiRun. The platform
chart installs it with the `cetirun-ai-gateway` values of
[Platform chart](#platform-chart). The
[Gateway chart README]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}})
describes each value and the measurements behind its default. These values
are important when you install the chart alone:

| Value | Source default |
| --- | --- |
| `global.registry`, `global.version` | Empty. With a registry, the image is `<registry>/cetirun-ai-gateway`. Without one, it is `image.repository`, `mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-ai-gateway`. The tag is `global.version`, then `image.tag`, then the `appVersion` of the chart. |
| `imagePullSecrets` | `[]`. Set it when the cluster cannot pull from the registry without credentials. |
| `replicaCount` | `2`. Redis or Valkey is necessary for more than one replica. The bundled Valkey supplies it. |
| `operatorIntegration` | `false`. The platform chart sets `true`. Keep `false` for a Gateway that does not serve CetiRun Agents. |
| `models` | `[]`. LiteLLM model entries. An empty list is valid, and models can also be added in the console. |
| `blockUnpricedModels` | `true`. The Gateway refuses a model that has no price. |
| `providerCredentials.existingSecret` | Empty. A Secret whose keys become environment variables of the Gateway, for `api_key: os.environ/<key>`. |
| `auth.masterKey`, `auth.saltKey`, `auth.adminUI.passwordSecret` | Each has `existingSecret`, empty, and `key`. Without a Secret, the chart generates the value and keeps it in its `-auth` Secret. Do not change the salt key after the installation. |
| `postgresql.bundled.enabled`, `postgresql.urlSecret.existingSecret` | `true`, empty. To use your own PostgreSQL, set `false` and a Secret with the `database-url` key. `make deploy` refuses `false`. |
| `postgresql.bundled.persistence.size`, `storageClass` | `100Gi`, empty for the default class of the cluster. Both are fixed at the installation. |
| `redis.bundled.enabled`, `redis.host`, `redis.passwordSecret.existingSecret` | `true`, empty, empty. To use your own Redis or Valkey, set `false` and a host. The connection does not use TLS. `make deploy` refuses `false`. |
| `service.type`, `service.port` | `ClusterIP`, `4000`. The chart creates no Ingress. |
| `resources`, `postgresql.bundled.resources`, `redis.bundled.resources` | `{}`. Nothing is reserved. See [Resources]({{< repo "app/cetirun-ai-gateway/chart/README.md" >}}#resources). |
| `metrics.enabled`, `metrics.podMonitor.enabled` | `false`, `false`. The platform chart sets both to `true`. |

The other values, for example `priceTable`, `spendLogs`, `router`, and
`budgetExceededStatusCode`, have the defaults that the `cetirun-ai-gateway`
rows of [Platform chart](#platform-chart) give.

## Operator chart

Source: [operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}).

| Value | Purpose / default |
| --- | --- |
| `replicaCount`, `leaderElection` | `2`, `true` |
| `domainSuffix` | The deployment domain. It is necessary. |
| `namespace` | `cetirun-system`. The operator resource templates use it directly. |
| `istioRevision` | Empty. The operator then detects the revision automatically. |
| `istioGateway` | `aks-istio-ingress/cetirun-gateway`. Agent VirtualServices use it. |
| `cetirun.chart.repoURL`, `cetirun.chart.version` | An accessible OCI agent chart and its version |
| `cetirun.defaultModel` | `gpt-6-astra`. Select a model that is available to the Copilot accounts of your users. |
| `cetirun.helmValues` | Deployment-wide agent values |
| `acrDockerSecret` | The registry Secret in the operator namespace, for the Helm client of the operator |
| `image` | The operator image pin. The pull policy is `IfNotPresent`. An empty tag uses `appVersion`. |
| `imagePullSecrets` | The values declare it, but the chart does not render it into the operator Deployment. |
| `azure.clientId` | Empty. The Workload Identity client ID in the annotation of the operator ServiceAccount. If it is empty and Agent Identity is enabled, the annotation uses `agentIdentity.blueprintClientId`. The Agent Identity lifecycle authenticates as the Blueprint in both cases. `make deploy` stops if it is empty. With `make deploy`, use the Blueprint client ID, unless another operator integration must use a different identity. |
| `sandboxAdmission.enabled`, `sandboxAdmission.podsEnabled` | `false`. They run the Sandbox admission webhook for configuration and Agent writes, and for Sandbox Pods. Kubernetes 1.37 or later, `tlsSecretName`, and an administrator are necessary. `make deploy` sets `enabled`. See [Policy-bound Teams](../../operations/policy-teams/) |
| `sandboxAdmission.tlsSecretName` | Empty. A `kubernetes.io/tls` Secret that already exists, for `cetirun-sandbox-admission.<namespace>.svc`. The chart issues no certificate. |
| `sandboxAdmission.enforce`, `sandboxAdmission.caBundle` | `false` and empty. They make the refusals of the webhook binding, with the base64 PEM bundle of the CA that issued its certificate. `make deploy` sets `enforce`, and `caBundle` is necessary for its first run. On a new installation, it starts the admission server without enforcement first. For a direct Helm install, do not enable enforcement on the first install. Enable it after the webhook answers. |
| `sandboxAdmission.adminUsers`, `sandboxAdmission.adminGroups` | `[]`. The identities that can write policy-bound configuration directly and reauthorize Agents. At least one identity is necessary. Blanket groups such as `system:authenticated` are refused. A later `make deploy` stops if they change. |
| `sandboxSources.enabled` | `false`. It opts in to Kubernetes capability declarations for configuration admission, and to sources for held native Sandbox preparation. |
| `sandboxSources.nativeExecution` | `false`. It selects the common OpenClaw/Hermes reconciliation for new lifetimes, with owned bootstrap, enforced admission, and the AI Gateway. |
| `sandboxSources.plannedMigration` | `false`. It allows new moves from a healthy source that an administrator requests, with one CSI/RWOP HOME. It must have `nativeExecution` and upgraded CRD/Operator/admission/Portal readers. When it is disabled, the current checkpoints and cleanup permissions stay. |
| `sandboxSources.bootstrapOwnedPrerequisites` | `false`. It also bootstraps the owned namespace, operator access, HOME, and native authentication, for new namespaces only. Explicit Agent chart/image/port pins are necessary. |
| `sandboxSources.platformConfigMapName` | Empty. A ConfigMap that already exists in the operator namespace and contains a versioned `platform.json`. Configuration admission and the controller share it. |
| `sandboxSources.homeClaimName`, `sandboxSources.skillsClaimName` | `home`, `skills`. Claims that already exist stay read-only. Bootstrap can create an owned HOME only in new namespaces. Published skills stay external. |
| `sandboxSources.skillsCatalogConfigMapName` | Empty. It opts in to a separate operator-owned `skills.json` catalog. This catalog binds exact Team identities to Blob NFS content that is already published. Owned bootstrap is necessary. |
| `sandboxSources.helpers` | `doctor`, `logger`, `desktop` default to `false`. The helper selection for legacy held preparation. It does not enable reconciled (`v2`) helpers. |
| `skillMarketplace.enabled` | `false`. It uses the [Skill Marketplace](../../operations/skill-marketplace/) for the Team skills of policy-bound Agents, through `CETIRUN_SKILL_MARKETPLACE`. Every skill entry without the `npm:` prefix then names a marketplace skill. The operator does not pass these entries to `skills.clawHub`, and it refuses an Agent whose `skillOverrides` add or remove one. The operator creates the skills claim of each common-executor Agent of a policy-bound Team. Until the claim exists, the Agent waits (`SkillsMaterializationPending`); this is not a configuration error. The value needs `sandboxSources.enabled`, `sandboxSources.nativeExecution`, `sandboxSources.skillsClaimName`, and an empty `sandboxSources.skillsCatalogConfigMapName`. The chart refuses other settings. A release that `--reuse-values` upgrades from a chart without this value keeps it off. The other values are in [Skill Marketplace values](#skill-marketplace-values). |

Reconciled (`v2`) native lifetimes use the helper settings of the runtime
chart, not `sandboxSources.helpers`. The chart sets both
`openclaw.doctor.enabled` and its `doctor.enabled` alias to `true` by default.
As a result, OpenClaw runs doctor at each Pod initialization, also with
`sandboxSources.helpers.doctor: false`. Hermes does not run doctor.

For these lifetimes, `Sandbox.spec.resources.helpers` supplies the resources of
every init container and Agent sidecar. This includes doctor and the credential
retirement steps.

The Portal's new-Sandbox form and the
[native example]({{< repo "examples/native-sandbox/README.md" >}}#helper-memory)
recommend a `2Gi` helper memory limit. A `512Mi` limit can cause an OOMKill of
the OpenClaw initialization. This value is an initial recommendation, not an
enforced minimum. Sandboxes that already exist keep their stored resource
settings. To change them, you must do an explicit coordinated update.

For Sandbox sources, configuration admission and Pod admission must be enabled
and enforced. The chart uses the same `sandboxSources.platformConfigMapName` and
operator `namespace` for both processes. There are no separate admission-source
values. `CETIRUN_SANDBOX_SOURCES` stays operator-only. Admission receives the
selected platform namespace and ConfigMap name in `CETIRUN_SANDBOX_ADMISSION`.
When sources are disabled, these settings are omitted.

The dedicated admission ServiceAccount keeps the namespace-scoped ConfigMap
`get` Role and the RuntimeClass/StorageClass `get` permissions that it already
has. When sources are enabled, they grant the controller `get`, `list`,
`watch`, `create`, `update`, `patch` and `delete` on namespaced
`ciliumnetworkpolicies.cilium.io`. These permissions are for the guarded
staging of compiled policies. These controller permissions are cluster-wide,
but staging still examines the exact Agent ownership and namespace identities.
Admission stays read-only. No write permissions for cluster-wide Cilium
policies or Cilium endpoints are added.

Sources also grant controller-only `get` on Istio Gateways. With this
permission, the controller can pin the exact native-access inputs. This is
read-only access to shared routing infrastructure. It does not change
Gateways, and it does not automatically activate routes or runtimes. The
optional skills catalog adds only PersistentVolume `get`, for its dependency
metadata. It adds no Secret lists and no cluster-wide ConfigMap access.

Use one release version for the Operator, Sandbox admission, Portal, and
configuration-change tools. Native execution selects the common flow without a
second rollout gate. The retired `sandboxSources.reconciledExecution` value is
rejected. You must remove it.

The persisted mode and ownership still control the lifetimes that already
exist. Configuration changes do not migrate namespace or HOME state.
Mixed-version rollouts and pre-upgrade bootstrap-only recovery are not
promised.
See [common Sandbox reconciliation]({{< repo "app/cetirun-operator/README.md" >}}#common-sandbox-reconciliation-explicit-opt-in).

By default, sources are read-only, and they do not create capability
registrations. With `bootstrapOwnedPrerequisites: true`, the operator creates
an owned protected namespace and its operator Role/RoleBinding. It also
creates HOME from the selected Sandbox, and a native authentication Secret.
Durable receipts bind those resources to their UIDs. Namespace inputs that
already exist stay read-only. They are never adopted, resized, or replaced.

The Agent must explicitly set the approved chart version, image, and port.
Published skills and model credentials stay external prerequisites. Bootstrap
does not use empty skills storage as a substitute. It does not authorize
execution or fencing. Tenant Helm values cannot select the source ConfigMap or
promote a different chart version.

When `skillsCatalogConfigMapName` is set, new catalog-backed bootstrap can
create owned static PV/PVC bindings to read-only skills that are already
published. The approval must match the current Team name and UID. Editable Team
storage settings are not authority. The operator records ownership before
effects occur. It never uploads content, resolves storage credentials, or
changes the publication that backs the skills.

Bindings use `Retain` and exact UID/resourceVersion cleanup. External
claims and older bootstrap receipts that already exist stay in their current
mode. See the
[catalog and recovery contract]({{< repo "app/cetirun-operator/README.md" >}}#published-blob-nfs-skills-catalog-separate-opt-in).

These sources use authoritative Kubernetes reads to examine explicit platform
declarations and the ownership of resources that already exist. Each
applicable configuration admission request examines the current inventory. It
does not use a cross-request cache. If the inventory is absent, inaccessible,
malformed, or drifted, it blocks governed configuration writes that must have
capability checks. It does not block authorized cleanup/status operations,
suspension-only updates, or unrelated legacy writes.

An authorized suspension can only change `spec.suspended` from false (or
omitted) to true. It keeps the other spec fields and the caller-controlled
metadata. Creation, resume, changes to bindings, and reauthorization still
must have a valid inventory.

Webhook match rules and outage behavior do not change. If the admission
service is unavailable, other matched writes still fail closed. The source
configuration does not give Portal callers Kubernetes admission authority. It
does not replace trusted Team assignments.

**These declarations do not qualify a platform or enable
execution:** the stock binary still has no qualified writer-exclusion adapter.
As a result, held staging stops at that gate. An empty Pod list or PVC access
mode is not fencing evidence. Before you enable this option, see the
[source and lifecycle contract]({{< repo "app/cetirun-operator/README.md" >}}#kubernetes-backed-sources-opt-in).

The pull access of the Node kubelet and the operator’s credentials to pull OCI
charts are different paths. If one path works, this does not prove that the
other path works. The
[operator Deployment]({{< repo "app/cetirun-operator/chart/templates/deployment.yaml" >}})
mounts `acrDockerSecret` for the Helm registry client. It does not use
`imagePullSecrets`. As a result, that value alone cannot fix an operator image
pull failure. Use a separately configured, authorized image-pull path, and
examine the pod spec.

The deploy helper's `NAMESPACE` sets the Helm release namespace. The chart's
`namespace` controls its workload, service account, and binding. Keep these
two values aligned. A change to `--namespace` alone does not move those
resources.

The operator chart manages all four CRDs under `templates/crds/`. Helm
upgrades them with the release. Their `helm.sh/resource-policy: keep`
annotation keeps them during an uninstall. It does not protect them against a
schema downgrade.

`sandboxAdmission.placementConfigMapName` defaults to empty. To select a
placement registry in the operator namespace, configuration admission and Pod
admission must be enabled. The server serves the placement endpoint during
bootstrap. `enforce: true` also installs its cluster-wide checks for Pod
writes, scheduler bindings, and updates/deletion of reserved Nodes. There is no
namespace or Pod-label exemption.

Only read permissions for Nodes, DaemonSets, and explicit platform
PriorityClasses are added. State reads use the same operator-namespace
ConfigMap role. Pod fields derived from a PriorityClass are compared with the
current class state. The legacy core `bindings` endpoint is denied. Annotation
overrides in a binding cannot change a Pod without its normal validation.

This is a dedicated-pool pilot control, not an execution switch. Before you
enable it, register the complete current Node identity inventory and the
platform workloads that are allowed. Unknown or replaced nodes cannot receive a
binding. DaemonSet replacements on reserved nodes must have explicit Pod-UID
enrollment.

If the registry or the webhook is unavailable, it blocks matched writes across
the cluster. Ordinary Pod status and deletion stay outside those rules. Keep
the resources that serve requests and the registry-repair resources on
unreserved nodes that are registered separately. New Pods that serve requests
are also gated. Keep replicas that operate correctly, and keep a stopped
maintenance recovery procedure. Do not plan for a bypass for the workloads that
serve requests.

`sandboxSources.placementConfigMapName` separately opts the controller into
durable placement binding. It must match the admission registry. The default
is empty. When it is set, it adds only Node `get` permission to the controller.

After zero-replica staging, the current reservation is bound to the exact
namespace/HOME/workload identities in the preparation receipt that already
exists. This occurs before the held expectation is published. If the
enrollment is absent, the preparation stays incomplete. Changed identities must
have a separately qualified handoff, not an automatic reassignment.

Cleanup keeps the historical Node UID/provider-ID tuple. It does not depend on
the live registry. This option upgrades selected internal receipts to `v2`.
Keep compatible readers until the cleanup of these receipts is complete. This
option does not qualify fencing, and it does not enable execution.

`sandboxAdmission.validationMode` defaults to `compatible`. `strict` is an
explicit option. For this mode, the schema/binary upgrades must occur in order.
The two modes do not disable authorization, and they do not change the webhook
failure policy. Separately from the Helm upgrade, examine the schema
compatibility and the ownership compatibility of resources that already exist.

`sandboxAdmission.coordinationConfigMapName` and `coordinationConfigMapUID`
default to empty. This is also true when older `--reuse-values` releases do not
have them. Explicit non-string values still fail validation. Set both values to
opt in to coordinated Team, Sandbox, and AgentPolicy updates/deletions. These
changes are coordinated against one protected authority that already exists in
the operator namespace.

For this option, `validationMode: strict` is necessary. Configuration
admission and Pod admission must also be enabled and enforced. The chart does
not create authority state, and it does not grant write access to admission.
Admission uses the same namespace-scoped ConfigMap `get` permission that it
already has. The state must be separate from the platform, placement, and
skills ConfigMaps.

When this option is selected, a primary-resource update must have an exact
submitted coordination reservation. It must also have the normal configuration
authorization. Administrator or controller identity alone is not a bypass. To
submit or observe an update, use the operator's explicit `sandbox-config-change`
command with pinned before/after inputs. Admission does only reads, also for
dry runs. If the authority is absent, corrupt, or replaced, it blocks matched
updates.

Primary-resource deletion also must have the exact submitted reservation, the
original UID/resourceVersion preconditions, and background propagation. This
also applies to unreferenced configurations and legacy Teams. To delete a
primary resource, use `sandbox-config-change --action delete --before FILE`,
without `--after`. To recover uncertain outcomes, use `--action observe-delete`
with the unchanged original file.

The reservation covers the whole source, not only a Team class. Update tickets
never authorize deletion. An absent authority does not exempt a deletion.
Before you enable this option, upgrade the operational clients.

Deletion acceptance is observed when the exact original UID is in termination
at a new version, or when it is absent. Delayed conditional requests cannot
delete a replacement. Then an explicit `delete` can release the recorded
reservation without a new submission. A read-only observation keeps the
reservation. `deletion-accepted` does not mean that finalizers, dependents, or
physical cleanup are complete.

If that UID stays live at a newer resourceVersion, the old conditional Delete
cannot apply. `observe-delete` reports
`deletion-superseded-reservation-retained`. Explicit recovery reports
`deletion-superseded`, and it does not send the Delete again. Then a Portal
deletion can authorize a new request that uses the administrator's fresh
UID/resourceVersion. An unchanged uncertain operation stays held.

If a configuration is already in termination, it can remove its current
finalizers through its normal authorized update path. A new change ticket is
not necessary for this. This read-only cleanup check must have the exact
current object. There must be no other content changes. Also, no execution or
change reservation can use that configuration.

The cleanup cannot add finalizers, change policy, or release an incomplete
operation. It cannot continue when the authority is absent. Dry runs do not
change the source or the authority. The ordinary updater still rejects objects
in termination.

This option covers configuration updates/deletions. It does not cover creation,
Agent changes, storage, Secrets, or the other execution inputs. The status
exclusions and the narrow garbage-collector cleanup exclusions that already
exist do not change. This option is not the complete source-write boundary
that is necessary for runtime activation. It does not enable a governed Agent.
Keep the execution hold until all participants and the independently qualified
runtime provider are integrated.

The Portal's
[Upgrade action](../../guides/lifecycle/#pin-and-update-a-chart) still reads
`cetirun-system/cetirun-operator`. If you use a non-default operator namespace,
also examine that dependency.

Keep `istioGateway` aligned with the portal chart's `istio.gateway`. The
namespace of the Gateway resource can be different from its workload namespace.
Set `cetirun.helmValues.networkPolicy.istioIngressNamespace` to the workload
namespace, so that agent NetworkPolicies allow gateway traffic.

Instead, the governed native-access component resolves consumer namespaces
from the trusted platform ingress topology. It pins their identities
independently of the Gateway resource. Its VirtualService keeps the configured
Gateway reference. But it exports to the enrolled namespaces of those ingress
workloads, not implicitly to the namespace of the Gateway resource. These
inputs do not qualify gateway authentication, and they do not enable governed
execution.

`cetirun.helmValues` is a default layer, not an enforcement boundary. Team
settings and per-instance `spec.helm.values` can override it. Lists are
replaced, not appended. But platform-reserved inputs such as `imagePullSecrets`
are rejected at both levels. Then Copilot `spec.model` has priority over the
deployment default.

Each instance's `spec.helm.version` is pinned during the first reconciliation.
As a result, a change to the operator's default chart version alone does not
upgrade the instances that already exist.

## Portal chart

Source: [portal values]({{< repo "app/cetirun-portal/chart/values.yaml" >}}).

| Value | Purpose / default |
| --- | --- |
| `replicaCount` | `2` |
| `istio.host` | Deployment apex hostname |
| `istio.gateway` | `aks-istio-ingress/cetirun-gateway` |
| `istio.gatewayNamespace`, `istio.gatewayServiceAccount` | `aks-istio-ingress`, `aks-istio-ingressgateway-external`. The access controls use them. |
| `istio.sidecarInjection` | `true`. Injection must be enabled on the namespace. |
| `admins.aliases` | Explicit administrator aliases |
| `features.tailscaleEnabled` | `false` |
| `features.maxInstancesPerUser` | `1`. `0` means unlimited. |
| `features.allowedAgentTypes` | Defaults to `hermes`, `openclaw`, `custom`. Remove a type to disable its creation. The form and the backend both examine it. |
| `sandboxRuntimeProfiles` | Approved chart/image/port pins for built-in Sandbox creation. If a profile is absent, creation is rejected, unless controller defaults are explicitly enabled. |
| `sandboxRuntimeDefaultsFromController` | `false`. Allow absent profiles only after the Operator's common Sandbox executor is enabled. |
| `skillMarketplace.enabled`, `skillMarketplace.catalogNamespace` | `false`, `cetirun-system`. When enabled, Team skills come from the Skill Marketplace catalog in the operator namespace. Team saves examine new skills against the catalog, Policy saves keep the skills that a Team still selects, and the Agent page shows the Team's skills. It also adds the **Skills** administration page and `get` access to the `cetirun-skill-marketplace` ConfigMap. Keep it equal to the operator's `skillMarketplace.enabled`. See [Skill Marketplace administration](../crds/#skill-marketplace-administration). |
| `noticeBanner` | `""` (no banner). Set it to show a deployment notice. |
| `image`, `imagePullSecrets` | Portal image pin and pull access |

The backend enforces `features.allowedAgentTypes`, the Tailscale feature flag,
and the per-user instance limit. Custom uses the current Portal login and
instance-ownership checks. A separate admission grant is not necessary for
Custom. Per-user and Team-specific type and image policy is deferred to
AgentPolicy. For the request contract and configuration, see
[Portal self-service](../agent-harness/#portal-self-service). Operator runtime
qualifications and Sandbox admission stay separate checks.

Enable `sandboxRuntimeDefaultsFromController` only when the Operator uses
`sandboxSources.nativeExecution`. The platform chart rejects a mismatch.
Disable this Portal setting before you disable native execution. Configured
profiles and explicit image/port requests are still subject to their current
validation and admission checks.

`istio.sidecarInjection: true` is only a chart validation assertion. It does
not label the namespace, and it does not inject a pod that runs. The deploy
helper labels the namespace. Direct Helm users must do that separately.

Port 8080 uses strict mTLS. The internal ownership port 8081 disables strict
mTLS and depends on the chart's NetworkPolicy. Both ports must have the
intended gateway namespace.

The portal service name comes from `fullnameOverride` or the chart name, not
from the Helm release name. If you change that name or the namespace, align the
shared Istio chart's `portalAuth.clusterHost`. `admins.aliases` controls the
portal administrative APIs. It does not control access to every native instance
UI. By default, `noticeBanner` is empty. When a notice is necessary, set it to
deployment text that you reviewed.

For shared authentication settings, use [Istio operations](../../operations/istio/).

## Skill Marketplace values

Source: [operator values]({{< repo "app/cetirun-operator/chart/values.yaml" >}}).
The operator chart runs the Skill Marketplace with the values below
`skillMarketplace`. In the platform chart, they are below
`cetirun-operator.skillMarketplace`. `make deploy` sets most of them from the
`skillMarketplace` block of the environment file. For the procedure, see
[Skill Marketplace](../../operations/skill-marketplace/).

| Value | Purpose / default |
| --- | --- |
| `enabled` | `false`. See the [operator values](#operator-chart). |
| `repository.url` | Necessary. An `https://` URL, or with `auth: ssh` an `ssh://` URL or the `user@host:path` form. The chart refuses an `https://` URL with credentials, and a URL with a query or a fragment. It also refuses an `http://` URL, unless `allowInsecureHTTP` is `true`. |
| `repository.ref` | Empty. A branch, a tag, or a full ref. Empty uses the branch that the remote HEAD names. |
| `repository.path` | Empty. The directory of the skills, relative to the repository root. Empty is the root. |
| `repository.syncInterval` | `5m`. How often the worker examines the remote. At least `30s`. |
| `repository.auth` | `none`. Also `token` or `ssh`. |
| `repository.existingSecret` | Empty. Necessary for `token` and `ssh`. A Secret in the operator namespace with the key `token`, or with the keys `ssh-privatekey` and `known_hosts`. |
| `repository.tokenUsername` | `x-access-token`. The user name that the worker sends with the token. |
| `repository.allowInsecureHTTP` | `false`. `true` allows an `http://` URL. Use it only for tests in a private network. |
| `storage.resourceGroup`, `storage.storageAccount`, `storage.containerName` | Necessary. The Blob NFS container of the Team releases. The account name has 3 to 24 lowercase letters and digits. |
| `storage.storageClassName` | `azureblob-nfs-premium`. The StorageClass of the worker's volume and of the Agents' volumes. |
| `storage.size` | `100Gi`. The capacity of the worker's own read-write volume and claim. The storage values are part of their names: when you change a storage value, the worker gets a new volume and claim, and Helm removes the earlier ones. The container keeps its data. See [Move to other storage](../../operations/skill-marketplace/#move-to-other-storage). |
| `reservedNames` | `cetirun-memory`, `connected-services`. Skill names that CetiRun or its images supply. The worker rejects repository skills with these names. |
| `teamGCGrace` | `1h`. How long the worker keeps the releases of a Team that left the policy-bound Teams, after it found the Team gone. A restart of the worker starts the time again. |
| `limits.maxRepositorySize`, `limits.maxRepositoryObjects`, `limits.maxSkillFiles`, `limits.maxSkillSize`, `limits.maxSkillMDSize`, `limits.maxSkillDepth`, `limits.maxDescriptionLength` | `256Mi`, `100000`, `500`, `5Mi`, `256Ki`, `8`, `1024`. The largest repository commit that the worker loads, the most objects in it, and the limits of each skill. The worker refuses a commit over a repository limit before it decodes the commit. Below `repository.path`, the repository limits also bound the paths of the tree as the worker reads it: each path counts, and the paths together count against `maxRepositorySize`. The worker does not publish a skill that exceeds a skill limit. Keep `worker.resources.limits.memory` at least three times `maxRepositorySize` plus 256 MiB. Sizes take a whole number of bytes, optionally with `Ki`, `Mi` or `Gi`; the chart refuses other forms. `maxSkillMDSize` cannot be larger than `maxSkillSize`, or the worker does not start. See [Skill limits](../../operations/skill-marketplace/#skill-limits). |
| `worker.resources` | Requests: CPU `50m`, memory `128Mi`. Limits: CPU `1`, memory `1Gi`. |
| `worker.podAnnotations`, `worker.nodeSelector`, `worker.tolerations`, `worker.affinity` | Empty |

The worker is the Deployment `cetirun-skill-marketplace` in the operator
namespace. It runs the operator image with the command `/skill-marketplace`,
one replica with the `Recreate` strategy, as a non-root user, with a read-only
root filesystem and without an Istio sidecar. Its ServiceAccount
`cetirun-skill-marketplace` has only a Role in the operator namespace. The Role
lets the worker read the ConfigMap `cetirun-skill-marketplace-teams` and read
and update the ConfigMap `cetirun-skill-marketplace`. The operator writes the
first ConfigMap, and it creates the second one. The operator also creates the
Agents' claims and volumes. Its ClusterRole already has these permissions.
