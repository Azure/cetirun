{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{- define "cetirun.agent.hermes.validateSecrets" -}}{{- end }}
{{- define "cetirun.agent.hermes.validateDesktopBrowserConfig" -}}{{- end }}

{{- define "cetirun.agent.hermes.validatePorts" -}}
{{- $apiPort := int .Values.hermes.env.API_SERVER_PORT -}}
{{- if and .Values.desktop.enabled (eq $apiPort 5900) -}}
{{- fail "hermes API port must not collide with desktop VNC port (5900)" }}
{{- end -}}
{{- if eq $apiPort 9119 -}}
{{- fail "hermes API port must not collide with dashboard port (9119)" }}
{{- end -}}
{{- if eq (int .Values.msteamsWebhookPort) 9119 -}}
{{- fail "MS Teams webhook port must not collide with Hermes dashboard port (9119)" }}
{{- end -}}
{{- if and .Values.desktop.enabled (eq $apiPort 6080) -}}
{{- fail "hermes API port must not collide with noVNC port (6080)" }}
{{- end -}}
{{- end }}

{{- define "cetirun.agent.hermes.validateTtydPort" -}}
{{- if .Values.ttyd.enabled -}}
{{- $apiPort := int .Values.hermes.env.API_SERVER_PORT -}}
{{- $ttydPort := int .Values.ttyd.port -}}
{{- if and .Values.desktop.enabled (eq $ttydPort 5900) -}}
{{- fail "ttyd.port must not collide with desktop VNC port (5900)" }}
{{- end -}}
{{- if eq $ttydPort 9119 -}}
{{- fail "ttyd.port (9119) must not collide with Hermes dashboard port (9119)" }}
{{- end -}}
{{- if eq $ttydPort $apiPort -}}
{{- fail (printf "ttyd.port (%d) must not collide with hermes API port (%d)" $ttydPort $apiPort) }}
{{- end -}}
{{- if and .Values.desktop.enabled (eq $ttydPort 6080) -}}
{{- fail "ttyd.port (6080) must not collide with noVNC port (6080)" }}
{{- end -}}
{{- end -}}
{{- end }}

{{- define "cetirun.agent.hermes.image" -}}
{{- $repo := .Values.hermes.image.repository -}}
{{- $tag := .Values.hermes.image.tag | default .Chart.AppVersion -}}
{{- $digest := .Values.hermes.image.digest -}}
{{ $repo }}:{{ $tag }}{{ if $digest }}@{{ $digest }}{{ end }}
{{- end }}

{{- define "cetirun.agent.hermes.imagePullPolicy" -}}
{{ .Values.hermes.image.pullPolicy | default .Values.image.pullPolicy | default "Always" }}
{{- end }}

{{/*
Hermes agent-specific named templates.
These are called by dispatcher templates in _helpers.tpl based on .Values.agentType.
*/}}

{{/*
Hermes init containers
*/}}
{{- define "cetirun.agent.hermes.initContainers" -}}
- name: init-home-data
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
  command: ["/bin/sh", "/etc/cetirun/hermes-init-home.sh"]
  env:
    - name: CETIRUN_INIT_IMAGE
      value: {{ include "cetirun.image" . | quote }}
    - name: HERMES_HOME
      value: "/home/vibe/.hermes"
  volumeMounts:
    - name: data
      mountPath: /home-data
    - name: tmp
      mountPath: /tmp
    - name: config
      mountPath: /etc/cetirun
      readOnly: true
    - name: secrets
      mountPath: /etc/cetirun-secrets
      readOnly: true
{{ include "cetirun.initSkillsContainer" . }}
{{- end }}

{{/*
Hermes container args (none)
*/}}
{{- define "cetirun.agent.hermes.args" -}}
{{- end }}

{{/*
Hermes container ports (agent-specific only)
*/}}
{{- define "cetirun.agent.hermes.ports" -}}
- name: api
  containerPort: {{ .Values.hermes.env.API_SERVER_PORT }}
  protocol: TCP
- name: dashboard
  containerPort: 9119
  protocol: TCP
- name: cetirun-auth
  containerPort: 18790
  protocol: TCP
{{- end }}

{{/*
Hermes container env vars (agent-specific only)
*/}}
{{- define "cetirun.agent.hermes.envVars" -}}
- name: HERMES_HOME
  value: "/home/vibe/.hermes"
- name: HERMES_DASHBOARD_ENABLED
  value: "true"
- name: HERMES_DASHBOARD_PORT
  value: "9119"
- name: API_SERVER_ENABLED
  value: {{ .Values.hermes.env.API_SERVER_ENABLED | quote }}
- name: API_SERVER_HOST
  value: {{ .Values.hermes.env.API_SERVER_HOST | quote }}
- name: API_SERVER_PORT
  value: {{ .Values.hermes.env.API_SERVER_PORT | quote }}
- name: API_SERVER_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: API_SERVER_KEY
- name: CETIRUN_AUTH_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: CETIRUN_AUTH_KEY
- name: CETIRUN_AGENT_TYPE
  value: "hermes"
{{- end }}

{{/*
Hermes liveness/readiness probe path and port
*/}}
{{- define "cetirun.agent.hermes.probePathPort" -}}
path: /health
port: api
{{- end }}

{{/*
Hermes startup probe path and port
*/}}
{{- define "cetirun.agent.hermes.startupProbePathPort" -}}
path: /health
port: api
{{- end }}

{{/*
Hermes Tailscale serve port value
*/}}
{{- define "cetirun.agent.hermes.tailscaleServePort" -}}
{{ .Values.hermes.env.API_SERVER_PORT | quote }}
{{- end }}

