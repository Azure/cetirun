// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/CetiRun/app/internal/registryauth"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type terminalRegistryReviewer struct {
	client.Client
	namespace, account string
}

func (c *terminalRegistryReviewer) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if review, ok := object.(*authorizationv1.SubjectAccessReview); ok {
		access := review.Spec.ResourceAttributes
		review.Status.Allowed = review.Spec.User == "system:serviceaccount:"+c.namespace+":"+c.account &&
			access.Namespace == c.namespace && access.Group == "" && access.Resource == "pods" &&
			access.Subresource == "exec" && (access.Verb == "get" || access.Verb == "create")
		return nil
	}
	return c.Client.Create(ctx, object, opts...)
}

func TestCommonCustomTerminalAllowsRegistryIsolation(t *testing.T) {
	for _, backend := range []string{"kata", "container"} {
		t.Run(backend, func(t *testing.T) {
			f := customTerminalFixture(t, backend)
			ctx := context.Background()
			agent := f.current(t)
			namespace, workload := instanceNamespace(agent.Name), instanceServiceName(agent.Name)
			account := terminalpolicy.WorkloadName(workload)
			role := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{Name: terminalpolicy.ExecRole},
				Rules: []rbacv1.PolicyRule{{
					APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"get", "create"},
				}},
			}
			if err := f.kube.Create(ctx, role); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: account + "-example", Namespace: namespace},
				Spec:       corev1.PodSpec{ServiceAccountName: account},
			}
			if err := f.kube.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			reviewer := &terminalRegistryReviewer{Client: f.kube, namespace: namespace, account: account}
			if err := registryauth.CheckIsolation(ctx, reviewer, registryauth.DefaultNamespace, namespace, workload, "registry-source-example"); err != nil {
				t.Fatalf("the governed common terminal prevents private Registry repair or rotation: %v", err)
			}
			policy := &admissionv1.ValidatingAdmissionPolicy{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: workload + "-" + namespace + "-deny-exec"}, policy); err != nil {
				t.Fatal(err)
			}
			policy.Spec.Validations[0].Expression = "true"
			if err := f.kube.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			if err := registryauth.CheckIsolation(ctx, reviewer, registryauth.DefaultNamespace, namespace, workload); !errors.Is(err, registryauth.ErrIsolation) {
				t.Fatalf("modified terminal admission was accepted: %v", err)
			}
			if err := f.r.ensureCommonTerminalExecPolicy(ctx, agent); err != nil {
				t.Fatal(err)
			}
			if err := registryauth.CheckIsolation(ctx, reviewer, registryauth.DefaultNamespace, namespace, workload); err != nil {
				t.Fatalf("reconciled terminal admission was not recognized: %v", err)
			}
		})
	}
}

func TestCommonCustomTerminalExecPolicyFences(t *testing.T) {
	for _, scenario := range []string{"steady", "policy-drift", "binding-drift", "policy-owner", "binding-owner", "stale-agent"} {
		t.Run(scenario, func(t *testing.T) {
			f := customTerminalFixture(t, "container")
			ctx, agent := context.Background(), f.current(t)
			key := client.ObjectKey{Name: instanceServiceName(agent.Name) + "-" + instanceNamespace(agent.Name) + "-deny-exec"}
			policy := &admissionv1.ValidatingAdmissionPolicy{}
			binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
			if err := f.kube.Get(ctx, key, policy); err != nil {
				t.Fatal(err)
			}
			if err := f.kube.Get(ctx, key, binding); err != nil {
				t.Fatal(err)
			}
			main, terminal := f.workload(t), customTerminalDeployment(t, f)
			originalPolicy, originalBinding := policy.DeepCopy(), binding.DeepCopy()
			switch scenario {
			case "policy-drift":
				policy.Spec.FailurePolicy = ptr.To(admissionv1.Ignore)
				if err := f.kube.Update(ctx, policy); err != nil {
					t.Fatal(err)
				}
			case "binding-drift":
				binding.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Warn}
				if err := f.kube.Update(ctx, binding); err != nil {
					t.Fatal(err)
				}
			case "policy-owner":
				policy.OwnerReferences[0].UID = "foreign-agent"
				if err := f.kube.Update(ctx, policy); err != nil {
					t.Fatal(err)
				}
			case "binding-owner":
				binding.OwnerReferences[0].UID = "foreign-agent"
				if err := f.kube.Update(ctx, binding); err != nil {
					t.Fatal(err)
				}
			case "stale-agent":
				agent.Generation++
			}
			err := f.r.ensureCommonTerminalExecPolicy(ctx, agent)
			rejected := scenario == "policy-owner" || scenario == "binding-owner" || scenario == "stale-agent"
			if (err != nil) != rejected {
				t.Fatalf("ownership/generation fence: %v", err)
			}
			observedPolicy := &admissionv1.ValidatingAdmissionPolicy{}
			observedBinding := &admissionv1.ValidatingAdmissionPolicyBinding{}
			if err := f.kube.Get(ctx, key, observedPolicy); err != nil {
				t.Fatal(err)
			}
			if err := f.kube.Get(ctx, key, observedBinding); err != nil {
				t.Fatal(err)
			}
			if rejected {
				if !equality.Semantic.DeepEqual(observedPolicy, policy) || !equality.Semantic.DeepEqual(observedBinding, binding) {
					t.Fatal("rejected policy operation changed an object")
				}
			} else if !equality.Semantic.DeepEqual(observedPolicy.Spec, originalPolicy.Spec) ||
				!equality.Semantic.DeepEqual(observedBinding.Spec, originalBinding.Spec) ||
				observedPolicy.UID != originalPolicy.UID || observedBinding.UID != originalBinding.UID {
				t.Fatal("terminal policy did not converge in place")
			}
			if scenario == "steady" && (observedPolicy.ResourceVersion != policy.ResourceVersion ||
				observedBinding.ResourceVersion != binding.ResourceVersion) {
				t.Fatal("steady policy reconciliation wrote unchanged objects")
			}
			if !equality.Semantic.DeepEqual(main, f.workload(t)) || !equality.Semantic.DeepEqual(terminal, customTerminalDeployment(t, f)) {
				t.Fatal("exec policy reconciliation replaced or changed a workload")
			}
		})
	}
}

