{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{/*
OpenClaw agent-specific named templates.
These are called by dispatcher templates in _helpers.tpl based on .Values.agentType.
*/}}

{{/*
OpenClaw init containers
*/}}
{{- define "cetirun.agent.openclaw.initContainers" -}}
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
  command: ["/bin/sh", "/etc/cetirun/init-home.sh"]
  {{- if dig "git" "azureDevOps" "enabled" true .Values.openclaw }}
  env:
    - name: AZDEVOPS_GIT_CREDENTIAL_ENABLED
      value: "true"
  {{- end }}
  volumeMounts:
    - name: data
      mountPath: /home-data
    - name: tmp
      mountPath: /tmp
    - name: config
      mountPath: /etc/cetirun
      readOnly: true
{{- if and .Values.openclaw.doctor.enabled .Values.doctor.enabled }}
- name: doctor
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
  command: ["openclaw", "doctor", "--fix", "--non-interactive", "--yes"]
  env:
    - name: OPENCLAW_STATE_DIR
      value: "/home/vibe/.openclaw"
    - name: OPENCLAW_CONFIG_PATH
      value: "/home/vibe/.openclaw/openclaw.json"
  volumeMounts:
    - name: data
      mountPath: /home/vibe
    - name: tmp
      mountPath: /tmp
{{- end }}
{{ include "cetirun.initSkillsContainer" . }}
{{- end }}

{{/*
OpenClaw container args
*/}}
{{- define "cetirun.agent.openclaw.args" -}}
args:
  - --bind
  - lan
  - --port
  - {{ .Values.openclaw.config.gateway.port | quote }}
{{- end }}

{{/*
OpenClaw container ports (agent-specific only)
*/}}
{{- define "cetirun.agent.openclaw.ports" -}}
- name: gateway
  containerPort: {{ .Values.service.targetPort }}
  protocol: TCP
- name: cetirun-auth
  containerPort: 18790
  protocol: TCP
{{- end }}

{{/*
OpenClaw container env vars (agent-specific only)
*/}}
{{- define "cetirun.agent.openclaw.envVars" -}}
- name: OPENCLAW_STATE_DIR
  value: "/home/vibe/.openclaw"
- name: BRAVE_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: BRAVE_API_KEY
      optional: true
- name: PERPLEXITY_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: PERPLEXITY_API_KEY
      optional: true
{{- if eq (.Values.secrets.provider | default "github_copilot") "openai" }}
- name: OPENAI_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: MODEL_API_KEY
      optional: true
{{- else if eq (.Values.secrets.provider | default "github_copilot") "anthropic" }}
- name: ANTHROPIC_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: MODEL_API_KEY
      optional: true
{{- end }}
# Gateway token (required)
- name: OPENCLAW_GATEWAY_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: OPENCLAW_GATEWAY_TOKEN
# Config file path
- name: OPENCLAW_CONFIG_PATH
  value: "/home/vibe/.openclaw/openclaw.json"
# Bearer key for the in-pod cetirun-auth helper service used by the
# Portal "Copilot Auth" UI. Auto-generated and preserved across upgrades.
- name: CETIRUN_AUTH_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun.secretName" . }}
      key: CETIRUN_AUTH_KEY
- name: CETIRUN_AGENT_TYPE
  value: "openclaw"
{{- end }}

{{/*
OpenClaw liveness/readiness probe path and port
*/}}
{{- define "cetirun.agent.openclaw.probePathPort" -}}
path: /healthz
port: gateway
{{- end }}

{{/*
OpenClaw startup probe path and port
*/}}
{{- define "cetirun.agent.openclaw.startupProbePathPort" -}}
path: /health
port: gateway
{{- end }}

{{/*
OpenClaw Tailscale serve port value
*/}}
{{- define "cetirun.agent.openclaw.tailscaleServePort" -}}
{{ .Values.openclaw.config.gateway.port | quote }}
{{- end }}

{{/*
OpenClaw logger volumeMount
*/}}
{{- define "cetirun.agent.openclaw.loggerVolumeMount" -}}
- name: data
  mountPath: /home/vibe/.openclaw
  subPath: .openclaw
  readOnly: true
{{- end }}

