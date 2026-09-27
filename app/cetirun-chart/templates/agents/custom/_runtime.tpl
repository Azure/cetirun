{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.agent.custom.configmapData" }}
  custom-init-home.sh: |
{{ .Files.Get "agents/custom/bootstrap/init-home.sh" | indent 4 }}
{{- end }}

{{- define "cetirun.agent.custom.validateDesktopBrowserConfig" -}}
{{- end }}

{{- define "cetirun.agent.custom.servicePorts" -}}
{{- range .Values.runtime.ports }}
- name: {{ .name | quote }}
  port: {{ .number }}
  targetPort: {{ .name | quote }}
  protocol: TCP
{{- end }}
{{- end }}

{{- define "cetirun.agent.custom.networkPolicyIngressPorts" -}}
{{/* Agent HTTP callers are bound separately; the gateway exposes only platform services. */}}
{{- end }}

{{- define "cetirun.custom.validate" -}}
{{- if not .Values.runtime -}}{{- fail "Custom requires a platform-prepared runtime plan" -}}{{- end -}}
{{- $r := .Values.runtime -}}
{{- if or (ne $r.contractVersion "v1") (ne $r.agent.type "custom") -}}{{- fail "Invalid Custom runtime contract" -}}{{- end -}}
{{- if or (not $r.agent.image) (not $r.agent.home) (not $r.bindings.home.name) (not $r.bindings.context.name) (and (not $r.bindings.agentManagedModel) (not $r.bindings.model.name)) (not $r.planHash) -}}
{{- fail "Custom requires image, model, HOME and context bindings" -}}
{{- end -}}
{{- if ne $r.bindings.namespace.name .Release.Namespace -}}{{- fail "Custom binding namespace mismatch" -}}{{- end -}}
{{- if or .Values.extraEnv .Values.extraEnvFrom .Values.initContainers .Values.sidecars .Values.extraVolumes .Values.extraVolumeMounts .Values.imagePullSecrets -}}
{{- fail "Custom does not accept raw environment, container, volume or pull-Secret overrides" -}}
{{- end -}}
{{- end }}

{{- define "cetirun.custom.securityContext" }}
runAsNonRoot: true
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "cetirun.custom.peer" }}
namespaceSelector:
  matchLabels:
    kubernetes.io/metadata.name: {{ .namespace | quote }}
podSelector:
  matchLabels:
    {{- toYaml .podLabels | nindent 4 }}
{{- end }}

{{- define "cetirun.custom.runtime" -}}
{{- include "cetirun.custom.validate" . -}}
{{- $r := .Values.runtime -}}
{{- $b := $r.bindings -}}
{{- $name := include "cetirun.fullname" . -}}
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ $name }}
  ownerReferences:
    - apiVersion: cetirun.microsoft.com/v1alpha1
      kind: Agent
      name: {{ $b.agentName | quote }}
      uid: {{ $b.agentUID | quote }}
      controller: true
      blockOwnerDeletion: true
  labels:
    {{- include "cetirun.labels" . | nindent 4 }}
  annotations:
    cetirun.microsoft.com/agent-uid: {{ $b.agentUID | quote }}
    cetirun.microsoft.com/home-uid: {{ $b.home.uid | quote }}
    cetirun.microsoft.com/custom-plan-hash: {{ $r.workloadHash | quote }}
