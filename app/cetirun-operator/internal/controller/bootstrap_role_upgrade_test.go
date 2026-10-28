// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconciledBootstrapRoleUpgradesOnlyPreMTLSContract(t *testing.T) {
	ctx := context.Background()
	f, attempt, receipt, role := legacyBootstrapRoleFixture(t)
	object, currentReceipt, err := f.r.readSandboxBootstrap(ctx, f.current(t))
	if err != nil {
		t.Fatal(err)
	}
	desired := f.r.sandboxBootstrapObjects(f.current(t), object, currentReceipt)[1].(*rbacv1.Role)
	if validateSandboxBootstrapObject(role, desired, receipt.Objects[1].UID, false) == nil {
		t.Fatal("pre-mTLS Role unexpectedly passed the current strict validator")
	}
	patches := 0
	f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
		if _, ok := object.(*rbacv1.Role); ok {
			patches++
		}
		return c.Patch(ctx, object, patch, options...)
	}})
	if err := f.r.bootstrapSandboxOwned(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	upgraded := &rbacv1.Role{}
	if err := f.kube.Get(ctx, client.ObjectKeyFromObject(role), upgraded); err != nil {
		t.Fatal(err)
	}
	if upgraded.UID != receipt.Objects[1].UID || !equality.Semantic.DeepEqual(upgraded.Rules, desired.Rules) || patches != 1 {
		t.Fatalf("bootstrap Role upgrade = %#v; patches=%d", upgraded, patches)
	}
	attempt, err = f.r.resolveSandboxInputs(ctx, f.current(t), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.bootstrapSandboxOwned(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if patches != 1 {
		t.Fatalf("current bootstrap Role was patched again: %d", patches)
	}
}

func TestReconciledBootstrapRoleUpgradeRejectsForeignRulesAndUID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *nativeSandboxFixture, *sandboxBootstrapReceipt, *rbacv1.Role)
	}{
		{name: "foreign rule", mutate: func(t *testing.T, _ *nativeSandboxFixture, _ *sandboxBootstrapReceipt, role *rbacv1.Role) {
			t.Helper()
			role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}})
		}},
		{name: "recorded UID", mutate: func(t *testing.T, f *nativeSandboxFixture, receipt *sandboxBootstrapReceipt, _ *rbacv1.Role) {
			t.Helper()
			receipt.Objects[1].UID = types.UID("foreign-role-uid")
			object, _, err := f.r.readSandboxBootstrap(context.Background(), f.current(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.r.saveSandboxBootstrap(context.Background(), object, receipt); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f, attempt, receipt, role := legacyBootstrapRoleFixture(t)
			tc.mutate(t, f, receipt, role)
			if err := f.kube.Update(ctx, role); err != nil {
				t.Fatal(err)
			}
			if err := f.r.bootstrapSandboxOwned(ctx, attempt); err == nil || !strings.Contains(err.Error(), "Sandbox bootstrap operator Role changed") && !strings.Contains(err.Error(), "ownership or UID changed") {
				t.Fatalf("foreign bootstrap Role was accepted: %v", err)
			}
		})
	}
}

func TestReconciledBootstrapRoleUpgradeRejectsDependencyDriftAndUnreconciledReceipt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *nativeSandboxFixture, *sandboxBootstrapReceipt)
	}{
		{name: "unreconciled receipt", mutate: func(t *testing.T, f *nativeSandboxFixture, receipt *sandboxBootstrapReceipt) {
			t.Helper()
			receipt.Reconciled = false
			saveBootstrapRoleReceipt(t, f, receipt)
		}},
		{name: "recorded RoleBinding UID", mutate: func(t *testing.T, f *nativeSandboxFixture, receipt *sandboxBootstrapReceipt) {
			t.Helper()
			receipt.Objects[2].UID = types.UID("foreign-rolebinding-uid")
			saveBootstrapRoleReceipt(t, f, receipt)
		}},
		{name: "RoleBinding subjects", mutate: func(t *testing.T, f *nativeSandboxFixture, receipt *sandboxBootstrapReceipt) {
			t.Helper()
			binding := &rbacv1.RoleBinding{}
			if err := f.kube.Get(context.Background(), client.ObjectKey{Namespace: receipt.Namespace, Name: receipt.Objects[2].Name}, binding); err != nil {
				t.Fatal(err)
			}
			binding.Subjects = nil
			if err := f.kube.Update(context.Background(), binding); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f, attempt, receipt, role := legacyBootstrapRoleFixture(t)
			tc.mutate(t, f, receipt)
			if err := f.r.bootstrapSandboxOwned(ctx, attempt); err == nil {
				t.Fatal("bootstrap Role migration accepted an invalid dependency")
			}
			assertBootstrapRoleRules(t, f, role, legacySandboxBootstrapRoleRules)
		})
	}
}

