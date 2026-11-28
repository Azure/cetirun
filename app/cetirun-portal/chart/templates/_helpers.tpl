{{/*
Expand the name of the chart.
*/}}
{{- define "cetirun-portal.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "cetirun-portal.fullname" -}}
{{- $name := default .Chart.Name .Values.fullnameOverride }}
{{- printf "%s" $name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Shared registry and version defaults.
*/}}
{{- define "cetirun-portal.registry" -}}
{{- .Values.global.registry | trimSuffix "/" -}}
{{- end }}

{{- define "cetirun-portal.version" -}}
{{- .Values.global.version -}}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "cetirun-portal.labels" -}}
app.kubernetes.io/name: {{ include "cetirun-portal.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ include "cetirun-portal.version" . | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "cetirun-portal.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cetirun-portal.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Container image reference with optional digest pinning.
When image.digest is set, produces: repository:tag@digest
Otherwise: repository:tag
*/}}
{{- define "cetirun-portal.image" -}}
{{- $registry := include "cetirun-portal.registry" . -}}
{{- $repo := .Values.image.repository -}}
{{- if $registry }}{{- $repo = printf "%s/cetirun-portal" $registry -}}{{- end -}}
{{- $tag := include "cetirun-portal.version" . | default .Values.image.tag | default .Chart.AppVersion -}}
{{ $repo }}:{{ $tag }}{{ if .Values.image.digest }}@{{ .Values.image.digest }}{{ end }}
{{- end }}

{{/*
usage: the AI Gateway usage from the logs database in Azure Data Explorer,
read with a workload identity (internal/usage).
*/}}
{{- define "cetirun-portal.usageEnabled" -}}
{{- $usage := .Values.usage | default dict }}
{{- if and (kindIs "map" $usage) $usage.enabled }}true{{ end }}
{{- end }}

{{- define "cetirun-portal.validateUsage" -}}
{{- $usage := .Values.usage | default dict }}
{{- if not (kindIs "map" $usage) }}
{{- fail "usage must be an object" }}
{{- end }}
{{- if and (hasKey $usage "enabled") (not (kindIs "bool" $usage.enabled)) }}
{{- fail "usage.enabled must be a boolean" }}
{{- end }}
{{- if $usage.enabled }}
{{- if not (regexMatch "^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$" (toString (dig "adx" "endpoint" "" $usage))) }}
{{- fail "usage.adx.endpoint must be the query URI of the Kusto cluster, such as https://<name>.<region>.kusto.windows.net" }}
{{- end }}
{{- if not (regexMatch "^[A-Za-z][A-Za-z0-9]{0,99}$" (toString (dig "adx" "database" "" $usage))) }}
{{- fail "usage.adx.database must be the name of the logs database: letters and digits" }}
{{- end }}
{{- if not (regexMatch "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" (toString (dig "adx" "clientId" "" $usage))) }}
{{- fail "usage.adx.clientId must be the client ID of the managed identity with the Viewer role on the logs database" }}
{{- end }}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$" (toString ($usage.clusterName | default ""))) }}
{{- fail "usage.clusterName must be a DNS label: the clusterName of the cetirun-observability chart, which names this cluster in its rows" }}
{{- end }}
{{- if not .Values.serviceAccount.create }}
{{- fail "usage.enabled needs serviceAccount.create: the chart names the usage identity on the ServiceAccount it creates" }}
{{- end }}
{{- end }}
{{- end }}