spec:
  replicas: {{ .Values.replicaCount }}
  serviceName: {{ $name }}
  updateStrategy:
    type: OnDelete
  selector:
    matchLabels:
      {{- include "cetirun.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      {{- include "cetirun.platform.podMetadata" . | nindent 6 }}
    spec:
      {{- include "cetirun.platform.podSettings" . | nindent 6 }}
      initContainers:
        - name: init-home-data
          image: {{ $r.agent.image | quote }}
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "/etc/cetirun/custom-init-home.sh"]
          securityContext:
            {{- include "cetirun.custom.securityContext" . | nindent 12 }}
          env:
            - name: SOURCE_HOME
              value: {{ $r.agent.home | quote }}
            - name: TARGET_HOME
              value: /home-data
            - name: CETIRUN_INIT_IMAGE
              value: {{ $r.agent.image | quote }}
            - name: CETIRUN_INIT_COMMON
              value: /etc/cetirun/init-home-common.sh
            - name: CETIRUN_CONTEXT_DIR
              value: /etc/cetirun-context
            {{- if .Values.skills.shared }}
            - name: CETIRUN_SHARED_SKILLS_DIR
              value: /etc/cetirun-shared-skills
            - name: CETIRUN_CONTEXT_VIEW
              value: /etc/cetirun-context-view
            {{- end }}
          volumeMounts:
            - name: data
              mountPath: /home-data
            - name: config
              mountPath: /etc/cetirun
              readOnly: true
            - name: context
              mountPath: /etc/cetirun-context
              readOnly: true
            - name: tmp
              mountPath: /tmp
            {{- if .Values.skills.shared }}
            - name: shared-skills
              mountPath: /etc/cetirun-shared-skills
              readOnly: true
            - name: context-view
              mountPath: /etc/cetirun-context-view
            {{- end }}
      containers:
        - name: {{ .Chart.Name }}
          image: {{ $r.agent.image | quote }}
          imagePullPolicy: IfNotPresent
          securityContext:
            {{- include "cetirun.custom.securityContext" . | nindent 12 }}
          env:
            {{- range $r.environment }}
            - name: {{ .name }}
              {{- if .credentialSlot }}
              {{- if ne .credentialSlot "model" }}{{- fail "Unknown Custom credential slot" }}{{- end }}
              valueFrom:
                secretKeyRef:
                  name: {{ $b.model.name | quote }}
                  key: {{ $b.model.key | quote }}
              {{- else }}
              value: {{ .value | quote }}
              {{- end }}
            {{- end }}
          {{- if $r.http }}
          ports:
            {{- range $r.ports }}
            - name: {{ .name | quote }}
              containerPort: {{ .number }}
              protocol: TCP
            {{- end }}
          startupProbe:
            httpGet:
              path: {{ $r.http.startupPath | quote }}
              port: {{ $r.http.portName | quote }}
            periodSeconds: 5
            failureThreshold: 60
          readinessProbe:
            httpGet:
              path: {{ $r.http.healthPath | quote }}
              port: {{ $r.http.portName | quote }}
            periodSeconds: 10
          {{- end }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          volumeMounts:
            - name: data
              mountPath: {{ $r.agent.home | quote }}
            - name: {{ ternary "context-view" "context" .Values.skills.shared }}
              mountPath: {{ printf "%s/.agents" $r.agent.home | quote }}
              readOnly: true
              {{- if .Values.skills.shared }}
              subPath: current
              {{- end }}
            - name: tmp
              mountPath: /tmp
        {{- if .Values.desktop.enabled }}
        {{- include "cetirun.platform.desktop" . | nindent 8 }}
        {{- end }}
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: {{ $b.home.name | quote }}
        - name: context
          configMap:
            name: {{ $b.context.name | quote }}
            defaultMode: 0444
            items:
              {{- range $key, $path := $b.context.files }}
              - key: {{ $key | quote }}
                path: {{ $path | quote }}
              {{- end }}
        - name: config
          configMap:
            name: {{ include "cetirun.configMapName" . }}
            defaultMode: 0444
        {{- include "cetirun.platform.temporaryVolumes" . | nindent 8 }}
        {{- if .Values.skills.shared }}
        - name: shared-skills
          persistentVolumeClaim:
            claimName: {{ include "cetirun.sharedSkillsPVCName" . }}
            readOnly: true
        - name: context-view
          emptyDir: {}
        {{- end }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $name }}
automountServiceAccountToken: false
{{- end }}