func TestReconciledBootstrapRoleUpgradeRetriesAfterPatchConflict(t *testing.T) {
	ctx := context.Background()
	f, attempt, _, role := legacyBootstrapRoleFixture(t)
	f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
		if _, ok := object.(*rbacv1.Role); ok {
			return apierrors.NewConflict(schema.GroupResource{Group: rbacv1.GroupName, Resource: "roles"}, object.GetName(), errors.New("synthetic optimistic-lock conflict"))
		}
		return c.Patch(ctx, object, patch, options...)
	}})
	if err := f.r.bootstrapSandboxOwned(ctx, attempt); !apierrors.IsConflict(err) {
		t.Fatalf("bootstrap Role patch conflict = %v, want conflict", err)
	}
	assertBootstrapRoleRules(t, f, role, legacySandboxBootstrapRoleRules)

	f.r.Client = f.kube
	attempt, err := f.r.resolveSandboxInputs(ctx, f.current(t), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.bootstrapSandboxOwned(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	assertBootstrapRoleRules(t, f, role, targetSandboxBootstrapRoleRules)
}

func TestReconciledBootstrapRoleUpgradeRejectsUnknownTarget(t *testing.T) {
	ctx := context.Background()
	f, attempt, receipt, role := legacyBootstrapRoleFixture(t)
	object, currentReceipt, err := f.r.readSandboxBootstrap(ctx, f.current(t))
	if err != nil {
		t.Fatal(err)
	}
	desired := f.r.sandboxBootstrapObjects(f.current(t), object, currentReceipt)
	target := desired[1].(*rbacv1.Role)
	target.Rules = append(sandboxBootstrapRoleRulesCopy(target.Rules), rbacv1.PolicyRule{APIGroups: []string{"example.test"}, Resources: []string{"future-resources"}, Verbs: []string{"get"}})
	result, err := f.r.upgradeReconciledBootstrapRole(ctx, attempt, receipt, desired, role)
	if err != nil || result != role {
		t.Fatalf("unknown target migration = %#v, %v", result, err)
	}
	assertBootstrapRoleRules(t, f, role, legacySandboxBootstrapRoleRules)
}

func legacyBootstrapRoleFixture(t *testing.T) (*nativeSandboxFixture, *sandboxPreparationAttempt, *sandboxBootstrapReceipt, *rbacv1.Role) {
	t.Helper()
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
	object, receipt, err := f.r.readSandboxBootstrap(ctx, f.current(t))
	if err != nil || object == nil || !receipt.Reconciled || receipt.AgentType != api.AgentTypeCustom || receipt.Objects[1].UID == "" {
		t.Fatalf("reconciled Custom bootstrap receipt = %#v, %v", receipt, err)
	}
	role := &rbacv1.Role{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: receipt.Namespace, Name: receipt.Objects[1].Name}, role); err != nil {
		t.Fatal(err)
	}
	role.Rules = sandboxBootstrapRoleRulesCopy(legacySandboxBootstrapRoleRules)
	if err := f.kube.Update(ctx, role); err != nil {
		t.Fatal(err)
	}
	attempt, err := f.r.resolveSandboxInputs(ctx, f.current(t), true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !attempt.reconciled || !sandboxBootstrapReceiptHasFinalizer(t, f, object) {
		t.Fatal("fixture did not retain a reconciled bootstrap receipt")
	}
	return f, attempt, receipt, role
}

func sandboxBootstrapReceiptHasFinalizer(t *testing.T, f *nativeSandboxFixture, object client.Object) bool {
	t.Helper()
	current := object.DeepCopyObject().(client.Object)
	if err := f.kube.Get(context.Background(), client.ObjectKeyFromObject(object), current); err != nil {
		t.Fatal(err)
	}
	for _, finalizer := range current.GetFinalizers() {
		if finalizer == sandbox.BootstrapFinalizer {
			return true
		}
	}
	return false
}

func saveBootstrapRoleReceipt(t *testing.T, f *nativeSandboxFixture, receipt *sandboxBootstrapReceipt) {
	t.Helper()
	object, _, err := f.r.readSandboxBootstrap(context.Background(), f.current(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.r.saveSandboxBootstrap(context.Background(), object, receipt); err != nil {
		t.Fatal(err)
	}
}

func assertBootstrapRoleRules(t *testing.T, f *nativeSandboxFixture, role *rbacv1.Role, want []rbacv1.PolicyRule) {
	t.Helper()
	current := &rbacv1.Role{}
	if err := f.kube.Get(context.Background(), client.ObjectKeyFromObject(role), current); err != nil {
		t.Fatal(err)
	}
	if !equality.Semantic.DeepEqual(current.Rules, want) {
		t.Fatalf("bootstrap Role rules = %#v, want %#v", current.Rules, want)
	}
}

func sandboxBootstrapRoleRulesCopy(rules []rbacv1.PolicyRule) []rbacv1.PolicyRule {
	result := make([]rbacv1.PolicyRule, len(rules))
	for i := range rules {
		result[i] = *rules[i].DeepCopy()
	}
	return result
}
