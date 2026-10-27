// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type customProvider struct {
	bindings  runtimebindings.Custom
	applyErr  error
	applied   bool
	approve   func(*appsv1.StatefulSet) error
	leaseErr  error
	completed []controller.CustomApplyOutcome
}

type provisioningClient struct{ client.Client }

func (c provisioningClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetUID() == "" {
		obj.SetUID(types.UID("synthetic-" + obj.GetName()))
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (p *customProvider) Resolve(context.Context, *api.Agent, *api.Team) (runtimebindings.Custom, error) {
	return p.bindings, nil
}

func (p *customProvider) ApproveApply(_ context.Context, _ *api.Agent, current *appsv1.StatefulSet, _ controller.CustomApplyOperation) (controller.CustomApplyLease, error) {
	p.applied = true
	if p.approve != nil {
		if err := p.approve(current); err != nil {
			return nil, err
		}
	}
	if p.applyErr != nil {
		return nil, p.applyErr
	}
	return p, nil
}

func (p *customProvider) ResumeApply(context.Context, *api.Agent, controller.CustomApplyOperation) (controller.CustomApplyLease, error) {
	return p, nil
}

func (p *customProvider) Check(context.Context) error { return p.leaseErr }

func (p *customProvider) Complete(_ context.Context, outcome controller.CustomApplyOutcome) error {
	p.completed = append(p.completed, outcome)
	return nil
}

func customFixture(name string, port *int32) (*api.Agent, runtimebindings.Custom, []client.Object) {
	cr := newCR(name, api.PhasePending, nil, 1, 0, "")
	cr.UID = types.UID(name + "-agent-uid")
	cr.Spec.User.Alias = "same-user"
	image := "registry.example.com/agent@sha256:" + strings.Repeat("a", 64)
	cr.Spec.Agent = api.HarnessSpec{Type: api.AgentTypeCustom, Image: ptr.To(image), Username: ptr.To("worker"), Port: port}
	cr.Spec.GatewayTokenSecretRef = ""
	cr.Spec.Helm.Values = map[string]runtime.RawExtension{"ttyd": {Raw: []byte(`{"enabled":false}`)}}
	ref := func(name string) runtimebindings.ObjectReference {
		return runtimebindings.ObjectReference{Name: name, UID: name + "-uid", Revision: "1"}
	}
	b := runtimebindings.Custom{
		AgentName: name, AgentUID: string(cr.UID), Generation: 1, Namespace: ref("cetirun-" + name),
		ApprovedType: "custom", ApprovedImage: image, PolicyRevision: "policy-1",
		Model: runtimebindings.ModelAccess{ObjectReference: ref(name + "-model"), Key: "api-key", BaseURL: "http://gateway.models.svc/v1"},
		Home: runtimebindings.Home{Name: "data-openclaw-" + name + "-cetirun-0", FSGroup: 2000,
			Claim: &runtimebindings.HomeClaim{StorageClass: "default", StorageSize: "100Gi", AccessMode: "ReadWriteOnce"}},
		Context: runtimebindings.Context{ObjectReference: ref(name + "-context"), Files: map[string]string{
			"canonical": "AGENTS.md", "memory": "skills/cetirun-memory/SKILL.md",
		}},
		Network: runtimebindings.Network{Revision: "network-1", GatewayPort: 8080,
			Gateway: runtimebindings.Peer{Namespace: "models", PodLabels: map[string]string{"app": "gateway"}},
			DNS:     runtimebindings.Peer{Namespace: "kube-system", PodLabels: map[string]string{"k8s-app": "kube-dns"}},
		},
	}
	meta := func(ref runtimebindings.ObjectReference) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: ref.Name, Namespace: b.Namespace.Name, UID: types.UID(ref.UID), ResourceVersion: ref.Revision}
	}
	nsMeta := meta(b.Namespace)
	nsMeta.Namespace = ""
	secret := &corev1.Secret{ObjectMeta: meta(b.Model.ObjectReference), Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"api-key": []byte("sk-dummy")}}
	secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: cr.Name, UID: cr.UID}}
	home := &corev1.PersistentVolumeClaim{ObjectMeta: meta(ref(b.Home.Name)), Spec: b.Home.Claim.PVCSpec(),
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	home.Annotations = map[string]string{"cetirun.microsoft.com/agent-uid": string(cr.UID)}
	home.Spec.VolumeName = name + "-volume"
	return cr, b, []client.Object{
		cr, &corev1.Namespace{ObjectMeta: nsMeta}, secret,
		home,
		&corev1.ConfigMap{ObjectMeta: meta(b.Context.ObjectReference), Immutable: ptr.To(true),
			Data: map[string]string{"canonical": "synthetic canonical contract", "memory": "synthetic memory skill"}},
	}
}
