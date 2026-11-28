{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun-keyvault-tls.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "cetirun-keyvault-tls.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "cetirun-keyvault-tls.image" -}}
{{- printf "%s:%s@%s" .Values.image.repository .Values.image.tag .Values.image.digest -}}
{{- end -}}
{{- define "cetirun-keyvault-tls.labels" -}}
app.kubernetes.io/name: {{ include "cetirun-keyvault-tls.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "cetirun-keyvault-tls.validate" -}}
{{- $kv := .Values.keyVault -}}
{{- $vaultName := toString ($kv.name | default "") -}}
{{- if or (not (regexMatch "^[a-z][a-z0-9-]{1,22}[a-z0-9]$" $vaultName)) (contains "--" $vaultName) -}}
{{- fail "keyVault.name must be a lowercase Azure Key Vault name" -}}
{{- end -}}
{{- $tenant := toString ($kv.tenantId | default "") -}}
{{- if not (regexMatch "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" $tenant) -}}
{{- fail "keyVault.tenantId must be a UUID" -}}
{{- end -}}
{{- if eq (lower $tenant) "00000000-0000-0000-0000-000000000000" -}}
{{- fail "keyVault.tenantId must not be all zeroes" -}}
{{- end -}}
{{- $client := toString ($kv.clientId | default "") -}}
{{- if not (regexMatch "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$" $client) -}}
{{- fail "keyVault.clientId must be a UUID" -}}
{{- end -}}
{{- if eq (lower $client) "00000000-0000-0000-0000-000000000000" -}}
{{- fail "keyVault.clientId must not be all zeroes" -}}
{{- end -}}
{{- if not (regexMatch "^[A-Za-z0-9-]{1,127}$" (toString ($kv.certificateSecretName | default ""))) -}}
{{- fail "keyVault.certificateSecretName must be a Key Vault secret name" -}}
{{- end -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" (toString (.Values.image.digest | default ""))) -}}
{{- fail "image.digest must be a sha256 digest" -}}
{{- end -}}
{{- if or (and (not (kindIs "int" .Values.replicaCount)) (not (kindIs "int64" .Values.replicaCount)) (not (kindIs "float64" .Values.replicaCount))) (ne (float64 .Values.replicaCount) 2.0) -}}
{{- fail "replicaCount must be exactly 2 to preserve CSI consumers" -}}
{{- end -}}
{{- end -}}
