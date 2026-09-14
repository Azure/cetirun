{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

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
    {{- $config := deepCopy .Values.hermes.config }}
    {{- $provider := .Values.secrets.provider | default "github_copilot" }}
    {{- $hermesProvider := "copilot" }}
    {{- if eq $provider "openai" }}
    {{- $hermesProvider = "openai" }}
    {{- else if eq $provider "anthropic" }}
    {{- $hermesProvider = "anthropic" }}
    {{- end }}
    {{- $model := required "secrets.model must be set by the CetiRun operator or Helm values" .Values.secrets.model }}
    {{- $_ := set $config "model" (dict "default" $model "provider" $hermesProvider) }}
    {{- $msteamsAuthType := default "" .Values.msteamsAuthType }}
    {{- $teamsConfigured :=
        or
          (and
            (gt (int .Values.msteamsWebhookPort) 0)
            .Values.secrets.msteamsAppId
            .Values.secrets.msteamsTenantId
            (or
              (and (or (eq $msteamsAuthType "") (eq $msteamsAuthType "secret")) .Values.secrets.msteamsAppPassword)
              (or (eq $msteamsAuthType "managedIdentity") (eq $msteamsAuthType "certificate"))
            )
          )
          (and (gt (int .Values.msteamsWebhookPort) 0) .Values.secrets.existingSecret)
    }}
    {{- if $teamsConfigured }}
    {{- $platforms := dig "platforms" (dict) $config }}
    {{- $cetirunTeams := dict "enabled" true "extra" (dict "port" (int .Values.msteamsWebhookPort)) }}
    {{- $_ := set $platforms "cetirun_teams" $cetirunTeams }}
    {{- $_ := set $config "platforms" $platforms }}
    {{- end }}
    {{- if .Values.skills.shared }}
    {{- $skills := dig "skills" (dict) $config }}
    {{- $existingDirs := dig "external_dirs" (list) $skills }}
    {{- $_ := set $skills "external_dirs" (append $existingDirs .Values.skills.mountPath | uniq) }}
    {{- $_ := set $config "skills" $skills }}
    {{- end }}
    {{- if .Values.skills.npm }}
    {{- $skills := dig "skills" (dict) $config }}
    {{- $existingDirs := dig "external_dirs" (list) $skills }}
    {{- $npmDirs := include "cetirun.npmSkillDirs" . | fromJsonArray }}
    {{- $_ := set $skills "external_dirs" (concat $existingDirs $npmDirs | uniq) }}
    {{- $_ := set $config "skills" $skills }}
    {{- end }}
{{ $config | toYaml | indent 4 }}
  hermes-soul.md: |
{{ .Files.Get "personality/hermes/SOUL.md" | indent 4 }}
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
{{ .Files.Get "configs/hermes-init-home.sh" | indent 4 }}
  init-skills.sh: |
{{ .Files.Get "configs/init-skills.sh" | indent 4 }}
  hermes-auth.sh: |
{{ .Files.Get "configs/hermes-auth.sh" | indent 4 }}
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
