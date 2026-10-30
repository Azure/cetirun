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

{{/* New ingress options retain their defaults when upgrading older values. */}}
{{- define "cetirun-istio.ingressSettings" -}}
{{- $settings := dict "includeWildcardHost" true "cookieSecretLegacyNamespace" "istio-system" "skipJwtBearerTokens" (ne .Values.oauth2Proxy.mode "local-oidc") -}}
{{- if hasKey .Values.gateway "includeWildcardHost" -}}
{{- $value := .Values.gateway.includeWildcardHost -}}
{{- if not (kindIs "bool" $value) -}}
{{- fail "gateway.includeWildcardHost must be a boolean" -}}
{{- end -}}
{{- $_ := set $settings "includeWildcardHost" $value -}}
{{- end -}}
{{- if hasKey .Values "cookieSecretLegacyNamespace" -}}
{{- $value := .Values.cookieSecretLegacyNamespace -}}
{{- if not (kindIs "string" $value) -}}
{{- fail "cookieSecretLegacyNamespace must be a namespace string or empty to disable migration" -}}
{{- end -}}
{{- if and $value (or (gt (len $value) 63) (not (regexMatch `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$` $value))) -}}
{{- fail "cookieSecretLegacyNamespace must be a valid namespace or empty to disable migration" -}}
{{- end -}}
{{- $_ := set $settings "cookieSecretLegacyNamespace" $value -}}
{{- end -}}
{{- $bearer := .Values.oauth2Proxy.skipJwtBearerTokens -}}
{{- if not (kindIs "invalid" $bearer) -}}
{{- if not (kindIs "bool" $bearer) -}}
{{- fail "oauth2Proxy.skipJwtBearerTokens must be a boolean or null for the mode default" -}}
{{- end -}}
{{- if and (eq .Values.oauth2Proxy.mode "local-oidc") $bearer -}}
{{- fail "oauth2Proxy.skipJwtBearerTokens=true is not supported in local-oidc mode" -}}
{{- end -}}
{{- $_ := set $settings "skipJwtBearerTokens" $bearer -}}
{{- end -}}
{{- $logging := dict "request" true "auth" true "standard" true -}}
{{- if hasKey .Values.oauth2Proxy "logging" -}}
{{- if not (kindIs "map" .Values.oauth2Proxy.logging) -}}
{{- fail "oauth2Proxy.logging must be an object" -}}
{{- end -}}
{{- range $key, $value := .Values.oauth2Proxy.logging -}}
{{- if not (hasKey $logging $key) -}}
{{- fail (printf "oauth2Proxy.logging.%s is not supported" $key) -}}
{{- end -}}
{{- if not (kindIs "bool" $value) -}}
{{- fail (printf "oauth2Proxy.logging.%s must be a boolean" $key) -}}
{{- end -}}
{{- $_ := set $logging $key $value -}}
{{- end -}}
{{- end -}}
{{- $_ := set $settings "logging" $logging -}}
{{- toJson $settings -}}
{{- end }}

{{/* Keep anonymous Teams webhook bypasses on exact route shapes only. */}}
{{- define "cetirun-istio.validateWebhookBypasses" -}}
{{- if and .Values.authPolicy.oauthCallbackPath (ne .Values.authPolicy.oauthCallbackPath "/api/v1/connections/callback") -}}
{{- fail "authPolicy.oauthCallbackPath supports only /api/v1/connections/callback" -}}
{{- end -}}
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
