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
{{- $npmDirs := .SkillDirs }}
{{- $_ := set $skills "external_dirs" (concat $existingDirs $npmDirs | uniq) }}
{{- $_ := set $config "skills" $skills }}
{{- end }}
{{ $config | toYaml }}
