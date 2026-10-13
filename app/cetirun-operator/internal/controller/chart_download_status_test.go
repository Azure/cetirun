// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
)

// failingPrepareHelm fails every chart preparation with err.
type failingPrepareHelm struct {
	chartHelm
	err error
}

func (h *failingPrepareHelm) Prepare(string, map[string]any, string) (*helm.PreparedRelease, error) {
	return nil, h.err
}

// The Agent's conditions and Warning event report a chart download failure
// with a fixed message, and other errors with their own text. The phase and
// the reason do not change.
func TestChartDownloadFailureIsReportedWithAFixedMessage(t *testing.T) {
	cause := &url.Error{Op: "Get", URL: "https://storage.example.com/blobs/sha256-0?query=synthetic", Err: errors.New("connection reset by peer")}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"chart download": {
			err: &helm.ChartDownloadError{Chart: "oci://registry.example.com/helm/cetirun:1.0.0", Version: "1.0.0",
				Err: fmt.Errorf(`failed to perform "Fetch" on source: %w`, cause)},
			want: "Agent chart 1.0.0 could not be downloaded from the registry; the operator will retry",
		},
		"configuration": {
			err:  errors.New("helm.values.image must use spec.agent.image"),
			want: "helm.values.image must use spec.agent.image",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, agent, _ := legacyGatewayFixture(t)
			r.HelmClient = &failingPrepareHelm{err: tc.err}
			recorder := record.NewFakeRecorder(100)
			r.EventRecorder = recorder
			ctx := context.Background()
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}

			_, err := r.Reconcile(ctx, req)
			if err == nil || strings.Contains(err.Error(), "storage.example.com") {
				t.Fatalf("reconcile error = %v", err)
			}
			current := &api.Agent{}
			if err := r.Get(ctx, req.NamespacedName, current); err != nil {
				t.Fatal(err)
			}
			if current.Status.Phase != api.PhaseFailed {
				t.Fatalf("phase = %q, want Failed", current.Status.Phase)
			}
			for _, kind := range []string{api.ConditionAgentConfigReady, api.ConditionReady} {
				condition := meta.FindStatusCondition(current.Status.Conditions, kind)
				if condition == nil || condition.Reason != "AgentConfigInvalid" || condition.Message != tc.want {
					t.Fatalf("%s condition = %+v, want reason AgentConfigInvalid and message %q", kind, condition, tc.want)
				}
			}
			warnings := []string{}
			for len(recorder.Events) > 0 {
				if event := <-recorder.Events; strings.HasPrefix(event, "Warning ") {
					warnings = append(warnings, event)
				}
			}
			if len(warnings) != 1 || warnings[0] != "Warning AgentConfigInvalid "+tc.want {
				t.Fatalf("warning events = %q, want one with message %q", warnings, tc.want)
			}
		})
	}
}
