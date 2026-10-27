// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconciledCustomAPIServerCreatesOwnedRuntimeSecretWithoutCallerGatewayReference(t *testing.T) {
	h := newNativeStartAPIHarnessForAgent(t, true, false, corev1.ReadWriteOncePod, "openclaw")
	ctx := h.ctx
	h.r.HelmClient = &commonCustomAPILocalHelm{nativeStartLocalHelm: h.localHelm}

	policy := &api.AgentPolicy{}
	if err := h.admin.Get(ctx, client.ObjectKey{Name: "policy-example"}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.Harnesses.Allowed = append(policy.Spec.Harnesses.Allowed, api.AgentTypeCustom)
	if err := h.admin.Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := (&AgentPolicyReconciler{
		Client: h.admin, APIReader: h.admin, Source: h.r.SandboxPreparation.Configuration, NativeExecution: true,
	}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: policy.Name}}); err != nil {
		t.Fatalf("reconcile Custom policy authorization: %v", err)
	}
	if _, err := (&TeamReconciler{Client: h.admin, APIReader: h.admin}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "team-example"}}); err != nil {
		t.Fatalf("reconcile Team after Custom policy change: %v", err)
	}

	requested := testfixture.New("custom").Agent.DeepCopy()
	requested.ObjectMeta = metav1.ObjectMeta{Name: "custom-served"}
	requested.Spec.Team, requested.Spec.SandboxRef = "", ""
	requested.Spec.GatewayTokenSecretRef = ""
	requested.Spec.ModelAccess = modelAccess(true, false)
	requested.Spec.Helm.Version = h.localHelm.chart.Metadata.Version
	requested.Spec.Agent.Image = ptr.To("registry.example.com/custom-gateway:" + h.localHelm.chart.Metadata.AppVersion)
	requested.Spec.Agent.Port = ptr.To[int32](18789)
	h.r.CustomApprovedImages = []string{*requested.Spec.Agent.Image}

	var lastErr error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		probe := requested.DeepCopy()
		lastErr = h.member.Create(ctx, probe, client.DryRunAll)
		return lastErr == nil && probe.Spec.GatewayTokenSecretRef == "" && probe.Spec.Team == "team-example" && probe.Spec.SandboxRef == "sandbox-example", nil
	}); err != nil {
		t.Fatalf("served Custom Agent validation/defaulting rejected an empty gateway reference: %v; last create: %v", err, lastErr)
	}
	if err := h.member.Create(ctx, requested); err != nil {
		t.Fatalf("create Custom Agent through served API: %v", err)
	}
	if requested.Spec.GatewayTokenSecretRef != "" {
		t.Fatalf("served Custom Agent persisted caller gateway reference %q", requested.Spec.GatewayTokenSecretRef)
	}

	h.nativeSandboxFixture.agent = requested
	bootstrapped := false
	for attempt := 0; attempt < 4; attempt++ {
		_, reconcileErr := h.reconcile()
		namespace := &corev1.Namespace{}
		err := h.admin.Get(ctx, client.ObjectKey{Name: instanceNamespace(requested.Name)}, namespace)
		if err == nil {
			bootstrapped = true
			break
		}
		if reconcileErr != nil {
			t.Fatalf("bootstrap served Custom Agent: %v", reconcileErr)
		}
	}
	if !bootstrapped {
		current := h.current(t)
		t.Fatalf("served Custom bootstrap did not create its namespace: phase=%s conditions=%v", current.Status.Phase, current.Status.Conditions)
	}
	h.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Namespace: instanceNamespace(requested.Name), Name: "default",
	}})
	secret := &corev1.Secret{}
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := h.reconcile(); err != nil {
			t.Fatalf("prepare common Custom start through served API: %v", err)
		}
		current := h.current(t)
		if err := h.admin.Get(ctx, client.ObjectKey{
			Namespace: instanceNamespace(current.Name), Name: sandboxRuntimeSecretName(current),
		}, secret); err != nil {
			t.Fatalf("read common Custom runtime Secret: %v", err)
		}
		if len(secret.Data["CETIRUN_AUTH_KEY"]) != 0 {
			break
		}
	}
	current := h.current(t)
	if !hasAgentOwner(secret, current) {
		t.Fatalf("common Custom runtime Secret is not owned by its Agent: owners=%v", secret.OwnerReferences)
	}
	if len(secret.Data["CETIRUN_AUTH_KEY"]) == 0 {
		t.Fatal("common Custom runtime Secret has no controller-owned Gateway credential")
	}
}

// commonCustomAPILocalHelm adapts the envtest's real Helm storage client to the
// ordinary HelmInstaller seam. The shared native-start fixture stages retained
// one-shot workloads only, while this test deliberately exercises a common
// reconciled Custom release whose rendered manifest is allowed to run.
type commonCustomAPILocalHelm struct{ *nativeStartLocalHelm }

func (h *commonCustomAPILocalHelm) Prepare(_ string, values map[string]any, version string) (*helm.PreparedRelease, error) {
	if version != h.chart.Metadata.Version {
		return nil, errors.New("local chart version differs from the requested version")
	}
	return &helm.PreparedRelease{Values: values, ChartVersion: version}, nil
}

func (*commonCustomAPILocalHelm) Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error) {
	prepared.Values = values
	return prepared, nil
}

func (h *commonCustomAPILocalHelm) InstallOrUpgrade(namespace, name string, prepared *helm.PreparedRelease) error {
	if err := prepared.AuthorizeSandboxApply(context.Background()); err != nil {
		return err
	}
	manifest, err := prepared.SandboxManifest()
	if err != nil {
		return err
	}
	config, err := h.actionConfig(namespace, nil)
	if err != nil {
		return err
	}
	if _, err := config.Releases.Last(name); err == nil {
		return nil
	} else if !errors.Is(err, driver.ErrReleaseNotFound) {
		return err
	}
	install := action.NewInstall(config)
	install.Namespace, install.ReleaseName = namespace, name
	install.DisableHooks, install.SkipCRDs = true, true
	install.PostRenderer = nativeStartSealedManifest(manifest)
	_, err = install.RunWithContext(context.Background(), h.chart, prepared.Values)
	return err
}

func (*commonCustomAPILocalHelm) CustomApplied(string, string, *helm.PreparedRelease) (bool, error) {
	return true, nil
}

func (*commonCustomAPILocalHelm) Uninstall(string, string) error { return nil }
