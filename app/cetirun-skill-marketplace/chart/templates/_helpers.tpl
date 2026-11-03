{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{- define "cetirun-skill-marketplace.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "cetirun-skill-marketplace.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ include "cetirun-skill-marketplace.name" . }}{{ end }}
{{- end }}
{{- define "cetirun-skill-marketplace.labels" -}}
app.kubernetes.io/name: {{ include "cetirun-skill-marketplace.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "cetirun-skill-marketplace.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cetirun-skill-marketplace.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "cetirun-skill-marketplace.serviceAccountName" -}}
{{- default (include "cetirun-skill-marketplace.fullname" .) .Values.serviceAccount.name }}
{{- end }}
{{- define "cetirun-skill-marketplace.catalogNamespace" -}}
{{- default .Release.Namespace .Values.catalog.namespace }}
{{- end }}
{{- define "cetirun-skill-marketplace.storageName" -}}
{{- printf "%s-%s-storage" (include "cetirun-skill-marketplace.fullname" .) .Release.Namespace | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "cetirun-skill-marketplace.image" -}}
{{- $global := .Values.global | default dict -}}
{{- $repository := .Values.image.repository -}}
{{- if $global.registry -}}
{{- $repository = printf "%s/cetirun-skill-marketplace" (trimSuffix "/" $global.registry) -}}
{{- end -}}
{{- $tag := .Values.image.tag | default $global.version | default .Chart.AppVersion -}}
{{- if .Values.image.digest }}{{ printf "%s:%s@%s" $repository $tag .Values.image.digest }}{{ else }}{{ printf "%s:%s" $repository $tag }}{{ end }}
{{- end }}
