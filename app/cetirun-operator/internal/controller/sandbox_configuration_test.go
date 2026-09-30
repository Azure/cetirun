// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

func sandboxConfigurationFixture() (*api.Sandbox, controller.SandboxConfigurationSnapshot, []client.Object) {
	resources := api.SandboxContainerResources{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
		},
	}
	object := &api.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "example-sandbox", UID: "example-sandbox-uid", Generation: 4},
		Spec: api.SandboxSpec{
			Type: api.SandboxKata, Resources: api.SandboxResources{Agent: resources, Helpers: resources},
			Storage: api.SandboxStorage{
				Home:    api.SandboxHomeStorage{StorageClassName: "example-home", Size: resource.MustParse("10Gi"), AccessMode: corev1.ReadWriteOncePod},
				Scratch: api.SandboxScratchStorage{SizeLimit: resource.MustParse("1Gi")},
			},
		},
	}
	capabilities := sandbox.Capabilities{
		RuntimeClassName: "example-kata", Revision: "synthetic-qualified",
		HostResourceLimits: true, ReadOnlyRootFilesystem: true,
		HomeAccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
		IPFamilies:           []corev1.IPFamily{corev1.IPv4Protocol},
		Protocols:            []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
		GuestExternalNetwork: true, DenyPrecedence: true, NodeTraffic: true, DNSIPRestrictions: true,
		BlockedPorts: true, AuthenticatedIngress: true, DisabledIngress: true,
		EnforcementObservation: true, RestrictiveFailure: true, KubernetesAccess: true, EntraID: true,
	}
	platform := sandboxpolicy.Platform{
		Adapter: sandboxpolicy.AdapterRegistration{Name: sandboxpolicy.AzureCNIOverlayCilium, Qualified: true, Available: true, Revision: "synthetic-only"},
		Topology: sandboxpolicy.Topology{
			Revision: "synthetic-topology", ManagedPeersComplete: true,
			PodCIDRs: []string{"10.244.0.0/16"}, ServiceCIDRs: []string{"10.96.0.0/12"},
			NodeCIDRs: []string{"192.168.0.0/16"}, ManagementCIDRs: []string{"10.1.0.0/16"},
			MetadataCIDRs: []string{"169.254.169.254/32"},
			DNS:           []sandboxpolicy.Peer{{Name: "dns", Namespace: "kube-system", UID: "example-dns", IPs: []string{"10.96.0.10"}}},
			Gateway: []sandboxpolicy.Destination{{
				Peer:  sandboxpolicy.Peer{Name: "gateway", Namespace: "platform", UID: "example-gateway", IPs: []string{"10.96.1.50"}},
				Ports: []api.AgentPolicyPort{{Port: 8443, Protocol: corev1.ProtocolTCP}},
			}},
			Ingress: []sandboxpolicy.Destination{{
				Peer:  sandboxpolicy.Peer{Name: "ingress", Namespace: "platform", UID: "example-ingress", IPs: []string{"10.96.1.60"}},
				Ports: []api.AgentPolicyPort{{Port: 8080, Protocol: corev1.ProtocolTCP}},
			}},
		},
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	snapshot := controller.SandboxConfigurationSnapshot{
		ObservedAt:    now,
		Registrations: map[api.SandboxType]sandbox.Registration{api.SandboxKata: {Available: true, Capabilities: capabilities}},
		Platforms:     map[api.SandboxType]sandboxpolicy.Platform{api.SandboxKata: platform},
		RuntimeClasses: map[string]controller.SandboxRuntimePrerequisite{
			"example-kata": {UID: "example-runtime-uid", Handler: "kata"},
		},
		StorageClasses: map[string]controller.SandboxStoragePrerequisite{
			"example-home": {UID: "example-storage-uid", Provisioner: "csi.example.com"},
		},
	}
	objects := []client.Object{
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "example-kata", UID: "example-runtime-uid"}, Handler: "kata"},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "example-home", UID: "example-storage-uid"}, Provisioner: "csi.example.com"},
	}
	return object, snapshot, objects
}

func configurationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, nodev1.AddToScheme, storagev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func TestSandboxConfigurationPrerequisiteConditions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*api.Sandbox, *controller.SandboxConfigurationSnapshot, *[]client.Object)
		status metav1.ConditionStatus
		reason string
	}{
		{"ready prerequisites", func(*api.Sandbox, *controller.SandboxConfigurationSnapshot, *[]client.Object) {}, metav1.ConditionTrue, "PrerequisitesReady"},
		{"invalid configuration", func(o *api.Sandbox, _ *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			o.Spec.Resources.Agent.Limits = nil
		}, metav1.ConditionFalse, "InvalidConfiguration"},
		{"unregistered runtime", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			s.Registrations = nil
		}, metav1.ConditionFalse, "UnsupportedControl"},
		{"runtime unavailable", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			r := s.Registrations[api.SandboxKata]
			r.Available = false
			s.Registrations[api.SandboxKata] = r
		}, metav1.ConditionFalse, "RuntimeUnavailable"},
		{"missing runtime reference", func(_ *api.Sandbox, _ *controller.SandboxConfigurationSnapshot, o *[]client.Object) { *o = (*o)[1:] }, metav1.ConditionFalse, "ReferenceMissing"},
		{"replaced runtime reference", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			s.RuntimeClasses["example-kata"] = controller.SandboxRuntimePrerequisite{UID: "old-runtime", Handler: "kata"}
		}, metav1.ConditionFalse, "ReferenceChanged"},
		{"changed runtime handler", func(_ *api.Sandbox, _ *controller.SandboxConfigurationSnapshot, o *[]client.Object) {
			(*o)[0].(*nodev1.RuntimeClass).Handler = "other-handler"
		}, metav1.ConditionFalse, "ReferenceChanged"},
		{"missing storage reference", func(_ *api.Sandbox, _ *controller.SandboxConfigurationSnapshot, o *[]client.Object) { *o = (*o)[:1] }, metav1.ConditionFalse, "ReferenceMissing"},
		{"storage not approved", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			s.StorageClasses = nil
		}, metav1.ConditionFalse, "UnapprovedStorage"},
		{"replaced storage reference", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			s.StorageClasses["example-home"] = controller.SandboxStoragePrerequisite{UID: "old-storage", Provisioner: "csi.example.com"}
		}, metav1.ConditionFalse, "ReferenceChanged"},
		{"missing platform", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			s.Platforms = nil
		}, metav1.ConditionUnknown, "PrerequisitesUnavailable"},
		{"unavailable adapter", func(_ *api.Sandbox, s *controller.SandboxConfigurationSnapshot, _ *[]client.Object) {
			p := s.Platforms[api.SandboxKata]
			p.Adapter.Available = false
			s.Platforms[api.SandboxKata] = p
		}, metav1.ConditionUnknown, "PrerequisitesUnavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object, snapshot, prerequisites := sandboxConfigurationFixture()
			tc.mutate(object, &snapshot, &prerequisites)
			objects := append([]client.Object{object}, prerequisites...)
			c := fake.NewClientBuilder().WithScheme(configurationScheme(t)).WithStatusSubresource(object).WithObjects(objects...).Build()
			r := &controller.SandboxReconciler{
				Client: c, Now: func() time.Time { return snapshot.ObservedAt },
				Source: controller.SandboxConfigurationSourceFunc(func(context.Context) (controller.SandboxConfigurationSnapshot, error) { return snapshot, nil }),
			}
			result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: object.Name}})
			if err != nil || result.RequeueAfter <= 0 {
				t.Fatalf("configuration reconcile: %+v, %v", result, err)
			}
			got := &api.Sandbox{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(object), got); err != nil {
				t.Fatal(err)
			}
			condition := apimeta.FindStatusCondition(got.Status.Conditions, api.ConditionReady)
			if condition == nil || condition.Status != tc.status || condition.Reason != tc.reason ||
				condition.ObservedGeneration != object.Generation || got.Status.ObservedGeneration != object.Generation {
				t.Fatalf("unexpected prerequisite status: %+v", got.Status)
			}
		})
	}
}

