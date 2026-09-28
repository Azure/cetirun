{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{/*
Database connection only.

Used by the migration hook, which runs before any chart-managed resource exists.
A pre-install hook cannot reference the chart's own Secret or ServiceAccount:
the pod is rejected or never scheduled, and the install hangs until it times
out. The administrator's database Secret is fine because they created it.

Schema migration needs no other credential.
*/}}
{{- define "cetirun-gateway.databaseEnv" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ .Values.postgresql.urlSecret.existingSecret }}
      key: {{ .Values.postgresql.urlSecret.key }}
{{- end }}

{{/*
Everything the proxy needs, including credentials held in chart-managed Secrets.
*/}}
{{- define "cetirun-gateway.commonEnv" -}}
{{ include "cetirun-gateway.databaseEnv" . }}
- name: LITELLM_MASTER_KEY
  valueFrom:
    secretKeyRef:
      {{- if .Values.auth.masterKey.existingSecret }}
      name: {{ .Values.auth.masterKey.existingSecret }}
      key: {{ .Values.auth.masterKey.key }}
      {{- else }}
      name: {{ include "cetirun-gateway.secretName" . }}
      key: master-key
      {{- end }}
- name: LITELLM_SALT_KEY
  valueFrom:
    secretKeyRef:
      {{- if .Values.auth.saltKey.existingSecret }}
      name: {{ .Values.auth.saltKey.existingSecret }}
      key: {{ .Values.auth.saltKey.key }}
      {{- else }}
      name: {{ include "cetirun-gateway.secretName" . }}
      key: salt-key
      {{- end }}
{{- end }}

{{- define "cetirun-gateway.proxyEnv" -}}
{{ include "cetirun-gateway.commonEnv" . }}
- name: STORE_MODEL_IN_DB
  value: "True"
{{- if .Values.migrations.enabled }}
{{- /* The migration hook owns schema changes, so proxies must not race it. */}}
- name: DISABLE_SCHEMA_UPDATE
  value: "True"
{{- end }}
{{- if .Values.auth.adminUI.enabled }}
- name: UI_USERNAME
  value: {{ .Values.auth.adminUI.username | quote }}
- name: UI_PASSWORD
  valueFrom:
    secretKeyRef:
      {{- if .Values.auth.adminUI.passwordSecret.existingSecret }}
      name: {{ .Values.auth.adminUI.passwordSecret.existingSecret }}
      key: {{ .Values.auth.adminUI.passwordSecret.key }}
      {{- else }}
      name: {{ include "cetirun-gateway.secretName" . }}
      key: ui-password
      {{- end }}
{{- else }}
- name: DISABLE_ADMIN_UI
  value: "True"
{{- end }}
{{- if .Values.redis.passwordSecret.existingSecret }}
- name: REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.redis.passwordSecret.existingSecret }}
      key: {{ .Values.redis.passwordSecret.key }}
{{- end }}
{{- end }}