{{/*
Hermes logger volumeMount
*/}}
{{- define "cetirun.agent.hermes.loggerVolumeMount" -}}
- name: data
  mountPath: /home/vibe/.hermes
  subPath: .hermes
  readOnly: true
{{- end }}

{{/*
Hermes service ports (agent-specific only)
*/}}
{{- define "cetirun.agent.hermes.servicePorts" -}}
- port: {{ .Values.hermes.env.API_SERVER_PORT }}
  targetPort: api
  protocol: TCP
  name: api
- port: 9119
  targetPort: dashboard
  protocol: TCP
  name: dashboard
- port: 18790
  targetPort: cetirun-auth
  protocol: TCP
  name: cetirun-auth
{{- end }}

{{/*
Hermes networkpolicy ingress ports (agent-specific only)
*/}}
{{- define "cetirun.agent.hermes.networkPolicyIngressPorts" -}}
- protocol: TCP
  port: {{ .Values.hermes.env.API_SERVER_PORT }}
- protocol: TCP
  port: 9119
- protocol: TCP
  port: 18790
{{- end }}

{{/*
Hermes configmap data
*/}}
{{- define "cetirun.agent.hermes.configmapData" }}
  hermes-config.yaml: |
    {{- if .Values.runtime }}
{{ index .Values.runtime.configFiles "hermes-config.yaml" | indent 4 }}
    {{- else }}
    {{- $context := dict "Values" .Values "Chart" .Chart "SkillDirs" (include "cetirun.npmSkillDirs" . | fromJsonArray) "TailscaleEnabled" (include "cetirun.tailscaleEnabled" .) }}
{{ tpl (.Files.Get "agents/hermes/config/native.tpl") $context | trim | indent 4 }}
    {{- end }}
  hermes-soul.md: |
{{ .Files.Get "agents/hermes/personality/SOUL.md" | indent 4 }}
  hermes-user.md: |
    § Name: {{ .Values.user.displayName | default "" }}
    § Alias: {{ .Values.user.alias | default "" }}
  hermes-team.md: |
    {{- if or .Values.user.role .Values.user.team .Values.user.description }}
    {{- if .Values.user.role }}
    § Role: {{ .Values.user.role }}
    {{- end }}
    {{- if .Values.user.team }}
    § Team: {{ .Values.user.team }}
    {{- end }}
    {{- if .Values.user.description }}
    § Notes:
    {{- range .Values.user.description }}
    § - {{ . }}
    {{- end }}
    {{- end }}
    {{- end }}
  hermes-init-home.sh: |
{{ .Files.Get "agents/hermes/bootstrap/hermes-init-home.sh" | indent 4 }}
  hermes-apply-config.py: |
{{ .Files.Get "agents/hermes/bootstrap/hermes-apply-config.py" | indent 4 }}
  init-skills.sh: |
{{ .Files.Get "configs/init-skills.sh" | indent 4 }}
  hermes-auth.sh: |
{{ .Files.Get "agents/hermes/bootstrap/hermes-auth.sh" | indent 4 }}
  session-logger.sh: |
{{ .Files.Get "configs/session-logger.sh" | indent 4 }}
{{- end }}

{{/*
Hermes secret data (agent-specific only).
Receives the standard chart context (.). Uses lookup to find existing secrets.
*/}}
{{- define "cetirun.agent.hermes.secretData" -}}
  {{- $secretName := include "cetirun.secretName" . -}}
  {{- $existingSecret := lookup "v1" "Secret" .Release.Namespace $secretName -}}
  {{- $apiServerKey := .Values.hermes.env.API_SERVER_KEY | default "" }}
  {{- if and (not $apiServerKey) $existingSecret $existingSecret.data (index $existingSecret.data "API_SERVER_KEY") }}
  {{- $apiServerKey = (index $existingSecret.data "API_SERVER_KEY" | b64dec) }}
  {{- else if not $apiServerKey }}
  {{- $apiServerKey = randAlphaNum 48 }}
  {{- end }}

  {{- /* API_SERVER_KEY and hermes.env for Hermes API server. */}}
  API_SERVER_KEY: {{ $apiServerKey | b64enc | quote }}
  {{- $openaiKey := "" }}
  {{- $anthropicKey := "" }}
  {{- if eq (.Values.secrets.provider | default "github_copilot") "openai" }}
  {{- $openaiKey = .Values.secrets.apiKey | default "" }}
  {{- else if eq (.Values.secrets.provider | default "github_copilot") "anthropic" }}
  {{- $anthropicKey = .Values.secrets.apiKey | default "" }}
  {{- end }}
  {{- $hermesEnv := printf "API_SERVER_ENABLED=%s\nAPI_SERVER_HOST=%s\nAPI_SERVER_PORT=%s\nAPI_SERVER_KEY=%s\nAPI_SERVER_CORS_ORIGINS=%s\nOPENAI_API_KEY=%s\nANTHROPIC_API_KEY=%s\nBRAVE_API_KEY=%s\nPERPLEXITY_API_KEY=%s\n" (.Values.hermes.env.API_SERVER_ENABLED | toString) (.Values.hermes.env.API_SERVER_HOST | toString) (.Values.hermes.env.API_SERVER_PORT | toString) $apiServerKey (.Values.hermes.env.API_SERVER_CORS_ORIGINS | toString) $openaiKey $anthropicKey (.Values.secrets.braveApiKey | default "") (.Values.secrets.perplexityApiKey | default "") }}
  hermes.env: {{ $hermesEnv | b64enc | quote }}
{{- end }}
