{{/*
Expand the name of the chart.
*/}}
{{- define "cetirun.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "cetirun.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "cetirun.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "cetirun.labels" -}}
helm.sh/chart: {{ include "cetirun.chart" . }}
{{ include "cetirun.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "cetirun.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cetirun.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "cetirun.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cetirun.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the secret to use
*/}}
{{- define "cetirun.secretName" -}}
{{- if .Values.secrets.existingSecret }}
{{- .Values.secrets.existingSecret }}
{{- else }}
{{- include "cetirun.fullname" . }}
{{- end }}
{{- end }}

{{/*
Create the name of the configmap to use
*/}}
{{- define "cetirun.configMapName" -}}
{{- printf "%s-config" (include "cetirun.fullname" .) }}
{{- end }}

{{/*
Create the name of the PVC to use
*/}}
{{- define "cetirun.pvcName" -}}
{{- if .Values.persistence.existingClaim }}
{{- .Values.persistence.existingClaim }}
{{- else }}
{{- printf "%s-data" (include "cetirun.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Validate that required secrets are provided when not using existing secret
*/}}
{{- define "cetirun.validateSecrets" -}}
{{- $agentType := .Values.agentType | default "hermes" -}}
{{- if not (has $agentType (list "openclaw" "hermes")) -}}
{{- fail (printf "agentType must be one of: openclaw, hermes; got %q" $agentType) -}}
{{- end -}}
{{- if and (eq $agentType "openclaw") (not .Values.secrets.existingSecret) }}
{{- if not .Values.secrets.openclawGatewayToken }}
{{- fail "secrets.openclawGatewayToken is required when not using an existing secret" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Validate Azure Workload Identity prerequisites
*/}}
{{- define "cetirun.validateWorkloadIdentity" -}}
{{- if .Values.azureWorkloadIdentity.enabled -}}
{{- if and (not .Values.serviceAccount.create) (not .Values.serviceAccount.name) -}}
{{- fail "azureWorkloadIdentity.enabled requires serviceAccount.create=true or serviceAccount.name (a pre-annotated BYO service account)" }}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Validate autoscaling settings for single-instance app
*/}}
{{- define "cetirun.validateAutoscaling" -}}
{{- if .Values.autoscaling.enabled -}}
{{- if or (ne (int .Values.autoscaling.minReplicas) 1) (ne (int .Values.autoscaling.maxReplicas) 1) -}}
{{- fail "autoscaling.enabled requires minReplicas=1 and maxReplicas=1 for CetiRun agents" }}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Validate port consistency between service.targetPort and openclaw.config.gateway.port
*/}}
{{- define "cetirun.validatePorts" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- $apiPort := int .Values.hermes.env.API_SERVER_PORT -}}
{{- if eq $apiPort 9119 -}}
{{- fail "hermes API port must not collide with dashboard port (9119)" }}
{{- end -}}
{{- if eq (int .Values.msteamsWebhookPort) 9119 -}}
{{- fail "MS Teams webhook port must not collide with Hermes dashboard port (9119)" }}
{{- end -}}
{{- if and .Values.desktop.enabled (eq $apiPort 6080) -}}
{{- fail "hermes API port must not collide with noVNC port (6080)" }}
{{- end -}}
{{- else -}}
{{- if ne (int .Values.service.targetPort) (int .Values.openclaw.config.gateway.port) -}}
{{- fail (printf "service.targetPort (%d) must match openclaw.config.gateway.port (%d)" (int .Values.service.targetPort) (int .Values.openclaw.config.gateway.port)) }}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Validate ttyd port does not collide with gateway or noVNC ports
*/}}
{{- define "cetirun.validateTtydPort" -}}
{{- if .Values.ttyd.enabled -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- $apiPort := int .Values.hermes.env.API_SERVER_PORT -}}
{{- $ttydPort := int .Values.ttyd.port -}}
{{- if eq $ttydPort 9119 -}}
{{- fail "ttyd.port (9119) must not collide with Hermes dashboard port (9119)" }}
{{- end -}}
{{- if eq $ttydPort $apiPort -}}
{{- fail (printf "ttyd.port (%d) must not collide with hermes API port (%d)" $ttydPort $apiPort) }}
{{- end -}}
{{- if and .Values.desktop.enabled (eq $ttydPort 6080) -}}
{{- fail "ttyd.port (6080) must not collide with noVNC port (6080)" }}
{{- end -}}
{{- else -}}
{{- if or (eq (int .Values.ttyd.port) (int .Values.service.targetPort)) (eq (int .Values.ttyd.port) 6080) -}}
{{- fail (printf "ttyd.port (%d) must not collide with gateway port (%d) or noVNC port (6080)" (int .Values.ttyd.port) (int .Values.service.targetPort)) }}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Return the appropriate apiVersion for ingress
*/}}
{{- define "cetirun.ingress.apiVersion" -}}
{{- if and (.Capabilities.APIVersions.Has "networking.k8s.io/v1") (semverCompare ">= 1.19-0" .Capabilities.KubeVersion.Version) }}
{{- print "networking.k8s.io/v1" }}
{{- else if .Capabilities.APIVersions.Has "networking.k8s.io/v1beta1" }}
{{- print "networking.k8s.io/v1beta1" }}
{{- else }}
{{- print "extensions/v1beta1" }}
{{- end }}
{{- end }}

