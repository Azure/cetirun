// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func (r *AgentReconciler) toolsMCPCA(ctx context.Context) ([]byte, error) {
	if r.ToolsMCPURL == "" {
		return nil, nil
	}
	if r.OperatorNamespace == "" || r.ToolsMCPCASecretName == "" {
		return nil, fmt.Errorf("Tools MCP CA source is not configured")
	}
	var source corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.OperatorNamespace, Name: r.ToolsMCPCASecretName}, &source); err != nil {
		return nil, fmt.Errorf("Tools MCP CA source is unavailable")
	}
	ca := source.Data["ca.crt"]
	if len(ca) == 0 || len(ca) > 64<<10 || !x509.NewCertPool().AppendCertsFromPEM(ca) || bytes.Contains(ca, []byte("PRIVATE KEY")) {
		return nil, fmt.Errorf("Tools MCP CA source is invalid")
	}
	return ca, nil
}

func (r *AgentReconciler) toolsMCPCAHash(ctx context.Context) (string, error) {
	ca, err := r.toolsMCPCA(ctx)
	if err != nil || ca == nil {
		return "", err
	}
	return secretContentHash(map[string][]byte{"ca.crt": ca}), nil
}

func (r *AgentReconciler) ensureToolsMCPCA(ctx context.Context, instance *api.Agent) error {
	ca, err := r.toolsMCPCA(ctx)
	if err != nil || ca == nil {
		return err
	}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: r.ToolsMCPCASecretName}
	var target corev1.Secret
	if err := r.Get(ctx, key, &target); errors.IsNotFound(err) {
		target = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, OwnerReferences: []metav1.OwnerReference{registryauth.Owner(instance)}}, Data: map[string][]byte{"ca.crt": ca}}
		return r.Create(ctx, &target)
	} else if err != nil {
		return err
	}
	owned := false
	for _, owner := range target.OwnerReferences {
		if owner.UID == instance.UID && owner.Kind == "Agent" {
			owned = true
		}
	}
	if !owned {
		return fmt.Errorf("Tools MCP CA target ownership is invalid")
	}
	if len(target.Data) == 1 && bytes.Equal(target.Data["ca.crt"], ca) {
		return nil
	}
	target.Data = map[string][]byte{"ca.crt": ca}
	return r.Update(ctx, &target)
}
