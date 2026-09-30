// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package sandboxadmission

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// This harness exercises the rendered chart's create/update authorization
// protocol with real authentication, RBAC, admission ordering and persistence.
// Only the webhook transport is redirected to its loopback test server.
func TestAuthorizationAPIServerBindings(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, binary := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, binary))
		if assets == "" || err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			t.Fatal("set KUBEBUILDER_ASSETS to preinstalled Kubernetes 1.37 envtest binaries")
		}
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
		ControlPlaneStartTimeout: 45 * time.Second, ControlPlaneStopTimeout: 30 * time.Second,
	}
	env.ControlPlane.GetAPIServer().Configure().Set("authorization-mode", "RBAC")
	cfg, err := env.Start()
	if err != nil {
		_ = env.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, rbacv1.AddToScheme, admissionregistrationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	newClient := func(config *rest.Config) client.Client {
		cl, err := client.New(config, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	admin := newClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	create := func(object client.Object) {
		t.Helper()
		if err := admin.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	userClient := func(name string, groups ...string) client.Client {
		user, err := env.AddUser(envtest.User{Name: name, Groups: groups}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		role := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "authorization-test-"},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{api.GroupVersion.Group}, Resources: []string{"agents", "teams", "sandboxes", "agentpolicies"},
				Verbs: []string{"get", "list", "create", "update", "patch", "delete"},
			}},
		}
		create(role)
		create(&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: role.Name},
			Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: name}},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		})
		return newClient(user.Config())
	}
	member := userClient("assigned-member")
	groupMember := userClient("assigned-group-member", "engineering-members")
	outsider := userClient("unassigned-member")
	conflicted := userClient("conflicting-member", "engineering-members", "other-members")
	f := newFixture(t)
	for _, object := range []client.Object{f.box, f.policy, f.team} {
		object.SetUID("")
		object.SetResourceVersion("")
		create(object)
	}
	config := testConfig()
	config.AdminGroups = []string{"system:masters"}
	config.TeamAssignments = []TeamAssignment{
		{Team: f.team.Name, Users: []string{"assigned-member"}, Groups: []string{"engineering-members"}},
		{Team: "other-team", Groups: []string{"other-members"}},
	}
	handler, err := NewHandler(admin, config)
	if err != nil {
		t.Fatal(err)
	}
	handler.registrations = f.handler.registrations
	var updateMutations atomic.Int64
	mux := http.NewServeMux()
	mux.Handle(DefaultingPath, &admission.Webhook{Handler: admission.HandlerFunc(func(ctx context.Context, request admission.Request) admission.Response {
		if request.Operation == admissionv1.Update {
			updateMutations.Add(1)
		}
		return handler.DefaultAgent(ctx, request)
	})})
	mux.Handle(ValidationPath, &admission.Webhook{Handler: admission.HandlerFunc(handler.Validate)})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	validating, mutating := renderedWebhooks(t, ca)
	redirect := func(config *admissionregistrationv1.WebhookClientConfig) {
		t.Helper()
		if config.Service == nil || config.Service.Path == nil {
			t.Fatal("rendered admission webhook omitted its Service path")
		}
		config.URL = ptr.To(server.URL + *config.Service.Path)
		config.Service = nil
	}
	for i := range validating.Webhooks {
		redirect(&validating.Webhooks[i].ClientConfig)
	}
	for i := range mutating.Webhooks {
		hook := &mutating.Webhooks[i]
		if hook.ReinvocationPolicy == nil || *hook.ReinvocationPolicy != admissionregistrationv1.NeverReinvocationPolicy {
			t.Fatal("packaged authorization mutation must explicitly disable reinvocation")
		}
		redirect(&hook.ClientConfig)
	}
	create(validating)
	create(mutating)
	newAgent := func(name string) *api.Agent {
		agent := f.agent.DeepCopy()
		agent.ObjectMeta = metav1.ObjectMeta{Name: name, Finalizers: []string{api.Finalizer}}
		agent.Spec.Team, agent.Spec.SandboxRef, agent.Spec.PolicyClass = "", "", ""
		return agent
	}
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		probe := newAgent("authorization-ready")
		err := member.Create(ctx, probe, client.DryRunAll)
		return err == nil && probe.Annotations[sandboxbinding.AuthorizationAnnotation] != "", nil
	}); err != nil {
		t.Fatal("authorization admission did not become active:", err)
	}
	denied := func(err error) {
		t.Helper()
		if !apierrors.IsForbidden(err) {
			t.Fatalf("expected admission denial despite broad main-resource RBAC, got %v", err)
		}
	}
	t.Run("trusted user and group creation persists normal authorization with generated names", func(t *testing.T) {
		for _, writer := range []client.Client{member, groupMember} {
			agent := newAgent("")
			agent.GenerateName = "assigned-agent-"
			agent.Labels = map[string]string{"role": "admin", "team": "caller-team"}
			agent.Spec.User.Team = "caller-team"
			if err := writer.Create(ctx, agent); err != nil {
				t.Fatal(err)
			}
			stored, binding, err := sandboxbinding.Read(ctx, admin, client.ObjectKeyFromObject(agent))
			if err != nil || !strings.HasPrefix(agent.Name, agent.GenerateName) || binding.AgentUID != agent.UID ||
				binding.TeamUID != f.team.UID || binding.SandboxUID != f.box.UID || binding.AgentPolicyUID != f.policy.UID {
				t.Fatalf("API-assigned identity or current reference authorization was not durable: %#v, %v", binding, err)
			}
			if stored.Spec.Team != f.team.Name || stored.Spec.PolicyClass != api.AgentPolicyClassNormal ||
				stored.Spec.SandboxRef != f.box.Name || stored.Status.Sandbox != nil {
				t.Fatal("creation defaulting promoted the workload or claimed enforcement")
			}
			stored.Spec.Model = "member-application-model"
			if err := writer.Update(ctx, stored); err != nil {
				t.Fatal("assigned member lost ordinary application updates:", err)
			}
			for _, edit := range []func(*api.Agent){
				func(a *api.Agent) { a.Spec.Team = "other-team" },
				func(a *api.Agent) { a.Spec.PolicyClass = api.AgentPolicyClassAdmin },
				func(a *api.Agent) { a.Spec.SandboxRef = "other-box" },
				func(a *api.Agent) { delete(a.Annotations, sandboxbinding.AuthorizationAnnotation) },
				func(a *api.Agent) { a.Annotations[sandboxbinding.AuthorizationAnnotation] = "forged" },
				func(a *api.Agent) {
					a.Annotations[sandboxbinding.ReauthorizeAnnotation] = `{"requestID":"member-request"}`
				},
			} {
				candidate := stored.DeepCopy()
				edit(candidate)
				denied(writer.Update(ctx, candidate))
			}
			denied(outsider.Update(ctx, stored.DeepCopy()))
		}
	})
	t.Run("caller context and ambiguous memberships cannot choose authority", func(t *testing.T) {
		for name, writer := range map[string]client.Client{"outsider": outsider, "conflicted": conflicted} {
			agent := newAgent("denied-" + name)
			agent.Spec.Team = f.team.Name
			agent.Labels = map[string]string{"team": f.team.Name, "role": "admin"}
			denied(writer.Create(ctx, agent))
		}
		for name, edit := range map[string]func(*api.Agent){
			"team":  func(a *api.Agent) { a.Spec.Team = "caller-team" },
			"class": func(a *api.Agent) { a.Spec.PolicyClass = api.AgentPolicyClassAdmin },
			"record": func(a *api.Agent) {
				a.Annotations = map[string]string{sandboxbinding.AuthorizationAnnotation: fixtureAuthorization(t, f)}
			},
			"request": func(a *api.Agent) { a.Annotations = map[string]string{sandboxbinding.ReauthorizeAnnotation: ""} },
		} {
			agent := newAgent("forged-" + name)
			edit(agent)
			denied(member.Create(ctx, agent))
		}
		legacy := newAgent("unassigned-legacy")
		if err := outsider.Create(ctx, legacy); err != nil {
			t.Fatal("unmapped legacy behavior changed:", err)
		}
		if legacy.Spec.Team != "" || legacy.Spec.SandboxRef != "" || sandboxbinding.Present(legacy) {
			t.Fatal("unmapped legacy creation acquired Sandbox authority")
		}
	})
	t.Run("creation dry run persists nothing and cannot be replayed", func(t *testing.T) {
		agent := newAgent("dry-run-authorization")
		if err := member.Create(ctx, agent, client.DryRunAll); err != nil {
			t.Fatal(err)
		}
		if _, _, err := sandboxbinding.Read(ctx, admin, client.ObjectKeyFromObject(agent)); !apierrors.IsNotFound(err) {
			t.Fatalf("dry-run authorization became durable: %v", err)
		}
		agent.UID, agent.ResourceVersion = "", ""
		denied(member.Create(ctx, agent))
		if err := member.Create(ctx, newAgent(agent.Name)); err != nil {
			t.Fatal("fresh create after dry run failed:", err)
		}
	})
	t.Run("same-name policy replacement before first reconcile requires exact admin reauthorization", func(t *testing.T) {
		policy, alternate := f.policy.DeepCopy(), f.policy.DeepCopy()
		policy.ObjectMeta = metav1.ObjectMeta{Name: "replacement-policy"}
		alternate.ObjectMeta = metav1.ObjectMeta{Name: "alternate-policy"}
		create(policy)
		create(alternate)
		team := f.team.DeepCopy()
		team.ObjectMeta = metav1.ObjectMeta{Name: "replacement-team"}
		team.Spec.NormalAgentPolicyRef = policy.Name
		create(team)
		agent := newAgent("replacement-agent")
		agent.Spec.Team = team.Name
		create(agent)
		original, err := sandboxbinding.AuthorizedBinding(agent)
		if err != nil || agent.Status.Sandbox != nil {
			t.Fatalf("initial authorization missing before first apply: %v", err)
		}
		denied(admin.Delete(ctx, team.DeepCopy()))
		denied(admin.Delete(ctx, f.box.DeepCopy()))
		denied(admin.Delete(ctx, policy.DeepCopy()))
		// An authorized slot change permits deleting the now-unreferenced policy.
		// Re-selecting its same-name replacement must not renew Agent authority.
		team.Spec.NormalAgentPolicyRef = alternate.Name
		if err := admin.Update(ctx, team); err != nil {
			t.Fatal(err)
		}
		if err := admin.Delete(ctx, policy); err != nil {
			t.Fatal(err)
		}
		policy.ObjectMeta = metav1.ObjectMeta{Name: policy.Name}
		create(policy)
		if policy.UID == original.AgentPolicyUID {
			t.Fatal("API server did not assign a replacement policy UID")
		}
		team.Spec.NormalAgentPolicyRef = policy.Name
		if err := admin.Update(ctx, team); err != nil {
			t.Fatal(err)
		}
		candidate := agent.DeepCopy()
		candidate.Spec.Model = "unrelated-update"
		denied(admin.Update(ctx, candidate))
		unchanged, binding, err := sandboxbinding.Read(ctx, admin, client.ObjectKeyFromObject(agent))
		if err != nil || binding != original || unchanged.Status.Sandbox != nil {
			t.Fatalf("replacement silently renewed pending authorization: %v, %v", binding, err)
		}
		target := sandbox.Binding{AgentUID: agent.UID, TeamUID: team.UID, SandboxUID: f.box.UID, PolicyClass: api.AgentPolicyClassNormal, AgentPolicyUID: policy.UID}
		request, err := sandboxbinding.FormatReauthorization("replace-policy-1", target)
		if err != nil {
			t.Fatal(err)
		}
		candidate = unchanged.DeepCopy()
		candidate.Annotations[sandboxbinding.ReauthorizeAnnotation] = request
		denied(member.Update(ctx, candidate.DeepCopy()))
		stale := candidate.DeepCopy()
		stale.Annotations[sandboxbinding.ReauthorizeAnnotation] = strings.Replace(request, string(policy.UID), string(original.AgentPolicyUID), 1)
		denied(admin.Update(ctx, stale))
		wrongAgent := candidate.DeepCopy()
		wrongAgent.Annotations[sandboxbinding.ReauthorizeAnnotation] = strings.Replace(request, string(agent.UID), "another-agent-uid", 1)
		denied(admin.Update(ctx, wrongAgent))
		dryRun := candidate.DeepCopy()
		if err := admin.Update(ctx, dryRun, client.DryRunAll); err != nil {
			t.Fatal("explicit dry-run reauthorization failed:", err)
		}
		_, afterDryRun, err := sandboxbinding.Read(ctx, admin, client.ObjectKeyFromObject(agent))
		if err != nil || afterDryRun != original {
			t.Fatal("dry-run reauthorization changed durable authority")
		}
		beforeMutation := updateMutations.Load()
		if err := admin.Update(ctx, candidate); err != nil {
			t.Fatal("explicit reauthorization failed:", err)
		}
		if updateMutations.Load() != beforeMutation+1 {
			t.Fatal("metadata-only administrator reauthorization did not invoke the packaged mutating webhook exactly once")
		}
		stored, actual, err := sandboxbinding.Read(ctx, admin, client.ObjectKeyFromObject(agent))
		if err != nil || actual != target || stored.Status.Sandbox != nil || stored.Generation != agent.Generation {
			t.Fatalf("metadata-only reauthorization was not persisted independently of status/generation: %v, %v", actual, err)
		}
		replay := stored.DeepCopy()
		replay.Annotations[sandboxbinding.ReauthorizeAnnotation] += " "
		denied(admin.Update(ctx, replay))
		stored.Spec.Model = "ordinary-admin-update"
		if err := admin.Update(ctx, stored); err != nil {
			t.Fatal("reauthorized Agent could not accept an ordinary update:", err)
		}
	})
	t.Run("changed Team defaults never reselect an existing Agent", func(t *testing.T) {
		box := f.box.DeepCopy()
		box.ObjectMeta = metav1.ObjectMeta{Name: "second-default"}
		create(box)
		policy := f.policy.DeepCopy()
		policy.ObjectMeta = metav1.ObjectMeta{Name: "multiple-selection"}
		policy.Spec.SandboxSelection.AllowMultiple = true
		create(policy)
		team := f.team.DeepCopy()
		team.ObjectMeta = metav1.ObjectMeta{Name: "changing-default-team"}
		team.Spec.SandboxRefs = []string{f.box.Name, box.Name}
		team.Spec.NormalAgentPolicyRef = policy.Name
		create(team)
		agent := newAgent("persisted-default-agent")
		agent.Spec.Team = team.Name
		create(agent)
		before, err := sandboxbinding.AuthorizedBinding(agent)
		if err != nil {
			t.Fatal(err)
		}
		team.Spec.DefaultSandboxRef = box.Name
		if err := admin.Update(ctx, team); err != nil {
			t.Fatal(err)
		}
		agent.Spec.Model = "default-preserving-update"
		if err := admin.Update(ctx, agent); err != nil {
			t.Fatal(err)
		}
		after, err := sandboxbinding.AuthorizedBinding(agent)
		if err != nil || before != after || agent.Spec.SandboxRef != f.box.Name {
			t.Fatal("an update re-applied a changed Team default")
		}
		fresh := newAgent("new-default-agent")
		fresh.Spec.Team = team.Name
		create(fresh)
		if fresh.Spec.SandboxRef != box.Name {
			t.Fatal("new creation did not use the new Team default")
		}
		agent.Spec.SandboxRef = ""
		denied(admin.Update(ctx, agent))
	})
}
