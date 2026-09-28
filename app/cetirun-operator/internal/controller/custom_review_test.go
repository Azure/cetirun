// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

func runningCustomReview(t *testing.T) *completionScenario {
	t.Helper()
	s := newCompletionScenario(t)
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	s.observeApplied()
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *completionScenario) workload() *appsv1.StatefulSet {
	s.t.Helper()
	workload := &appsv1.StatefulSet{}
	key := types.NamespacedName{Namespace: s.provider.bindings.Namespace.Name, Name: "openclaw-" + s.agent.Name + "-cetirun"}
	if err := s.client.Get(context.Background(), key, workload); err != nil {
		s.t.Fatal(err)
	}
	return workload
}

func TestCustomOnDeleteReadinessAndSteadyCache(t *testing.T) {
	s := runningCustomReview(t)
	current := s.workload()
	current.Status.CurrentRevision = "old"
	current.Status.UpdateRevision = "new"
	if err := s.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	prepares := s.helm.prepareCalls
	for i := 0; i < 3; i++ {
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
	}
	if s.current().Status.Phase != api.PhaseReady || s.helm.prepareCalls != prepares {
		t.Fatal("OnDelete revision bookkeeping or steady-state cache is incorrect")
	}
	if s.current().Status.LastReconcileTime == nil {
		t.Fatal("Custom status has no reconciliation timestamp")
	}
}

func TestCustomPlatformAndReceiptUpdatesDoNotStopWriter(t *testing.T) {
	for _, change := range []string{"network", "pdb", "namespace-revision", "generation", "ttyd"} {
		t.Run(change, func(t *testing.T) {
			s := runningCustomReview(t)
			before := s.workload().DeepCopy()
			oldHash := s.helm.lastValues["runtime"].(map[string]any)["workloadHash"]
			approvals := s.provider.store.approvals
			s.helm.installCalled = false
			switch change {
			case "network":
				s.reconciler.CetiRunHelmValues = map[string]any{"networkPolicy": map[string]any{"blockedCIDRs": []any{"10.0.0.0/8", "169.254.169.254/32"}}}
			case "pdb":
				s.reconciler.CetiRunHelmValues = map[string]any{"podDisruptionBudget": map[string]any{"enabled": true}}
			case "ttyd":
				s.reconciler.CetiRunHelmValues = map[string]any{"ttyd": map[string]any{"port": 9000, "shell": "/bin/sh"}}
			case "generation":
				current := s.current()
				current.Generation++
				current.Spec.User.DisplayName = "Updated label"
				if err := s.client.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
				s.provider.bindings.Generation = current.Generation
			case "namespace-revision":
				ns := &corev1.Namespace{}
				if err := s.client.Get(context.Background(), types.NamespacedName{Name: s.provider.bindings.Namespace.Name}, ns); err != nil {
					t.Fatal(err)
				}
				ns.Labels = map[string]string{"example.com/metadata": "updated"}
				if err := s.client.Update(context.Background(), ns); err != nil {
					t.Fatal(err)
				}
				s.provider.bindings.Namespace.Revision = ns.ResourceVersion
			}
			if err := s.step(); err != nil {
				t.Fatal(err)
			}
			if s.provider.store.approvals != approvals || !reflect.DeepEqual(before.Spec, s.workload().Spec) ||
				s.helm.lastValues["runtime"].(map[string]any)["workloadHash"] != oldHash || s.current().Status.Phase != api.PhaseReady {
				t.Fatal("platform-only change interrupted the writer")
			}
			if change != "ttyd" && !s.helm.installCalled {
				t.Fatal("platform/audit update was not applied")
			}
		})
	}
}

type reviewClient struct {
	client.Client
	podsForbidden bool
	noRoutes      bool
}

func (c reviewClient) List(ctx context.Context, obj client.ObjectList, options ...client.ListOption) error {
	if _, ok := obj.(*corev1.PodList); ok && c.podsForbidden {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("synthetic denial"))
	}
	return c.Client.List(ctx, obj, options...)
}

