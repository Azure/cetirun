{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{/*
Database connection only: all that schema migration needs, so it is all the
migrate init container gets.
*/}}
{{- define "cetirun-ai-gateway.databaseEnv" -}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun-ai-gateway.databaseUrlSecret" . }}
      key: {{ include "cetirun-ai-gateway.databaseUrlKey" . }}
{{- end }}

{{/*
Everything the proxy needs, including credentials held in chart-managed Secrets.
*/}}
{{- define "cetirun-ai-gateway.commonEnv" -}}
{{ include "cetirun-ai-gateway.databaseEnv" . }}
- name: LITELLM_MASTER_KEY
  valueFrom:
    secretKeyRef:
      {{- if .Values.auth.masterKey.existingSecret }}
      name: {{ .Values.auth.masterKey.existingSecret }}
      key: {{ .Values.auth.masterKey.key }}
      {{- else }}
      name: {{ include "cetirun-ai-gateway.secretName" . }}
      key: master-key
      {{- end }}
- name: LITELLM_SALT_KEY
  valueFrom:
    secretKeyRef:
      {{- if .Values.auth.saltKey.existingSecret }}
      name: {{ .Values.auth.saltKey.existingSecret }}
      key: {{ .Values.auth.saltKey.key }}
      {{- else }}
      name: {{ include "cetirun-ai-gateway.secretName" . }}
      key: salt-key
      {{- end }}
{{- end }}

{{- define "cetirun-ai-gateway.proxyEnv" -}}
{{ include "cetirun-ai-gateway.commonEnv" . }}
- name: STORE_MODEL_IN_DB
  value: "True"
{{- if .Values.migrations.enabled }}
{{- /* The migrate init container owns schema changes, so proxies must not
       migrate on startup as well. */}}
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
      name: {{ include "cetirun-ai-gateway.secretName" . }}
      key: ui-password
      {{- end }}
{{- else }}
- name: DISABLE_ADMIN_UI
  value: "True"
{{- end }}
{{- if include "cetirun-ai-gateway.redisPasswordSecret" . }}
- name: REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "cetirun-ai-gateway.redisPasswordSecret" . }}
      key: {{ include "cetirun-ai-gateway.redisPasswordKey" . }}
{{- end }}
{{- if .Values.metrics.enabled }}
{{- /* The proxy records its samples in files here, and the separate metrics
       process serves them from there. Set from the start, so every sample the
       proxy records goes to the files. */}}
- name: PROMETHEUS_MULTIPROC_DIR
  value: {{ include "cetirun-ai-gateway.metricsDirectory" . }}
{{- end }}
{{ include "cetirun-ai-gateway.monitoringEnv" . }}
{{- end }}

{{/*
Every container of the release that holds a credential in its environment gets
this. Container Insights otherwise copies each container's environment, values
from Secrets included, into its ContainerInventory table once a minute, on any
cluster where environment variable collection is on, as it is by default. It
only applies to that collection; nothing else reads the variable.
*/}}
{{- define "cetirun-ai-gateway.monitoringEnv" -}}
- name: AZMON_COLLECT_ENV
  value: "False"
{{- end }}
