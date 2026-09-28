{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.platform.desktop" -}}
- name: desktop
  image: "{{ include "cetirun.desktopImage" . }}"
  imagePullPolicy: {{ .Values.desktop.image.pullPolicy }}
  securityContext:
    runAsUser: 1024
    runAsNonRoot: true
    readOnlyRootFilesystem: false
    allowPrivilegeEscalation: false
    seccompProfile:
      type: RuntimeDefault
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
  checksum/config: {{ include (print .Template.BasePath "/configmap.yaml") . | sha256sum }}
  {{- if $bindings }}
  cetirun.microsoft.com/custom-plan-hash: {{ .Values.runtime.workloadHash | quote }}
  cetirun.microsoft.com/model-revision: {{ $bindings.model.revision | quote }}
  cetirun.microsoft.com/context-revision: {{ $bindings.context.revision | quote }}
  {{- else }}
  checksum/secret: {{ include (print .Template.BasePath "/secret.yaml") . | sha256sum }}
  {{- end }}
  {{- with .Values.podAnnotations }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
labels:
  {{- include "cetirun.selectorLabels" . | nindent 2 }}
  {{- if and (not $bindings) .Values.azureWorkloadIdentity.enabled }}
  azure.workload.identity/use: "true"
  {{- end }}
{{- end }}

{{- define "cetirun.platform.podSettings" -}}
{{- $bindings := (.Values.runtime | default dict).bindings -}}
{{- with .Values.runtimeClassName }}
runtimeClassName: {{ . | quote }}
{{- end }}
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
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
serviceAccountName: {{ include "cetirun.serviceAccountName" . }}
automountServiceAccountToken: {{ include "cetirun.automountServiceAccountToken" . }}
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- end }}
{{ include "cetirun.platform.scheduling" . }}
{{- end }}

{{- define "cetirun.platform.temporaryVolumes" -}}
- name: tmp
  emptyDir: {}
{{- if .Values.desktop.enabled }}
- name: tmp-desktop
  emptyDir: {}
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