func (c reviewClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
	if _, ok := obj.(*istiov1beta1.VirtualService); ok && c.noRoutes {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "networking.istio.io", Kind: "VirtualService"}}
	}
	return c.Client.Get(ctx, key, obj, options...)
}

func TestCustomPodPermissionFailurePrecedesHandoff(t *testing.T) {
	s := newCompletionScenario(t)
	s.reconciler.APIReader = reviewClient{Client: s.client, podsForbidden: true}
	_ = s.step()
	if s.provider.store.approvals != 0 || s.helm.installCalled {
		t.Fatal("missing Pod permissions reached lifecycle handoff")
	}
	c := meta.FindStatusCondition(s.current().Status.Conditions, api.ConditionAgentConfigReady)
	if c == nil || c.Reason != "CustomPermissionDenied" {
		t.Fatal("permission failure was not diagnosed")
	}
}

func TestCustomRouteErrorsAndPendingReleaseKeepObservedState(t *testing.T) {
	for _, unavailable := range []string{"route-api", "pending-release", "registry"} {
		t.Run(unavailable, func(t *testing.T) {
			s := runningCustomReview(t)
			s.restart()
			if unavailable == "route-api" {
				s.reconciler.Client = reviewClient{Client: s.client, noRoutes: true}
			} else if unavailable == "pending-release" {
				s.helm.prepareErr = helm.ErrReleaseOperationPending
			} else {
				s.helm.prepareErr = fmt.Errorf("synthetic registry timeout")
			}
			if err := s.step(); err != nil {
				t.Fatal(err)
			}
			if s.current().Status.Phase != api.PhaseReady {
				t.Fatal("dependency failure discarded observed Ready workload")
			}
			if unavailable == "route-api" {
				c := meta.FindStatusCondition(s.current().Status.Conditions, api.ConditionIngressReady)
				if c == nil || c.Status != metav1.ConditionFalse {
					t.Fatal("unavailable route API was not reported")
				}
			}
		})
	}
}

type observedProvider struct {
	*recoveryProvider
	deadline bool
	fail     bool
}

func (p *observedProvider) Resolve(ctx context.Context, agent *api.Agent, team *api.Team) (runtimebindings.Custom, error) {
	_, p.deadline = ctx.Deadline()
	if p.fail {
		return runtimebindings.Custom{}, fmt.Errorf("synthetic provider outage")
	}
	return p.recoveryProvider.Resolve(ctx, agent, team)
}

func TestCustomProviderDeadlineAndOutage(t *testing.T) {
	s := runningCustomReview(t)
	provider := &observedProvider{recoveryProvider: s.provider, fail: true}
	s.reconciler.CustomBindings = provider
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if !provider.deadline || s.current().Status.Phase != api.PhaseReady {
		t.Fatal("provider deadline or independent running observation was lost")
	}
	condition := meta.FindStatusCondition(s.current().Status.Conditions, api.ConditionAgentConfigReady)
	if condition == nil || condition.Reason != "CustomBindingsPending" {
		t.Fatal("provider outage not reported separately")
	}
}

func TestCustomForeignRouteIsNotAdoptedOrDeleted(t *testing.T) {
	s := runningCustomReview(t)
	key := types.NamespacedName{Namespace: s.provider.bindings.Namespace.Name, Name: "cetirun-" + s.agent.Name}
	route := &istiov1beta1.VirtualService{}
	if err := s.client.Get(context.Background(), key, route); err != nil {
		t.Fatal(err)
	}
	route.OwnerReferences[0].UID = "foreign"
	if err := s.client.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	version := route.ResourceVersion
	_ = s.step()
	if err := s.client.Get(context.Background(), key, route); err != nil || route.ResourceVersion != version {
		t.Fatal("foreign route was updated")
	}
	current := s.current()
	current.Spec.Suspended = true
	if err := s.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	_ = s.step()
	if err := s.client.Get(context.Background(), key, route); err != nil || route.ResourceVersion != version {
		t.Fatal("foreign route was deleted or changed on suspension")
	}
	if s.current().Status.Phase != api.PhaseSuspended || *s.workload().Spec.Replicas != 0 {
		t.Fatal("route conflict hid the completed suspension")
	}
}

