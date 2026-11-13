// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestConfigurationSubmissionDiagnostics(t *testing.T) {
	const secret = "sk-dummy-submission-detail"
	for _, scenario := range []struct {
		name   string
		invoke func(t *testing.T) error
		want   SubmissionDiagnostic
	}{
		{
			name: "before source submission",
			invoke: func(t *testing.T) error {
				store, storage, before, after := configurationFixture(t, "Team")
				ticket := changingConfiguration(t, store, before, after)
				adapter := configurationAdapter(t, storage, storage, before, after)
				return adapter.Submit(context.Background(), ticket, func(context.Context) error {
					return errors.New(secret)
				})
			},
			want: SubmissionDiagnostic{Stage: SubmissionStageGuard, State: SubmissionNotSubmitted, Reason: "guard"},
		},
		{
			name: "definitive server rejection",
			invoke: func(t *testing.T) error {
				store, storage, before, after := configurationFixture(t, "Sandbox")
				ticket := changingConfiguration(t, store, before, after)
				writer := interceptor.NewClient(storage, interceptor.Funcs{
					Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
						return apierrors.NewForbidden(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "sandboxes"}, before.GetName(), errors.New(secret))
					},
				})
				adapter := configurationAdapter(t, writer, storage, before, after)
				return adapter.Submit(context.Background(), ticket, func(ctx context.Context) error { return store.CheckChange(ctx, ticket) })
			},
			want: SubmissionDiagnostic{Stage: SubmissionStageSourceUpdate, State: SubmissionRejected, Reason: "forbidden", HTTPStatus: 403},
		},
		{
			name: "uncertain transport result",
			invoke: func(t *testing.T) error {
				store, storage, before, after := configurationFixture(t, "AgentPolicy")
				ticket := changingConfiguration(t, store, before, after)
				writer := interceptor.NewClient(storage, interceptor.Funcs{
					Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
						return errors.New(secret)
					},
				})
				adapter := configurationAdapter(t, writer, storage, before, after)
				return adapter.Submit(context.Background(), ticket, func(ctx context.Context) error { return store.CheckChange(ctx, ticket) })
			},
			want: SubmissionDiagnostic{Stage: SubmissionStageSourceUpdate, State: SubmissionUnknown, Reason: "unknown"},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			err := scenario.invoke(t)
			if err == nil {
				t.Fatal("submission unexpectedly succeeded")
			}
			diagnostic, found := SubmissionDiagnosticFor(err)
			if !found || diagnostic != scenario.want {
				t.Fatalf("diagnostic = %#v, found = %v; want %#v", diagnostic, found, scenario.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("submission error exposed server detail: %q", err)
			}
			if !errors.Is(err, ErrUnavailable) && scenario.want.State != SubmissionNotSubmitted {
				t.Fatalf("submission did not preserve unavailable outcome: %v", err)
			}
		})
	}
}

func TestSubmissionDiagnosticForRejectsUnrelatedErrors(t *testing.T) {
	if _, found := SubmissionDiagnosticFor(errors.New("unrelated")); found {
		t.Fatal("unrelated error was treated as a submission diagnostic")
	}
}

func TestMutationFailureDiagnosticUsesBoundedUnknownReasons(t *testing.T) {
	for _, scenario := range []struct {
		name, reason string
		err          error
	}{
		{"canceled", "canceled", context.Canceled},
		{"deadline", "timeout", context.DeadlineExceeded},
		{"eof", "transport", errors.Join(io.EOF, errors.New("sk-dummy-eof"))},
		{"server", "unknown", apierrors.NewInternalError(errors.New("sk-dummy-server"))},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			result := classifyMutationFailure(SubmissionStageStatusUpdate, scenario.err)
			diagnostic, found := SubmissionDiagnosticFor(result)
			if !found || diagnostic.Stage != SubmissionStageStatusUpdate || diagnostic.State != SubmissionUnknown || diagnostic.Reason != scenario.reason {
				t.Fatalf("diagnostic = %#v, found = %v", diagnostic, found)
			}
		})
	}
}

func TestSubmissionDiagnosticRequiresConsistentServerRejection(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		code   int32
		reason metav1.StatusReason
		state  SubmissionState
	}{
		{"conflict", 409, metav1.StatusReasonConflict, SubmissionRejected},
		{"invalid", 422, metav1.StatusReasonInvalid, SubmissionRejected},
		{"timeout", 504, metav1.StatusReasonTimeout, SubmissionUnknown},
		{"throttled", 429, metav1.StatusReasonTooManyRequests, SubmissionUnknown},
		{"misleading server error", 500, metav1.StatusReasonForbidden, SubmissionUnknown},
		{"mismatched rejection", 409, metav1.StatusReasonForbidden, SubmissionUnknown},
		{"unknown reason", 403, "sk-dummy-reason", SubmissionUnknown},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			original := &apierrors.StatusError{ErrStatus: metav1.Status{
				Code: scenario.code, Reason: scenario.reason, Message: "sk-dummy-server-body",
			}}
			result := classifySourceUpdateFailure(original)
			diagnostic, found := SubmissionDiagnosticFor(result)
			if !found || diagnostic.State != scenario.state || strings.Contains(diagnostic.Reason, "sk-dummy") || strings.Contains(result.Error(), "sk-dummy") {
				t.Fatalf("unsafe or incorrect server diagnostic: %+v", diagnostic)
			}
			var statusError *apierrors.StatusError
			if !errors.Is(result, original) || !errors.As(result, &statusError) || statusError != original {
				t.Fatal("diagnostic lost the original typed error")
			}
		})
	}
}
