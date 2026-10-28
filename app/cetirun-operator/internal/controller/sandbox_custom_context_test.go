// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

type contextTestClient struct{ client.Client }

func (c contextTestClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if object.GetUID() == "" {
		object.SetUID(types.UID("synthetic-" + object.GetName()))
	}
	return c.Client.Create(ctx, object, options...)
}

func TestCustomSandboxContextIsImmutableAgentOwnedTeamSnapshot(t *testing.T) {
	fixture := testfixture.New("custom")
	fixture.Team.Spec.MemberContext.Role = "Synthetic reviewer"
	fixture.Team.Spec.MemberContext.Description = []string{"Review the requested change.", "Do not expose credentials."}
	client := contextTestClient{fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()}
	reconciler := &AgentReconciler{Client: client, APIReader: client}

	snapshot, err := reconciler.ensureCustomSandboxContext(context.Background(), fixture.Agent, fixture.Team, fixture.Namespace.Name)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Immutable == nil || !*snapshot.Immutable ||
		!equality.Semantic.DeepEqual(snapshot.OwnerReferences, []metav1.OwnerReference{sandboxworkload.AgentOwner(fixture.Agent.Name, fixture.Agent.UID)}) ||
		!strings.Contains(snapshot.Data["AGENTS.md"], "Synthetic reviewer") ||
		!strings.Contains(snapshot.Data["AGENTS.md"], "Do not expose credentials.") {
		t.Fatalf("Custom context snapshot did not exactly retain the bound Team context: %#v", snapshot)
	}

	again, err := reconciler.ensureCustomSandboxContext(context.Background(), fixture.Agent, fixture.Team, fixture.Namespace.Name)
	if err != nil || again.Name != snapshot.Name || again.UID != snapshot.UID {
		t.Fatalf("unchanged Team context did not reuse its immutable snapshot: %#v, %v", again, err)
	}
	updated := fixture.Team.DeepCopy()
	updated.Spec.MemberContext.Role = "Different synthetic reviewer"
	next, err := reconciler.ensureCustomSandboxContext(context.Background(), fixture.Agent, updated, fixture.Namespace.Name)
	if err != nil || next.Name == snapshot.Name || next.UID == types.UID("") {
		t.Fatalf("changed Team context did not create a distinct immutable snapshot: %#v, %v", next, err)
	}
}

func TestReconciledCustomTeamDigestCaptureRejectsSnapshotRace(t *testing.T) {
	fixture := testfixture.New("custom")
	scheme := newTestScheme(t)
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	team := fixture.Team.DeepCopy()
	team.ResourceVersion = "1"
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(team).Build()
	reader := contextTestClient{base}
	reconciler := &AgentReconciler{APIReader: reader}
	source := sandboxworkload.SourceReference{
		APIVersion: api.GroupVersion.String(), Kind: "Team", Name: team.Name, UID: team.UID, ResourceVersion: team.ResourceVersion,
	}
	team.Spec.MemberContext.Role = "changed after the rendered context snapshot"
	if err := base.Update(context.Background(), team); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.nativeSourceDigest(context.Background(), source, fixture.Agent.Spec.SandboxRef, true); err == nil {
		t.Fatal("Team update after the rendered context snapshot was accepted for initial digest capture")
	}
}

func TestReconciledCustomContextSnapshotCannotMixWithUpdatedTeamDigest(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "custom", true, false,
		func(r *AgentReconciler, kube client.Client) {
			policy := &api.AgentPolicy{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "policy-example"}, policy); err != nil {
				t.Fatal(err)
			}
			policy.Spec.Harnesses.Allowed = append(policy.Spec.Harnesses.Allowed, api.AgentTypeCustom)
			if err := kube.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "example"}, agent); err != nil {
				t.Fatal(err)
			}
			r.CustomApprovedImages = []string{*agent.Spec.Agent.Image}
		},
	)
	contexts := &corev1.ConfigMapList{}
	if err := f.kube.List(ctx, contexts, client.InNamespace(instanceNamespace(f.agent.Name))); err != nil {
		t.Fatal(err)
	}
	for index := range contexts.Items {
		if strings.HasPrefix(contexts.Items[index].Name, "custom-context-") {
			if err := f.kube.Delete(ctx, &contexts.Items[index]); err != nil {
				t.Fatal(err)
			}
		}
	}
	stages := f.installer.stages
	changed := false
	f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
			if err := c.Create(ctx, object, options...); err != nil {
				return err
			}
			if changed || !strings.HasPrefix(object.GetName(), "custom-context-") {
				return nil
			}
			team := &api.Team{}
			if err := c.Get(ctx, client.ObjectKey{Name: "team-example"}, team); err != nil {
				return err
			}
			team.Spec.MemberContext.Role = "changed after context snapshot creation"
			if err := c.Update(ctx, team); err != nil {
				return err
			}
			changed = true
			return nil
		},
	})
	if _, err := f.reconcile(); !errors.Is(err, errSandboxSourceChanged) {
		t.Fatalf("reconciled Custom must retry a Team/context snapshot race: %v", err)
	}
	current := f.current(t)
	if current.Status.Phase != api.PhasePending {
		t.Fatalf("transient Team/context race became a permanent configuration failure: %s", current.Status.Phase)
	}
	if !changed || f.installer.stages != stages {
		t.Fatalf("Team/context race reached workload apply: changed=%t stages=%d want=%d", changed, f.installer.stages, stages)
	}
}
