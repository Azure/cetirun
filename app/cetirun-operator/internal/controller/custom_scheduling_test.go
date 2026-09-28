// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCustomInvalidPlatformSchedulingCannotReachHandoff(t *testing.T) {
	for _, defaults := range []map[string]any{
		{"runtimeClassName": "INVALID_CLASS"},
		{"nodeSelector": map[string]any{"example.com/node": "value/with/slash"}},
		{"nodeSelector": map[string]any{"bad key": ""}},
	} {
		cr, b, objects := customFixture("example", nil)
		scheme := testScheme()
		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(objects...).Build()
		p, h := &customProvider{bindings: b}, &fakeHelmClient{useHarness: true}
		r := &controller.AgentReconciler{Client: c, Scheme: scheme, HelmClient: h, CustomBindings: p,
			CetiRunHelmValues: defaults, EventRecorder: record.NewFakeRecorder(100)}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: cr.Name}})
		if err == nil || p.applied || h.installCalled {
			t.Fatalf("invalid scheduling reached handoff/apply: err=%v approve=%v apply=%v", err, p.applied, h.installCalled)
		}
	}
}