func TestCustomBlockedDeletionRevokesAccessAndStopsWorkload(t *testing.T) {
	s := newCompletionScenario(t)
	s.provider.store.failures = 5
	_ = s.step()
	s.observeApplied()
	if err := s.client.Delete(context.Background(), s.current()); err != nil {
		t.Fatal(err)
	}
	_ = s.step()
	if s.current().Status.Phase != api.PhaseDeleting || *s.workload().Spec.Replicas != 0 || s.pending() == nil {
		t.Fatal("blocked deletion did not quiesce the instance")
	}
	route := &istiov1beta1.VirtualService{}
	err := s.client.Get(context.Background(), types.NamespacedName{Namespace: s.provider.bindings.Namespace.Name, Name: "cetirun-" + s.agent.Name}, route)
	if !apierrors.IsNotFound(err) {
		t.Fatal("blocked deletion retained an owned route")
	}
}

func TestCustomDesktopDisableAndEnableCleansRoute(t *testing.T) {
	s := runningCustomReview(t)
	s.provider.approve = func(sts *appsv1.StatefulSet) error {
		sts.Spec.Replicas = ptr.To[int32](0)
		return s.client.Update(context.Background(), sts)
	}
	for _, enabled := range []bool{false, true} {
		current := s.current()
		current.Generation++
		current.Spec.Helm.Values = map[string]runtime.RawExtension{"desktop": {Raw: []byte(fmt.Sprintf(`{"enabled":%v}`, enabled))}}
		if err := s.client.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
		s.provider.bindings.Generation = current.Generation
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
		route := &istiov1beta1.VirtualService{}
		err := s.client.Get(context.Background(), types.NamespacedName{Namespace: s.provider.bindings.Namespace.Name, Name: "cetirun-" + s.agent.Name}, route)
		if (err == nil) != enabled {
			t.Fatalf("desktop=%v route=%v", enabled, err)
		}
		if err := s.client.Delete(context.Background(), s.workload()); err != nil {
			t.Fatal(err)
		}
		s.observeApplied()
	}
}

