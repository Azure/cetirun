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
{{- if and (hasKey $sources "nativeExecution") (not (kindIs "bool" $sources.nativeExecution)) -}}
{{- fail "sandboxSources.nativeExecution must be a boolean" -}}
{{- end -}}
{{- if and (hasKey $sources "reconciledExecution") (not (kindIs "bool" $sources.reconciledExecution)) -}}
{{- fail "sandboxSources.reconciledExecution must be a boolean" -}}
{{- end -}}
{{- if and $sources.reconciledExecution (not $sources.nativeExecution) -}}
{{- fail "sandboxSources.reconciledExecution requires nativeExecution and a completed reader-first upgrade" -}}
{{- end -}}
{{- if and (hasKey $sources "plannedMigration") (not (kindIs "bool" $sources.plannedMigration)) -}}
{{- fail "sandboxSources.plannedMigration must be a boolean" -}}
{{- end -}}
{{- if and $sources.plannedMigration (not $sources.reconciledExecution) -}}
{{- fail "sandboxSources.plannedMigration requires reconciledExecution and upgraded readers" -}}
{{- end -}}
{{- if and $sources.nativeExecution (not (and $sources.enabled $sources.bootstrapOwnedPrerequisites .Values.aiGateway.enabled)) -}}
{{- fail "sandboxSources.nativeExecution requires enabled sources, owned bootstrap and AI Gateway" -}}
{{- end -}}
{{- if and $sources.nativeExecution $sources.placementConfigMapName -}}
{{- fail "sandboxSources.nativeExecution uses RuntimeClass scheduling; a placement registry is unsupported" -}}
{{- end -}}
{{- if and (hasKey $sources "bootstrapOwnedPrerequisites") (not (kindIs "bool" $sources.bootstrapOwnedPrerequisites)) -}}
{{- fail "sandboxSources.bootstrapOwnedPrerequisites must be a boolean" -}}
{{- end -}}
{{- if and $sources.bootstrapOwnedPrerequisites (not $sources.enabled) -}}
{{- fail "sandboxSources.bootstrapOwnedPrerequisites requires enabled sources" -}}
{{- end -}}
{{- if and (hasKey $sources "placementConfigMapName") (not (kindIs "string" $sources.placementConfigMapName)) -}}
{{- fail "sandboxSources.placementConfigMapName must be a resource name" -}}
{{- end -}}
{{- if $sources.placementConfigMapName -}}
{{- if not (and $sources.enabled (eq $sources.placementConfigMapName .Values.sandboxAdmission.placementConfigMapName)) -}}
{{- fail "sandboxSources.placementConfigMapName requires enabled sources and the same admission placement registry" -}}
{{- end -}}
{{- if or (eq $sources.placementConfigMapName $sources.platformConfigMapName) (eq $sources.placementConfigMapName $sources.skillsCatalogConfigMapName) (gt (len $sources.placementConfigMapName) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $sources.placementConfigMapName)) -}}
{{- fail "sandboxSources.placementConfigMapName must be a separate valid ConfigMap name" -}}
{{- end -}}
{{- end -}}
{{- if and (hasKey $sources "skillsCatalogConfigMapName") (not (kindIs "string" $sources.skillsCatalogConfigMapName)) -}}
{{- fail "sandboxSources.skillsCatalogConfigMapName must be a resource name" -}}
{{- end -}}
{{- if $sources.skillsCatalogConfigMapName -}}
{{- if not (and $sources.enabled $sources.bootstrapOwnedPrerequisites) -}}
{{- fail "sandboxSources.skillsCatalogConfigMapName requires enabled owned bootstrap" -}}
{{- end -}}
{{- if or (eq $sources.skillsCatalogConfigMapName $sources.platformConfigMapName) (gt (len $sources.skillsCatalogConfigMapName) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $sources.skillsCatalogConfigMapName)) -}}
{{- fail "sandboxSources.skillsCatalogConfigMapName must be a separate valid ConfigMap name" -}}
{{- end -}}
{{- end -}}
{{- if $sources.enabled -}}
{{- range $key, $_ := $sources -}}
{{- if not (has $key (list "enabled" "nativeExecution" "reconciledExecution" "plannedMigration" "bootstrapOwnedPrerequisites" "platformConfigMapName" "homeClaimName" "skillsClaimName" "skillsCatalogConfigMapName" "placementConfigMapName" "helpers")) -}}
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
{{- $optionalSkills := and (eq $field "skillsClaimName") (eq $name "") (not $sources.skillsCatalogConfigMapName) -}}
{{- if and (not $optionalSkills) (or (gt (len $name) 253) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $name))) -}}
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
{{- if and $sources.nativeExecution $sources.helpers.desktop -}}
{{- fail "sandboxSources.nativeExecution does not support the desktop helper" -}}
{{- end -}}
{{- $config := deepCopy $sources -}}
{{- if not $sources.nativeExecution -}}
{{- $_ := unset $config "nativeExecution" -}}
{{- end -}}
{{- if not $sources.reconciledExecution -}}
{{- $_ := unset $config "reconciledExecution" -}}
{{- end -}}
{{- if not $sources.plannedMigration -}}
{{- $_ := unset $config "plannedMigration" -}}
{{- end -}}
{{- if not $sources.bootstrapOwnedPrerequisites -}}
{{- $_ := unset $config "bootstrapOwnedPrerequisites" -}}
{{- end -}}
{{- if not $sources.skillsCatalogConfigMapName -}}
{{- $_ := unset $config "skillsCatalogConfigMapName" -}}
{{- end -}}
{{- if not $sources.placementConfigMapName -}}
{{- $_ := unset $config "placementConfigMapName" -}}
{{- end -}}
{{- if .Values.sandboxAdmission.coordinationConfigMapName -}}
{{- if eq .Values.sandboxAdmission.coordinationConfigMapName $sources.skillsCatalogConfigMapName -}}
{{- fail "Sandbox coordination and skills catalog require separate ConfigMaps" -}}
{{- end -}}
{{- $_ := set $config "coordinationConfigMapName" .Values.sandboxAdmission.coordinationConfigMapName -}}
{{- $_ := set $config "coordinationConfigMapUID" .Values.sandboxAdmission.coordinationConfigMapUID -}}
{{- end -}}
{{- $_ := set $config "admissionServiceAccount" "cetirun-sandbox-admission" -}}
{{- toJson $config -}}
{{- end -}}
{{- end }}

{{/*
Image of the per-Agent GitHub Copilot proxy: the AI Gateway image, from the
same registry and release as the rest of CetiRun. Empty when the proxy is off.
*/}}
{{- define "cetirun-operator.copilotProxyImage" -}}
{{- $proxy := .Values.aiGateway.copilotProxy | default dict -}}
{{- if $proxy.enabled -}}
{{- $image := $proxy.image | default dict -}}
{{- $registry := include "cetirun-operator.registry" . -}}
{{- $repo := $image.repository | default "cetirun.azurecr.io/cetirun-ai-gateway" -}}
{{- if $registry }}{{- $repo = printf "%s/cetirun-ai-gateway" $registry -}}{{- end -}}
{{- $tag := include "cetirun-operator.version" . | default $image.tag | default .Chart.AppVersion -}}
{{ $repo }}:{{ $tag }}{{ if $image.digest }}@{{ $image.digest }}{{ end }}
{{- end -}}
{{- end }}
