{{/*
Chart name.
*/}}
{{- define "cetirun-istio.name" -}}
{{- .Chart.Name }}
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "cetirun-istio.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: cetirun
{{- end }}

{{/*
Lua-safe domain pattern (dots escaped as %.).
*/}}
{{- define "cetirun-istio.luaDomainPattern" -}}
{{- .Values.domain | replace "." "%." }}
{{- end }}

{{/* Keep anonymous Teams webhook bypasses on exact route shapes only. */}}
{{- define "cetirun-istio.validateWebhookBypasses" -}}
{{- range .Values.authPolicy.webhookBypassPaths -}}
{{- if ne . "/{*}/api/messages" -}}
{{- fail "authPolicy.webhookBypassPaths supports only /{*}/api/messages" -}}
{{- end -}}
{{- end -}}
{{- range .Values.authPolicy.legacySubdomainWebhookBypassPaths -}}
{{- if ne . "/api/messages" -}}
{{- fail "authPolicy.legacySubdomainWebhookBypassPaths supports only /api/messages" -}}
{{- end -}}
{{- end -}}
{{- end }}
