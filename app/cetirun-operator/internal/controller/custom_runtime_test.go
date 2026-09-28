// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type customProvider struct {
	bindings  runtimebindings.Custom
	applyErr  error
	applied   bool
	approve   func(*appsv1.StatefulSet) error
	leaseErr  error
	completed []controller.CustomApplyOutcome
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
	ref := func(name string) runtimebindings.ObjectReference {
		return runtimebindings.ObjectReference{Name: name, UID: name + "-uid", Revision: "1"}
	}
	b := runtimebindings.Custom{
		AgentName: name, AgentUID: string(cr.UID), Generation: 1, Namespace: ref("cetirun-" + name),
		ApprovedType: "custom", ApprovedImage: image, PolicyRevision: "policy-1",
		Model: runtimebindings.ModelAccess{ObjectReference: ref(name + "-model"), Key: "api-key", BaseURL: "http://gateway.models.svc/v1"},
		Home:  runtimebindings.Home{ObjectReference: ref(name + "-home"), FSGroup: 2000},
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
	return cr, b, []client.Object{
		cr, &corev1.Namespace{ObjectMeta: nsMeta}, secret,
		&corev1.PersistentVolumeClaim{ObjectMeta: meta(b.Home.ObjectReference),
			Spec:   corev1.PersistentVolumeClaimSpec{VolumeName: name + "-volume"},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
		&corev1.ConfigMap{ObjectMeta: meta(b.Context.ObjectReference), Immutable: ptr.To(true),
			Data: map[string]string{"canonical": "synthetic canonical contract", "memory": "synthetic memory skill"}},
	}
}

func TestCustomReconcileUsesBindingsNotNativeSecrets(t *testing.T) {
	for _, port := range []*int32{nil, ptr.To[int32](8080)} {
		cr, b, objects := customFixture("example", port)
		scheme := testScheme()
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
		hc := &fakeHelmClient{useHarness: true}
		provider := &customProvider{bindings: b}
		r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: hc, CustomBindings: provider, EventRecorder: record.NewFakeRecorder(100)}
		key := types.NamespacedName{Name: cr.Name}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if !hc.installCalled || !provider.applied {
			t.Fatal("valid Custom plan did not reach approved application")
		}
		current := &api.Agent{}
		if err := c.Get(context.Background(), key, current); err != nil {
			t.Fatal(err)
		}
		if current.Status.AccessURL != "" || current.Spec.Agent.Port == nil != (port == nil) {
			t.Fatal("Custom received a native URL or default port")
		}
		secrets := &corev1.SecretList{}
		if err := c.List(context.Background(), secrets); err != nil || len(secrets.Items) != 1 {
			t.Fatal("Custom generated a native Secret")
		}
		raw := fmt.Sprint(hc.lastValues["runtime"])
		if strings.Contains(raw, "sk-dummy") || strings.Contains(raw, "gateway-token") {
			t.Fatal("secret content/native reference leaked into release")
		}
		// Publish only a synthetic workload observation, not a running Pod.
		sts := statefulSet(cr.Name, 1)
		sts.Status.CurrentRevision = "revision-1"
		sts.Status.UpdateRevision = "revision-1"
		sts.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: cr.Name, UID: cr.UID}}
		sts.Annotations = map[string]string{
			"cetirun.microsoft.com/agent-uid": string(cr.UID), "cetirun.microsoft.com/home-uid": b.Home.UID,
			"cetirun.microsoft.com/custom-plan-hash": hc.lastValues["runtime"].(map[string]any)["workloadHash"].(string),
		}
		if err := c.Create(context.Background(), sts); err != nil {
			t.Fatal(err)
		}
		hc.installCalled = false
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if hc.installCalled {
			t.Fatal("unchanged Custom plan was reapplied")
		}
		if err := c.Get(context.Background(), key, current); err != nil || current.Status.Phase != api.PhaseReady ||
			current.Status.AccessURL != "" {
			t.Fatal("Custom readiness observation failed")
		}
		steadyVersion := current.ResourceVersion
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(context.Background(), key, current); err != nil || current.ResourceVersion != steadyVersion {
			t.Fatal("steady-state status caused another watch event")
		}
		// A model rotation must not be lost to the old native-secret cache.
		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: b.Namespace.Name, Name: b.Model.Name}, secret); err != nil {
			t.Fatal(err)
		}
		secret.Data["api-key"] = []byte("sk-dummy-rotated")
		if err := c.Update(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
		provider.bindings.Model.Revision = secret.ResourceVersion
		provider.applyErr = fmt.Errorf("no handoff")
		provider.applied = false
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if !provider.applied || hc.installCalled {
			t.Fatal("rotation bypassed or did not reach the controlled replacement gate")
		}
		provider.applyErr = nil
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if hc.installCalled {
			t.Fatal("a replicas=1 workload can recreate its old writer during replacement")
		}
		provider.approve = func(current *appsv1.StatefulSet) error {
			current.Spec.Replicas = ptr.To[int32](0)
			return c.Update(context.Background(), current)
		}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		if !hc.installCalled {
			t.Fatal("approved stopped workload did not receive the rotated binding")
		}
	}
}

