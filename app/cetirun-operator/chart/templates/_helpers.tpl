{{/*
Shared registry and version defaults.
*/}}
{{- define "cetirun-operator.registry" -}}
{{- .Values.global.registry | trimSuffix "/" -}}
{{- end }}

{{- define "cetirun-operator.version" -}}
{{- .Values.global.version -}}
{{- end }}

{{- define "cetirun-operator.chartRepoURL" -}}
{{- $registry := include "cetirun-operator.registry" . -}}
{{- if $registry -}}
{{- printf "oci://%s/helm/cetirun" $registry -}}
{{- else -}}
{{- .Values.cetirun.chart.repoURL -}}
{{- end -}}
{{- end }}

{{- define "cetirun-operator.chartVersion" -}}
{{- include "cetirun-operator.version" . | default .Values.cetirun.chart.version | default .Chart.AppVersion -}}
{{- end }}

{{/*
Container image reference with optional digest pinning.
When image.digest is set, produces: repository:tag@digest
Otherwise: repository:tag
*/}}
{{- define "cetirun-operator.image" -}}
{{- $registry := include "cetirun-operator.registry" . -}}
{{- $repo := .Values.image.repository -}}
{{- if $registry }}{{- $repo = printf "%s/cetirun-operator" $registry -}}{{- end -}}
{{- $tag := include "cetirun-operator.version" . | default .Values.image.tag | default .Chart.AppVersion -}}
{{ $repo }}:{{ $tag }}{{ if .Values.image.digest }}@{{ .Values.image.digest }}{{ end }}
{{- end }}
