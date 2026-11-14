{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{- define "cetirun-observability.collectorName" -}}cetirun-adx-mon-collector{{- end }}
{{- define "cetirun-observability.ingestorName" -}}cetirun-adx-mon-ingestor{{- end }}
{{- define "cetirun-observability.alerterName" -}}cetirun-adx-mon-alerter{{- end }}
{{- define "cetirun-observability.kubeStateMetricsName" -}}cetirun-kube-state-metrics{{- end }}
{{- define "cetirun-observability.telemetryName" -}}cetirun-telemetry{{- end }}

{{- define "cetirun-observability.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .context.Release.Name }}
app.kubernetes.io/version: {{ .context.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .context.Release.Service }}
app.kubernetes.io/part-of: cetirun-observability
{{- end }}

{{- define "cetirun-observability.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .context.Release.Name }}
{{- end }}

{{/*
Image of a CetiRun component: global.registry replaces the repository's
registry, and global.version is the tag when the image sets none.
*/}}
{{- define "cetirun-observability.image" -}}
{{- $global := .context.Values.global | default dict -}}
{{- $repository := .image.repository -}}
{{- if $global.registry -}}
{{- $repository = printf "%s/%s" (trimSuffix "/" $global.registry) .name -}}
{{- end -}}
{{- $tag := .image.tag | default $global.version | default .context.Chart.AppVersion -}}
{{- if .image.digest }}{{ printf "%s:%s@%s" $repository $tag .image.digest }}{{ else }}{{ printf "%s:%s" $repository $tag }}{{ end }}
{{- end }}

{{/*
Image that CetiRun does not build, such as ADX-Mon's: used as configured,
pinned by its digest when one is set.
*/}}
{{- define "cetirun-observability.externalImage" -}}
{{- if .digest }}{{ printf "%s:%s@%s" .repository .tag .digest }}{{ else }}{{ printf "%s:%s" .repository .tag }}{{ end }}
{{- end }}

{{- define "cetirun-observability.alerterEnabled" -}}
{{- if .Values.alerter.endpoint }}true{{ end }}
{{- end }}

{{- define "cetirun-observability.ingestorEndpoint" -}}
https://{{ include "cetirun-observability.ingestorName" . }}.{{ .Release.Namespace }}.svc:9090
{{- end }}

{{- define "cetirun-observability.ingestorTLSSecret" -}}
{{- .Values.ingestor.tls.secretName | default (printf "%s-tls" (include "cetirun-observability.ingestorName" .)) -}}
{{- end }}

{{/*
The settings every template needs, checked once. Fails with a message that
names the value to set.
*/}}
{{- define "cetirun-observability.validate" -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$" (.Values.clusterName | toString)) }}
{{- fail "clusterName must name this CetiRun cluster: lowercase letters, digits and hyphens, at most 63 characters" }}
{{- end }}
{{- if not (regexMatch "^https://[^/?#]+$" (.Values.adx.endpoint | toString)) }}
{{- fail "adx.endpoint must be the query URI of the Kusto cluster, such as https://<name>.<region>.kusto.windows.net" }}
{{- end }}
{{- range $role, $name := .Values.adx.databases }}
{{- if not (regexMatch "^[A-Za-z][A-Za-z0-9]{0,99}$" ($name | toString)) }}
{{- fail (printf "adx.databases.%s must be a database name of letters and digits" $role) }}
{{- end }}
{{- end }}
{{- if not (regexMatch "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" (.Values.ingestor.clientId | toString)) }}
{{- fail "ingestor.clientId must be the client ID of the managed identity that the ingestor uses through workload identity" }}
{{- end }}
{{- if not (has (.Values.telemetry.events | toString) (list "all" "warnings-and-cetirun" "none")) }}
{{- fail "telemetry.events must be all, warnings-and-cetirun or none" }}
{{- end }}
{{- if lt (int .Values.metrics.scrapeInterval) 10 }}
{{- fail "metrics.scrapeInterval must be at least 10 seconds" }}
{{- end }}
{{- if hasKey .Values "image" }}
{{- fail "image is replaced by collector.image and ingestor.image: the chart runs the ADX-Mon images on MCR" }}
{{- end }}
{{- range $component := list "collector" "ingestor" "alerter" "kubeStateMetrics" }}
{{- $image := (index $.Values $component).image }}
{{- if not (and $image.repository $image.tag) }}
{{- fail (printf "%s.image needs a repository and a tag" $component) }}
{{- end }}
{{- if and $image.digest (not (regexMatch "^sha256:[0-9a-f]{64}$" ($image.digest | toString))) }}
{{- fail (printf "%s.image.digest must be sha256: followed by 64 hexadecimal digits" $component) }}
{{- end }}
{{- end }}
{{- if include "cetirun-observability.alerterEnabled" . }}
{{- if not (regexMatch "^https?://[^/?#\\s]+(/[^?#\\s]*)?$" (.Values.alerter.endpoint | toString)) }}
{{- fail "alerter.endpoint must be the base URL of the service that receives the alerts, such as https://alerts.example.com" }}
{{- end }}
{{- if hasSuffix "/" (.Values.alerter.endpoint | toString) }}
{{- fail "alerter.endpoint must not end with a slash: the alerter adds /alerts" }}
{{- end }}
{{- if not (regexMatch "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" (.Values.alerter.clientId | toString)) }}
{{- fail "alerter.clientId must be the client ID of the managed identity that the alerter uses through workload identity" }}
{{- end }}
{{- $interval := .Values.alerter.rules.collectionHealth.interval | toString }}
{{- if not (regexMatch "^([0-9]+(s|m|h))+$" $interval) }}
{{- fail "alerter.rules.collectionHealth.interval must be a Go duration in seconds, minutes or hours, such as 5m" }}
{{- end }}
{{- /* The AlertRule resource of ADX-Mon refuses a shorter interval, and the
   rule is a hook, so the release would fail after its other changes. */}}
{{- $seconds := 0 }}
{{- range $part := regexFindAll "[0-9]+[smh]" $interval -1 }}
{{- $seconds = add $seconds (mul (atoi (regexFind "[0-9]+" $part)) (get (dict "s" 1 "m" 60 "h" 3600) (regexFind "[smh]" $part))) }}
{{- end }}
{{- if lt (int $seconds) 30 }}
{{- fail "alerter.rules.collectionHealth.interval must be at least 30s, the shortest interval of an ADX-Mon AlertRule" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
A TOML string: JSON string escapes are valid TOML basic string escapes.
*/}}
{{- define "cetirun-observability.toml" -}}{{ . | toString | toJson }}{{- end }}

{{/*
The log routes, in order: the first route that matches a Pod decides where all
its containers' logs go. Agent routes come first and match by Pod label in any
namespace, so an Agent's logs always reach the Agent logs database. Platform
routes match the release namespace and the configured platform namespaces.
Pods that match no route are not collected; kube-system is not collected.
*/}}
{{- define "cetirun-observability.logRoutes" -}}
{{- $logs := .Values.adx.databases.logs -}}
{{- $agents := .Values.adx.databases.agentLogs -}}
{{- $routes := list -}}
{{- if .Values.logs.agents.enabled }}
{{- $routes = append $routes (dict "labels" (dict "app.kubernetes.io/name" "cetirun") "destination" (printf "%s:AgentLogs" $agents)) -}}
{{- $routes = append $routes (dict "labels" (dict "app.kubernetes.io/name" "cetirun-terminal") "destination" (printf "%s:AgentLogs" $agents)) -}}
{{- $routes = append $routes (dict "labels" (dict "app.kubernetes.io/name" "cetirun-copilot-proxy") "destination" (printf "%s:CopilotProxy" $agents)) -}}
{{- end }}
{{- $routes = append $routes (dict "namespace" .Release.Namespace "labels" (dict "app.kubernetes.io/name" "cetirun-ai-gateway" "app.kubernetes.io/component" "gateway") "destination" (printf "%s:Gateway" $logs)) -}}
{{- $routes = append $routes (dict "namespace" .Release.Namespace "labels" (dict "app.kubernetes.io/name" (include "cetirun-observability.telemetryName" .)) "destination" (printf "%s:Telemetry" $logs)) -}}
{{- with .Values.logs.ingressNamespace }}
{{- $routes = append $routes (dict "namespace" . "destination" (printf "%s:Ingress" $logs)) -}}
{{- end }}
{{- range (prepend .Values.logs.platformNamespaces .Release.Namespace | uniq) }}
{{- $routes = append $routes (dict "namespace" . "destination" (printf "%s:Platform" $logs)) -}}
{{- end }}
{{- toJson $routes -}}
{{- end }}

{{/*
The collector's configuration. Its listener binds the Pod's loopback address:
it serves only the collector's own metrics, which it scrapes itself, and no
write endpoint. The kubelet is scraped at the node's name, which the collector
replaces for $(HOSTNAME), with the collector's service account, which may read
node metrics only.
*/}}
{{- define "cetirun-observability.collectorConfig" -}}
{{- $toml := "cetirun-observability.toml" -}}
# Rendered by the cetirun-observability chart.
endpoint = {{ include $toml (include "cetirun-observability.ingestorEndpoint" .) }}
storage-dir = "/var/lib/cetirun-adx-mon/collector"
listen-addr = "127.0.0.1:8080"
max-batch-size = 10000
max-segment-age-seconds = 30
max-disk-usage = {{ int64 .Values.collector.maxDiskUsage }}
lift-labels = [
  { name = "cluster", column = "Cluster" },
  { name = "host", column = "Host" },
  { name = "adxmon_namespace", column = "Namespace" },
  { name = "adxmon_pod", column = "Pod" },
  { name = "adxmon_container", column = "Container" },
]
lift-resources = [
  { name = "cluster", column = "Cluster" },
  { name = "namespace", column = "Namespace" },
  { name = "pod", column = "Pod" },
  { name = "container", column = "Container" },
]

# Every metric names its cluster and its node. Without the node, the series
# that the kubelet and the collector of each node report under the same labels
# would be one series.
[add-labels]
cluster = {{ include $toml .Values.clusterName }}
host = "$(HOSTNAME)"

[prometheus-scrape]
database = {{ include $toml .Values.adx.databases.metrics }}
scrape-interval = {{ int .Values.metrics.scrapeInterval }}
scrape-timeout = 10
default-drop-metrics = true
keep-metrics = [
{{- range .Values.metrics.keep }}
  {{ include $toml . }},
{{- end }}
]
drop-metrics = [
{{- range .Values.metrics.drop }}
  {{ include $toml . }},
{{- end }}
]
static-scrape-target = [
  { host-regex = ".*", url = "http://127.0.0.1:8080/metrics", namespace = {{ include $toml .Release.Namespace }}, pod = {{ include $toml (include "cetirun-observability.collectorName" .) }}, container = "collector" },
  { host-regex = ".*", url = "https://$(HOSTNAME):10250/metrics/cadvisor", namespace = "kube-system", pod = "kubelet", container = "cadvisor" },
  { host-regex = ".*", url = "https://$(HOSTNAME):10250/metrics/resource", namespace = "kube-system", pod = "kubelet", container = "resource" },
  { host-regex = ".*", url = "https://$(HOSTNAME):10250/metrics", namespace = "kube-system", pod = "kubelet", container = "kubelet" },
]

[[host-log]]
{{- if .Values.logs.node.enabled }}
# The node journal of the kubelet and of containerd, each line unparsed in
# Body.message. A kubelet line keeps its klog header, whose first letter is the
# severity: I, W, E or F.
{{- range (list (dict "unit" "kubelet.service" "table" "Kubelet") (dict "unit" "containerd.service" "table" "Containerd")) }}

[[host-log.journal-target]]
matches = [{{ include $toml (printf "_SYSTEMD_UNIT=%s" .unit) }}]
database = {{ include $toml $.Values.adx.databases.logs }}
table = {{ include $toml .table }}
journal-fields = ["_HOSTNAME"]
{{- end }}
{{- end }}
{{- range (include "cetirun-observability.logRoutes" . | fromJsonArray) }}

[[host-log.static-pod-target]]
{{- with .namespace }}
namespace = {{ include $toml . }}
{{- end }}
destination = {{ include $toml .destination }}
parsers = ["json"]
{{- with .labels }}
[host-log.static-pod-target.label-targets]
{{- range $key, $value := . }}
{{ include $toml $key }} = {{ include $toml $value }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