func TestCustomReconcileRejectsUnreadyOrForeignBindings(t *testing.T) {
	cases := []string{"no provider", "wrong UID", "wrong generation", "wrong namespace", "wrong secret owner", "wrong secret revision",
		"missing home", "mutable context", "missing canonical", "old writer", "foreign workload", "registry"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			cr, b, objects := customFixture("example", nil)
			provider := &customProvider{bindings: b}
			switch name {
			case "wrong UID":
				provider.bindings.AgentUID = "another-agent"
			case "wrong generation":
				provider.bindings.Generation++
			case "wrong namespace":
				provider.bindings.Namespace.Name = "cetirun-other"
			case "wrong secret owner":
				objects[2].(*corev1.Secret).OwnerReferences[0].UID = "different-uid"
			case "wrong secret revision":
				provider.bindings.Model.Revision = "2"
			case "missing home":
				provider.bindings.Home.Name = "absent-home"
			case "mutable context":
				objects[4].(*corev1.ConfigMap).Immutable = ptr.To(false)
			case "missing canonical":
				delete(objects[4].(*corev1.ConfigMap).Data, "canonical")
			case "old writer":
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old-writer", Namespace: b.Namespace.Name},
					Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: b.Home.Name}}}}}})
			case "foreign workload":
				objects = append(objects, statefulSet(cr.Name, 1))
			case "registry":
				cr.Spec.Agent.RegistrySecretRef = &api.RegistrySecretRef{Name: "registry-source"}
			}
			scheme := testScheme()
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
			hc := &fakeHelmClient{useHarness: true}
			r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: hc, CustomBindings: provider, EventRecorder: record.NewFakeRecorder(100)}
			if name == "no provider" {
				r.CustomBindings = nil
			}
			_, _ = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name}})
			if hc.installCalled {
				t.Fatal("unready or unowned bindings reached Helm")
			}
			home := &corev1.PersistentVolumeClaim{}
			if err := c.Get(context.Background(), types.NamespacedName{Namespace: b.Namespace.Name, Name: b.Home.Name}, home); err != nil ||
				string(home.UID) != b.Home.UID || len(home.OwnerReferences) != 0 {
				t.Fatal("rejected plan changed or claimed HOME")
			}
		})
	}
}

func TestCustomSameUserInstancesDoNotShareBindings(t *testing.T) {
	one, b, objects := customFixture("one", nil)
	two, _, second := customFixture("two", nil)
	objects = append(objects, second...)
	provider := &customProvider{bindings: b}
	scheme := testScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
	hc := &fakeHelmClient{useHarness: true}
	r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: hc, CustomBindings: provider, EventRecorder: record.NewFakeRecorder(100)}
	if one.Spec.User.Alias != two.Spec.User.Alias {
		t.Fatal("fixture should use the same user")
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: two.Name}})
	if err != nil || hc.installCalled {
		t.Fatal("another instance's binding was accepted")
	}
}

func TestCustomSuspensionWorksWithoutBindingsAndWithInvalidConfig(t *testing.T) {
	cr, b, objects := customFixture("example", nil)
	cr.Spec.Suspended = true
	cr.Spec.Agent.RegistrySecretRef = &api.RegistrySecretRef{Name: "unsupported"}
	sts := statefulSet(cr.Name, 1)
	sts.Annotations = map[string]string{"cetirun.microsoft.com/agent-uid": string(cr.UID)}
	sts.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: cr.Name, UID: cr.UID}}
	objects = append(objects, sts)
	scheme := testScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
	hc := &fakeHelmClient{useHarness: true}
	r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: hc, EventRecorder: record.NewFakeRecorder(100)}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(sts), sts); err != nil || *sts.Spec.Replicas != 0 || hc.installCalled {
		t.Fatal("suspension was gated by Custom runtime configuration")
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: b.Namespace.Name, Name: b.Home.Name}, home); err != nil {
		t.Fatal("suspension removed HOME")
	}
}

func TestCustomRendererDriftDoesNotReplaceWorkload(t *testing.T) {
	cr, b, objects := customFixture("example", nil)
	scheme := testScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
	hc := &fakeHelmClient{useHarness: true}
	provider := &customProvider{bindings: b}
	r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: hc, CustomBindings: provider, EventRecorder: record.NewFakeRecorder(100)}
	key := types.NamespacedName{Name: cr.Name}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, cr); err != nil {
		t.Fatal(err)
	}
	plan := strings.SplitN(cr.Status.ObservedOperatorHash, ":", 2)[0]
	cr.Status.ObservedOperatorHash = plan + ":previous-renderer"
	if err := c.Status().Update(context.Background(), cr); err != nil {
		t.Fatal(err)
	}
	sts := statefulSet(cr.Name, 1)
	sts.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: cr.Name, UID: cr.UID}}
	sts.Annotations = map[string]string{"cetirun.microsoft.com/agent-uid": string(cr.UID),
		"cetirun.microsoft.com/home-uid": b.Home.UID, "cetirun.microsoft.com/custom-plan-hash": plan}
	if err := c.Create(context.Background(), sts); err != nil {
		t.Fatal(err)
	}
	hc.installCalled, provider.applied = false, false
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if hc.installCalled || provider.applied {
		t.Fatal("renderer drift triggered replacement")
	}
}
