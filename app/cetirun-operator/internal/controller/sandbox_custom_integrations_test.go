// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCommonCustomPrivateRegistryUsesItsActualServiceAccount(t *testing.T) {
	for _, mode := range []string{"headless", "chat"} {
		t.Run(mode, func(t *testing.T) {
			f, source := reconciledRegistryFixture(t, "custom", withCommonCustom(t, mode))
			var subjects []string
			var subjectsMu sync.Mutex
			audit := interceptor.NewClient(f.kube, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
					if review, ok := object.(*authorizationv1.SubjectAccessReview); ok {
						subjectsMu.Lock()
						subjects = append(subjects, review.Spec.User)
						subjectsMu.Unlock()
						review.Status.Denied = true
						return nil
					}
					return c.Create(ctx, object, options...)
				},
			})
			f.r.Client, f.r.APIReader = audit, audit
			for range 4 {
				f.reconcileOK(t)
			}
			workload := f.workload(t)
			subject := "system:serviceaccount:" + workload.Namespace + ":" + workload.Spec.Template.Spec.ServiceAccountName
			if !slices.Contains(subjects, subject) {
				t.Fatal("registry isolation checked a different identity from the actual Custom Pod")
			}
			name, err := registryauth.PullSecretName(f.agent.UID)
			if err != nil {
				t.Fatal(err)
			}
			pull := &corev1.Secret{}
			if err := f.kube.Get(context.Background(), client.ObjectKey{Namespace: workload.Namespace, Name: name}, pull); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pull.Data, source.Data) ||
				!slices.Equal(workload.Spec.Template.Spec.ImagePullSecrets, []corev1.LocalObjectReference{{Name: name}}) {
				t.Fatal("Custom lost its owned registry projection")
			}
			source.Data[corev1.DockerConfigJsonKey], err = registryauth.Encode(*f.current(t).Spec.Agent.Image,
				registryauth.Credentials{Username: "example", Password: "rotated-synthetic-token"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.kube.Update(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			f.reconcileOK(t)
			if !reflect.DeepEqual(f.workload(t).Spec, workload.Spec) {
				t.Fatal("registry credential rotation restarted Custom")
			}
		})
	}
}

func TestCommonCustomCopilotProjectionsUseChartCapabilities(t *testing.T) {
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "custom", true, true, withCommonCustom(t, "headless"))
	for range 2 {
		f.reconcileOK(t)
	}
	agent := f.current(t)
	if agent.Status.AIGateway == nil || agent.Status.AIGateway.Copilot == nil {
		t.Fatal("fixture did not prepare a Copilot projection")
	}
	agent.Annotations[api.AnnotationCopilotSignInGeneration] = "0123abcd"
	agent.Annotations[api.AnnotationCopilotModels] = `[{"id":"gpt-5.5","responsesOnly":true}]`
	if err := f.kube.Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	agent.Status.AIGateway.Copilot.ProxyKeyID = strings.Repeat("c", 64)
	agent.Status.AIGateway.Copilot.Models = []string{"copilot/gpt-5.5"}
	if err := f.kube.Status().Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.r.prepareReconciledSandbox(context.Background(), f.current(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy := prepared.Values["aiGateway"].(map[string]any)["copilotProxy"].(map[string]any)
	if proxy[aiGatewayChartProxyKeyValue] != strings.Repeat("c", 64) ||
		proxy[aiGatewayChartSignInValue] != "0123abcd" ||
		!reflect.DeepEqual(proxy["responsesModels"], []any{"gpt-5.5"}) {
		t.Fatal("Custom omitted the current Copilot key, sign-in or Responses projection:", proxy)
	}
	expectation, err := prepared.ReconcileExpectation()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(expectation.Workloads, func(workload w.RenderedWorkload) bool {
		return workload.Kind == "Deployment" && workload.Identity.Name == copilotProxyServiceName(agent) &&
			workload.Template.Annotations[api.AnnotationCopilotSignInGeneration] == "0123abcd"
	}) {
		t.Fatal("Custom omitted the actual Copilot proxy or its current sign-in projection")
	}
}

func TestCommonCustomCopilotResourcesAndSuspension(t *testing.T) {
	for _, mode := range []string{"headless", "chat"} {
		for label, access := range map[string]struct{ shared, copilot bool }{
			"mixed":        {true, true},
			"copilot-only": {false, true},
			"shared-only":  {true, false},
		} {
			t.Run(mode+"/"+label, func(t *testing.T) {
				f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "custom", access.shared, access.copilot, withCommonCustom(t, mode))
				for range 2 {
					f.reconcileOK(t)
				}
				agent := f.current(t)
				prepared, err := f.r.prepareReconciledSandbox(context.Background(), agent, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				expectation, err := prepared.ReconcileExpectation()
				if err != nil {
					t.Fatal(err)
				}
				name := copilotProxyServiceName(agent)
				counts := map[string]int{}
				for _, object := range expectation.Controls {
					if object.GetName() == name {
						counts[object.GetKind()]++
					}
				}
				for _, workload := range expectation.Workloads {
					if workload.Kind == "Deployment" && workload.Identity.Name == name {
						counts["Deployment"]++
						spec := workload.Template.Spec
						if workload.Replicas != 1 || spec.ServiceAccountName != name ||
							spec.RuntimeClassName != nil || spec.AutomountServiceAccountToken == nil ||
							*spec.AutomountServiceAccountToken {
							t.Fatal("Copilot proxy lost its independent workload or identity boundary")
						}
						for _, volume := range spec.Volumes {
							if volume.PersistentVolumeClaim != nil {
								t.Fatal("Copilot proxy received Agent storage")
							}
						}
					}
					if workload.Kind == "StatefulSet" {
						for _, source := range w.PodSourceReferences(workload.Template.Spec, expectation.Namespace.Name) {
							if source.Kind == "Secret" && (source.Name == api.CopilotTokenSecretName || source.Name == aiGatewayProxySecretName) {
								t.Fatal("Agent received Copilot proxy credentials")
							}
						}
					}
				}
				want := 0
				if access.copilot {
					want = 1
				}
				for _, kind := range []string{"Deployment", "Service", "ServiceAccount", "ConfigMap", "NetworkPolicy"} {
					if counts[kind] != want {
						t.Fatalf("expected %d Copilot %s, got %d", want, kind, counts[kind])
					}
				}
				if !access.copilot {
					return
				}
				agent.Spec.Suspended = true
				if err := f.kube.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
				suspended, err := f.r.prepareReconciledSandbox(context.Background(), f.current(t), nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				stopped, err := suspended.ReconcileExpectation()
				if err != nil {
					t.Fatal(err)
				}
				if len(stopped.Workloads) != len(expectation.Workloads) {
					t.Fatal("suspension dropped an owned workload instead of stopping it")
				}
				for _, workload := range stopped.Workloads {
					if workload.Replicas != 0 {
						t.Fatal("suspended Custom left a workload running")
					}
				}
			})
		}
	}
}
