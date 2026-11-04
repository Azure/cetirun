{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun.context.enabled" -}}
{{- $nativeReconciled := and .Values.sandboxWorkload .Values.sandboxWorkload.reconciled (ne (include "cetirun.agent.type" .) "custom") -}}
{{- if and (or (not .Values.sandboxWorkload) $nativeReconciled) (or .Values.memoryContext.enabled (and (eq (include "cetirun.agent.type" .) "custom") .Values.skills.shared)) -}}true{{- end -}}
{{- end -}}

{{- define "cetirun.context.home" -}}
{{- if eq (include "cetirun.agent.type" .) "custom" -}}
{{- .Values.runtime.agent.home -}}
{{- else -}}/home/vibe{{- end -}}
{{- end -}}

{{- define "cetirun.context.validate" -}}
{{- if include "cetirun.context.enabled" . -}}
{{- $root := printf "%s/.agents" (include "cetirun.context.home" .) -}}
{{- if and .Values.skills.shared (ne .Values.skills.mountPath (printf "%s/skills" $root)) -}}
{{- $alias := .Values.skills.mountPath -}}
{{- if or (not (hasPrefix "/" $alias)) (ne $alias (clean $alias)) (eq $alias "/") -}}{{- fail "skills.mountPath must be a clean absolute path" -}}{{- end -}}
{{- range list $root (printf "%s/.cetirun/memory" (include "cetirun.context.home" .)) "/opt/cetirun-memory" "/etc/cetirun" "/tmp" -}}
{{- if or (eq . $alias) (hasPrefix (printf "%s/" .) $alias) (hasPrefix (printf "%s/" $alias) .) -}}
{{- fail "skills.mountPath overlaps a platform path" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range .Values.extraVolumeMounts -}}
{{- $mount := clean .mountPath -}}
{{- if or (eq $mount "/") (eq $mount $root) (hasPrefix (printf "%s/" $root) $mount) (hasPrefix (printf "%s/" $mount) $root) -}}
{{- fail "the Agent context path is reserved" -}}
{{- end -}}
{{- end -}}
{{- range .Values.extraVolumes -}}
{{- if eq .name "context-view" -}}{{- fail "the Agent context volume is reserved" -}}{{- end -}}
{{- end -}}
{{- range concat (.Values.initContainers | default list) (.Values.sidecars | default list) -}}
{{- range .volumeMounts -}}
{{- if eq .name "context-view" -}}{{- fail "additional containers cannot mount the Agent context volume" -}}{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "cetirun.context.initEnv" -}}
{{- if or (not .Values.sandboxWorkload) (and .Values.sandboxWorkload.reconciled (ne (include "cetirun.agent.type" .) "custom")) }}
{{- if include "cetirun.context.enabled" . }}
- name: CETIRUN_CONTEXT_VIEW
  value: /etc/cetirun-context-view
{{- end }}
{{- if and .Values.skills.shared (eq (include "cetirun.agent.type" .) "custom") (include "cetirun.context.enabled" .) }}
- name: CETIRUN_SHARED_SKILLS_DIR
  value: /etc/cetirun-shared-skills
{{- end }}
{{- if .Values.memoryContext.enabled }}
- name: CETIRUN_MEMORY_ASSETS
  value: /opt/cetirun-memory
{{- end }}
{{- end }}
{{- end -}}

{{- define "cetirun.context.initMounts" -}}
{{- if or (not .Values.sandboxWorkload) (and .Values.sandboxWorkload.reconciled (ne (include "cetirun.agent.type" .) "custom")) }}
{{- if include "cetirun.context.enabled" . }}
- name: context-view
  mountPath: /etc/cetirun-context-view
{{- end }}
{{- if and .Values.skills.shared (eq (include "cetirun.agent.type" .) "custom") (include "cetirun.context.enabled" .) }}
- name: shared-skills
  mountPath: /etc/cetirun-shared-skills
  readOnly: true
{{- end }}
{{- include "cetirun.memory.mounts" . }}
{{- end }}
{{- end -}}

{{- define "cetirun.context.mount" -}}
{{- if include "cetirun.context.enabled" . }}
{{- if ne (include "cetirun.agent.type" .) "custom" }}
{{- if .Values.skills.shared }}
- name: shared-skills
  mountPath: {{ .Values.skills.mountPath | quote }}
  readOnly: true
{{- end }}
- name: context-view
  mountPath: /home/vibe/.agents/AGENTS.md
  subPath: current/AGENTS.md
  readOnly: true
{{- else }}
- name: context-view
  mountPath: {{ printf "%s/.agents" (include "cetirun.context.home" .) | quote }}
  subPath: current
  readOnly: true
{{- if and .Values.skills.shared (ne .Values.skills.mountPath (printf "%s/.agents/skills" (include "cetirun.context.home" .))) }}
- name: context-view
  mountPath: {{ .Values.skills.mountPath | quote }}
  subPath: current/skills
  readOnly: true
{{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "cetirun.context.volume" -}}
{{- if include "cetirun.context.enabled" . }}
- name: context-view
  {{- if and .Values.sandboxWorkload .Values.sandboxWorkload.reconciled }}
  emptyDir:
    sizeLimit: {{ .Values.sandboxWorkload.scratchSizeLimit | quote }}
  {{- else if ne (include "cetirun.agent.type" .) "custom" }}
  emptyDir:
    sizeLimit: 40Mi
  {{- else }}
  emptyDir: {}
  {{- end }}
{{- end }}
{{- end -}}