func TestCustomDeletingOrphanedRecordsCanFinish(t *testing.T) {
	s := newCompletionScenario(t)
	s.provider.store.failures = 1
	_ = s.step()
	s.observeApplied()
	if err := s.client.Delete(context.Background(), s.current()); err != nil {
		t.Fatal(err)
	}
	records := &corev1.ConfigMapList{}
	if err := s.client.List(context.Background(), records, client.InNamespace("cetirun-system")); err != nil {
		t.Fatal(err)
	}
	for i := range records.Items {
		records.Items[i].OwnerReferences = nil
		if err := s.client.Update(context.Background(), &records.Items[i]); err != nil {
			t.Fatal(err)
		}
	}
	workload := s.workload()
	workload.OwnerReferences = nil
	if err := s.client.Update(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
	route := &istiov1beta1.VirtualService{}
	key := types.NamespacedName{Namespace: s.provider.bindings.Namespace.Name, Name: "cetirun-" + s.agent.Name}
	if err := s.client.Get(context.Background(), key, route); err != nil {
		t.Fatal(err)
	}
	route.OwnerReferences = nil
	if err := s.client.Update(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.client.Get(context.Background(), types.NamespacedName{Name: s.agent.Name}, &api.Agent{}); !apierrors.IsNotFound(err) {
		t.Fatalf("orphaned records blocked Agent finalization: %v", err)
	}
}

func TestControllerPlatformConfigurationParity(t *testing.T) {
	configuration := map[string]any{
		"tolerations": []any{map[string]any{"key": "agents", "operator": "Equal", "value": "true", "effect": "NoSchedule"}},
		"affinity": map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
			"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "pool", "operator": "In", "values": []any{"agents"}}}}},
		}}},
		"topologySpreadConstraints": []any{map[string]any{"maxSkew": 1, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "ScheduleAnyway",
			"labelSelector": map[string]any{"matchLabels": map[string]any{"app": "agents"}}}},
	}
	run := func(typ api.AgentType) map[string]map[string]any {
		cr, bindings, objects := customFixture("example", nil)
		if typ != api.AgentTypeCustom {
			cr = newCR("example", api.PhasePending, nil, 1, 0, "")
			cr.Spec.Agent.Type = typ
			objects = []client.Object{cr}
		}
		scheme := testScheme()
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
		h := &fakeHelmClient{useHarness: true}
		r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: h,
			CustomBindings: &customProvider{bindings: bindings}, CetiRunHelmValues: configuration,
			ChartRegistry: "registry.example.com", DefaultModel: "test-model", DomainSuffix: "platform.example.com",
			IstioRevision: "test", EventRecorder: record.NewFakeRecorder(100)}
		for i := 0; i < 4 && !h.installCalled; i++ {
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name}}); err != nil {
				t.Fatal(err)
			}
		}
		if !h.installCalled {
			t.Fatalf("%s did not reach the actual preparation path", typ)
		}
		network := h.lastValues["networkPolicy"].(map[string]any)
		if network["istioNamespace"] != "aks-istio-system" || network["istioIngressNamespace"] != "aks-istio-ingress" {
			t.Fatalf("%s lost platform defaults: %v", typ, network)
		}
		desktop := h.lastValues["desktop"].(map[string]any)
		if desktop["novncPathPrefix"] != "example/desktop/" || desktop["image"].(map[string]any)["repository"] != "registry.example.com/cetirun-desktop" {
			t.Fatal("controller did not preserve desktop routing/image defaults")
		}
		ch, err := loader.Load("../../../cetirun-chart")
		if err != nil {
			t.Fatal(err)
		}
		caps := chartutil.DefaultCapabilities.Copy()
		caps.APIVersions = append(caps.APIVersions, "networking.istio.io/v1/Sidecar")
		values, err := chartutil.ToRenderValues(ch, h.lastValues, chartutil.ReleaseOptions{Name: "openclaw-example", Namespace: "cetirun-example", IsInstall: true}, caps)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := engine.Render(ch, values)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]map[string]any{}
		for filename, contents := range rendered {
			if strings.HasSuffix(filename, "NOTES.txt") {
				continue
			}
			for _, document := range strings.Split(contents, "\n---") {
				var object map[string]any
				if err := yaml.Unmarshal([]byte(document), &object); err != nil {
					t.Fatal(err)
				}
				if len(object) != 0 {
					result[object["kind"].(string)] = object
				}
			}
		}
		return result
	}
	custom := run(api.AgentTypeCustom)
	for _, typ := range []api.AgentType{api.AgentTypeHermes, api.AgentTypeOpenClaw} {
		native := run(typ)
		network := custom["NetworkPolicy"]["spec"].(map[string]any)
		egress := network["egress"].([]any)
		if !reflect.DeepEqual(egress[1:], native["NetworkPolicy"]["spec"].(map[string]any)["egress"]) {
			t.Fatal("shared network egress diverged across controller paths")
		}
		hosts := func(objects map[string]map[string]any) []any {
			return objects["Sidecar"]["spec"].(map[string]any)["egress"].([]any)[0].(map[string]any)["hosts"].([]any)
		}
		if !reflect.DeepEqual(hosts(custom)[:len(hosts(native))], hosts(native)) {
			t.Fatal("shared mesh discovery scope diverged")
		}
		pod := func(objects map[string]map[string]any) map[string]any {
			return objects["StatefulSet"]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		}
		for _, field := range []string{"tolerations", "affinity", "topologySpreadConstraints", "runtimeClassName", "nodeSelector"} {
			if !reflect.DeepEqual(pod(custom)[field], pod(native)[field]) {
				t.Fatalf("platform scheduling %s diverged", field)
			}
		}
	}
}
