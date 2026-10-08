{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.sandbox.validate" -}}
{{- $s := .Values.sandboxWorkload -}}
{{- if or (ne $s.contractVersion "v1") (ne $s.executionHeld true) (ne (int .Values.replicaCount) 0) -}}
{{- fail "Sandbox render plans must retain the execution hold" -}}
{{- end -}}
{{- if eq (include "cetirun.agent.type" .) "custom" -}}
{{- fail "policy-bound Custom rendering is unsupported" -}}
{{- end -}}
{{- if or (ne (index .Values.podAnnotations "sidecar.istio.io/inject") "false") .Values.sidecarScope.enabled (not .Values.networkPolicy.enabled) (gt (len .Values.sidecarScope.additionalEgressHosts) 0) -}}
{{- fail "Sandbox network controls are protected" -}}
{{- end -}}
{{- $dns := $s.dnsConfig | default dict -}}
{{- $servers := $dns.nameservers | default list -}}
{{- if or (eq (len $servers) 0) (gt (len $servers) 3) -}}
{{- fail "Sandbox requires one to three compiler-selected DNS server IPs" -}}
{{- end -}}
{{- if or (not .Values.runtimeClassName) (not .Values.persistence.existingClaim) .Values.persistence.useStatefulSetVolumeClaim (not .Values.skills.existingClaim) -}}
{{- fail "Sandbox requires a runtime and separately owned existing HOME/skills claims" -}}
{{- end -}}
{{- if or .Values.azureWorkloadIdentity.enabled .Values.agentIdentity.enabled (ne (include "cetirun.automountServiceAccountToken" .) "false") -}}
{{- fail "Sandbox identities must use compiler-owned explicit projections" -}}
{{- end -}}
{{- if or .Values.initContainers .Values.sidecars .Values.extraVolumes .Values.extraVolumeMounts .Values.extraEnvFrom .Values.skills.npm .Values.autoscaling.enabled .Values.ingress.enabled .Values.execRestriction.enabled -}}
{{- fail "Sandbox protected extension paths are not supported" -}}
{{- end -}}
{{- if not $s.objects -}}
{{- fail "Sandbox requires compiler-owned enforcement resources" -}}
{{- end -}}
{{- end -}}