{{/*
Return true if the ingress supports pathType
*/}}
{{- define "cetirun.ingress.supportsPathType" -}}
{{- if eq (include "cetirun.ingress.apiVersion" .) "networking.k8s.io/v1" }}
{{- print "true" }}
{{- else }}
{{- print "false" }}
{{- end }}
{{- end }}

{{/*
Validate shared skills configuration
*/}}
{{- define "cetirun.validateSkills" -}}
{{- if .Values.skills.shared -}}
{{- if not .Values.skills.resourceGroup -}}
{{- fail "skills.resourceGroup is required when skills.shared is true" }}
{{- end -}}
{{- if not .Values.skills.storageAccount -}}
{{- fail "skills.storageAccount is required when skills.shared is true" }}
{{- end -}}
{{- if not .Values.skills.containerName -}}
{{- fail "skills.containerName is required when skills.shared is true" }}
{{- end -}}
{{- if not .Values.skills.mountPath -}}
{{- fail "skills.mountPath is required when skills.shared is true" }}
{{- end -}}
{{- if not .Values.skills.storageSize -}}
{{- fail "skills.storageSize is required when skills.shared is true" }}
{{- end -}}
{{- if not .Values.skills.storageClassName -}}
{{- fail "skills.storageClassName is required when skills.shared is true" }}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Derive whether Tailscale is enabled.
True when secrets.tailscaleAuthKey is set in values, OR when the
resolved Secret (chart-managed or existingSecret) already contains a
non-empty TAILSCALE_AUTH_KEY (covers helm-upgrade and operator-managed
scenarios where the key is written directly into the Secret).
*/}}
{{- define "cetirun.tailscaleEnabled" -}}
{{- if .Values.secrets.tailscaleAuthKey -}}
true
{{- else -}}
{{- $secretName := include "cetirun.secretName" . -}}
{{- $existingSecret := lookup "v1" "Secret" .Release.Namespace $secretName -}}
{{- if and $existingSecret $existingSecret.data -}}
{{- $key := index $existingSecret.data "TAILSCALE_AUTH_KEY" | default "" -}}
{{- if $key -}}
true
{{- end -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Container image reference with optional digest pinning.
When image.digest is set, produces: repository:tag@digest
Otherwise: repository:tag
*/}}
{{- define "cetirun.image" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- $repo := .Values.hermes.image.repository -}}
{{- $tag := .Values.hermes.image.tag | default .Chart.AppVersion -}}
{{- $digest := .Values.hermes.image.digest -}}
{{ $repo }}:{{ $tag }}{{ if $digest }}@{{ $digest }}{{ end }}
{{- else -}}
{{- $repo := .Values.image.repository | default .Values.openclaw.image.repository | default "cetirun.azurecr.io/openclaw-gateway" -}}
{{- $tag := .Values.image.tag | default .Values.openclaw.image.tag | default .Chart.AppVersion -}}
{{- $digest := .Values.image.digest | default .Values.openclaw.image.digest -}}
{{ $repo }}:{{ $tag }}{{ if $digest }}@{{ $digest }}{{ end }}
{{- end -}}
{{- end }}

