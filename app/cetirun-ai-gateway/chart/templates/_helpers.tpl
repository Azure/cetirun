{{/*
Expand the name of the chart.
*/}}
{{- define "cetirun-ai-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this
(by the DNS naming spec).
*/}}
{{- define "cetirun-ai-gateway.fullname" -}}
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
{{- define "cetirun-ai-gateway.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "cetirun-ai-gateway.labels" -}}
helm.sh/chart: {{ include "cetirun-ai-gateway.chart" . }}
{{ include "cetirun-ai-gateway.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "cetirun-ai-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cetirun-ai-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Selector labels of the Gateway pods themselves. The component label keeps the
Gateway's Service and Deployment from also selecting the bundled PostgreSQL
and Valkey pods, which share the release's name and instance labels.
*/}}
{{- define "cetirun-ai-gateway.gatewaySelectorLabels" -}}
{{ include "cetirun-ai-gateway.selectorLabels" . }}
app.kubernetes.io/component: gateway
{{- end }}

{{/*
Service account name
*/}}
{{- define "cetirun-ai-gateway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cetirun-ai-gateway.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Registry and version overrides propagated by the cetirun-platform umbrella
chart, the same convention as the Operator and Portal charts.
*/}}
{{- define "cetirun-ai-gateway.registry" -}}
{{- (.Values.global).registry | default "" | trimSuffix "/" -}}
{{- end }}

{{- define "cetirun-ai-gateway.version" -}}
{{- (.Values.global).version | default "" -}}
{{- end }}

{{/*
Gateway image. global.registry replaces the registry host, global.version the
tag; a digest, when set, pins the exact image.
*/}}
{{- define "cetirun-ai-gateway.image" -}}
{{- $registry := include "cetirun-ai-gateway.registry" . -}}
{{- $repo := .Values.image.repository -}}
{{- if $registry }}{{- $repo = printf "%s/cetirun-ai-gateway" $registry -}}{{- end -}}
{{- $tag := include "cetirun-ai-gateway.version" . | default .Values.image.tag | default .Chart.AppVersion -}}
{{ $repo }}:{{ $tag }}{{ if .Values.image.digest }}@{{ .Values.image.digest }}{{ end }}
{{- end }}

{{/*
Reference for a third-party image pinned by tag and optional digest.
*/}}
{{- define "cetirun-ai-gateway.dependencyImage" -}}
{{ .repository }}:{{ .tag }}{{ if .digest }}@{{ .digest }}{{ end }}
{{- end }}

{{/*
Name of the chart-managed Secret holding platform credentials.
*/}}
{{- define "cetirun-ai-gateway.secretName" -}}
{{- printf "%s-auth" (include "cetirun-ai-gateway.fullname" .) }}
{{- end }}

{{/*
Name of the ConfigMap holding the generated LiteLLM configuration.
*/}}
{{- define "cetirun-ai-gateway.configMapName" -}}
{{- printf "%s-config" (include "cetirun-ai-gateway.fullname" .) }}
{{- end }}

{{/*
Name of the Secret holding inline provider credentials.
*/}}
{{- define "cetirun-ai-gateway.providerSecretName" -}}
{{- if .Values.providerCredentials.existingSecret }}
{{- .Values.providerCredentials.existingSecret }}
{{- else }}
{{- printf "%s-providers" (include "cetirun-ai-gateway.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Name of a bundled dependency's resources: <fullname>-<suffix>, at most .max
characters. A name that would be longer keeps as much of the fullname as fits
and adds a hash of the whole fullname, so two long release names that share a
prefix still get different names. Names that fit are unchanged.

Called with (dict "context" $ "suffix" "postgresql" "max" 52).
*/}}
{{- define "cetirun-ai-gateway.bundledName" -}}
{{- $fullname := include "cetirun-ai-gateway.fullname" .context -}}
{{- $name := printf "%s-%s" $fullname .suffix -}}
{{- if gt (len $name) (int .max) -}}
{{- $keep := int (sub (int .max) (add (len .suffix) 10)) -}}
{{- $name = printf "%s-%s-%s" ($fullname | trunc $keep | trimSuffix "-") ($fullname | sha256sum | trunc 8) .suffix -}}
{{- end -}}
{{- $name -}}
{{- end }}

{{/*
The bundled PostgreSQL is a StatefulSet. Its pods carry the label
controller-revision-hash=<name>-<hash>, where the hash has up to ten
characters and a label value at most 63, so the name has at most 52. Measured
on Kubernetes 1.37: from 54 characters no pod was created, and 53 only worked
because that revision's hash had nine. The pod's own name, <name>-0, allows
more.
*/}}
{{- define "cetirun-ai-gateway.postgresqlName" -}}
{{- include "cetirun-ai-gateway.bundledName" (dict "context" . "suffix" "postgresql" "max" 52) }}
{{- end }}

{{/*
The bundled Valkey is a Deployment and a Service, which allow 63.
*/}}
{{- define "cetirun-ai-gateway.valkeyName" -}}
{{- include "cetirun-ai-gateway.bundledName" (dict "context" . "suffix" "valkey" "max" 63) }}
{{- end }}

{{/*
Where the database connection URL comes from: the bundled PostgreSQL's Secret,
or the administrator's.
*/}}
{{- define "cetirun-ai-gateway.databaseUrlSecret" -}}
{{- if .Values.postgresql.bundled.enabled }}
{{- include "cetirun-ai-gateway.postgresqlName" . }}
{{- else }}
{{- .Values.postgresql.urlSecret.existingSecret }}
{{- end }}
{{- end }}

{{- define "cetirun-ai-gateway.databaseUrlKey" -}}
{{- if .Values.postgresql.bundled.enabled }}database-url{{ else }}{{ .Values.postgresql.urlSecret.key }}{{ end }}
{{- end }}

{{/*
Redis coordination: bundled Valkey, an external server, or none.
*/}}
{{- define "cetirun-ai-gateway.redisEnabled" -}}
{{- if or .Values.redis.bundled.enabled .Values.redis.host }}true{{ end }}
{{- end }}

{{- define "cetirun-ai-gateway.redisHost" -}}
{{- if .Values.redis.bundled.enabled }}
{{- include "cetirun-ai-gateway.valkeyName" . }}
{{- else }}
{{- .Values.redis.host }}
{{- end }}
{{- end }}

{{- define "cetirun-ai-gateway.redisPort" -}}
{{- if .Values.redis.bundled.enabled }}6379{{ else }}{{ .Values.redis.port }}{{ end }}
{{- end }}

{{/*
Secret and key holding the Redis password, empty when there is none. The
administrator's redis.passwordSecret wins, for the bundled Valkey too, so both
Valkey and the Gateway read one stable value; otherwise the bundled Valkey uses
the Secret the chart generates for it.
*/}}
{{- define "cetirun-ai-gateway.redisPasswordSecret" -}}
{{- if .Values.redis.passwordSecret.existingSecret }}
{{- .Values.redis.passwordSecret.existingSecret }}
{{- else if .Values.redis.bundled.enabled }}
{{- include "cetirun-ai-gateway.valkeyName" . }}
{{- end }}
{{- end }}

{{- define "cetirun-ai-gateway.redisPasswordKey" -}}
{{- if .Values.redis.passwordSecret.existingSecret }}{{ .Values.redis.passwordSecret.key }}{{ else if .Values.redis.bundled.enabled }}password{{ end }}
{{- end }}

{{/*
Whether the chart generates the bundled Valkey's password.
*/}}
{{- define "cetirun-ai-gateway.generateValkeyPassword" -}}
{{- if and .Values.redis.bundled.enabled (not .Values.redis.passwordSecret.existingSecret) }}true{{ end }}
{{- end }}

{{/*
Models configured at install. None is valid: standalone Gateways can add
console models later; AgentPolicy shared grants require chart bindings.
A CetiRun deployment may instead use the GitHub Copilot models the operator
registers for each Agent. The copilot/
prefix is reserved only while operatorIntegration is on: a standalone Gateway
may already serve a model under such a name.
*/}}
{{- define "cetirun-ai-gateway.validateModels" -}}
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
{{- if $.Values.operatorIntegration }}
{{- $info := .model_info | default dict }}
{{- if hasKey $info "id" }}
{{- fail (printf "model %s configures reserved model_info.id; operatorIntegration derives the deployment identity from the complete model configuration" .model_name) }}
{{- end }}
{{- range ($info.access_groups | default list) }}
{{- if hasPrefix "cetirun-model-" (lower (printf "%v" .)) }}
{{- fail "the cetirun-model- access group prefix is reserved for operatorIntegration" }}
{{- end }}
{{- end }}
{{- end }}
{{- if hasPrefix "cetirun-" (lower .model_name) }}
{{- fail (printf "model_name %s uses the reserved cetirun- prefix. That prefix belongs to models the CetiRun operator registers automatically; a collision would serve both under one name and split traffic between them." .model_name) }}
{{- else if and $.Values.operatorIntegration (hasPrefix "copilot/" (lower .model_name)) }}
{{- fail (printf "model_name %s uses the copilot/ prefix, which operatorIntegration reserves for the GitHub Copilot models the CetiRun operator registers for each Agent; a collision would serve both under one name and split traffic between them." .model_name) }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Redis is required above one replica. Without it each replica enforces its own
copy of every budget and rate limit, so the effective limit is multiplied by the
replica count. Recording stays accurate; enforcement does not.
*/}}
{{- define "cetirun-ai-gateway.validateRedis" -}}
{{- if and .Values.redis.bundled.enabled .Values.redis.host }}
{{- fail "redis.host is set while redis.bundled.enabled is true. To use an external Redis, set redis.bundled.enabled=false." }}
{{- end }}
{{- if and (gt (int .Values.replicaCount) 1) (not (include "cetirun-ai-gateway.redisEnabled" .)) }}
{{- fail "Redis is required when replicaCount is above 1. Each replica otherwise tracks its own copy of every key budget and rate limit, so a limit is multiplied by the replica count. Keep redis.bundled.enabled, set redis.host, or set replicaCount to 1." }}
{{- end }}
{{- end }}

{{/*
The database is required: the bundled PostgreSQL, or a connection URL supplied
through a Secret rather than a plain value.
*/}}
{{- define "cetirun-ai-gateway.validateDatabase" -}}
{{- if and .Values.postgresql.bundled.enabled .Values.postgresql.urlSecret.existingSecret }}
{{- fail "postgresql.urlSecret.existingSecret is set while postgresql.bundled.enabled is true. To use an external database, set postgresql.bundled.enabled=false." }}
{{- end }}
{{- if not (include "cetirun-ai-gateway.databaseUrlSecret" .) }}
{{- fail "a database is required: the Gateway needs PostgreSQL for virtual keys, spend records and models added through the console. Keep postgresql.bundled.enabled, or set postgresql.urlSecret.existingSecret." }}
{{- end }}
{{- end }}

{{/*
Provider credentials must come from exactly one source.
*/}}
{{- define "cetirun-ai-gateway.validateProviderCredentials" -}}
{{- if and .Values.providerCredentials.existingSecret .Values.providerCredentials.inline }}
{{- fail "set either providerCredentials.existingSecret or providerCredentials.inline, not both." }}
{{- end }}
{{- end }}

{{/*
Where the proxy and the metrics process share their Prometheus samples.
*/}}
{{- define "cetirun-ai-gateway.metricsDirectory" -}}
/tmp/litellm-metrics
{{- end }}

{{/*
The requestLog, spendLogs and metrics settings, as JSON, with the defaults in
values.yaml for whatever is missing. helm upgrade --reuse-values keeps the
values of the chart version a release was installed with, so a release from
before these settings has none of them. Templates read them through this,
with fromJson, never from .Values directly. The render tests check that
rendering without the settings gives the same manifests as rendering with the
defaults.
*/}}
{{- define "cetirun-ai-gateway.logsAndMetrics" -}}
{{- $requestLog := .Values.requestLog | default dict -}}
{{- $spendLogs := .Values.spendLogs | default dict -}}
{{- $metrics := .Values.metrics | default dict -}}
{{- $podMonitor := $metrics.podMonitor | default dict -}}
{{- $settings := dict -}}
{{- $_ := set $settings "requestLog" (dict "enabled" (dig "enabled" true $requestLog)) -}}
{{- $_ = set $settings "spendLogs" (dict "enabled" (dig "enabled" true $spendLogs)) -}}
{{- $monitor := dict "enabled" (dig "enabled" false $podMonitor) -}}
{{- $_ = set $monitor "apiVersion" (dig "apiVersion" "azmonitoring.coreos.com/v1" $podMonitor) -}}
{{- $_ = set $monitor "interval" (dig "interval" "30s" $podMonitor) -}}
{{- $_ = set $monitor "labels" (dig "labels" dict $podMonitor) -}}
{{- $metricsSettings := dict "enabled" (dig "enabled" false $metrics) "port" (dig "port" nil $metrics) -}}
{{- $_ = set $metricsSettings "excludeLabels" (dig "excludeLabels" (list "client_ip" "user_agent" "user_email") $metrics) -}}
{{- $_ = set $metricsSettings "podMonitor" $monitor -}}
{{- $_ = set $settings "metrics" $metricsSettings -}}
{{- toJson $settings -}}
{{- end }}

{{/*
The router settings, as JSON, with the defaults in values.yaml for whatever is
missing, for the same reason as cetirun-ai-gateway.logsAndMetrics: a release
installed before them upgrades with --reuse-values. Templates read them
through this, with fromJson.
*/}}
{{- define "cetirun-ai-gateway.router" -}}
{{- $router := .Values.router | default dict -}}
{{- $health := $router.healthChecks | default dict -}}
{{- $settings := dict -}}
{{- $_ := set $settings "failover" (dig "failover" true $router) -}}
{{- $_ = set $settings "continuationAffinity" (dig "continuationAffinity" true $router) -}}
{{- $_ = set $settings "encryptedContentAffinity" (dig "encryptedContentAffinity" false $router) -}}
{{- $_ = set $settings "fallbacks" (dig "fallbacks" list $router) -}}
{{- $_ = set $settings "contextWindowFallbacks" (dig "contextWindowFallbacks" list $router) -}}
{{- $_ = set $settings "healthChecks" (dict "enabled" (dig "enabled" false $health) "intervalSeconds" (dig "intervalSeconds" 300 $health)) -}}
{{- toJson $settings -}}
{{- end }}

{{/*
The model names the background health checks probe: those configured in
models, apart from LiteLLM auto routers, whose own models are probed instead.
*/}}
{{- define "cetirun-ai-gateway.healthCheckModels" -}}
{{- $names := list -}}
{{- range .Values.models -}}
{{- if not (hasPrefix "auto_router/" (toString (get (.litellm_params | default dict) "model"))) -}}
{{- $names = append $names .model_name -}}
{{- end -}}
{{- end -}}
{{- toJson (uniq $names) -}}
{{- end }}

{{/*
Whether the cluster serves the PodMonitor API the chart is configured for.
Azure Monitor managed Prometheus installs azmonitoring.coreos.com; a cluster
without it has no such API, and installing a PodMonitor there would fail.
*/}}
{{- define "cetirun-ai-gateway.podMonitorAvailable" -}}
{{- $podMonitor := (include "cetirun-ai-gateway.logsAndMetrics" . | fromJson).metrics.podMonitor -}}
{{- if .Capabilities.APIVersions.Has (printf "%s/PodMonitor" $podMonitor.apiVersion) }}true{{ end }}
{{- end }}

{{/*
The metrics port: metrics.port when set, otherwise 9090, or 9091 when the proxy
already listens on 9090 (service.port), so turning metrics on never needs a
port to be chosen by hand.
*/}}
{{- define "cetirun-ai-gateway.metricsPort" -}}
{{- $metrics := (include "cetirun-ai-gateway.logsAndMetrics" . | fromJson).metrics -}}
{{- if $metrics.port -}}
{{- int $metrics.port -}}
{{- else if eq (int .Values.service.port) 9090 -}}
9091
{{- else -}}
9090
{{- end -}}
{{- end }}

{{/*
A metrics port set by hand has to be its own, and a PodMonitor needs metrics
to scrape.
*/}}
{{- define "cetirun-ai-gateway.validateMetrics" -}}
{{- $metrics := (include "cetirun-ai-gateway.logsAndMetrics" . | fromJson).metrics -}}
{{- if and $metrics.enabled $metrics.port (eq (int $metrics.port) (int .Values.service.port)) }}
{{- fail (printf "metrics.port %d is also service.port. The metrics process needs a port of its own: set metrics.port to another one, or leave it unset to have one chosen." (int $metrics.port)) }}
{{- end }}
{{- if and $metrics.podMonitor.enabled (not $metrics.enabled) }}
{{- fail "metrics.podMonitor.enabled is set while metrics.enabled is false, so the PodMonitor would have nothing to scrape. Set metrics.enabled as well, or turn the PodMonitor off." }}
{{- end }}
{{- end }}

{{/*
LiteLLM's spend log is turned off only in favour of the request log, which then
keeps the record of each call.
*/}}
{{- define "cetirun-ai-gateway.validateRequestRecords" -}}
{{- $settings := include "cetirun-ai-gateway.logsAndMetrics" . | fromJson -}}
{{- if and (not $settings.spendLogs.enabled) (not $settings.requestLog.enabled) }}
{{- fail "spendLogs.enabled is false while requestLog.enabled is false, which would leave no record of each call. LiteLLM's spend log is turned off only together with the request log: set requestLog.enabled as well, or keep spendLogs.enabled." }}
{{- end }}
{{- end }}
