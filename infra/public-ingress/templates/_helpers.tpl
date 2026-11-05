{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}

{{- define "cetirun-public-ingress.validate" -}}
{{- if ne .Release.Namespace .Values.ingressNamespace -}}
{{- fail "the Helm release namespace must equal ingressNamespace" -}}
{{- end -}}
{{- $domain := required "domain is required" .Values.domain -}}
{{- if or (gt (len $domain) 253) (not (regexMatch `^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$` $domain)) -}}
{{- fail "domain must be one lowercase DNS hostname without a wildcard" -}}
{{- end -}}
{{- if not (regexMatch `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` .Values.ingressNamespace) -}}
{{- fail "ingressNamespace must be a Kubernetes namespace name" -}}
{{- end -}}
{{- $serviceSuffix := printf ".%s.svc.cluster.local" .Values.ingressNamespace -}}
{{- if or (not (hasSuffix $serviceSuffix .Values.ingressServiceHostname)) (not (regexMatch `^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$` .Values.ingressServiceHostname)) -}}
{{- fail "ingressServiceHostname must be an explicit Service hostname in ingressNamespace" -}}
{{- end -}}
{{- if not (regexMatch `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` .Values.gateway.name) -}}
{{- fail "gateway.name must be a Kubernetes name" -}}
{{- end -}}
{{- range $setting := list .Values.acme.stagingCertificateEnabled .Values.acme.productionCertificateEnabled -}}
{{- if not (kindIs "bool" $setting) -}}
{{- fail "acme certificate enabled settings must be booleans" -}}
{{- end -}}
{{- end -}}
{{- range $name := list .Values.acme.stagingIssuerName .Values.acme.productionIssuerName .Values.acme.stagingCertificateName .Values.acme.productionCertificateName .Values.acme.stagingSecretName .Values.acme.productionSecretName -}}
{{- if not (regexMatch `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` $name) -}}
{{- fail "acme issuer, certificate, and Secret names must be Kubernetes names" -}}
{{- end -}}
{{- end -}}
{{- if eq .Values.acme.stagingIssuerName .Values.acme.productionIssuerName -}}
{{- fail "staging and production issuer names must differ" -}}
{{- end -}}
{{- if and .Values.acme.stagingCertificateEnabled .Values.acme.productionCertificateEnabled (eq .Values.acme.stagingSecretName .Values.acme.productionSecretName) -}}
{{- fail "staging and production certificate Secret names must differ" -}}
{{- end -}}
{{- if and .Values.acme.stagingCertificateEnabled .Values.acme.productionCertificateEnabled (eq .Values.acme.stagingCertificateName .Values.acme.productionCertificateName) -}}
{{- fail "staging and production certificate names must differ" -}}
{{- end -}}
{{- end -}}

{{- define "cetirun-public-ingress.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: cetirun
{{- end -}}

{{- define "cetirun-public-ingress.keep" -}}
helm.sh/resource-policy: keep
{{- end -}}
