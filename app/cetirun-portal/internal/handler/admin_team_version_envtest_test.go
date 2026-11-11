// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/configurationcoordination"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestTeamGenerationThroughPortalAndServedAPI(t *testing.T) {
	if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
		t.Fatal("Team version regression refuses an existing cluster")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, name := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, name))
		if assets == "" || err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			t.Fatal("set KUBEBUILDER_ASSETS to preinstalled isolated test binaries")
		}
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../../cetirun-operator/config/crd/bases"}, ErrorIfCRDPathMissing: true,
		ControlPlaneStartTimeout: time.Minute, ControlPlaneStopTimeout: 30 * time.Second,
	}
	config, err := environment.Start()
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kc, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, object := range []client.Object{
		&api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "standard-policy"}, Spec: api.AgentPolicySpec{Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeHermes}}}},
		&api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "standard"}, Spec: adminSandboxFixture()},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "version-test"}},
	} {
		if err := kc.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	authority := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "version-test", Name: "coordination", Finalizers: []string{"sandbox.cetirun.microsoft.com/coordination"}}, Data: map[string]string{"coordination.json": `{"version":1,"executions":[],"changes":[]}`}}
	if err := kc.Create(ctx, authority); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"direct", "coordinated"} {
		t.Run(mode, func(t *testing.T) {
			portal := kube.NewClient(kc)
			if mode == "coordinated" {
				coordinator, err := configurationcoordination.New(kc, kc, configurationcoordination.Authority{Namespace: authority.Namespace, Name: authority.Name, UID: authority.UID})
				if err != nil {
					t.Fatal(err)
				}
				portal.SetConfigurationCoordination(coordinator)
			}
			h := &AdminTeamHandler{Kube: portal}
			name := "team-" + mode
			team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: api.TeamSpec{AgentPolicyRef: "standard-policy", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard", MemberContext: api.TeamContextSpec{Role: "Engineer"}}}
			if err := kc.Create(ctx, team); err != nil {
				t.Fatal(err)
			}
			read := func() TeamDTO {
				t.Helper()
				w := httptest.NewRecorder()
				h.GetTeam(w, teamVersionRequest(t, http.MethodGet, name, nil).WithContext(ctx))
				if w.Code != http.StatusOK {
					t.Fatalf("GET: %d %s", w.Code, w.Body.String())
				}
				var dto TeamDTO
				if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
					t.Fatal(err)
				}
				return dto
			}
			writes := 0
			refreshStatus := func() {
				t.Helper()
				current := &api.Team{}
				if err := kc.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
					t.Fatal(err)
				}
				writes++
				current.Status.Conditions = []metav1.Condition{{Type: "VersionTest", Status: metav1.ConditionTrue, Reason: "Checked", Message: fmt.Sprintf("Check %d", writes), LastTransitionTime: metav1.Now(), ObservedGeneration: current.Generation}}
				if err := kc.Status().Update(ctx, current); err != nil {
					t.Fatal(err)
				}
			}
			update := func(dto TeamDTO, role string, legacy bool, want int) {
				t.Helper()
				body := TeamUpsertRequest{UID: dto.UID, ResourceVersion: dto.ResourceVersion, AgentPolicyRef: dto.AgentPolicyRef, SandboxRefs: dto.SandboxRefs, DefaultSandboxRef: dto.DefaultSandboxRef, Role: role}
				request := teamVersionRequest(t, http.MethodPut, name, body).WithContext(ctx)
				if !legacy {
					request.Header.Set(teamGenerationHeader, fmt.Sprint(dto.Generation))
				}
				w := httptest.NewRecorder()
				h.UpdateTeam(w, request)
				if w.Code != want {
					t.Fatalf("PUT: got %d want %d: %s", w.Code, want, w.Body.String())
				}
			}
			remove := func(dto TeamDTO, want int) {
				t.Helper()
				w := httptest.NewRecorder()
				request := teamVersionRequest(t, http.MethodDelete, name, map[string]any{"uid": dto.UID, "resourceVersion": dto.ResourceVersion}).WithContext(ctx)
				request.Header.Set(teamGenerationHeader, fmt.Sprint(dto.Generation))
				h.DeleteTeam(w, request)
				if w.Code != want {
					t.Fatalf("DELETE: got %d want %d: %s", w.Code, want, w.Body.String())
				}
			}
			original := read()
			refreshStatus()
			refreshed := read()
			if refreshed.Generation != original.Generation || refreshed.ResourceVersion == original.ResourceVersion {
				t.Fatal("status did not change only the resource version")
			}
			update(original, original.Role, true, http.StatusConflict)
			update(original, original.Role, false, http.StatusOK)
			if read().ResourceVersion != refreshed.ResourceVersion {
				t.Fatal("unchanged save wrote the Team")
			}
			update(original, "Reviewer", false, http.StatusOK)
			updated := read()
			if updated.Generation != original.Generation+1 || updated.Role != "Reviewer" {
				t.Fatal("real API did not advance configuration generation")
			}
			update(original, "Reviewer", false, http.StatusConflict)
			remove(original, http.StatusConflict)
			current := &api.Team{}
			if err := kc.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
				t.Fatal(err)
			}
			current.Labels = map[string]string{"example.com/keep": "yes"}
			if err := kc.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			refreshStatus()
			update(updated, "Maintainer", false, http.StatusOK)
			if err := kc.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
				t.Fatal(err)
			}
			if current.Labels["example.com/keep"] != "yes" || len(current.Status.Conditions) != 1 {
				t.Fatal("update dropped current metadata or status")
			}
			beforeDelete := read()
			refreshStatus()
			remove(beforeDelete, http.StatusNoContent)
			replacement := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: team.Spec}
			if err := kc.Create(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			if replacement.Generation != original.Generation {
				t.Fatal("replacement fixture must share the original generation")
			}
			update(original, "Reviewer", false, http.StatusConflict)
			remove(original, http.StatusConflict)
			remove(read(), http.StatusNoContent)
		})
	}
}
