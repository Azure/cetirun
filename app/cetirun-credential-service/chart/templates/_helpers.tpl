{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{- define "cetirun-credential-service.name" -}}cetirun-credential-service{{- end }}
{{- define "cetirun-credential-service.fullname" -}}
{{- default (include "cetirun-credential-service.name" .) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}
{{- define "cetirun-credential-service.labels" -}}
app.kubernetes.io/name: {{ include "cetirun-credential-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "cetirun-credential-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cetirun-credential-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "cetirun-credential-service.serviceAccountName" -}}
{{- default (include "cetirun-credential-service.fullname" .) .Values.serviceAccount.name -}}
{{- end }}
{{/*
Container image reference with optional digest pinning.
When digest is set, produces: repository:tag@digest.
*/}}
{{- define "cetirun-credential-service.image" -}}
{{- $repository := required "image repository is required" .repository -}}
{{- $tag := default .defaultTag .tag -}}
{{- $image := printf "%s:%s" $repository $tag -}}
{{- if .digest -}}{{ printf "%s@%s" $image .digest }}{{- else -}}{{ $image }}{{- end -}}
{{- end }}
