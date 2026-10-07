{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- $config := deepCopy .Values.hermes.config }}
{{- $provider := .Values.secrets.provider | default "github_copilot" }}
{{- $hermesProvider := "copilot" }}
{{- if eq $provider "openai" }}
{{- $hermesProvider = "openai" }}
{{- else if eq $provider "anthropic" }}
{{- $hermesProvider = "anthropic" }}
{{- end }}
{{- $model := required "secrets.model must be set by the CetiRun operator or Helm values" .Values.secrets.model }}
{{- $gateway := .Values.aiGateway | default dict }}
{{- if and (dig "enabled" false $gateway) (eq $provider "openai") }}
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
{{- $providers := dig "providers" (dict) $config }}
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
{{- else }}
{{- $_ := set $config "model" (dict "default" $model "provider" $hermesProvider) }}
{{- end }}
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
{{- $npmDirs := .SkillDirs }}
{{- $_ := set $skills "external_dirs" (concat $existingDirs $npmDirs | uniq) }}
{{- $_ := set $config "skills" $skills }}
{{- end }}
{{ $config | toYaml }}