{{/*
OpenClaw service ports (agent-specific only)
*/}}
{{- define "cetirun.agent.openclaw.servicePorts" -}}
- port: {{ .Values.service.port }}
  targetPort: {{ .Values.service.targetPort }}
  protocol: TCP
  name: gateway
  {{- if eq .Values.service.type "NodePort" }}
  {{- with .Values.service.nodePort }}
  nodePort: {{ . }}
  {{- end }}
  {{- end }}
- port: 18790
  targetPort: cetirun-auth
  protocol: TCP
  name: cetirun-auth
{{- end }}

{{/*
OpenClaw networkpolicy ingress ports (agent-specific only)
*/}}
{{- define "cetirun.agent.openclaw.networkPolicyIngressPorts" -}}
- protocol: TCP
  port: {{ .Values.service.targetPort }}
- protocol: TCP
  port: 18790
{{- end }}

{{/*
OpenClaw configmap data
*/}}
{{- define "cetirun.agent.openclaw.configmapData" }}
  openclaw.json: |
    {{- $config := deepCopy .Values.openclaw.config }}
    {{- /* Model provider auth profile and default model.
       Supports: github_copilot (token-based), openai (api-key), anthropic (api-key). */}}
    {{- $provider := .Values.secrets.provider | default "github_copilot" }}
    {{- $model := required "secrets.model must be set by the CetiRun operator or Helm values" .Values.secrets.model }}
    {{- $authMode := .Values.secrets.authMode }}
    {{- $auth := dig "auth" (dict) $config }}
    {{- if eq $provider "github_copilot" }}
    {{- $_ := set $auth "profiles" (dict "github-copilot:github" (dict "provider" "github-copilot" "mode" "token")) }}
    {{- else if eq $provider "openai" }}
    {{- $profile := dict "provider" "openai" }}
    {{- if $authMode }}
    {{- $_ := set $profile "mode" $authMode }}
    {{- end }}
    {{- $_ := set $auth "profiles" (dict "openai:default" $profile) }}
    {{- else if eq $provider "anthropic" }}
    {{- $profile := dict "provider" "anthropic" }}
    {{- if $authMode }}
    {{- $_ := set $profile "mode" $authMode }}
    {{- end }}
    {{- $_ := set $auth "profiles" (dict "anthropic:default" $profile) }}
    {{- end }}
    {{- $_ := set $config "auth" $auth }}
    {{- /* Compose full model identifier as <provider>/<model> */}}
    {{- $providerPrefix := $provider | replace "_" "-" }}
    {{- $modelFull := printf "%s/%s" $providerPrefix $model }}
    {{- /* Default model and compaction settings */}}
    {{- $compaction := dict "reserveTokensFloor" (int 20000) "memoryFlush" (dict "enabled" true "softThresholdTokens" (int 4000)) }}
    {{- $modelConfig := dict }}
    {{- $agentsCurrent := dig "agents" (dict) $config }}
    {{- $defaultsCurrent := dig "defaults" (dict) $agentsCurrent }}
    {{- $_ := set $defaultsCurrent "compaction" $compaction }}
    {{- if eq $provider "github_copilot" }}
    {{- $fallbacks := list }}
    {{- $_ := set $defaultsCurrent "model" (dict "primary" $modelFull "fallbacks" $fallbacks) }}
    {{- $modelsDict := dict $modelFull $modelConfig }}
    {{- range $fallbacks }}
    {{- $_ := set $modelsDict . (dict) }}
    {{- end }}
    {{- $_ := set $defaultsCurrent "models" $modelsDict }}
    {{- else }}
    {{- $_ := set $defaultsCurrent "model" (dict "primary" $modelFull) }}
    {{- $_ := set $defaultsCurrent "models" (dict $modelFull $modelConfig) }}
    {{- end }}
    {{- $_ := set $agentsCurrent "defaults" $defaultsCurrent }}
    {{- $_ := set $config "agents" $agentsCurrent }}
    {{- /* Custom API base URL → models.providers.<provider>.baseUrl */}}
    {{- if and (ne $provider "github_copilot") .Values.secrets.apiBase }}
    {{- $modelEntry := dict "id" $model "name" $model }}
    {{- $providerEntry := dict "baseUrl" .Values.secrets.apiBase "models" (list $modelEntry) }}
    {{- $models := dig "models" (dict) $config }}
    {{- $providers := dig "providers" (dict) $models }}
    {{- $_ := set $providers $providerPrefix $providerEntry }}
    {{- $_ := set $models "providers" $providers }}
    {{- $_ := set $config "models" $models }}
    {{- end }}
    {{- /* Force tools.profile=full so coding/system tools (Bash, Read, Edit, etc.)
       are always available. Newer OpenClaw defaults to "messaging" which strips them. */}}
    {{- $tools := dig "tools" (dict) $config }}
    {{- $_ := set $tools "profile" "full" }}
    {{- $sessions := dig "sessions" (dict) $tools }}
    {{- $_ := set $sessions "visibility" "all" }}
    {{- $_ := set $tools "sessions" $sessions }}
    {{- $_ := set $config "tools" $tools }}
    {{- /* Enable web search when Brave or Perplexity API key is configured */}}
    {{- if or .Values.secrets.braveApiKey .Values.secrets.perplexityApiKey }}
    {{- $webSearchProvider := "brave" }}
    {{- if .Values.secrets.perplexityApiKey }}
    {{- $webSearchProvider = "perplexity" }}
    {{- end }}
    {{- $webSearch := dict "enabled" true "provider" $webSearchProvider }}
    {{- $tools := dig "tools" (dict) $config }}
    {{- $web := dig "web" (dict) $tools }}
    {{- $_ := set $web "search" $webSearch }}
    {{- $_ := set $tools "web" $web }}
    {{- $_ := set $config "tools" $tools }}
    {{- end }}
    {{- $pluginEntries := dig "plugins" "entries" (dict) $config }}
    {{- $pluginsAllow := dig "plugins" "allow" (list) $config }}
    {{- if or .Values.secrets.telegramBotToken .Values.secrets.telegramTokenFile }}
    {{- $_ := set $pluginEntries "telegram" (dict "enabled" true) }}
    {{- end }}
    {{- if and .Values.secrets.slackBotToken .Values.secrets.slackAppToken }}
    {{- $_ := set $pluginEntries "slack" (dict "enabled" true) }}
    {{- end }}
    {{- if .Values.secrets.discordBotToken }}
    {{- $_ := set $pluginEntries "discord" (dict "enabled" true) }}
    {{- end }}
    {{- if and .Values.secrets.feishuAppId .Values.secrets.feishuAppSecret }}
    {{- $_ := set $pluginEntries "feishu" (dict "enabled" true) }}
    {{- end }}
    {{- $msteamsAuthType := default "" .Values.msteamsAuthType }}
    {{- if or
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
    {{- $_ := set $pluginEntries "msteams" (dict "enabled" true) }}
    {{- end }}
    {{- /* Enable ACPX plugin when acpx.enabled is true */}}
    {{- if .Values.openclaw.acpx.enabled }}
    {{- $acpxConfig := dict
      "permissionMode" .Values.openclaw.acpx.permissionMode
      "nonInteractivePermissions" .Values.openclaw.acpx.nonInteractivePermissions
    }}
    {{- $_ := set $pluginEntries "acpx" (dict "enabled" true "config" $acpxConfig) }}
    {{- /* Explicitly allow the acpx plugin to suppress non-bundled plugin warnings */}}
    {{- if not (has "acpx" $pluginsAllow) }}
    {{- $pluginsAllow = append $pluginsAllow "acpx" }}
    {{- end }}
    {{- $acp := dict
      "enabled" true
      "dispatch" (dict "enabled" .Values.openclaw.acpx.dispatch.enabled)
      "backend" .Values.openclaw.acpx.backend
      "defaultAgent" .Values.openclaw.acpx.defaultAgent
      "allowedAgents" .Values.openclaw.acpx.allowedAgents
      "maxConcurrentSessions" (.Values.openclaw.acpx.maxConcurrentSessions | int)
      "stream" (dict
        "coalesceIdleMs" (.Values.openclaw.acpx.stream.coalesceIdleMs | int)
        "maxChunkChars" (.Values.openclaw.acpx.stream.maxChunkChars | int)
      )
      "runtime" (dict
        "ttlMinutes" (.Values.openclaw.acpx.runtime.ttlMinutes | int)
      )
    }}
    {{- $_ := set $config "acp" $acp }}
    {{- end }}
    {{- if or $pluginEntries (gt (int .Values.msteamsWebhookPort) 0) }}
    {{- $plugins := dig "plugins" (dict) $config }}
    {{- $_ := set $plugins "entries" $pluginEntries }}
    {{- if $pluginsAllow }}
    {{- $_ := set $plugins "allow" $pluginsAllow }}
    {{- end }}
    {{- /* Plugins (msteams, acpx) are npm-installed and auto-discovered — only allowlist needed */}}
    {{- if gt (int $.Values.msteamsWebhookPort) 0 }}
    {{- if not (has "msteams" $pluginsAllow) }}
    {{- $pluginsAllow = append $pluginsAllow "msteams" }}
    {{- $_ := set $plugins "allow" $pluginsAllow }}
    {{- end }}
    {{- end }}
    {{- $_ := set $config "plugins" $plugins }}
    {{- end }}
    {{- /* Fix Telegram connectivity on dual-stack (IPv4+IPv6) clusters.
       Node 22+ enables Happy Eyeballs (autoSelectFamily) by default, which
       causes ENETUNREACH when IPv6 is configured but unreachable. Disable
       autoSelectFamily at the config level so it applies to both gateway
       and embedded agent modes. */}}
    {{- $channels := dig "channels" (dict) $config }}
    {{- $telegram := dig "telegram" (dict) $channels }}
    {{- $network := dig "network" (dict) $telegram }}
    {{- $_ := set $network "autoSelectFamily" false }}
    {{- $_ := set $telegram "network" $network }}
    {{- /* Support tokenFile for Telegram (reads bot token from file instead of env var) */}}
    {{- if .Values.secrets.telegramTokenFile }}
    {{- $_ := set $telegram "tokenFile" .Values.secrets.telegramTokenFile }}
    {{- end }}
    {{- $_ := set $channels "telegram" $telegram }}
    {{- /* Set groupPolicy=open for Slack when Slack plugin is configured */}}
    {{- if or (and .Values.secrets.slackBotToken .Values.secrets.slackAppToken) (dig "plugins" "slack" "enabled" false .Values.openclaw) }}
    {{- $slack := dig "slack" (dict) $channels }}
    {{- $_ := set $slack "groupPolicy" "open" }}
    {{- $_ := set $channels "slack" $slack }}
    {{- end }}
    {{- /* Configure msteams channel when webhook port is set AND credentials are available */}}
    {{- if and (gt (int $.Values.msteamsWebhookPort) 0) (or (and .Values.secrets.msteamsAppId .Values.secrets.msteamsTenantId) .Values.secrets.existingSecret) }}
    {{- $msteams := dig "msteams" (dict) $channels }}
    {{- $_ := set $msteams "enabled" true }}
    {{- $_ := set $msteams "dmPolicy" "allowlist" }}
    {{- $_ := set $msteams "groupPolicy" "allowlist" }}
    {{- $_ := set $msteams "replyStyle" "thread" }}
    {{- $_ := set $msteams "webhook" (dict "port" (int $.Values.msteamsWebhookPort) "path" "/api/messages") }}
    {{- $_ := set $channels "msteams" $msteams }}
    {{- end }}
    {{- $_ := set $config "channels" $channels }}
    {{- if .Values.skills.shared }}
    {{- $skills := dig "skills" (dict) $config }}
    {{- $load := dig "load" (dict) $skills }}
    {{- $_ := set $load "extraDirs" (list .Values.skills.mountPath) }}
    {{- $_ := set $load "watch" true }}
    {{- $_ := set $load "watchDebounceMs" (int 250) }}
    {{- $_ := set $skills "load" $load }}
    {{- $_ := set $config "skills" $skills }}
    {{- end }}
    {{- if .Values.skills.npm }}
    {{- $skills := dig "skills" (dict) $config }}
    {{- $load := dig "load" (dict) $skills }}
    {{- $existingDirs := dig "extraDirs" (list) $load }}
    {{- $npmDirs := include "cetirun.npmSkillDirs" . | fromJsonArray }}
    {{- $_ := set $load "extraDirs" (concat $existingDirs $npmDirs) }}
    {{- $_ := set $load "watch" true }}
    {{- $_ := set $load "watchDebounceMs" (int 250) }}
    {{- $_ := set $skills "load" $load }}
    {{- $_ := set $config "skills" $skills }}
    {{- end }}
    {{- /* Relax gateway auth when Tailscale is enabled and relaxGatewayAuth is true.
       Tailscale provides its own authentication layer, so the gateway doesn't need
       additional device auth for access via the tailnet. Only enable this when no
       non-tailnet ingress path exists (ingress disabled, network policy enforced). */}}
    {{- if and (include "cetirun.tailscaleEnabled" .) .Values.tailscale.relaxGatewayAuth }}
    {{- $gw := dig "gateway" (dict) $config }}
    {{- $cui := dig "controlUi" (dict) $gw }}
    {{- $_ := set $cui "allowInsecureAuth" true }}
    {{- $_ := set $cui "dangerouslyDisableDeviceAuth" true }}
    {{- $_ := set $gw "controlUi" $cui }}
    {{- $_ := set $config "gateway" $gw }}
    {{- end }}
    {{- /* Add meta block so the config observer does not flag this as anomalous.
       OpenClaw uses meta.lastTouchedVersion to track the last writer. */}}
    {{- $meta := dig "meta" (dict) $config }}
    {{- $_ := set $meta "lastTouchedVersion" (.Chart.Version | default "unknown") }}
    {{- $_ := set $config "meta" $meta }}
{{ $config | toPrettyJson | indent 4 }}
  user-md: |
    # USER.md - About Your Human

    - **Name:** {{ .Values.user.displayName | default "" }}
    - **Alias:** {{ .Values.user.alias | default "" }}
    - **What to call them:** {{ .Values.user.displayName | default "" }}
    - **Pronouns:**
    - **Timezone:**
    {{- if .Values.user.role }}
    - **Role:** {{ .Values.user.role }}
    {{- end }}
    {{- if .Values.user.team }}
    - **Team:** {{ .Values.user.team }}
    {{- end }}
    {{- if .Values.user.description }}
    - **Notes:**
    {{- range .Values.user.description }}
      - {{ . }}
    {{- end }}
    {{- end }}

    ## Context
    {{- if and .Values.user.alias .Values.user.baseDomain }}

    - Has access to this instance at https://{{ .Values.user.alias }}.{{ .Values.user.baseDomain }}
    {{- end }}

    ---

    _Update this as you learn more about them._
  {{- if .Values.openclaw.acpx.enabled }}
  acpx-config.json: |
    {{- $acpxAgents := dict }}
    {{- range $name, $agent := .Values.openclaw.acpx.agents }}
    {{- $_ := set $acpxAgents $name $agent }}
    {{- end }}
{{ dict "agents" $acpxAgents | toPrettyJson | indent 4 }}
{{ end }}
  init-home.sh: |
{{ .Files.Get "configs/init-home.sh" | indent 4 }}
  init-skills.sh: |
{{ .Files.Get "configs/init-skills.sh" | indent 4 }}
  openclaw-auth.sh: |
{{ .Files.Get "configs/openclaw-auth.sh" | indent 4 }}
  session-logger.sh: |
{{ .Files.Get "configs/session-logger.sh" | indent 4 }}
{{- end }}

{{/*
OpenClaw secret data (agent-specific only)
*/}}
{{- define "cetirun.agent.openclaw.secretData" -}}
{{- end }}
