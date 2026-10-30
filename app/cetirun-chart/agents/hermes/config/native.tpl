{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- $config := deepCopy .Values.hermes.config }}
{{- $model := required "secrets.model must be set by the CetiRun operator or Helm values" .Values.secrets.model }}
{{- $gateway := .Values.aiGateway | default dict }}
{{- /* On the platform AI Gateway: one provider holding the models the Gateway
   offers this Agent, with their context windows. Hermes reads the Agent's key
   from the container environment, so the key is in neither this file nor
   Hermes' .env. Discovery stays off: the list is the operator's. */}}
{{- $models := dict }}
{{- range (dig "models" (list) $gateway) }}
{{- $entry := dict }}
{{- if .contextWindow }}
{{- $_ := set $entry "context_length" (int .contextWindow) }}
{{- end }}
{{- $_ := set $models (printf "%v" .id) $entry }}
{{- end }}
{{- if not (hasKey $models $model) }}
{{- $_ := set $models $model (dict) }}
{{- end }}
{{- $providers := dict }}
{{- $_ := set $providers "cetirun-gateway" (dict
      "name" "CetiRun AI Gateway"
      "api" (required "secrets.apiBase must be set for the AI Gateway" .Values.secrets.apiBase)
      "key_env" (dig "hermes" "keyEnv" "CETIRUN_GATEWAY_API_KEY" $gateway)
      "transport" "chat_completions"
      "default_model" $model
      "discover_models" false
      "models" $models) }}
{{- $_ := set $config "providers" $providers }}
{{- $_ := set $config "model" (dict "default" $model "provider" "cetirun-gateway") }}
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
{{- $contextSkills := and (or (not .Values.sandboxWorkload) (dig "reconciled" false (.Values.sandboxWorkload | default dict))) (dig "enabled" false (.Values.memoryContext | default dict)) }}
{{- if or .Values.skills.shared $contextSkills }}
{{- $skills := dig "skills" (dict) $config }}
{{- $existingDirs := dig "external_dirs" (list) $skills }}
{{- $skillsPath := "/home/vibe/.agents/skills" }}
{{- if not $contextSkills }}{{- $skillsPath = .Values.skills.mountPath }}{{- end }}
{{- $_ := set $skills "external_dirs" (append $existingDirs $skillsPath | uniq) }}
{{- $_ := set $config "skills" $skills }}
{{- end }}
{{- if .Values.skills.npm }}
{{- $skills := dig "skills" (dict) $config }}
{{- $existingDirs := dig "external_dirs" (list) $skills }}
{{- $npmDirs := .SkillDirs }}
{{- $_ := set $skills "external_dirs" (concat $existingDirs $npmDirs | uniq) }}
{{- $_ := set $config "skills" $skills }}
{{- end }}
{{- if $contextSkills }}
{{- $plugins := dig "plugins" (dict) $config }}
{{- $_ := set $plugins "enabled" (append (dig "enabled" (list) $plugins) "cetirun-memory-context" | uniq) }}
{{- $_ := set $config "plugins" $plugins }}
{{- end }}
{{- if .Values.connectionsMCP.enabled }}
{{- $servers := dig "mcp_servers" (dict) $config }}
{{- $_ := set $servers "cetirun-connections" (dict "url" .Values.connectionsMCP.url "transport" "streamable-http" "strict_redirect_headers" true "ssl_verify" true) }}
{{- $_ := set $config "mcp_servers" $servers }}
{{- else }}
{{- $servers := dig "mcp_servers" (dict) $config }}
{{- $_ := unset $servers "cetirun-connections" }}
{{- end }}
{{ $config | toYaml }}
