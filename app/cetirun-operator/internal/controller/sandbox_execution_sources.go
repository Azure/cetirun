// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *AgentReconciler) executionSourceDigest(ctx context.Context, source w.SourceReference,
	box string, class api.AgentPolicyClass, capture bool,
) (string, error) {
	var object client.Object
	if source.APIVersion == api.GroupVersion.String() && source.Kind == "Team" {
		object = &api.Team{}
	} else {
		version, err := schema.ParseGroupVersion(source.APIVersion)
		if err != nil {
			return "", err
		}
		resource := &unstructured.Unstructured{}
		resource.SetGroupVersionKind(version.WithKind(source.Kind))
		object = resource
	}
	key := client.ObjectKey{Namespace: source.Namespace, Name: source.Name}
	if err := r.APIReader.Get(ctx, key, object); err != nil {
		return "", err
	}
	if client.ObjectKeyFromObject(object) != key || object.GetUID() != source.UID ||
		object.GetResourceVersion() == "" || !object.GetDeletionTimestamp().IsZero() ||
		(capture && object.GetResourceVersion() != source.ResourceVersion) {
		return "", fmt.Errorf("%w: effective source identity changed: %s", errSandboxSourceChanged, w.SourceKey(source))
	}
	if team, ok := object.(*api.Team); ok {
		inputs, err := w.ExecutionTeamInputs(team)
		if err != nil {
			return "", err
		}
		return inputs.DigestFor(box, class)
	}
	return w.ExecutionObjectDigest(object)
}

func (r *AgentReconciler) captureExecutionSourceDigests(ctx context.Context, intent sandboxcoordination.Intent) (map[string]string, error) {
	digests := map[string]string{}
	box := intent.SelectedSandbox()
	for _, source := range intent.Sources {
		if !w.EffectiveSource(source) {
			continue
		}
		digest, err := r.executionSourceDigest(ctx, source, box, intent.Expected.Binding.PolicyClass, true)
		if err != nil {
			return nil, err
		}
		digests[w.SourceKey(source)] = digest
	}
	return digests, ctx.Err()
}

func (r *AgentReconciler) verifyEffectiveExecutionSources(ctx context.Context, intent sandboxcoordination.Intent) error {
	if len(intent.Sources) == 0 || len(intent.Sources) > sandboxcoordination.MaxSources {
		return fmt.Errorf("execution source inventory exceeds its bound")
	}
	box := intent.SelectedSandbox()
	for _, source := range intent.Sources {
		if source.APIVersion == "apps/v1" && source.Kind == "StatefulSet" &&
			source.Namespace == intent.Namespace.Name && source.Name == intent.Workload.Name {
			continue
		}
		if w.EffectiveSource(source) {
			current, err := r.executionSourceDigest(ctx, source, box, intent.Expected.Binding.PolicyClass, false)
			if err != nil {
				return err
			}
			if current != intent.SourceDigests[w.SourceKey(source)] {
				return fmt.Errorf("%w: effective source inputs changed: %s", errSandboxSourceChanged, w.SourceKey(source))
			}
		} else if _, err := r.readSandboxSource(ctx, source); err != nil {
			return err
		}
	}
	return ctx.Err()
}
