---
title: "Chart values"
description: "Every value and default of the CetiRun agent, platform, operator, and Portal Helm charts."
weight: 25
---

These tables list every value of four CetiRun charts with its default from the
`values.yaml` file of the chart. `values.yaml` is the source of the defaults.
`values.schema.json` only validates the values, and the `default` notes in it
are not used. For the effect of the important values and the rules that
connect them, see [Helm configuration](../helm/).

The **Default** column shows a JSON form of each value. A list of objects shows
only its number of entries. A mapping that has no row for each key shows its
keys. A row without a default introduces the values below it.

For an operator-managed Agent, the operator sets many values of the agent
chart. It rejects the values that [Helm-value admission](../crds/#helm-value-admission)
lists. For the platform chart, the values below `cetirun-operator`,
`cetirun-portal`, and the other subchart keys are the values of that subchart.

## Agent chart

Source: [app/cetirun-chart/values.yaml]({{< repo "app/cetirun-chart/values.yaml" >}}).

<!-- generated: values-agent -->

| Value | Default | Description |
| --- | --- | --- |
| `global` |  | Shared release coordinates. Parent charts can set these once and Helm propagates them to subcharts through the global values namespace. |
| `global.registry` | `""` | Optional registry hostname override for all CetiRun images. Empty preserves the component repository defaults below. |
| `global.version` | `""` | Optional shared image and component version override. Empty preserves component tags and Chart.appVersion defaults. |
| `agentType` | `"hermes"` | Agent framework of the instance: `openclaw`, `hermes`, or `custom`. The operator sets it from the Agent type. |
| `replicaCount` | `1` | Number of replicas for the agent gateway Note: Typically should be 1 as agent runtimes maintain state |
| `runtimeClassName` | `"kata-vm-isolation"` | Kubernetes RuntimeClass for pod sandboxing (e.g. kata-vm-isolation for AKS Pod Sandboxing) |
| `sandboxWorkload` | `null` | Internal render contract that the operator writes for Sandbox Agents. It is never a user override. Without `reconciled: true`, it renders only a held workload with zero replicas. The common executor sets `reconciled: true` and renders at most one replica. Agents without a Sandbox leave it `null`. |
| `cetiadmin` |  | Operator-prepared CetiAdmin binding; never a tenant override or RBAC grant. The Operator supplies the remaining projection fields after verifying ownership. |
| `cetiadmin.enabled` | `false` |  |
| `image` |  | Deprecated OpenClaw image alias. Prefer openclaw.image. |
| `image.repository` | `""` | Image repository |
| `image.pullPolicy` | `""` | Image pull policy (empty uses openclaw.image.pullPolicy) |
| `image.tag` | `""` | Image tag (defaults to chart appVersion if not set) |
| `image.digest` | `""` | Image digest for pinning to an exact image (e.g. sha256:abc123...). When set, the image reference becomes repository:tag@digest. |
| `imagePullSecrets` | `[]` | Image pull secrets for private registries |
| `nameOverride` | `""` | String to override the default name |
| `fullnameOverride` | `""` | String to override the default fullname |
| `serviceAccount` |  | Service account configuration |
| `serviceAccount.create` | `false` | Specifies whether a service account should be created |
| `serviceAccount.annotations` | `{}` | Annotations to add to the service account |
| `serviceAccount.name` | `""` | The name of the service account to use (an existing service account). If set, no ClusterRoleBinding is created (user manages RBAC externally). If not set and create is true, a name is generated using the fullname template. |
| `serviceAccount.role` | `""` | Bind service account to a built-in Kubernetes ClusterRole for kubectl access. Options: "" (none), "view" (read-only), "cluster-admin" (full access) Only applies when using chart-created service account (name is not set). When set, automounts the service account token automatically. |
| `serviceAccount.automountToken` | `null` | Explicitly control the default Kubernetes API token mount. Null preserves historical behavior. The operator forces false for Agent Identity workloads. |
| `azureWorkloadIdentity` |  | Azure Workload Identity configuration Enables Azure AD workload identity for the pod, allowing passwordless access to Azure resources (Key Vault, Storage, etc.) via managed identity. Requires: AKS cluster with OIDC issuer and workload identity enabled, plus a federated credential linking the managed identity to this service account. |
| `azureWorkloadIdentity.enabled` | `false` | Enable Azure Workload Identity |
| `azureWorkloadIdentity.clientId` | `""` | Client ID of the User-Assigned Managed Identity |
| `agentIdentity` |  | Typed Microsoft Entra Agent Identity Broker integration. The operator owns these values; an instance workload must not select its Broker endpoint. |
| `agentIdentity.enabled` | `false` |  |
| `connectionsMCP.enabled` | `false` |  |
| `connectionsMCP.url` | `""` |  |
| `connectionsMCP.audience` | `"cetirun-connections-mcp"` |  |
| `connectionsMCP.namespace` | `"cetirun-tools"` |  |
| `connectionsMCP.serviceAccount` | `"cetirun-connections-mcp"` |  |
| `connectionsMCP.caSecretName` | `""` |  |
| `aiGateway` |  | Required platform AI Gateway access, supplied by the Operator. Every Agent uses an Agent-scoped virtual key; upstream credentials stay in the AI Gateway. Policy-owned network controls enforce the allowed egress. |
| `aiGateway.enabled` | `true` |  |
| `aiGateway.keySecretName` | `""` | Secret in the release namespace holding this Agent's virtual key |
| `aiGateway.keySecretKey` | `"api-key"` | Key within that Secret |
| `aiGateway.models` | `[]` | Models the AI Gateway offers this Agent: id (the AI Gateway model name), name, and optionally contextWindow and maxTokens. The Agent is configured with all of them; secrets.model names the default. |
| `aiGateway.visionModels` | `[]` | Ids of the models in models that accept images, supplied by the Operator. OpenClaw and Hermes send images with a request to these models. For other models, Hermes has the image described first, and OpenClaw has another model describe it. |
| `aiGateway.reasoningModels` | `[]` | Ids of the models in models that reason, supplied by the Operator. OpenClaw offers its reasoning levels for these models and sends the level the owner picks as the reasoning effort. For other models, OpenClaw offers only the level off. Hermes does not read this value. |
| `aiGateway.hermes` |  | Hermes on the AI Gateway: the environment variable that carries the Agent's key to Hermes' AI Gateway provider. The chart sets it from the key Secret, so the key is in neither Hermes' configuration nor its .env file; init removes any assignment of it from .env, which Hermes loads over its environment. Since the key replaces the variable in the container, it must be an API key variable, ending in _API_KEY, such as OPENAI_API_KEY, and not one the chart writes into .env for another setting, such as BRAVE_API_KEY. |
| `aiGateway.hermes.keyEnv` | `"CETIRUN_GATEWAY_API_KEY"` |  |
| `aiGateway.copilotProxy` |  | Per-Agent GitHub Copilot proxy: a small LiteLLM, in its own Pod, that holds the owner's Copilot sign-in. Only the AI Gateway reaches it; the Agent reaches Copilot models through the AI Gateway with its own key. |
| `aiGateway.copilotProxy.enabled` | `false` |  |
| `aiGateway.copilotProxy.replicaCount` | `null` | Operator-owned component replicas; null retains standalone Helm behavior. |
| `aiGateway.copilotProxy.image` | `""` | Proxy image: the AI Gateway image |
| `aiGateway.copilotProxy.masterKeySecretName` | `""` | Secret holding the proxy's master key (key master-key), which the AI Gateway's routes to the proxy carry |
| `aiGateway.copilotProxy.masterKeyId` | `""` | Identifies the current master key without revealing it (the start of its SHA-256 hash). The proxy reads the key when it starts, so a new value restarts it. |
| `aiGateway.copilotProxy.signInGeneration` | `""` | Generation of the GitHub Copilot sign-in whose models the Agent uses. The proxy keeps the Copilot token it exchanged until that token expires, so a new sign-in, possibly of another account, restarts it. |
| `aiGateway.copilotProxy.tokenSecretName` | `""` | Secret holding the owner's GitHub token (key access-token), written by the Portal at sign-in. The proxy waits for it. |
| `aiGateway.copilotProxy.responsesModels` | `[]` | Copilot models served only through the Responses API. The proxy bridges chat completion requests for them. |
| `aiGateway.copilotProxy.resources` |  | Proxy container resources |
| `aiGateway.copilotProxy.resources.requests.cpu` | `"50m"` |  |
| `aiGateway.copilotProxy.resources.requests.memory` | `"256Mi"` |  |
| `aiGateway.copilotProxy.resources.limits.memory` | `"1Gi"` |  |
| `podAnnotations` | `{}` | Pod annotations |
| `podSecurityContext` |  | Pod security context |
| `podSecurityContext.fsGroup` | `1024` |  |
| `podSecurityContext.fsGroupChangePolicy` | `"OnRootMismatch"` |  |
| `podSecurityContext.runAsNonRoot` | `true` |  |
| `podSecurityContext.seccompProfile.type` | `"RuntimeDefault"` |  |
| `securityContext` |  | Container security context |
| `securityContext.capabilities` | `{}` |  |
| `securityContext.readOnlyRootFilesystem` | `false` |  |
| `securityContext.runAsNonRoot` | `true` |  |
| `securityContext.runAsUser` | `1024` |  |
| `securityContext.allowPrivilegeEscalation` | `true` |  |
| `securityContext.seccompProfile.type` | `"RuntimeDefault"` |  |
| `service` |  | Service configuration |
| `service.type` | `"ClusterIP"` | Service type |
| `service.port` | `18789` | Service port |
| `service.targetPort` | `18789` | Target port on the container |
| `service.nodePort` | `null` | NodePort (only used when service.type is NodePort) |
| `service.annotations` | `{}` | Service annotations |
| `ingress` |  | Ingress configuration |
| `ingress.enabled` | `false` | Enable ingress |
| `ingress.className` | `""` | Ingress class name |
| `ingress.annotations` | Keys: `nginx.ingress.kubernetes.io/proxy-read-timeout`, `nginx.ingress.kubernetes.io/proxy-send-timeout`, `nginx.ingress.kubernetes.io/proxy-http-version`, `nginx.ingress.kubernetes.io/proxy-buffering` | Additional annotations for the ingress WebSocket support annotations for common ingress controllers |
| `ingress.hosts` | 1 entry | Ingress hosts |
| `ingress.tls` | `[]` | TLS configuration |
| `resources` |  | Resource requests and limits |
| `resources.limits` |  | Resource limits |
| `resources.limits.cpu` | `"8000m"` |  |
| `resources.limits.memory` | `"16Gi"` |  |
| `resources.requests` |  | Resource requests |
| `resources.requests.cpu` | `"4000m"` |  |
| `resources.requests.memory` | `"8Gi"` |  |
| `autoscaling` |  | Horizontal Pod Autoscaler configuration |
| `autoscaling.enabled` | `false` | Enable HPA |
| `autoscaling.minReplicas` | `1` | Minimum number of replicas |
| `autoscaling.maxReplicas` | `3` | Maximum number of replicas |
| `autoscaling.targetCPUUtilizationPercentage` | `80` | Target CPU utilization percentage |
| `autoscaling.targetMemoryUtilizationPercentage` | `80` | Target memory utilization percentage |
| `autoscaling.metrics` | `[]` | Custom metrics for HPA |
| `autoscaling.behavior` | `{}` | Scaling behavior configuration |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Tolerations for pod scheduling |
| `affinity` | `{}` | Affinity rules for pod scheduling |
| `topologySpreadConstraints` | `[]` | Pod topology spread constraints |
| `extraEnv` | `[]` | Extra environment variables |
| `extraEnvFrom` | `[]` | Extra environment variables from secrets/configmaps |
| `extraVolumeMounts` | `[]` | Extra volume mounts for the container |
| `extraVolumes` | `[]` | Extra volumes for the pod |
| `doctor` |  | Deprecated OpenClaw doctor alias. Prefer openclaw.doctor. |
| `doctor.enabled` | `true` |  |
| `initContainers` | `[]` | Init containers |
| `sidecars` | `[]` | Sidecar containers |
| `persistence` |  | Persistent Volume configuration |
| `persistence.enabled` | `true` | Enable persistent storage |
| `persistence.storageClass` | `"default"` | Storage class name ("default" uses Azure Disk on AKS; empty uses the cluster default) |
| `persistence.accessMode` | `"ReadWriteOnce"` | Access mode for the PVC |
| `persistence.size` | `"100Gi"` | Size of the persistent volume |
| `persistence.existingClaim` | `""` | Existing PVC to use (if set, StatefulSet will use this instead of creating new PVCs) |
| `persistence.annotations` | `{}` | Annotations for the PVC |
| `persistence.retainPolicy` | `"Retain"` | Retain policy for the PVC |
| `persistence.useStatefulSetVolumeClaim` | `true` | Use StatefulSet volumeClaimTemplates (default: true) |
| `openclaw` |  | OpenClaw configuration |
| `openclaw.image` |  | OpenClaw gateway image configuration |
| `openclaw.image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/openclaw-gateway"` |  |
| `openclaw.image.pullPolicy` | `"Always"` |  |
| `openclaw.image.tag` | `""` |  |
| `openclaw.image.digest` | `""` |  |
| `openclaw.doctor` |  | Run `openclaw doctor --fix` as an init container on every pod start. |
| `openclaw.doctor.enabled` | `true` |  |
| `openclaw.portalSignIn` |  | Sign the owner in to the Control UI with their Portal sign-in, and approve their browsers without `openclaw devices approve`. OpenClaw's trusted-proxy mode: the ingress passes only the owner, with the email of their sign-in, through the Istio sidecar or the registered in-Pod mTLS endpoint for a common-executor Sandbox with AgentPolicy ingress.requireMTLS. The gateway token is then the password of loopback clients, such as the Terminal CLI. Plain Sandbox ingress and Tailscale keep token authentication. |
| `openclaw.portalSignIn.enabled` | `false` |  |
| `openclaw.config` | Keys: `gateway`, `logging`, `update`, `memory`, and 2 more | OpenClaw gateway configuration (openclaw.json content) |
| `openclaw.acpx` |  | ACPX plugin configuration (Agent Communication Protocol eXtensions). The CetiRun OpenClaw image installs the matching ACPX package. See https://docs.openclaw.ai/tools/acp-agents for full documentation. |
| `openclaw.acpx.enabled` | `true` | Enable ACPX plugin and ACP configuration |
| `openclaw.acpx.dispatch` |  | ACP dispatch configuration (enables agent-to-agent communication) |
| `openclaw.acpx.dispatch.enabled` | `true` |  |
| `openclaw.acpx.backend` | `"acpx"` | ACP backend implementation |
| `openclaw.acpx.defaultAgent` | `"claude"` | Default agent for ACP requests |
| `openclaw.acpx.allowedAgents` | `["claude", "codex", "opencode"]` | Agents allowed for ACP sessions |
| `openclaw.acpx.permissionMode` | `"approve-all"` | ACPX plugin permission mode (approve-all, approve-reads, or deny-all) |
| `openclaw.acpx.nonInteractivePermissions` | `"deny"` | How to handle permission prompts without a TTY (fail or deny). "deny" allows graceful degradation; "fail" aborts the session on prompts. |
| `openclaw.acpx.agents` | `{}` | Per-agent command overrides for the embedded ACPX runtime. |
| `openclaw.git` |  | Git credential configuration for Azure DevOps. When enabled, the init container installs a credential helper script that fetches Azure DevOps tokens via Azure CLI (`az account get-access-token`). Users must run `az login` before git operations will work. |
| `openclaw.git.azureDevOps.enabled` | `true` | Enable Azure DevOps git credential helper |
| `hermes` |  | Hermes configuration |
| `hermes.image` |  | Hermes gateway image configuration |
| `hermes.image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/hermes-gateway"` |  |
| `hermes.image.pullPolicy` | `"Always"` |  |
| `hermes.image.tag` | `""` |  |
| `hermes.image.digest` | `""` |  |
| `hermes.config` | Keys: `terminal`, `compression`, `agent`, `browser`, and 3 more | Hermes native config.yaml content. Model provider/model are rendered from secrets.provider/secrets.model so both agent types share credentials. |
| `hermes.env` |  | Environment variables written into ~/.hermes/.env (API server config only). Model auth is injected by the CetiRun AI Gateway. |
| `hermes.env.API_SERVER_ENABLED` | `"true"` |  |
| `hermes.env.API_SERVER_HOST` | `"0.0.0.0"` |  |
| `hermes.env.API_SERVER_PORT` | `"8642"` |  |
| `hermes.env.API_SERVER_KEY` | `""` |  |
| `hermes.env.API_SERVER_CORS_ORIGINS` | `""` |  |
| `hermes.env.HERMES_WEBAPP_PUBLIC_URL` | `""` | Operator-owned external Webapp URL, including its /ui/\<instance> prefix. |
| `hermes.gateway.enabled` | `true` |  |
| `user` |  | User identity (typically populated by the operator from Agent CR spec.user) |
| `user.alias` | `""` | User's short alias. Commonly used as part of the namespace name: cetirun-\<alias>. When left empty, the operator or platform may derive it from other context. |
| `user.displayName` | `""` | User's human-friendly display name, shown in UIs and logs where applicable. |
| `user.team` | `""` | Logical team name. Populated by the operator from Agent spec. |
| `user.role` | `""` | User's role. When non-empty, injected into USER.md Role field. Set by the operator when an Team with memberContext.role is referenced. |
| `user.description` | `[]` | User context lines. When non-empty, injected as Notes bullet list in USER.md. Set by the operator when an Team with memberContext.description is referenced. |
| `user.baseDomain` | `""` | Base domain for instance access URLs. Used to template the access URL in USER.md. Set this to your deployment domain (e.g. "example.com"). |
| `secrets` |  | Secrets configuration |
| `secrets.existingSecret` | `""` | Use existing secret instead of creating one |
| `secrets.annotations` | `{}` | Secret annotations |
| `secrets.braveApiKey` | `""` | Web search API keys (enables tools.web.search when set) Brave Search - structured results with free tier |
| `secrets.perplexityApiKey` | `""` |  |
| `secrets.tailscaleAuthKey` | `""` | Tailscale auth key (from https://login.tailscale.com/admin/settings/keys) Recommended: Reusable + Ephemeral. Setting this enables Tailscale (no separate toggle). |
| `secrets.provider` | `"openai"` | AI Gateway wire protocol; upstream providers are configured in the AI Gateway. |
| `secrets.model` | `""` | Model name. The operator supplies this from spec.model or its deployment default. |
| `secrets.authMode` | `""` | AI Gateway authentication mode. |
| `secrets.apiKey` | `""` | Removed direct credential setting; must remain empty. |
| `secrets.apiBase` | `""` | Operator-selected AI Gateway URL. |
| `secrets.openclawGatewayToken` | `""` | OpenClaw gateway token |
| `livenessProbe` |  | Liveness probe configuration |
| `livenessProbe.enabled` | `true` |  |
| `livenessProbe.initialDelaySeconds` | `30` |  |
| `livenessProbe.periodSeconds` | `180` |  |
| `livenessProbe.timeoutSeconds` | `180` |  |
| `livenessProbe.failureThreshold` | `3` |  |
| `livenessProbe.successThreshold` | `1` |  |
| `readinessProbe` |  | Readiness probe configuration |
| `readinessProbe.enabled` | `true` |  |
| `readinessProbe.initialDelaySeconds` | `30` |  |
| `readinessProbe.periodSeconds` | `30` |  |
| `readinessProbe.timeoutSeconds` | `10` |  |
| `readinessProbe.failureThreshold` | `6` |  |
| `readinessProbe.successThreshold` | `1` |  |
| `startupProbe` |  | Startup probe configuration |
| `startupProbe.enabled` | `true` |  |
| `startupProbe.initialDelaySeconds` | `10` |  |
| `startupProbe.periodSeconds` | `10` |  |
| `startupProbe.timeoutSeconds` | `5` |  |
| `startupProbe.failureThreshold` | `null` | Null selects 60 for common-executor OpenClaw Sandboxes, otherwise 30. An explicit positive integer is preserved. |
| `startupProbe.successThreshold` | `1` |  |
| `ttyd` |  | ttyd web terminal configuration |
| `ttyd.enabled` | `true` | Enable ttyd web terminal |
| `ttyd.port` | `7681` | ttyd listening port |
| `ttyd.shell` | `"/bin/zsh"` | Shell to launch (zsh matches the container's default shell) |
| `ttyd.basePath` | `"/term"` | Base path prefix for ttyd web UI (passed as --base-path flag). Required so ttyd generates correct WebSocket URLs when served behind a path prefix. |
| `ttyd.image` |  | Image of the isolated Custom terminal and of the static probe helper. Custom Agents in a Sandbox need it in the Operator's `cetirun.helmValues`, with an immutable digest. See [Custom terminal image](../helm/#custom-terminal-image). |
| `ttyd.image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-terminal"` |  |
| `ttyd.image.tag` | `""` |  |
| `ttyd.image.digest` | `""` |  |
| `ttyd.image.pullPolicy` | `"IfNotPresent"` |  |
| `ttyd.sessionSeconds` | `1800` | Longest time before the Custom terminal must authenticate again. The ttyd of OpenClaw and Hermes does not use it. |
| `ttyd.gatewayServiceAccount` | `"aks-istio-ingressgateway-external"` | Ingress gateway identity allowed to reach the Custom terminal. |
| `ttyd.resources.requests.cpu` | `"50m"` |  |
| `ttyd.resources.requests.memory` | `"64Mi"` |  |
| `ttyd.resources.limits.cpu` | `"500m"` |  |
| `ttyd.resources.limits.memory` | `"256Mi"` |  |
| `tailscale` |  | Tailscale configuration Enabled automatically when secrets.tailscaleAuthKey is set. Each StatefulSet pod registers as \<hostname>-\<ordinal> on the Tailscale network. Uses kernel networking (NET_ADMIN added automatically). |
| `tailscale.hostname` | `"cetirun"` | Hostname prefix on the Tailscale network. Each pod appends its StatefulSet ordinal: \<hostname>-0, \<hostname>-1, etc. |
| `tailscale.serve` | `false` | Enable `tailscale serve` to proxy the gateway with Tailscale HTTPS. When true, the gateway port is automatically used as the serve target. |
| `logger` |  | Session logger sidecar configuration. Tails session JSONL files to stdout, where the cluster's log collection captures them. With the ADX-Mon collection, query the Agent logs database: AgentLogs \| where Container == "logger" |
| `logger.enabled` | `true` | Enable session logger sidecar |
| `logger.resources` |  | Resource requests and limits |
| `logger.resources.limits.cpu` | `"100m"` |  |
| `logger.resources.limits.memory` | `"128Mi"` |  |
| `logger.resources.requests.cpu` | `"10m"` |  |
| `logger.resources.requests.memory` | `"64Mi"` |  |
| `logger.scanInterval` | `30` | Interval (seconds) between scans for new session files |
| `desktop` |  | Desktop sidecar configuration. A separate container runs Chrome and noVNC (Xvfb, Fluxbox, x11vnc, and websockify). The gateway connects to Chrome through CDP on `localhost:18800`. Disable it for headless instances that do not need a browser GUI. |
| `desktop.enabled` | `true` | Enable desktop sidecar |
| `desktop.image` |  | Desktop sidecar image configuration |
| `desktop.image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-desktop"` | Image repository |
| `desktop.image.pullPolicy` | `"Always"` | Image pull policy |
| `desktop.image.tag` | `""` | Image tag (defaults to chart appVersion if not set) |
| `desktop.image.digest` | `""` | Image digest for pinning to an exact image |
| `desktop.novncPathPrefix` | `""` | noVNC WebSocket path prefix injected by the operator (e.g. "example-agent/desktop/"). Used in the auto-connect redirect: ?path=\<novncPathPrefix>websockify |
| `desktop.logging` | `false` | Route x11vnc and websockify output to container stderr (visible via kubectl logs). Off by default; enable via spec.helm.values.desktop.logging=true for troubleshooting. |
| `desktop.resources` |  | Resource requests and limits for the desktop sidecar |
| `desktop.resources.limits.cpu` | `"2000m"` |  |
| `desktop.resources.limits.memory` | `"4Gi"` |  |
| `desktop.resources.requests.cpu` | `"500m"` |  |
| `desktop.resources.requests.memory` | `"1Gi"` |  |
| `desktop.screenResolution` | `"1920x1080x24"` | Virtual display resolution (WxHxD) |
| `sidecarScope` |  | Istio Sidecar scope configuration Restricts Envoy service discovery so each user namespace only sees its own services plus explicitly listed shared namespaces. Without this, any pod can query localhost:15000/clusters and enumerate every other user namespace, backend services, and infrastructure topology. |
| `sidecarScope.enabled` | `true` | Enable Istio Sidecar resource for discovery scoping |
| `sidecarScope.additionalEgressHosts` | `[]` | Additional namespace/host entries beyond the auto-included ones (own namespace, istioNamespace, istioIngressNamespace). Format: "namespace/hostname" or "namespace/*" |
| `networkPolicy` |  | Network Policy configuration Default deny-all. Inside the cluster, the Agent reaches only the DNS Pods, istiod, the platform services it uses (such as the AI Gateway) and the entries of allowedInternalCIDRs. Other egress can go to IPv4 addresses outside blockedCIDRs. |
| `networkPolicy.enabled` | `true` | Enable NetworkPolicy |
| `networkPolicy.istioNamespace` | `"istio-system"` | Namespace where istiod runs; also used as the default ingress gateway namespace unless istioIngressNamespace is set. Used for istiod mTLS egress (port 15012) and as the default ingress source. Typical istiod namespaces: AKS managed Istio: aks-istio-system; upstream Istio: istio-system. |
| `networkPolicy.dnsNamespace` | `"kube-system"` | Namespace where kube-dns runs (egress DNS) |
| `networkPolicy.dnsPodLabels` |  | Labels of the DNS Pods in dnsNamespace. Only these Pods get DNS traffic (TCP/UDP 53). Labels with an empty value are ignored. Helm merges maps, so to drop the default label, set k8s-app to "" rather than null. Without labels, every Pod in dnsNamespace gets DNS traffic. |
| `networkPolicy.dnsPodLabels.k8s-app` | `"kube-dns"` |  |
| `networkPolicy.agentIdentityBrokerNamespace` | `"cetirun-credentials"` | Namespace hosting the deployment-level Agent Identity Broker. |
| `networkPolicy.gatewayNamespace` | `"cetirun-system"` | Namespace of the platform AI Gateway, used when aiGateway.enabled. |
| `networkPolicy.gatewayPort` | `4000` | Service port of the platform AI Gateway. |
| `networkPolicy.allowedInternalCIDRs` | `[]` | Internal IPs/CIDRs to explicitly allow despite blockedCIDRs. Each entry needs a cidr and a list of ports. |
| `networkPolicy.blockedCIDRs` | 6 items | Ranges excluded from the public egress rule. The DNS, platform service and allowedInternalCIDRs rules are separate allow rules. |
| `skills` |  | Shared skills volume configuration. Auto-creates PV/PVC backed by Azure Blob CSI (NFS by default; Fuse/Fuse2 supported). Protocol, secretName, and mountOptions are install-time-only PV settings. |
| `skills.shared` | `false` | Enable shared skills PV/PVC |
| `skills.storageClassName` | `"azureblob-nfs-premium"` | StorageClass for the shared skills PV/PVC |
| `skills.protocol` | `"nfs"` | Install-time-only Blob CSI protocol. Use fuse2 for HTTPS-only storage accounts. |
| `skills.secretName` | `""` | Install-time-only namespace-local secret used by Blob Fuse mounts. |
| `skills.mountOptions` | `[]` | Install-time-only static PV mount options. Blobfuse2 commonly needs "-o allow_other". |
| `skills.resourceGroup` | `""` | Azure resource group containing the storage account |
| `skills.storageAccount` | `""` | Azure storage account name |
| `skills.containerName` | `"aks-skills"` | Blob container name |
| `skills.mountPath` | `"/home/vibe/.aks/skills"` | Shared skills are included in $HOME/.agents/skills. A different path adds a read-only alias to the same composed view for native Agents. |
| `skills.clawHub` | `[]` | ClawHub skill package names (reserved for future init-skills implementation). |
| `skills.npm` | `[]` | npm package names installed into /home/vibe/.cetirun/skills/npm/\<name> by the init-skills init container. Specs can be bare ("matrix"), scoped ("@scope/name"), or version-pinned ("name@1.2.3"). |
| `skills.npmRegistry` | `"https://registry.npmjs.org/"` | npm registry for runtime npm skill installation. Defaults to public npm; override to use a different package mirror. |
| `skills.storageSize` | `"100Gi"` | Storage capacity |
| `podDisruptionBudget` |  | Pod Disruption Budget configuration |
| `podDisruptionBudget.enabled` | `false` | Enable Pod Disruption Budget |
| `podDisruptionBudget.minAvailable` | `1` | Minimum available pods (number or percentage) |
| `execRestriction` |  | Block kubectl exec into openclaw pods via ValidatingAdmissionPolicy. Requires Kubernetes 1.30+. |
| `execRestriction.enabled` | `true` | Enable the exec restriction policy |
| `memoryContext.enabled` | `true` | Install the helper in standard Agents; protected Sandbox rendering retains its upstream contract. |
| `memoryContext.image` | `""` | Optional full image reference; empty follows global.registry/version. |
| `memoryContext.home` | `"/home/vibe"` |  |

<!-- /generated: values-agent -->

## Platform chart

Source: [app/cetirun-platform/values.yaml]({{< repo "app/cetirun-platform/values.yaml" >}}).

<!-- generated: values-platform -->

| Value | Default | Description |
| --- | --- | --- |
| `global` |  | Shared release coordinates propagated automatically to the Operator, Portal and AI Gateway subcharts. |
| `global.memoryContext` |  | Optional full Memory image override. Empty follows registry/version. |
| `global.memoryContext.image` | `""` |  |
| `global.portalChat` |  | Enable both Portal Chat and its private runtime caller. No database. |
| `global.portalChat.enabled` | `false` |  |
| `global.portalChat.identityRealm` | `""` |  |
| `global.registry` | `""` | Optional registry hostname override propagated to all CetiRun subcharts. Empty preserves each subchart's existing repository defaults. |
| `global.version` | `""` | Optional image and chart version override propagated to all subcharts. Empty preserves each synchronized subchart's existing version defaults. |
| `global.registryCredentialNamespace` | `"cetirun-registry-credentials"` | Shared by Portal and Operator; separate from the Agent Identity Broker. |
| `publicTLS` |  | Optional public HTTP-01 certificate preparation on an existing managed Istio ingress listener. cert-manager itself is installed separately. |
| `publicTLS.enabled` | `false` |  |
| `publicTLS.bootstrapOnly` | `false` | Render only the public TLS resources. This is intended for a certificate-first bootstrap before the rest of the platform is deployed. |
| `publicTLS.bootstrapRevision` | `1` | Reserved for the deployment helper: expected next Helm revision. Bootstrap upgrades refuse a stale revision after a concurrent deployment. |
| `publicTLS.domain` | `""` |  |
| `publicTLS.ingressNamespace` | `"aks-istio-ingress"` |  |
| `publicTLS.ingressServiceHostname` | `"aks-istio-ingressgateway-external.aks-istio-ingress.svc.cluster.local"` |  |
| `publicTLS.gateway.name` | `"cetirun-acme-http"` |  |
| `publicTLS.acme.email` | `""` |  |
| `publicTLS.acme.stagingIssuerName` | `"cetirun-letsencrypt-staging"` |  |
| `publicTLS.acme.productionIssuerName` | `"cetirun-letsencrypt-production"` |  |
| `publicTLS.acme.stagingCertificateEnabled` | `false` |  |
| `publicTLS.acme.stagingCertificateName` | `"cetirun-tls-staging"` |  |
| `publicTLS.acme.stagingSecretName` | `"cetirun-tls-staging"` |  |
| `publicTLS.acme.productionCertificateEnabled` | `false` |  |
| `publicTLS.acme.productionCertificateName` | `"cetirun-tls"` |  |
| `publicTLS.acme.productionSecretName` | `"cetirun-tls"` |  |
| `cetirun-connections-mcp.enabled` | `false` |  |
| `cetirun-agent-broker.enabled` | `false` |  |
| `istio.enabled` | `false` |  |
| `istio.oauth2Proxy.name` | `"oauth2-proxy"` |  |
| `istio.oauth2Proxy.namespace` | `"cetirun-istio"` |  |
| `cetirun-operator.enabled` | `true` |  |
| `cetirun-operator.namespace` | `"cetirun-system"` |  |
| `cetirun-operator.istioGateway` | `"aks-istio-ingress/cetirun-gateway"` |  |
| `cetirun-operator.cetirun.helmValues.networkPolicy.istioIngressNamespace` | `"aks-istio-ingress"` |  |
| `cetirun-operator.domainSuffix` | `""` |  |
| `cetirun-operator.aiGateway.enabled` | `true` |  |
| `cetirun-operator.sandboxAdmission.portalServiceAccount` | `"cetirun-portal"` |  |
| `cetirun-portal.enabled` | `true` |  |
| `cetirun-portal.istio.host` | `""` |  |
| `cetirun-portal.istio.gateway` | `"aks-istio-ingress/cetirun-gateway"` |  |
| `cetirun-portal.istio.gatewayNamespace` | `"aks-istio-ingress"` |  |
| `cetirun-portal.istio.gatewayServiceAccount` | `"aks-istio-ingressgateway-external"` |  |
| `cetirun-portal.features.aiGatewayEnabled` | `true` |  |
| `cetirun-portal.aiGateway.catalogNamespace` | `"cetirun-system"` |  |
| `cetirun-portal.configurationCoordination.namespace` | `""` |  |
| `cetirun-portal.configurationCoordination.name` | `""` |  |
| `cetirun-portal.configurationCoordination.uid` | `""` |  |
| `cetirun-portal.sandboxStorage.platformNamespace` | `""` |  |
| `cetirun-portal.sandboxStorage.platformConfigMapName` | `""` |  |
| `cetirun-portal.sandboxStorage.defaultStorageClassName` | `""` |  |
| `cetirun-ai-gateway.enabled` | `true` | Install the AI Gateway with the operator and Portal. Turn it off only when an AI Gateway is installed separately in the operator namespace. |
| `cetirun-ai-gateway.fullnameOverride` | `"cetirun-ai-gateway"` |  |
| `cetirun-ai-gateway.operatorIntegration` | `true` |  |
| `cetirun-ai-gateway.models` | `[]` | Shared model candidates granted through the selected AgentPolicy. The AI Gateway chart generates their deployment IDs and per-model access groups; the old cetirun-shared group is not a policy grant. Each model still needs pricing while the AI Gateway blocks unpriced models. An empty list is valid, but console-only models are not trusted shared bindings. Agents need an authorized model entitlement before model use. |
| `cetirun-ai-gateway.metrics` |  | LiteLLM's Prometheus metrics, on a port of their own in each AI Gateway pod, which the ADX-Mon collection of cetirun-observability scrapes. |
| `cetirun-ai-gateway.metrics.enabled` | `true` |  |
| `cetirun-ai-gateway.spendLogs` |  | LiteLLM's own row per call in its database, which the console's request log pages read, kept for seven days (the AI Gateway chart's spendLogs.retention). The AI Gateway's request log on stdout records each call as well, and the cluster's log collection keeps it outside the database. The database rows also hold each call's prompt and response unless spendLogs.storePrompts is false. The stdout request log never does. |
| `cetirun-ai-gateway.spendLogs.enabled` | `true` |  |
| `cetirun-observability` |  | ADX-Mon collection of the platform's and the Agents' logs, Kubernetes events and metrics into Azure Data Explorer. make deploy turns it on with the observability block of its environment file, which names the Kusto cluster, the databases and the ingestor's Azure identity. |
| `cetirun-observability.enabled` | `false` |  |

<!-- /generated: values-platform -->

## Operator chart

Source: [app/cetirun-operator/chart/values.yaml]({{< repo "app/cetirun-operator/chart/values.yaml" >}}).

<!-- generated: values-operator -->

| Value | Default | Description |
| --- | --- | --- |
| `global` |  | Shared release coordinates. An umbrella chart can set these once for all CetiRun subcharts. |
| `global.registry` | `""` | Optional registry hostname override for all CetiRun images and charts. Empty preserves the component defaults below. |
| `global.version` | `""` | Optional shared image and chart version override. Empty preserves the component and Chart.appVersion defaults below. |
| `global.registryCredentialNamespace` | `""` |  |
| `registryCredentials` |  | This chart owns the Registry source namespace. Portal and Operator must use the same namespace; do not reuse the Agent Identity Broker namespace. |
| `registryCredentials.namespace` | `"cetirun-registry-credentials"` |  |
| `registryCredentials.createNamespace` | `true` |  |
| `replicaCount` | `2` | Number of operator replicas (leader election keeps only one active). |
| `image` |  | Operator container image. |
| `image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-operator"` |  |
| `image.pullPolicy` | `"IfNotPresent"` |  |
| `image.tag` | `""` |  |
| `image.digest` | `""` | Image digest for pinning (e.g. sha256:abc123...). When set, pins the image; the tag is kept for readability. |
| `imagePullSecrets` | `[]` | Image pull secrets. |
| `acrDockerSecret` | `""` | Name of a kubernetes.io/dockerconfigjson Secret in the operator namespace whose credentials are passed to the Helm registry client via DOCKER_CONFIG. Required when the OCI chart registry (e.g. ACR) is private and the node's kubelet identity does not surface docker credentials inside the pod. |
| `namespace` | `"cetirun-system"` | Namespace to deploy the operator into. The namespace is created by the operator's Helm chart installation, not by the operator itself. |
| `istioRevision` | `""` | Istio revision label for AKS managed Istio sidecar injection. When empty (default), the operator auto-detects the revision from ConfigMaps in aks-istio-system. Set explicitly to override auto-detection or to pin a specific revision (e.g. "asm-1-22"). |
| `istioGateway` | `"aks-istio-ingress/cetirun-gateway"` | Namespace/name of the Istio Gateway used by instance VirtualServices. |
| `domainSuffix` | `""` | Base domain suffix for instance access URLs (e.g. alias.example.com). |
| `cetirun` |  | CetiRun Helm chart configuration. |
| `cetirun.defaultModel` | `"gpt-6-astra"` | The operator writes it to `secrets.model`, but the AI Gateway projection replaces that value. To select the default model of Agents, use `aiGateway.defaultModel`. |
| `cetirun.chart.repoURL` | `"oci://mcr.microsoft.com/aks/ai-runtime/cetirun/helm/cetirun"` | OCI registry URL. |
| `cetirun.chart.version` | `"0.1.2"` | Chart version to deploy. |
| `cetirun.helmValues` | `{}` | Operator-level Helm value overrides applied to every CetiRun instance. Merged on top of the operator's hardcoded defaults but beneath per-instance spec.helm.values, so instances can still override them. |
| `cetiadmin` |  | One Operator-owned ordinary Hermes installation. The umbrella uses these same values under cetirun-operator.cetiadmin; this chart never renders an Agent. |
| `cetiadmin.enabled` | `true` |  |
| `cetiadmin.legacyDisabledSource` | `false` |  |
| `cetiadmin.instanceName` | `"cetiadmin"` |  |
| `cetiadmin.owner.alias` | `""` |  |
| `cetiadmin.owner.tenantID` | `""` |  |
| `cetiadmin.owner.objectID` | `""` |  |
| `cetiadmin.platformReadNamespaces` | `[]` |  |
| `cetiadmin.kubernetes.apiServer` | `"https://kubernetes.default.svc"` |  |
| `cetiadmin.kubernetes.apiAudience` | `""` |  |
| `cetiadmin.kubernetes.caConfigMapName` | `"kube-root-ca.crt"` |  |
| `cetiadmin.kubernetes.apiEgressCIDRs` | `[]` |  |
| `cetiadmin.model.default` | `"auto"` |  |
| `cetiadmin.model.initialMonthlyBudgetUSD` | `""` |  |
| `cetiadmin.model.githubCopilot` | `false` |  |
| `cetiadmin.runtime.version` | `""` |  |
| `cetiadmin.runtime.image` | `""` |  |
| `cetiadmin.runtime.port` | `null` |  |
| `cetiadmin.azureWorkloadIdentity.enabled` | `false` |  |
| `cetiadmin.azureWorkloadIdentity.clientId` | `""` |  |
| `cetiadmin.sharedSkills.enabled` | `false` |  |
| `cetiadmin.monitor.enabled` | `false` |  |
| `cetiadmin.monitor.schedule` | `"every 30m"` |  |
| `cetiadmin.monitor.timezone` | `"UTC"` |  |
| `cetiadmin.monitor.deliver` | `"local"` |  |
| `cetiadmin.monitor.slackChannel` | `""` |  |
| `cetiadmin.monitor.slackSecretName` | `""` |  |
| `cetiadmin.retainedAgentUID` | `""` |  |
| `cetiadmin.hookTimeoutSeconds` | `600` |  |
| `leaderElection` | `true` | Enable leader election (recommended for production with replicaCount > 1). |
| `resources` |  | Resource requests/limits for the operator pod. |
| `resources.requests.cpu` | `"100m"` |  |
| `resources.requests.memory` | `"128Mi"` |  |
| `resources.limits.cpu` | `"500m"` |  |
| `resources.limits.memory` | `"2Gi"` |  |
| `nodeSelector` | `{}` | Node selector for the operator pod. |
| `tolerations` | `[]` | Tolerations for the operator pod. |
| `affinity` | `{}` | Affinity rules for the operator pod. |
| `podAnnotations` | `{}` | Additional pod annotations. |
| `chatAccess` |  | Trusted callers of private Custom Chat endpoints; not tenant overrides. |
| `chatAccess.enabled` | `false` |  |
| `chatAccess.callers` | `[]` |  |
| `serviceAccount` |  | Service account configuration. |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `"cetirun-operator-controller-manager"` |  |
| `serviceAccount.annotations` | `{}` |  |
| `sandboxAdmission` |  | Configuration admission only; this does not enable governed execution or provide a qualified Sandbox capability registration. |
| `sandboxAdmission.validationMode` | `"compatible"` | CR object decoding: compatible tolerates fields from a newer schema; strict rejects them. Authorization, Pod checks and server config stay strict. |
| `sandboxAdmission.enabled` | `false` | Deploy the independent, read-only admission server first. Requires Kubernetes >= 1.37, the validated admission minimum. The server uses native lifecycle sleep for an image-independent shutdown delay. |
| `sandboxAdmission.enforce` | `false` | Install fail-closed webhooks only after the server and TLS are ready. Requires Kubernetes >= 1.37, including native admission matchConditions. |
| `sandboxAdmission.podsEnabled` | `false` | Serves the final-Pod checks. The Pod webhook is installed only with `enforce: true`. It also denies exec, attach, and port-forward in the namespaces that the controller marks. It needs `sandboxAdmission.enabled: true`, and `sandboxSources.enabled` needs it. To run Sandbox Agents, also set `sandboxSources.nativeExecution` and `sandboxSources.bootstrapOwnedPrerequisites`. Other Pod mutators must exclude these namespaces, because the checks refuse metadata that they inject. |
| `sandboxAdmission.placementConfigMapName` | `""` | Optional operator-namespace placement.json ConfigMap. Requires Pod admission. When enforced, placement checks cover every namespace's Pod writes and bindings; an unavailable registry/server fails those requests. This does not supply a fencing receipt or authorize Agent execution. |
| `sandboxAdmission.coordinationConfigMapName` | `""` | Optional UID-pinned coordination authority for Team/Sandbox/AgentPolicy updates and deletions. Requires strict, enforced configuration and Pod admission. Both fields are required. No state is created and no execution is enabled. Native execution uses this same authority for selected-source reservations; there is no separate sandboxSources coordination setting. Keep it configured until existing native executions have stopped and released their reservations. |
| `sandboxAdmission.coordinationConfigMapUID` | `""` |  |
| `sandboxAdmission.timeoutSeconds` | `10` | Timeout for each fail-closed webhook call; integer 1..30 when enforce=true. Pod validation uses 80% of this budget, with at most four concurrent source GETs per request; the remainder is reserved for transport and response. |
| `sandboxAdmission.replicaCount` | `2` |  |
| `sandboxAdmission.priorityClassName` | `""` | Optional existing platform-managed PriorityClass. No class is created or selected automatically. |
| `sandboxAdmission.nodeSelector` | `{}` | Admission-only placement; never inherits the operator's placement. |
| `sandboxAdmission.affinity` | `{}` |  |
| `sandboxAdmission.tolerations` | `[]` |  |
| `sandboxAdmission.tlsSecretName` | `""` | Existing platform-issued TLS Secret in namespace, containing tls.crt and tls.key. The certificate must cover cetirun-sandbox-admission.\<namespace>.svc. This chart does not issue certificates or install cert-manager. |
| `sandboxAdmission.caBundle` | `""` | Base64-encoded PEM CA bundle supplied by the platform issuer. Required when enforce=true. Helm checks encoding/framing, not certificate trust, expiry, CA constraints, or the serving certificate's subject names. |
| `sandboxAdmission.adminUsers` | `[]` | Exact authenticated Kubernetes usernames/groups allowed to administer configuration. At least one nonempty list is required even before enforce. Blanket groups (system:authenticated, system:unauthenticated and the system:serviceaccounts groups) are refused; list service accounts as users. |
| `sandboxAdmission.adminGroups` | `[]` |  |
| `sandboxAdmission.portalServiceAccount` | `""` | Name of a ServiceAccount in this release's namespace that is added once to the administrators, as system:serviceaccount:\<release namespace>:\<name>, when admission is enabled. The CetiRun platform chart sets it to the Portal's ServiceAccount, with which the Portal writes Agents, Teams, Sandboxes and AgentPolicies. It does not count as the administrator required above: operations without a Portal page, such as reauthorization, still need one in adminUsers or adminGroups. Empty adds nothing. |
| `sandboxAdmission.teamAssignments` | `[]` | Trusted Kubernetes user/group to Team creation assignments. Conflicting memberships reject; Portal roles or caller-provided Team names are not proof. |
| `sandboxAdmission.resources.requests.cpu` | `"100m"` |  |
| `sandboxAdmission.resources.requests.memory` | `"128Mi"` |  |
| `sandboxAdmission.resources.limits.cpu` | `"500m"` |  |
| `sandboxAdmission.resources.limits.memory` | `"256Mi"` |  |
| `sandboxSources` |  | Kubernetes capability declarations for configuration admission and held native Sandbox preparation. Both use the same operator-namespace platform.json. Does not qualify a platform or implement physical writer fencing. Adds controller write permissions for compiled namespaced CiliumNetworkPolicies. Adds only get permission on shared Istio Gateways for pinned access inputs. |
| `sandboxSources.enabled` | `false` |  |
| `sandboxSources.nativeExecution` | `false` | Opt in to common OpenClaw/Hermes reconciliation with registered Kata RWO/RWOP HOME. Requires owned bootstrap, enforced admission and the AI Gateway. Existing receipts retain their recorded mode and owned resources. Neither Kubernetes updates nor observed shutdown are physical fencing. Disabling starts retains recorded cleanup and Pod finalization RBAC. |
| `sandboxSources.plannedMigration` | `false` | Allow new healthy-source moves of common-executor Agents with one filesystem CSI RWO/RWOP HOME. Upgrade CRDs, Operator, admission and Portal readers before enabling. Existing checkpoints remain readable and cleanup permissions are retained when disabled. |
| `sandboxSources.bootstrapOwnedPrerequisites` | `false` | Create owned namespace, HOME and native authentication only for new namespaces. Requires chart/image/port pins; the common flow can record them first. Published content stays external. |
| `sandboxSources.platformConfigMapName` | `""` | Existing operator-namespace ConfigMap containing versioned platform.json. Shared by configuration admission and the controller only when enabled. The platform owner supplies qualification, topology and exact object pins. |
| `sandboxSources.homeClaimName` | `"home"` | Claims in each protected Agent namespace; never adopted by name. |
| `sandboxSources.skillsClaimName` | `"skills"` |  |
| `sandboxSources.skillsCatalogConfigMapName` | `""` | Optional separate operator-namespace ConfigMap containing skills.json. Requires owned bootstrap. Selects already-published Blob NFS content by Team UID; creates only retained, read-only static PV/PVC bindings, never source content. |
| `sandboxSources.placementConfigMapName` | `""` | Optional placement.json registry for durable namespace/HOME/Node binding. Must match sandboxAdmission.placementConfigMapName. Records the existing reservation after zero-replica staging; does not enroll Nodes or grant execution. |
| `sandboxSources.helpers` |  | Approved helpers for legacy held preparation only. Reconciled (v2) lifetimes use agent chart helper settings instead. |
| `sandboxSources.helpers.doctor` | `false` |  |
| `sandboxSources.helpers.logger` | `false` |  |
| `sandboxSources.helpers.desktop` | `false` |  |
| `skillMarketplace.enabled` | `false` | Use the Skill Marketplace for Team skills of Sandbox Agents. On, every skill entry without the npm: prefix names a marketplace skill: the operator stops passing such Team entries to the reserved ClawHub list, and refuses an Agent whose skillOverrides add or remove one (marketplace skills are selected on the Team). The operator lists the policy-bound Teams for the worker and gives each of their Agents a read-only claim of the storage, named sandboxSources.skillsClaimName. The worker, a separate Deployment that runs the operator image, synchronizes the repository, publishes the Team releases and writes the catalog. Requires sandboxSources.enabled and sandboxSources.nativeExecution, and an empty sandboxSources.skillsCatalogConfigMapName: the marketplace replaces the published skills catalog. |
| `skillMarketplace.repository` |  | One Git repository of skills for the installation. Every directory that holds a SKILL.md below `path` is a skill. The worker never executes repository content. |
| `skillMarketplace.repository.url` | `""` | https:// URL, or ssh:// / user@host:path with auth ssh. Never put credentials in the URL. |
| `skillMarketplace.repository.ref` | `""` | Branch, tag or full ref. Empty uses the remote HEAD. |
| `skillMarketplace.repository.path` | `""` | Directory of the repository that holds the skills. Empty is the root. |
| `skillMarketplace.repository.syncInterval` | `"5m"` |  |
| `skillMarketplace.repository.auth` | `"none"` | none, token or ssh. |
| `skillMarketplace.repository.existingSecret` | `""` | Existing Secret in the operator namespace: key `token` for auth token; keys `ssh-privatekey` and `known_hosts` for auth ssh. |
| `skillMarketplace.repository.tokenUsername` | `"x-access-token"` |  |
| `skillMarketplace.repository.allowInsecureHTTP` | `false` | Allows an http:// URL. Only for tests inside a private network. |
| `skillMarketplace.storage` |  | The Blob NFS container that holds the Team releases. Use a storage account that only private endpoints can reach: NFS 3.0 has no client authentication. |
| `skillMarketplace.storage.resourceGroup` | `""` |  |
| `skillMarketplace.storage.storageAccount` | `""` |  |
| `skillMarketplace.storage.containerName` | `""` |  |
| `skillMarketplace.storage.storageClassName` | `"azureblob-nfs-premium"` |  |
| `skillMarketplace.storage.size` | `"100Gi"` | Capacity of the worker's read-write volume and claim. The storage is part of their names: a change of these values gives the worker a new volume and claim, and the operator moves the Agents' claims. |
| `skillMarketplace.reservedNames` | `["cetirun-memory", "connected-services"]` | Skill names that the platform or images supply. |
| `skillMarketplace.teamGCGrace` | `"1h"` | How long the releases of a Team that left the policy-bound Teams stay after the worker found it gone. |
| `skillMarketplace.limits` |  | Limits of the repository and of each skill. The worker does not publish a skill that exceeds a limit, and the Skills page shows the reason. Agents copy their Team's skills into the scratch space of their Sandbox when they start, so raise the size limits only together with that space. |
| `skillMarketplace.limits.maxRepositorySize` | `"256Mi"` | Largest repository commit that the worker loads: the pack that the Git server sends for the commit, the commit's objects together, and the paths of the files and directories below repository.path together. The worker checks the pack and the objects before it decodes the commit, and the paths while it reads the tree. Keep worker.resources.limits.memory at least three times this size plus 256Mi. |
| `skillMarketplace.limits.maxRepositoryObjects` | `100000` | Most objects in the repository commit: its files, its directories and the commit itself. Below repository.path, the worker also counts each path: a directory that the commit holds at two paths counts twice. |
| `skillMarketplace.limits.maxSkillFiles` | `500` | Most files in one skill. |
| `skillMarketplace.limits.maxSkillSize` | `"5Mi"` | Largest total size of the files of one skill. |
| `skillMarketplace.limits.maxSkillMDSize` | `"256Ki"` | Largest SKILL.md file. At most maxSkillSize. |
| `skillMarketplace.limits.maxSkillDepth` | `8` | Deepest directory level of a file in a skill. |
| `skillMarketplace.limits.maxDescriptionLength` | `1024` | Longest description in the SKILL.md frontmatter, in characters. The Agent Skills specification allows 1024. |
| `skillMarketplace.worker` |  | The worker Deployment `cetirun-skill-marketplace`. It runs without an Istio sidecar and talks only to the Kubernetes API, the Git host and its NFS mount. |
| `skillMarketplace.worker.resources.requests.cpu` | `"50m"` |  |
| `skillMarketplace.worker.resources.requests.memory` | `"128Mi"` |  |
| `skillMarketplace.worker.resources.limits.cpu` | `"1"` |  |
| `skillMarketplace.worker.resources.limits.memory` | `"1Gi"` |  |
| `skillMarketplace.worker.podAnnotations` | `{}` |  |
| `skillMarketplace.worker.nodeSelector` | `{}` |  |
| `skillMarketplace.worker.tolerations` | `[]` |  |
| `skillMarketplace.worker.affinity` | `{}` |  |
| `connectionsMCP` |  | Native MCP runtime integration and optional access to Agent credential records. |
| `connectionsMCP.nativeEnabled` | `false` |  |
| `connectionsMCP.url` | `""` |  |
| `connectionsMCP.audience` | `"cetirun-connections-mcp"` |  |
| `connectionsMCP.brokerNamespace` | `""` |  |
| `connectionsMCP.brokerServiceAccount` | `""` |  |
| `connectionsMCP.runtimeTypes` | `["hermes", "openclaw"]` |  |
| `connectionsMCP.caSecretName` | `""` |  |
| `agentIdentity` |  | Microsoft Entra Agent Identity lifecycle configuration. The feature is deployment-scoped and remains disabled unless every required field is set. |
| `agentIdentity.enabled` | `false` |  |
| `agentIdentity.cleanupEnabled` | `false` |  |
| `agentIdentity.tenantId` | `""` |  |
| `agentIdentity.blueprintClientId` | `""` |  |
| `agentIdentity.namePrefix` | `"cetirun-agent"` |  |
| `agentIdentity.brokerURL` | `"http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"` |  |
| `aiGateway` |  | Platform AI Gateway integration: the operator gives each built-in Agent its own virtual key and routes OpenClaw model traffic through the AI Gateway: the shared models every Agent may use and, per Agent, a GitHub Copilot proxy for the owner's account. The cetirun-platform chart installs the AI Gateway and requires this. While the operator has an AI Gateway to call, with enabled on or url set, deleting an Agent waits until its key is revoked; turning enabled off stops creating keys, not revoking them. Set neither to stop calling the AI Gateway, for example once it has been removed. |
| `aiGateway.enabled` | `true` | Every Agent harness takes its models from the platform AI Gateway. The platform chart requires it. Disabling it holds Agent execution; there is no platform-provided direct-provider fallback. |
| `aiGateway.url` | `""` | In-cluster AI Gateway URL: http://\<service>.\<namespace>.svc.cluster.local:\<port>. Empty means the AI Gateway named cetirun-ai-gateway in the operator namespace, where the platform chart installs it. |
| `aiGateway.peerNamespaces` | `[]` | Namespaces of the AI Gateway Pods that native Sandbox egress selects by the protected label sandbox.cetirun.microsoft.com/peer. With native execution on, an admission policy in these namespaces lets only the ReplicaSet controller create Pods with that label and refuses changes to it on existing Pods. Empty means the operator namespace. |
| `aiGateway.masterKeySecret` |  | Secret in the operator namespace that holds the AI Gateway master key. An AI Gateway installed separately must run in the operator namespace, or its master key Secret must be copied there. |
| `aiGateway.masterKeySecret.name` | `"cetirun-ai-gateway-auth"` |  |
| `aiGateway.masterKeySecret.key` | `"master-key"` |  |
| `aiGateway.defaultBudget` | `10000` | Monthly budget in US dollars written when a key is created. Changes an administrator makes later in the AI Gateway are kept. |
| `aiGateway.defaultModel` | `""` | Preferred default model of Agents that name none. Agents fall back to the first model available to them when it is not: a shared model, then a GitHub Copilot model, and an auto router of the AI Gateway only when nothing else is offered (a router picks a model per request, at a cost the Agent did not choose). Naming a router here or on the Agent makes it the default. |
| `aiGateway.copilotProxy` |  | Per-Agent GitHub Copilot proxy. Agents keep it unless their owner disables GitHub Copilot. It runs the AI Gateway image. When it is disabled here, OpenClaw Agents use only shared models, and the Portal creates no Agent while no shared model is offered. |
| `aiGateway.copilotProxy.enabled` | `true` |  |
| `aiGateway.copilotProxy.image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-ai-gateway"` |  |
| `aiGateway.copilotProxy.image.tag` | `""` | Defaults to the release version. |
| `aiGateway.copilotProxy.image.digest` | `""` |  |
| `aiGateway.copilotProxy.healthChecks` |  | Let the AI Gateway's background health checks probe each Agent's GitHub Copilot routes. Off by default: each probe is a real model call on the Agent owner's own Copilot allowance, about 288 a day per route at the AI Gateway's default interval, and an Agent has a route per Copilot model. On, the routes are probed like the AI Gateway's other deployments (its router.healthChecks), without retries. Off, a route whose proxy cannot be reached stays in rotation: where it shares a model's name with shared deployments, the requests that land on it fail until the proxy is back. A change registers each Agent's routes again within about ten minutes, without restarting Agents. |
| `aiGateway.copilotProxy.healthChecks.enabled` | `false` |  |
| `skillsStorageAccount` | `""` | Override the chart's default skills storage account. Empty string (default) means use the chart's built-in default. Deprecated: use cetirun.helmValues.skills.storageAccount instead. |
| `customRuntime` |  | Provisioning for approved Custom images. Qualify each image's UID/GID against this HOME storage. Every approved image must consume the platform AI Gateway binding (OPENAI_BASE_URL and OPENAI_API_KEY). |
| `customRuntime.enabled` | `true` |  |
| `customRuntime.approvedImages` | `[]` | Empty denies all Custom images until the administrator approves them. |
| `customRuntime.imageUsers` | `{}` | Numeric image identities required for approved Custom FQDN capture. Map each immutable approved image to {uid: N, gid: N}; UID 1337 is reserved. |
| `customRuntime.storageClass` | `"default"` |  |
| `customRuntime.storageSize` | `"100Gi"` |  |
| `customRuntime.accessMode` | `"ReadWriteOnce"` |  |
| `customRuntime.fsGroup` | `1024` |  |
| `customRuntime.context` | Keys: `AGENTS.md`, `skills/cetirun-memory/SKILL.md` |  |
| `azure` |  | Azure workload identity configuration for the Operator. |
| `azure.clientId` | `""` | Client ID of the operator's managed identity (enables workload identity). |
| `memoryContext.enabled` | `true` | Install Memory for Agents with a compatible pinned Chart. Request-level restrictions still apply. |
| `memoryContext.image` | `""` | Optional full image reference. Empty uses cetirun-memory-context from each selected agent chart's registry and appVersion. A digest may pin exact bytes. |

<!-- /generated: values-operator -->

## Portal chart

Source: [app/cetirun-portal/chart/values.yaml]({{< repo "app/cetirun-portal/chart/values.yaml" >}}).

<!-- generated: values-portal -->

| Value | Default | Description |
| --- | --- | --- |
| `global` |  | Shared release coordinates. An umbrella chart can set these once for all CetiRun subcharts. |
| `global.registry` | `""` | Optional registry hostname override for the Portal image. Empty preserves image.repository below. |
| `global.version` | `""` | Optional shared image and component version override. Empty preserves image.tag and Chart.appVersion defaults. |
| `global.registryCredentialNamespace` | `""` |  |
| `registryCredentials` |  | Created by the Operator chart, never by the Portal. |
| `registryCredentials.namespace` | `"cetirun-registry-credentials"` |  |
| `replicaCount` | `2` | Number of portal replicas. |
| `chat` |  | Optional Custom Chat. History stays in the user's browser. |
| `chat.enabled` | `false` |  |
| `chat.identityRealm` | `""` |  |
| `chat.sandboxStateNamespace` | `""` |  |
| `image` |  | Portal container image. |
| `image.repository` | `"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-portal"` |  |
| `image.pullPolicy` | `"IfNotPresent"` |  |
| `image.tag` | `""` |  |
| `image.digest` | `""` | Image digest for pinning (e.g. sha256:abc123...). When set, pins the image; the tag is kept for readability. |
| `imagePullSecrets` | `[]` | Image pull secrets. |
| `noticeBanner` | `""` | Optional notice banner message displayed in the portal UI. Set to a non-empty string to show a dismissible banner. Empty means no banner. |
| `istio` |  | Istio ingress configuration. |
| `istio.host` | `""` | Hostname for the portal VirtualService. |
| `istio.gateway` | `"aks-istio-ingress/cetirun-gateway"` | Istio gateway reference. |
| `istio.gatewayNamespace` | `"aks-istio-ingress"` | Namespace of the Istio ingress gateway (used by AuthorizationPolicy). |
| `istio.gatewayServiceAccount` | `"aks-istio-ingressgateway-external"` | Service account name of the Istio ingress gateway (used by AuthorizationPolicy). |
| `istio.legacyHost` | `""` | Optional legacy hostname to redirect to istio.host with a 301. Set to the old portal subdomain (e.g. portal.example.com) to redirect old users. Leave empty to disable. |
| `istio.sidecarInjection` | `true` | Confirm the target namespace has Istio sidecar injection enabled. Must be set to true; the AuthorizationPolicy requires mTLS enforcement. |
| `istio.internalAuthMTLS` | `false` | Require mesh mTLS on the internal ownership endpoint. Enable together with the Istio chart's portalAuth.mtls settings before forwarding ID tokens. |
| `resources` |  | Resource requests/limits. |
| `resources.requests.cpu` | `"100m"` |  |
| `resources.requests.memory` | `"256Mi"` |  |
| `resources.limits.cpu` | `"500m"` |  |
| `resources.limits.memory` | `"512Mi"` |  |
| `serviceAccount` |  | Service account configuration. |
| `serviceAccount.create` | `true` |  |
| `serviceAccount.name` | `"cetirun-portal"` |  |
| `serviceAccount.annotations` | `{}` |  |
| `nodeSelector` | `{}` | Node selector. |
| `tolerations` | `[]` | Tolerations. |
| `affinity` | `{}` | Affinity rules. |
| `podAnnotations` | `{}` | Additional pod annotations. |
| `admins` |  | Admin configuration. Aliases listed here can access /admin/* routes and admin APIs in the portal. Keep this list in sync with the istio chart's `authPolicy.adminAliases` (the istio deploy script sources both from the same `ADMIN_ALIASES` environment variable). |
| `admins.aliases` | `[]` |  |
| `features` |  | Feature flags. These control which features are exposed in the portal UI. |
| `features.tailscaleEnabled` | `false` | Enable the Tailscale integration feature. |
| `features.maxInstancesPerUser` | `1` | Maximum number of Agents a user may create. 0 = unlimited. |
| `features.allowedAgentTypes` | `["hermes", "openclaw", "custom"]` | Agent types users can create. Remove custom to disable Custom self-service. |
| `features.agentIdentityEnabled` | `false` | Enable the Microsoft Entra Agent Identity lifecycle for new Agents. |
| `features.agentToolsEnabled` | `false` | Enable owner-scoped account connections and Agent tool grants. |
| `features.aiGatewayEnabled` | `true` | Expose the platform AI Gateway: model sources for new OpenClaw and Hermes Agents, the GitHub Copilot sign-in and each Agent's model list. Keep it in step with the operator's aiGateway.enabled; the platform chart requires both. |
| `sandboxRuntimeDefaultsFromController` | `false` | Allow missing Sandbox runtime profiles when the Operator's common executor is enabled with sandboxSources.nativeExecution. Keep false when native execution is disabled. The platform chart rejects this opt-in without the matching Operator settings. |
| `sandboxRuntimeProfiles` | `{}` | Approved built-in runtime pins for Sandbox creation. Required unless sandboxRuntimeDefaultsFromController explicitly delegates missing profiles to the common controller, which pins inputs from its selected trusted chart. Each configured entry still needs chartVersion, a digest-pinned image and the matching port (OpenClaw 18789 or Hermes 8642); invalid entries reject creation before any Kubernetes write. Explicit requests remain unchanged. |
| `sandboxStorage` |  | Read-only source of the approved Sandbox HOME StorageClasses. The Portal offers only the verified classes that this platform ConfigMap pins. Configure `platformNamespace` and `platformConfigMapName` together. Leave both empty for a standalone Portal without this configuration. `defaultStorageClassName` is only a UI preference and does not approve a StorageClass. |
| `sandboxStorage.platformNamespace` | `""` |  |
| `sandboxStorage.platformConfigMapName` | `""` |  |
| `sandboxStorage.defaultStorageClassName` | `""` |  |
| `aiGateway.catalogNamespace` | `"cetirun-system"` | Namespace where the operator publishes the shared-model catalog (the operator's namespace). |
| `skillMarketplace` |  | Skill Marketplace: Team skills from one synchronized Git repository. When enabled, every Team skill without the npm: prefix is a marketplace skill: Team saves check new skills against the marketplace catalog, Policy saves keep skills a Team still selects, and Agent details show the Team's skills. Keep it in step with the operator's skillMarketplace.enabled. |
| `skillMarketplace.enabled` | `false` |  |
| `skillMarketplace.catalogNamespace` | `"cetirun-system"` | Namespace where the marketplace publishes its catalog (the operator's namespace). |
| `configurationCoordination` |  | Optional UID-pinned authority shared with strict Sandbox admission. Configure all three fields when protected AgentPolicy updates and deletions must use the platform's configuration-change coordination protocol. |
| `configurationCoordination.namespace` | `""` |  |
| `configurationCoordination.name` | `""` |  |
| `configurationCoordination.uid` | `""` |  |
| `agentIdentity.tenantId` | `""` |  |
| `agentIdentity.portalClientId` | `""` |  |
| `agentIdentity.brokerURL` | `"http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"` |  |
| `agentTools.runtimeTypes` | `["hermes", "openclaw"]` |  |
| `agentTools.brokerURL` | `"http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"` |  |

<!-- /generated: values-portal -->
