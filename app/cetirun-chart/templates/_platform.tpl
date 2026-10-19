{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.platform.desktop" -}}
- name: desktop
  image: "{{ include "cetirun.desktopImage" . }}"
  imagePullPolicy: {{ .Values.desktop.image.pullPolicy }}
  securityContext:
    {{- if .Values.sandboxWorkload }}
    {{- toYaml .Values.sandboxWorkload.securityContext | nindent 4 }}
    {{- else }}
    runAsUser: 1024
    runAsNonRoot: true
    readOnlyRootFilesystem: false
    allowPrivilegeEscalation: false
    seccompProfile:
      type: RuntimeDefault
    {{- end }}
  ports:
    - name: novnc
      containerPort: 6080
      protocol: TCP
    - name: chrome-cdp
      containerPort: 18800
      protocol: TCP
  env:
    - name: DISPLAY_NUM
      value: "99"
    - name: SCREEN_RESOLUTION
      value: {{ .Values.desktop.screenResolution | quote }}
    - name: VNC_PORT
      value: "5900"
    - name: NOVNC_PORT
      value: "6080"
    - name: NOVNC_PATH_PREFIX
      value: {{ .Values.desktop.novncPathPrefix | quote }}
    - name: DESKTOP_LOGGING
      value: {{ .Values.desktop.logging | quote }}
  resources:
    {{- toYaml .Values.desktop.resources | nindent 4 }}
  volumeMounts:
    - name: data
      mountPath: /home/vibe
    - name: tmp-desktop
      mountPath: /tmp
{{- end }}

{{- define "cetirun.platform.podMetadata" -}}
{{- $bindings := (.Values.runtime | default dict).bindings -}}
annotations:
  {{- if $bindings }}
  {{/* Chart labels must not create an unapplied OnDelete Pod revision. */}}
  checksum/config: {{ (include (print .Template.BasePath "/configmap.yaml") . | fromYaml).data | toJson | sha256sum }}
  cetirun.microsoft.com/custom-plan-hash: {{ .Values.runtime.workloadHash | quote }}
  cetirun.microsoft.com/model-revision: {{ $bindings.model.revision | quote }}
  cetirun.microsoft.com/context-revision: {{ $bindings.context.revision | quote }}
  {{- with $bindings.restartRequestID }}
  cetirun.microsoft.com/restart-request-id: {{ . | quote }}
  {{- end }}
  {{- else }}
  checksum/config: {{ include (print .Template.BasePath "/configmap.yaml") . | sha256sum }}
  checksum/secret: {{ include (print .Template.BasePath "/secret.yaml") . | sha256sum }}
  {{- end }}
  {{- with .Values.podAnnotations }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
labels:
  {{- include "cetirun.selectorLabels" . | nindent 2 }}
  {{- with .Values.sandboxWorkload }}
  {{- toYaml .podLabels | nindent 2 }}
  {{- end }}
  {{- if and (not $bindings) .Values.azureWorkloadIdentity.enabled }}
  azure.workload.identity/use: "true"
  {{- end }}
{{- end }}

{{- define "cetirun.platform.podSettings" -}}
{{- $bindings := (.Values.runtime | default dict).bindings -}}
{{- if .Values.sandboxWorkload }}
resources:
  {{- toYaml .Values.sandboxWorkload.podResources | nindent 2 }}
enableServiceLinks: false
shareProcessNamespace: false
dnsPolicy: None
dnsConfig:
  {{- toYaml .Values.sandboxWorkload.dnsConfig | nindent 2 }}
{{- end }}
{{- with .Values.runtimeClassName }}
runtimeClassName: {{ . | quote }}
{{- end }}
{{ include "cetirun.platform.pullSecrets" . }}
{{- if $bindings }}
serviceAccountName: {{ include "cetirun.fullname" . }}
automountServiceAccountToken: false
securityContext:
  runAsNonRoot: true
  fsGroup: {{ $bindings.home.fsGroup }}
  fsGroupChangePolicy: OnRootMismatch
  seccompProfile:
    type: RuntimeDefault
{{- else }}
serviceAccountName: {{ include "cetirun.serviceAccountName" . }}
automountServiceAccountToken: {{ include "cetirun.automountServiceAccountToken" . }}
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- end }}
{{ include "cetirun.platform.scheduling" . }}
{{- end }}

{{- define "cetirun.platform.pullSecrets" -}}
{{- $runtime := .Values.runtime | default dict -}}
{{- $bindings := $runtime.bindings | default dict -}}
{{- if $bindings.pullSecretName }}
imagePullSecrets:
  - name: {{ $bindings.pullSecretName | quote }}
{{- else if $runtime.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml $runtime.imagePullSecrets | nindent 2 }}
{{- else if .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml .Values.imagePullSecrets | nindent 2 }}
{{- end }}
{{- end }}

{{- define "cetirun.platform.temporaryVolumes" -}}
- name: tmp
  {{- if .Values.sandboxWorkload }}
  emptyDir:
    sizeLimit: {{ .Values.sandboxWorkload.scratchSizeLimit | quote }}
  {{- else }}
  emptyDir: {}
  {{- end }}
{{- if .Values.desktop.enabled }}
- name: tmp-desktop
  {{- if .Values.sandboxWorkload }}
  emptyDir:
    sizeLimit: {{ .Values.sandboxWorkload.scratchSizeLimit | quote }}
  {{- else }}
  emptyDir: {}
  {{- end }}
{{- end }}
{{- end }}

{{- define "cetirun.platform.scheduling" -}}
{{- with .Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .Values.topologySpreadConstraints }}
topologySpreadConstraints:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}
