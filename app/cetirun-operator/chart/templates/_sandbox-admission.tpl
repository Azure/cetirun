{{/*
Copyright (c) Microsoft Corporation.
Licensed under the MIT License.
*/}}
{{- define "cetirun-operator.sandboxCleanupMatch" -}}
- name: controller-cleanup-only
  expression: >-
    !(
      request.operation == "UPDATE" &&
      request.resource.group == "cetirun.microsoft.com" &&
      request.resource.resource == "agents" &&
      (has(request.subResource) ? request.subResource : "") == "" &&
      request.userInfo.username == {{ . | toJson }} &&
      object != null && oldObject != null &&
      has(oldObject.metadata.deletionTimestamp) &&
      oldObject.metadata.deletionTimestamp != null &&
      (has(object.apiVersion) ? object.apiVersion : "") ==
        (has(oldObject.apiVersion) ? oldObject.apiVersion : "") &&
      (has(object.kind) ? object.kind : "") ==
        (has(oldObject.kind) ? oldObject.kind : "") &&
      (has(object.spec) ? object.spec : {}) ==
        (has(oldObject.spec) ? oldObject.spec : {}) &&
      (has(object.status) ? object.status : {}) ==
        (has(oldObject.status) ? oldObject.status : {}) &&
      (has(object.metadata.name) ? object.metadata.name : "") ==
        (has(oldObject.metadata.name) ? oldObject.metadata.name : "") &&
      (has(object.metadata.namespace) ? object.metadata.namespace : "") ==
        (has(oldObject.metadata.namespace) ? oldObject.metadata.namespace : "") &&
      (has(object.metadata.uid) ? object.metadata.uid : "") ==
        (has(oldObject.metadata.uid) ? oldObject.metadata.uid : "") &&
      (has(object.metadata.labels) ? object.metadata.labels : {}) ==
        (has(oldObject.metadata.labels) ? oldObject.metadata.labels : {}) &&
      (has(object.metadata.annotations) ? object.metadata.annotations : {}) ==
        (has(oldObject.metadata.annotations) ? oldObject.metadata.annotations : {}) &&
      (has(object.metadata.ownerReferences) ? object.metadata.ownerReferences : []) ==
        (has(oldObject.metadata.ownerReferences) ? oldObject.metadata.ownerReferences : []) &&
      (has(object.metadata.deletionTimestamp) ? object.metadata.deletionTimestamp : null) ==
        (has(oldObject.metadata.deletionTimestamp) ? oldObject.metadata.deletionTimestamp : null) &&
      (has(object.metadata.deletionGracePeriodSeconds) ? object.metadata.deletionGracePeriodSeconds : null) ==
        (has(oldObject.metadata.deletionGracePeriodSeconds) ? oldObject.metadata.deletionGracePeriodSeconds : null) &&
      (has(object.metadata.generateName) ? object.metadata.generateName : "") ==
        (has(oldObject.metadata.generateName) ? oldObject.metadata.generateName : "") &&
      (has(object.metadata.creationTimestamp) ? object.metadata.creationTimestamp : null) ==
        (has(oldObject.metadata.creationTimestamp) ? oldObject.metadata.creationTimestamp : null) &&
      (has(object.metadata.generation) ? object.metadata.generation : 0) ==
        (has(oldObject.metadata.generation) ? oldObject.metadata.generation : 0) &&
      (has(object.metadata.resourceVersion) ? object.metadata.resourceVersion : "") ==
        (has(oldObject.metadata.resourceVersion) ? oldObject.metadata.resourceVersion : "") &&
      (has(object.metadata.selfLink) ? object.metadata.selfLink : "") ==
        (has(oldObject.metadata.selfLink) ? oldObject.metadata.selfLink : "") &&
      size(has(object.metadata.finalizers) ? object.metadata.finalizers : []) <
        size(has(oldObject.metadata.finalizers) ? oldObject.metadata.finalizers : []) &&
      (has(object.metadata.finalizers) ? object.metadata.finalizers : []).all(
        finalizer, finalizer in (has(oldObject.metadata.finalizers) ? oldObject.metadata.finalizers : [])
      ) &&
      (has(oldObject.metadata.finalizers) ? oldObject.metadata.finalizers : []).exists(
        finalizer, !(finalizer in (has(object.metadata.finalizers) ? object.metadata.finalizers : []))
      ) &&
      (has(oldObject.metadata.finalizers) ? oldObject.metadata.finalizers : []).all(
        finalizer,
        finalizer in (has(object.metadata.finalizers) ? object.metadata.finalizers : []) ||
        finalizer in [
          "cetirun.microsoft.com/finalizer",
          "cetirun.microsoft.com/sandbox-hold",
          "cetirun.microsoft.com/custom-runtime-ownership",
          "cetirun.microsoft.com/custom-apply-completion"
        ]
      )
    )
{{- end -}}
