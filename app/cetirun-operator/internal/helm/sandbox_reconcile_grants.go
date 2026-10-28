// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"helm.sh/helm/v3/pkg/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/rest"
)

type reconciledGrantKube struct {
	kube.Interface
	prepared *PreparedRelease
}

func (k *reconciledGrantKube) Create(resources kube.ResourceList) (*kube.Result, error) {
	result := &kube.Result{}
	for _, info := range resources {
		if info == nil || info.Object == nil {
			return result, fmt.Errorf("Sandbox grant creation requires a valid resource")
		}
		object, err := sandboxUnstructured(info.Object)
		if err != nil {
			return result, err
		}
		var created *kube.Result
		if w.KubernetesBindingResource(*object) {
			created, err = k.createBinding(info, *object)
		} else {
			created, err = k.Interface.Create(kube.ResourceList{info})
		}
		if created != nil {
			result.Created = append(result.Created, created.Created...)
		}
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

type reconciledGrantRESTClient struct {
	resource.RESTClient
}

func (c reconciledGrantRESTClient) Post() *rest.Request {
	// A retry must not hide an uncertain write behind a later rejection.
	return c.RESTClient.Post().MaxRetries(0)
}

func (k *reconciledGrantKube) createBinding(info *resource.Info, object unstructured.Unstructured) (*kube.Result, error) {
	if info.Client == nil || info.Mapping == nil {
		return nil, fmt.Errorf("Kubernetes grant creation requires its API resource mapping")
	}
	complete, err := k.prepared.AuthorizeSandboxBindingCreate(context.Background(), object)
	if err != nil {
		return nil, err
	}
	original := info.Client
	info.Client = reconciledGrantRESTClient{original}
	defer func() { info.Client = original }()
	result, err := k.Interface.Create(kube.ResourceList{info})
	return result, complete(err)
}

func sandboxBindingCreateRejected(err error) bool {
	var status apierrors.APIStatus
	if !errors.As(err, &status) || status.Status().Code < 400 || status.Status().Code >= 500 {
		return false
	}
	switch apierrors.ReasonForError(err) {
	case metav1.StatusReasonBadRequest, metav1.StatusReasonUnauthorized, metav1.StatusReasonForbidden,
		metav1.StatusReasonNotFound, metav1.StatusReasonAlreadyExists, metav1.StatusReasonConflict,
		metav1.StatusReasonInvalid, metav1.StatusReasonMethodNotAllowed, metav1.StatusReasonRequestEntityTooLarge,
		metav1.StatusReasonUnsupportedMediaType, metav1.StatusReasonTooManyRequests:
		return true
	default:
		return false
	}
}

func (k *reconciledGrantKube) Update(original, target kube.ResourceList, force bool) (*kube.Result, error) {
	if force {
		return &kube.Result{}, fmt.Errorf("Sandbox grant reconciliation cannot force replacement")
	}
	withoutGrants := func(resources kube.ResourceList) (kube.ResourceList, error) {
		var result kube.ResourceList
		for _, info := range resources {
			if info == nil || info.Object == nil {
				return nil, fmt.Errorf("Sandbox grant reconciliation requires a valid resource")
			}
			object, err := sandboxUnstructured(info.Object)
			if err != nil {
				return nil, err
			}
			if !w.KubernetesBindingResource(*object) {
				result = append(result, info)
			}
		}
		return result, nil
	}
	previous, err := withoutGrants(original)
	if err != nil {
		return &kube.Result{}, err
	}
	next, err := withoutGrants(target)
	if err != nil {
		return &kube.Result{}, err
	}
	// Grant retirement belongs to the UID-bound journal, not Helm's
	// name-only pruning against an older deployed release.
	result, err := k.Interface.Update(previous, next, false)
	if err != nil {
		return result, err
	}
	if result == nil {
		result = &kube.Result{}
	}
	for _, info := range target {
		desired, err := sandboxUnstructured(info.Object)
		if err != nil {
			return result, err
		}
		if !w.KubernetesBindingResource(*desired) {
			continue
		}
		if info.Client == nil || info.Mapping == nil {
			return result, fmt.Errorf("Kubernetes grant update requires its API resource mapping")
		}
		if err := k.prepared.CheckSandboxApply(context.Background()); err != nil {
			return result, err
		}
		currentObject, err := resource.NewHelper(info.Client, info.Mapping).Get(info.Namespace, info.Name)
		if apierrors.IsNotFound(err) {
			created, err := k.Create(kube.ResourceList{info})
			result.Created = append(result.Created, created.Created...)
			if err != nil {
				return result, err
			}
			continue
		}
		if err != nil {
			return result, err
		}
		current, err := sandboxUnstructured(currentObject)
		identity := desired.DeepCopy()
		// Release metadata may advance without changing the grant's identity.
		identity.SetLabels(map[string]string{sandboxpolicy.AgentUIDLabel: desired.GetLabels()[sandboxpolicy.AgentUIDLabel]})
		identity.SetAnnotations(map[string]string{sandboxpolicy.GrantRevisionAnnotation: desired.GetAnnotations()[sandboxpolicy.GrantRevisionAnnotation]})
		if err != nil || current.GetResourceVersion() == "" || !w.ReconciledObjectMatches(current, identity) {
			return result, fmt.Errorf("Kubernetes grant update requires its current owned binding")
		}
		patch, patchType, err := sandboxStagePatch(original.Get(info), info, current)
		if err != nil {
			return result, err
		}
		updated, err := info.Client.Patch(patchType).
			NamespaceIfScoped(info.Namespace, info.Mapping.Scope.Name() == meta.RESTScopeNameNamespace).
			Resource(info.Mapping.Resource.Resource).Name(info.Name).Body(patch).Do(context.Background()).Get()
		if err != nil {
			return result, fmt.Errorf("update current Kubernetes grant: %w", err)
		}
		if err := info.Refresh(updated, true); err != nil {
			return result, err
		}
		result.Updated = append(result.Updated, info)
	}
	return result, nil
}
