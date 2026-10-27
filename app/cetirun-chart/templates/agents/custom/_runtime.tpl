{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.agent.custom.configmapData" }}
  custom-init-home.sh: |
{{ .Files.Get "agents/custom/bootstrap/init-home.sh" | indent 4 }}
{{- end }}

{{- define "cetirun.agent.custom.validateDesktopBrowserConfig" -}}{{- end }}

{{- define "cetirun.agent.custom.servicePorts" -}}
{{- range .Values.runtime.ports }}
- name: {{ .name | quote }}
  port: {{ .number }}
  targetPort: {{ .name | quote }}
  protocol: TCP
{{- end }}
{{- end }}

{{- define "cetirun.agent.custom.networkPolicyIngressPorts" -}}{{- end }}

{{- define "cetirun.custom.validate" -}}
{{- $r := .Values.runtime | default dict -}}
{{- if not (and .Values.sandboxWorkload .Values.sandboxWorkload.reconciled) -}}
{{- fail "Custom requires the common reconciled Sandbox execution plan" -}}
{{- end -}}
{{- if or (not $r.agent) (not $r.model) $r.bindings $r.planHash -}}
{{- fail "Custom requires common Sandbox model and Harness inputs; legacy runtime plans are unsupported" -}}
{{- end -}}
{{- if or (ne $r.agent.type "custom") (not $r.agent.image) (not $r.agent.home) (not $r.model.apiBase) (not $r.model.keySecretName) -}}
{{- fail "Custom requires an approved image, HOME and AI Gateway binding" -}}
{{- end -}}
{{- if or .Values.agentIdentity.enabled .Values.connectionsMCP.enabled -}}
{{- fail "Custom does not support Agent Identity or CetiRun Connections MCP" -}}
{{- end -}}
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
{{- $s := .Values.sandboxWorkload -}}
{{- $emptySkills := eq ($s.skillsMode | default "") "empty" -}}
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ include "cetirun.fullname" . }}
  ownerReferences:
    {{- toYaml $s.ownerReferences | nindent 4 }}
  labels:
    {{- include "cetirun.labels" . | nindent 4 }}
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  serviceName: {{ include "cetirun.fullname" . }}
  updateStrategy:
    type: RollingUpdate
  selector:
    matchLabels:
      {{- include "cetirun.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      {{- include "cetirun.platform.podMetadata" . | nindent 6 }}
      {{- with $s.podFinalizers }}
      finalizers:
        {{- toYaml . | nindent 8 }}
      {{- end }}
    spec:
      {{- include "cetirun.platform.podSettings" . | nindent 6 }}
      initContainers:
        {{- include "cetirun.memory.init" . | nindent 8 }}
        - name: init-home-data
          image: {{ $r.agent.image | quote }}
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "/etc/cetirun/custom-init-home.sh"]
          securityContext:
            {{- toYaml $s.securityContext | nindent 12 }}
          resources:
            {{- toYaml $s.helperResources | nindent 12 }}
          env:
            - name: SOURCE_HOME
              value: {{ $r.agent.home | quote }}
            - name: TARGET_HOME
              value: /home-data
            - name: CETIRUN_INIT_IMAGE
              value: {{ $r.agent.image | quote }}
            - name: CETIRUN_INIT_COMMON
              value: /etc/cetirun/init-home-common.sh
            {{- include "cetirun.context.initEnv" . | nindent 12 }}
          volumeMounts:
            - name: data
              mountPath: /home-data
            - name: config
              mountPath: /etc/cetirun
              readOnly: true
            - name: tmp
              mountPath: /tmp
            {{- include "cetirun.context.initMounts" . | nindent 12 }}
        {{- with .Values.initContainers }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      containers:
        - name: {{ .Chart.Name }}
          image: {{ $r.agent.image | quote }}
          imagePullPolicy: IfNotPresent
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          env:
            - name: HOME
              value: {{ $r.agent.home | quote }}
            - name: OPENAI_BASE_URL
              value: {{ $r.model.apiBase | quote }}
            - name: OPENAI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: {{ $r.model.keySecretName | quote }}
                  key: {{ $r.model.keySecretKey | quote }}
            {{- if $r.agent.port }}
            - name: PORT
              value: {{ $r.agent.port | quote }}
            {{- end }}
            {{- if $r.agent.webUI }}
            - name: CETIRUN_WEB_BASE_PATH
              value: {{ required "Custom Web UI requires its platform base path" $r.agent.webBasePath | quote }}
            - name: CETIRUN_WEB_INSTANCE
              value: {{ required "Custom Web UI requires its Agent UID" $r.agent.webInstance | quote }}
            {{- end }}
            {{- with .Values.extraEnv }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- include "cetirun.memory.env" . | nindent 12 }}
            {{- with $s.identityEnvironment }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          {{- if $r.ports }}
          ports:
            {{- range $r.ports }}
            - name: {{ .name | quote }}
              containerPort: {{ .number }}
              protocol: TCP
            {{- end }}
          {{- end }}
          {{- with $r.http }}
          startupProbe:
            httpGet:
              path: {{ .startupPath | quote }}
              port: {{ .portName | quote }}
            periodSeconds: 5
            failureThreshold: 60
          readinessProbe:
            httpGet:
              path: {{ .healthPath | quote }}
              port: {{ .portName | quote }}
            periodSeconds: 10
          {{- end }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          volumeMounts:
            - name: data
              mountPath: {{ $r.agent.home | quote }}
            - name: tmp
              mountPath: /tmp
            - name: config
              mountPath: /etc/cetirun
              readOnly: true
            {{- with $s.identityMounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- if $emptySkills }}
            - name: empty-skills
              mountPath: {{ .Values.skills.mountPath | quote }}
              readOnly: true
            {{- else if .Values.skills.shared }}
            - name: shared-skills
              mountPath: {{ .Values.skills.mountPath | quote }}
              readOnly: true
            {{- end }}
            {{- with .Values.extraVolumeMounts }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
            {{- include "cetirun.memory.mounts" . | nindent 12 }}
        {{- with .Values.sidecars }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      volumes:
        - name: config
          configMap:
            name: {{ include "cetirun.configMapName" . }}
            defaultMode: 0444
        - name: data
          persistentVolumeClaim:
            claimName: {{ .Values.persistence.existingClaim | quote }}
        {{- include "cetirun.platform.temporaryVolumes" . | nindent 8 }}
        {{- if $emptySkills }}
        - name: empty-skills
          emptyDir:
            sizeLimit: {{ $s.scratchSizeLimit | quote }}
        {{- else if .Values.skills.shared }}
        - name: shared-skills
          persistentVolumeClaim:
            claimName: {{ .Values.skills.existingClaim | quote }}
            readOnly: true
        {{- end }}
        {{- with .Values.extraVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- include "cetirun.memory.volumes" . | nindent 8 }}
        {{- with $s.identityVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
{{- end }}