type terminalPolicyFailClient struct {
	client.Client
	failure error
}

func (c *terminalPolicyFailClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if _, ok := object.(*admissionv1.ValidatingAdmissionPolicyBinding); ok {
		return c.failure
	}
	return c.Client.Create(ctx, object, opts...)
}

func TestCommonCustomTerminalExecPolicySurfacesWriteFailure(t *testing.T) {
	f := customTerminalFixture(t, "container")
	ctx, agent := context.Background(), f.current(t)
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	key := client.ObjectKey{Name: instanceServiceName(agent.Name) + "-" + instanceNamespace(agent.Name) + "-deny-exec"}
	if err := f.kube.Get(ctx, key, binding); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Delete(ctx, binding); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic policy binding write failure")
	f.r.Client = &terminalPolicyFailClient{Client: f.kube, failure: failure}
	if err := f.r.ensureCommonTerminalExecPolicy(ctx, agent); !errors.Is(err, failure) {
		t.Fatalf("binding write failure was hidden: %v", err)
	}
}

func TestCommonCustomTerminalRestoresExecGateWithoutRollout(t *testing.T) {
	for _, backend := range []string{"kata", "container"} {
		t.Run(backend, func(t *testing.T) {
			f := customTerminalFixture(t, backend)
			ctx := context.Background()
			readyCustomWorkload(t, f)
			terminal := customTerminalDeployment(t, f)
			terminal.Status.ObservedGeneration = terminal.Generation
			terminal.Status.Replicas, terminal.Status.ReadyReplicas = 1, 1
			terminal.Status.UpdatedReplicas, terminal.Status.AvailableReplicas = 1, 1
			if err := f.kube.Status().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				f.reconcileOK(t)
			}
			agent := f.current(t)
			main, terminal := f.workload(t), customTerminalDeployment(t, f)
			stages := f.installer.stages
			policy := &admissionv1.ValidatingAdmissionPolicy{}
			key := client.ObjectKey{Name: instanceServiceName(agent.Name) + "-" + instanceNamespace(agent.Name) + "-deny-exec"}
			if err := f.kube.Get(ctx, key, policy); err != nil {
				t.Fatal(err)
			}
			if err := f.kube.Delete(ctx, policy); err != nil {
				t.Fatal(err)
			}
			f.reconcileOK(t)
			if err := f.kube.Get(ctx, key, &admissionv1.ValidatingAdmissionPolicy{}); err != nil {
				t.Fatal("steady reconciliation did not restore the exact terminal gate:", err)
			}
			if f.installer.stages != stages || !equality.Semantic.DeepEqual(main, f.workload(t)) ||
				!equality.Semantic.DeepEqual(terminal, customTerminalDeployment(t, f)) {
				t.Fatal("adding the missing exec gate rolled a retained workload")
			}
		})
	}
}
