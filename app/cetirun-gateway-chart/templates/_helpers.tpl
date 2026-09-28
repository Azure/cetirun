{{/*
Expand the name of the chart.
*/}}
{{- define "cetirun-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this
(by the DNS naming spec).
*/}}
{{- define "cetirun-gateway.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "cetirun-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "cetirun-gateway.labels" -}}
helm.sh/chart: {{ include "cetirun-gateway.chart" . }}
{{ include "cetirun-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "cetirun-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cetirun-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "cetirun-gateway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cetirun-gateway.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference, pinned by digest when one is supplied.
*/}}
{{- define "cetirun-gateway.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}{{ if .Values.image.digest }}@{{ .Values.image.digest }}{{ end }}
{{- end }}

{{/*
Name of the chart-managed Secret holding platform credentials.
*/}}
{{- define "cetirun-gateway.secretName" -}}
{{- printf "%s-auth" (include "cetirun-gateway.fullname" .) }}
{{- end }}

{{/*
Name of the ConfigMap holding the generated LiteLLM configuration.
*/}}
{{- define "cetirun-gateway.configMapName" -}}
{{- printf "%s-config" (include "cetirun-gateway.fullname" .) }}
{{- end }}

{{/*
Name of the Secret holding inline provider credentials.
*/}}
{{- define "cetirun-gateway.providerSecretName" -}}
{{- if .Values.providerCredentials.existingSecret }}
{{- .Values.providerCredentials.existingSecret }}
{{- else }}
{{- printf "%s-providers" (include "cetirun-gateway.fullname" .) }}
{{- end }}
{{- end }}

{{/*
True when Redis is configured.
*/}}
{{- define "cetirun-gateway.redisEnabled" -}}
{{- if .Values.redis.host }}true{{ end }}
{{- end }}

{{/*
At least one model must be configured. A Gateway with no models is not a usable
deployment, so this fails at install rather than at the first request.
*/}}
{{- define "cetirun-gateway.validateModels" -}}
{{- if not .Values.models }}
{{- fail "models is required: configure at least one model. A Gateway with no models cannot serve requests." }}
{{- end }}
{{- range .Values.models }}
{{- if not .model_name }}
{{- fail "each entry in models requires model_name" }}
{{- end }}
{{- if not .litellm_params }}
{{- fail (printf "model %s requires litellm_params" .model_name) }}
{{- end }}
{{- if not .litellm_params.model }}
{{- fail (printf "model %s requires litellm_params.model" .model_name) }}
{{- end }}
{{- if hasPrefix "cetirun-" (lower .model_name) }}
{{- fail (printf "model_name %s uses the reserved cetirun- prefix. That prefix belongs to models the CetiRun operator registers automatically; a collision would serve both under one name and split traffic between them." .model_name) }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Redis is required above one replica. Without it each replica enforces its own
copy of every budget and rate limit, so the effective limit is multiplied by the
replica count. Recording stays accurate; enforcement does not.
*/}}
{{- define "cetirun-gateway.validateRedis" -}}
{{- if and (gt (int .Values.replicaCount) 1) (not .Values.redis.host) }}
{{- fail "redis.host is required when replicaCount is above 1. Each replica otherwise tracks its own copy of every key budget and rate limit, so a limit is multiplied by the replica count. Set redis.host, or set replicaCount to 1." }}
{{- end }}
{{- end }}

{{/*
The database connection is required, and must be supplied through a Secret
rather than a plain value.
*/}}
{{- define "cetirun-gateway.validateDatabase" -}}
{{- if not .Values.postgresql.urlSecret.existingSecret }}
{{- fail "postgresql.urlSecret.existingSecret is required: the Gateway needs PostgreSQL for virtual keys, spend records and models added through the console." }}
{{- end }}
{{- end }}

{{/*
Provider credentials must come from exactly one source.
*/}}
{{- define "cetirun-gateway.validateProviderCredentials" -}}
{{- if and .Values.providerCredentials.existingSecret .Values.providerCredentials.inline }}
{{- fail "set either providerCredentials.existingSecret or providerCredentials.inline, not both." }}
{{- end }}
{{- end }}