{{/*
Container image pull policy for the selected agent image.
*/}}
{{- define "cetirun.imagePullPolicy" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{ .Values.hermes.image.pullPolicy | default .Values.image.pullPolicy | default "Always" }}
{{- else -}}
{{ .Values.image.pullPolicy | default .Values.openclaw.image.pullPolicy | default "Always" }}
{{- end }}
{{- end }}

{{/*
Shared skills PV name (cluster-scoped, globally unique).
*/}}
{{- define "cetirun.sharedSkillsPVName" -}}
{{- printf "%s-%s-shared-skills" (include "cetirun.fullname" .) .Release.Namespace | trunc 253 | trimSuffix "-" }}
{{- end }}

{{/*
Shared skills PVC name (namespace-scoped).
*/}}
{{- define "cetirun.sharedSkillsPVCName" -}}
{{- printf "%s-shared-skills" (include "cetirun.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Validate that browser attach-only mode does not point at the local desktop
CDP endpoint when the desktop sidecar is disabled.
*/}}
{{- define "cetirun.validateDesktopBrowserConfig" -}}
{{- if and (eq (.Values.agentType | default "hermes") "openclaw") (not .Values.desktop.enabled) (dig "browser" "enabled" true .Values.openclaw.config) (dig "browser" "attachOnly" false .Values.openclaw.config) -}}
{{- fail "desktop.enabled=false is incompatible with browser.attachOnly=true; enable the desktop sidecar or set openclaw.config.browser.attachOnly=false" -}}
{{- end -}}
{{- end }}

{{/*
Desktop sidecar container image reference with optional digest pinning.
When desktop.image.digest is set, produces: repository:tag@digest
Otherwise: repository:tag
*/}}
{{- define "cetirun.desktopImage" -}}
{{- include "cetirun.validateDesktopBrowserConfig" . -}}
{{ .Values.desktop.image.repository }}:{{ .Values.desktop.image.tag | default .Chart.AppVersion }}{{ if .Values.desktop.image.digest }}@{{ .Values.desktop.image.digest }}{{ end }}
{{- end }}

{{/*
=== Agent-type dispatcher templates ===
These dispatch to the appropriate agent-specific template based on .Values.agentType.
*/}}

{{/*
Dispatcher: init containers
*/}}
{{- define "cetirun.agent.initContainers" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.initContainers" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.initContainers" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: container args
*/}}
{{- define "cetirun.agent.args" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.args" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.args" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: container ports
*/}}
{{- define "cetirun.agent.ports" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.ports" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.ports" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: container env vars (agent-specific)
*/}}
{{- define "cetirun.agent.envVars" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.envVars" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.envVars" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: liveness/readiness probe path and port
*/}}
{{- define "cetirun.agent.probePathPort" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.probePathPort" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.probePathPort" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: startup probe path and port
*/}}
{{- define "cetirun.agent.startupProbePathPort" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.startupProbePathPort" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.startupProbePathPort" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: Tailscale serve port
*/}}
{{- define "cetirun.agent.tailscaleServePort" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.tailscaleServePort" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.tailscaleServePort" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: logger volumeMount
*/}}
{{- define "cetirun.agent.loggerVolumeMount" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.loggerVolumeMount" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.loggerVolumeMount" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: service ports
*/}}
{{- define "cetirun.agent.servicePorts" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.servicePorts" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.servicePorts" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: network policy ingress ports
*/}}
{{- define "cetirun.agent.networkPolicyIngressPorts" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.networkPolicyIngressPorts" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.networkPolicyIngressPorts" . -}}
{{- end -}}
{{- end }}

{{/*
Shared init container that installs npm-packaged skills onto the PVC.
Rendered only when .Values.skills.npm is non-empty. Used by both agents.
Both the openclaw-gateway and hermes-gateway images intentionally bundle
node/npm at /usr/local/bin/npm so this init container can run regardless
of agentType — see the agent Dockerfiles.
*/}}
{{- define "cetirun.initSkillsContainer" -}}
{{- if .Values.skills.npm }}
- name: init-skills
  image: "{{ include "cetirun.image" . }}"
  imagePullPolicy: {{ include "cetirun.imagePullPolicy" . }}
  securityContext:
    runAsUser: 1024
    runAsNonRoot: true
    allowPrivilegeEscalation: false
    capabilities:
      drop:
        - ALL
    seccompProfile:
      type: RuntimeDefault
  command: ["/bin/sh", "/etc/cetirun/init-skills.sh"]
  env:
    - name: NPM_PACKAGES
      value: {{ join " " .Values.skills.npm | quote }}
    - name: SKILLS_DIR
      value: "/home-data/.cetirun/skills/npm"
    - name: NPM_CACHE_DIR
      value: "/home-data/.cetirun/.npm-cache"
    {{- with .Values.skills.npmRegistry }}
    - name: NPM_REGISTRY
      value: {{ . | quote }}
    {{- end }}
  volumeMounts:
    - name: data
      mountPath: /home-data
    - name: tmp
      mountPath: /tmp
    - name: config
      mountPath: /etc/cetirun
      readOnly: true
{{- end }}
{{- end }}

{{/*
Helper: list of npm skill external dirs (one per package), used by both
agents to extend their skill load path. Sanitization mirrors init-skills.sh:
  "@scope/name@1.2.3" -> "/home/vibe/.cetirun/skills/npm/@scope__name"
  "matrix"            -> "/home/vibe/.cetirun/skills/npm/matrix"
*/}}
{{- define "cetirun.npmSkillDirs" -}}
{{- $dirs := list -}}
{{- range .Values.skills.npm -}}
  {{- $spec := . | toString -}}
  {{- $name := "" -}}
  {{- if hasPrefix "@" $spec -}}
    {{- $rest := trimPrefix "@" $spec -}}
    {{- $name = printf "@%s" (index (splitList "@" $rest) 0) -}}
  {{- else -}}
    {{- $name = index (splitList "@" $spec) 0 -}}
  {{- end -}}
  {{- $dir := replace "/" "__" $name -}}
  {{- $dirs = append $dirs (printf "/home/vibe/.cetirun/skills/npm/%s" $dir) -}}
{{- end -}}
{{- $dirs | toJson -}}
{{- end }}

{{/*
Dispatcher: configmap data
*/}}
{{- define "cetirun.agent.configmapData" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.configmapData" . -}}
{{- else -}}
{{- include "cetirun.agent.openclaw.configmapData" . -}}
{{- end -}}
{{- end }}

{{/*
Dispatcher: secret data
*/}}
{{- define "cetirun.agent.secretData" -}}
{{- if eq (.Values.agentType | default "hermes") "hermes" -}}
{{- include "cetirun.agent.hermes.secretData" . -}}
{{- end -}}
{{- end }}