func TestSandboxConfigurationStableTransitionsAndSourceFreshness(t *testing.T) {
	object, snapshot, prerequisites := sandboxConfigurationFixture()
	c := fake.NewClientBuilder().WithScheme(configurationScheme(t)).WithStatusSubresource(object).
		WithObjects(append([]client.Object{object}, prerequisites...)...).Build()
	now := snapshot.ObservedAt
	r := &controller.SandboxReconciler{
		Client: c, Now: func() time.Time { return now },
		Source: controller.SandboxConfigurationSourceFunc(func(context.Context) (controller.SandboxConfigurationSnapshot, error) { return snapshot, nil }),
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: object.Name}}
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first := &api.Sandbox{}
	if err := c.Get(context.Background(), request.NamespacedName, first); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := r.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	second := &api.Sandbox{}
	if err := c.Get(context.Background(), request.NamespacedName, second); err != nil {
		t.Fatal(err)
	}
	if first.ResourceVersion != second.ResourceVersion ||
		!first.Status.Conditions[0].LastTransitionTime.Equal(&second.Status.Conditions[0].LastTransitionTime) {
		t.Fatal("unchanged prerequisite status fabricated a transition or write")
	}
	now = now.Add(time.Minute)
	if _, err := r.Reconcile(context.Background(), request); !errors.Is(err, sandboxpolicy.ErrUnavailable) {
		t.Fatal("stale platform snapshot must not remain Ready", err)
	}
	if err := c.Get(context.Background(), request.NamespacedName, second); err != nil {
		t.Fatal(err)
	}
	if second.Status.Conditions[0].Status != metav1.ConditionUnknown ||
		!second.Status.Conditions[0].LastTransitionTime.Time.Equal(now) {
		t.Fatal("lost prerequisite freshness did not produce a truthful transition")
	}
	r.Source = nil
	if _, err := r.Reconcile(context.Background(), request); !errors.Is(err, sandboxpolicy.ErrUnavailable) {
		t.Fatal("an absent production source must not imply readiness", err)
	}
}

func TestAgentPolicyAcceptedIsConfigurationOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*api.AgentPolicy, *controller.SandboxConfigurationSnapshot)
		status metav1.ConditionStatus
		reason string
	}{
		{"accepted", func(*api.AgentPolicy, *controller.SandboxConfigurationSnapshot) {}, metav1.ConditionTrue, "PolicyAccepted"},
		{"unsupported FQDN", func(p *api.AgentPolicy, _ *controller.SandboxConfigurationSnapshot) {
			p.Spec.Network.BlockedFQDNs = []string{"blocked.example.com"}
		}, metav1.ConditionFalse, "UnsupportedControl"},
		{"invalid port", func(p *api.AgentPolicy, _ *controller.SandboxConfigurationSnapshot) {
			p.Spec.Network.BlockedPorts = []api.AgentPolicyPort{{Port: 70000}}
		}, metav1.ConditionFalse, "InvalidConfiguration"},
		{"required DNS conflict", func(p *api.AgentPolicy, _ *controller.SandboxConfigurationSnapshot) {
			p.Spec.Network.BlockedPorts = []api.AgentPolicyPort{{Port: 53}}
		}, metav1.ConditionFalse, "PolicyConflict"},
		{"unapproved grant", func(p *api.AgentPolicy, _ *controller.SandboxConfigurationSnapshot) {
			p.Spec.KubernetesAccess = &api.AgentPolicyAdminKubernetesAccess{ServiceAccountName: "example-admin", ClusterRoleBindings: []api.AgentPolicyClusterRoleBinding{{ClusterRoleName: "admin"}}}
		}, metav1.ConditionFalse, "UnapprovedGrant"},
		{"no registration", func(_ *api.AgentPolicy, s *controller.SandboxConfigurationSnapshot) { s.Registrations = nil }, metav1.ConditionFalse, "UnsupportedControl"},
		{"unavailable topology", func(_ *api.AgentPolicy, s *controller.SandboxConfigurationSnapshot) { s.Platforms = nil }, metav1.ConditionUnknown, "PrerequisitesUnavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, snapshot, _ := sandboxConfigurationFixture()
			policy := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "example-policy", UID: "example-policy-uid", Generation: 6}}
			tc.mutate(policy, &snapshot)
			agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "example-agent", UID: "example-agent-uid"}}
			c := fake.NewClientBuilder().WithScheme(configurationScheme(t)).WithStatusSubresource(policy, agent).WithObjects(policy, agent).Build()
			r := &controller.AgentPolicyReconciler{
				Client: c, Now: func() time.Time { return snapshot.ObservedAt },
				Source: controller.SandboxConfigurationSourceFunc(func(context.Context) (controller.SandboxConfigurationSnapshot, error) { return snapshot, nil }),
			}
			request := ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			actual := &api.AgentPolicy{}
			if err := c.Get(context.Background(), request.NamespacedName, actual); err != nil {
				t.Fatal(err)
			}
			condition := apimeta.FindStatusCondition(actual.Status.Conditions, api.ConditionAgentPolicyAccepted)
			if condition == nil || condition.Status != tc.status || condition.Reason != tc.reason ||
				condition.ObservedGeneration != policy.Generation || actual.Status.ObservedGeneration != policy.Generation {
				t.Fatalf("unexpected policy configuration condition: %+v", actual.Status)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(agent), agent); err != nil {
				t.Fatal(err)
			}
			if agent.Status.Sandbox != nil || len(agent.Status.Conditions) != 0 {
				t.Fatal("configuration acceptance must not report an Agent's enforcement")
			}
		})
	}
}

