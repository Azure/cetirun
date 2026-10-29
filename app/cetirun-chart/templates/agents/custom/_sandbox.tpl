{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.custom.runtime" -}}
{{- if .Values.sandboxWorkload -}}
{{- include "cetirun.custom.sandboxRuntime" . -}}
{{- else -}}
{{- include "cetirun.custom.legacyRuntime" . -}}
{{- end -}}
{{- end -}}

{{- define "cetirun.custom.validate" -}}
{{- if .Values.sandboxWorkload -}}
{{- include "cetirun.custom.sandboxValidate" . -}}
{{- else -}}
{{- include "cetirun.custom.legacyValidate" . -}}
{{- end -}}
{{- end -}}

{{- define "cetirun.custom.sandboxValidate" -}}
{{- $r := .Values.runtime | default dict -}}
{{- $s := .Values.sandboxWorkload | default dict -}}
{{- if not $s.reconciled -}}
{{- fail "Custom requires common reconciled Sandbox execution" -}}
{{- end -}}
{{- if or (not $r.agent) (not $r.model) $r.bindings $r.planHash -}}
{{- fail "Custom requires the compiler-owned Harness and Gateway contribution" -}}
{{- end -}}
{{- if or (ne $r.agent.type "custom") (not $r.agent.image) (not $r.agent.home) (not $r.model.apiBase) (not $r.model.keySecretName) -}}
{{- fail "Custom requires an approved image, HOME and Gateway binding" -}}
{{- end -}}
{{- if or (not $s.context) (not $s.context.name) (ne (len $s.context.files) 1) (ne (index $s.context.files "AGENTS.md") "AGENTS.md") -}}
{{- fail "Custom requires a controller-owned canonical context" -}}
{{- end -}}
{{- if and $r.http (or (not $r.agent.port) (not $s.loopbackProbe) (not $s.loopbackProbe.image)) -}}
{{- fail "Custom HTTP requires a platform loopback probe image and port" -}}
{{- end -}}
{{- if or .Values.agentIdentity.enabled .Values.connectionsMCP.enabled -}}
{{- fail "Custom Agent Identity and Connections require a qualified credential executor" -}}
{{- end -}}
{{- end -}}

{{- define "cetirun.custom.sandboxRuntime" -}}
{{- include "cetirun.custom.sandboxValidate" . -}}
{{- $r := .Values.runtime -}}
{{- $s := .Values.sandboxWorkload -}}
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
            - name: CETIRUN_CONTEXT_DIR
              value: /etc/cetirun-context
            - name: CETIRUN_CONTEXT_VIEW
              value: /etc/cetirun-context-view
            {{- if .Values.skills.shared }}
            - name: CETIRUN_SHARED_SKILLS_DIR
              value: /etc/cetirun-shared-skills
            {{- end }}
            {{- if .Values.memoryContext.enabled }}
            - name: CETIRUN_MEMORY_ASSETS
              value: /opt/cetirun-memory
            {{- end }}
          volumeMounts:
            - name: data
              mountPath: /home-data
            - name: config
              mountPath: /etc/cetirun
              readOnly: true
            - name: tmp
              mountPath: /tmp
            - name: context
              mountPath: /etc/cetirun-context
              readOnly: true
            - name: context-view
              mountPath: /etc/cetirun-context-view
            {{- if .Values.skills.shared }}
            - name: shared-skills
              mountPath: /etc/cetirun-shared-skills
              readOnly: true
            {{- end }}
            {{- if .Values.memoryContext.enabled }}
            - name: memory-assets
              mountPath: /opt/cetirun-memory
              readOnly: true
            {{- end }}
        {{- with $r.http }}
        - name: install-loopback-probe
          image: {{ $s.loopbackProbe.image | quote }}
          imagePullPolicy: IfNotPresent
          command: ["/loopback-probe", "install", "/cetirun-probe/http-probe"]
          securityContext:
            {{- toYaml $s.securityContext | nindent 12 }}
          resources:
            {{- toYaml $s.helperResources | nindent 12 }}
          volumeMounts:
            - name: loopback-probe
              mountPath: /cetirun-probe
        {{- end }}
      containers:
        - name: {{ .Chart.Name }}
          image: {{ $r.agent.image | quote }}
          imagePullPolicy: IfNotPresent
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          env:
            {{- range $r.environment }}
            - name: {{ .name }}
              {{- if .credentialSlot }}
              {{- if ne .credentialSlot "model" }}{{- fail "Unknown Custom credential slot" }}{{- end }}
              valueFrom:
                secretKeyRef:
                  name: {{ $r.model.keySecretName | quote }}
                  key: {{ $r.model.keySecretKey | quote }}
              {{- else }}
              value: {{ .value | quote }}
              {{- end }}
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
            exec:
              command: ["/cetirun-probe/http-probe", "--url", {{ printf "http://127.0.0.1:%v%s" $r.agent.port .startupPath | quote }}]
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 60
          readinessProbe:
            exec:
              command: ["/cetirun-probe/http-probe", "--url", {{ printf "http://127.0.0.1:%v%s" $r.agent.port .healthPath | quote }}]
            periodSeconds: 10
            timeoutSeconds: 3
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
            - name: context-view
              mountPath: {{ printf "%s/.agents" $r.agent.home | quote }}
              subPath: current
              readOnly: true
            {{- with $r.http }}
            - name: loopback-probe
              mountPath: /cetirun-probe
              readOnly: true
            {{- end }}
            {{- include "cetirun.memory.mounts" . | nindent 12 }}
      volumes:
        - name: config
          configMap:
            name: {{ include "cetirun.configMapName" . }}
            defaultMode: 0444
        - name: data
          persistentVolumeClaim:
            claimName: {{ .Values.persistence.existingClaim | quote }}
        - name: context
          configMap:
            name: {{ $s.context.name | quote }}
            defaultMode: 0444
            items:
              - key: AGENTS.md
                path: AGENTS.md
        - name: context-view
          emptyDir:
            sizeLimit: {{ $s.scratchSizeLimit | quote }}
        {{- with $r.http }}
        - name: loopback-probe
          emptyDir:
            sizeLimit: {{ $s.scratchSizeLimit | quote }}
        {{- end }}
        {{- include "cetirun.platform.temporaryVolumes" . | nindent 8 }}
        {{- if .Values.skills.shared }}
        - name: shared-skills
          persistentVolumeClaim:
            claimName: {{ .Values.skills.existingClaim | quote }}
            readOnly: true
        {{- end }}
        {{- include "cetirun.memory.volumes" . | nindent 8 }}
        {{- with $s.identityVolumes }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
{{- if and (not $r.ports) (not .Values.desktop.enabled) }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ include "cetirun.fullname" . }}
  ownerReferences:
    {{- toYaml $s.ownerReferences | nindent 4 }}
  labels:
    {{- include "cetirun.labels" . | nindent 4 }}
spec:
  type: ClusterIP
  clusterIP: None
  selector:
    {{- include "cetirun.selectorLabels" . | nindent 4 }}
{{- end }}
{{- end }}
