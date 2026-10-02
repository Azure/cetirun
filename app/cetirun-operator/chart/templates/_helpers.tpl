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

{{- define "cetirun-operator.sandboxSourcesConfig" -}}
{{- $sources := .Values.sandboxSources -}}
{{- if not (kindIs "map" $sources) -}}
{{- fail "sandboxSources must be an object" -}}
{{- end -}}
{{- if not (kindIs "bool" $sources.enabled) -}}
{{- fail "sandboxSources.enabled must be a boolean" -}}
{{- end -}}
{{- if $sources.enabled -}}
{{- range $key, $_ := $sources -}}
{{- if not (has $key (list "enabled" "platformConfigMapName" "homeClaimName" "skillsClaimName" "helpers")) -}}
{{- fail (printf "unsupported sandboxSources field %s" $key) -}}
{{- end -}}
{{- end -}}
{{- if not (and .Values.sandboxAdmission.enabled .Values.sandboxAdmission.enforce .Values.sandboxAdmission.podsEnabled) -}}
{{- fail "sandboxSources.enabled requires enforced Sandbox configuration and Pod admission" -}}
{{- end -}}
{{- range $field := list "platformConfigMapName" "homeClaimName" "skillsClaimName" -}}
{{- $name := get $sources $field -}}
{{- if not (kindIs "string" $name) -}}
{{- fail (printf "sandboxSources.%s must be a resource name" $field) -}}
{{- end -}}
{{- if or (gt (len $name) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $name)) -}}
{{- fail (printf "sandboxSources.%s must be a valid resource name" $field) -}}
{{- end -}}
{{- end -}}
{{- if eq $sources.homeClaimName $sources.skillsClaimName -}}
{{- fail "sandboxSources HOME and skills must use distinct claims" -}}
{{- end -}}
{{- if not (kindIs "map" $sources.helpers) -}}
{{- fail "sandboxSources.helpers must be an object" -}}
{{- end -}}
{{- range $key, $_ := $sources.helpers -}}
{{- if not (has $key (list "doctor" "logger" "desktop")) -}}
{{- fail (printf "unsupported sandboxSources.helpers field %s" $key) -}}
{{- end -}}
{{- end -}}
{{- range $field := list "doctor" "logger" "desktop" -}}
{{- if not (kindIs "bool" (get $sources.helpers $field)) -}}
{{- fail (printf "sandboxSources.helpers.%s must be a boolean" $field) -}}
{{- end -}}
{{- end -}}
{{- $config := deepCopy $sources -}}
{{- $_ := set $config "admissionServiceAccount" "cetirun-sandbox-admission" -}}
{{- toJson $config -}}
{{- end -}}
{{- end }}