func TestConfigurationReadFailureAndStatusFailureAreNotSuccess(t *testing.T) {
	object, snapshot, prerequisites := sandboxConfigurationFixture()
	base := fake.NewClientBuilder().WithScheme(configurationScheme(t)).WithStatusSubresource(object).
		WithObjects(append([]client.Object{object}, prerequisites...)...).Build()
	failure := errors.New("synthetic prerequisite read failure")
	reader := &configurationErrorReader{Reader: base, failure: failure}
	r := &controller.SandboxReconciler{
		Client: base, APIReader: reader, Now: func() time.Time { return snapshot.ObservedAt },
		Source: controller.SandboxConfigurationSourceFunc(func(context.Context) (controller.SandboxConfigurationSnapshot, error) { return snapshot, nil }),
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: object.Name}}
	if _, err := r.Reconcile(context.Background(), request); !errors.Is(err, failure) {
		t.Fatal("prerequisite read failure was hidden", err)
	}
	current := &api.Sandbox{}
	if err := base.Get(context.Background(), request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Conditions[0].Status != metav1.ConditionUnknown {
		t.Fatal("unreadable prerequisite was accepted")
	}
	reader.failure = nil
	r.Client = &configurationErrorClient{Client: base, failure: failure}
	if _, err := r.Reconcile(context.Background(), request); !errors.Is(err, failure) {
		t.Fatal("status persistence failure was hidden", err)
	}
	if err := base.Get(context.Background(), request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Conditions[0].Status != metav1.ConditionUnknown {
		t.Fatal("failed status write appeared committed")
	}
}

type configurationErrorReader struct {
	client.Reader
	failure error
}

func (r *configurationErrorReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, ok := object.(*nodev1.RuntimeClass); ok && r.failure != nil {
		return r.failure
	}
	return r.Reader.Get(ctx, key, object, options...)
}

type configurationErrorClient struct {
	client.Client
	failure error
}

func (c *configurationErrorClient) Status() client.SubResourceWriter {
	return configurationErrorWriter{SubResourceWriter: c.Client.Status(), failure: c.failure}
}

type configurationErrorWriter struct {
	client.SubResourceWriter
	failure error
}

func (w configurationErrorWriter) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return w.failure
}
