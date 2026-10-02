// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package registryauth

import (
	"context"
	"reflect"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The platform terminal has namespace-scoped exec RBAC, narrowed by admission
// to this Agent's main container and fixed shell commands. SAR cannot evaluate
// that admission rule, so verify it before accepting this one permission.
func terminalExecBound(ctx context.Context, c client.Client, agent *api.Agent) bool {
	if agent.UID == "" || !agent.DeletionTimestamp.IsZero() {
		return false
	}
	namespace := "cetirun-" + agent.Name
	workload := "openclaw-" + agent.Name + "-cetirun"
	name := terminalpolicy.WorkloadName(workload)
	owned := func(obj client.Object) bool {
		owners := obj.GetOwnerReferences()
		return obj.GetDeletionTimestamp().IsZero() && len(owners) == 1 &&
			owners[0].APIVersion == api.GroupVersion.String() && owners[0].Kind == "Agent" &&
			owners[0].Name == agent.Name && owners[0].UID == agent.UID
	}
	ns := &corev1.Namespace{}
	account := &corev1.ServiceAccount{}
	deployment := &appsv1.Deployment{}
	if c.Get(ctx, client.ObjectKey{Name: namespace}, ns) != nil || ns.UID == "" || !ns.DeletionTimestamp.IsZero() ||
		len(ns.OwnerReferences) != 0 && !owned(ns) ||
		ns.Annotations["cetirun.microsoft.com/agent-uid"] != string(agent.UID) ||
		c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, account) != nil || !owned(account) ||
		c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, deployment) != nil || !owned(deployment) ||
		deployment.Spec.Template.Spec.ServiceAccountName != name {
		return false
	}
	policyName := workload + "-" + namespace + "-deny-exec"
	policy := &admissionv1.ValidatingAdmissionPolicy{}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{}
	if c.Get(ctx, client.ObjectKey{Name: policyName}, policy) != nil || !owned(policy) ||
		c.Get(ctx, client.ObjectKey{Name: policyName}, binding) != nil || !owned(binding) {
		return false
	}
	spec := policy.Spec
	match := spec.MatchConstraints
	if spec.FailurePolicy == nil || *spec.FailurePolicy != admissionv1.Fail || spec.ParamKind != nil ||
		len(spec.Variables) != 0 || match == nil || len(match.ExcludeResourceRules) != 0 ||
		!reflect.DeepEqual(match.NamespaceSelector, &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}}) ||
		match.ObjectSelector != nil && (len(match.ObjectSelector.MatchLabels) != 0 || len(match.ObjectSelector.MatchExpressions) != 0) ||
		len(match.ResourceRules) != 1 || len(spec.Validations) != 1 ||
		spec.Validations[0].Expression != terminalpolicy.AdmissionExpression(namespace, workload+"-0", "cetirun", name, string(agent.UID)) ||
		!reflect.DeepEqual(spec.MatchConditions, []admissionv1.MatchCondition{{Name: "exclude-adminclaw", Expression: `request.userInfo.username != "system:serviceaccount:cetirun-admin:openclaw-admin-cetirun"`}}) {
		return false
	}
	rule := match.ResourceRules[0]
	if len(rule.ResourceNames) != 0 || !reflect.DeepEqual(rule.APIGroups, []string{""}) || !reflect.DeepEqual(rule.APIVersions, []string{"v1"}) ||
		!reflect.DeepEqual(rule.Resources, []string{"pods/exec"}) || !reflect.DeepEqual(rule.Operations, []admissionv1.OperationType{admissionv1.Connect}) ||
		rule.Scope != nil && *rule.Scope != admissionv1.AllScopes && *rule.Scope != admissionv1.NamespacedScope {
		return false
	}
	return binding.Spec.PolicyName == policyName && binding.Spec.ParamRef == nil && binding.Spec.MatchResources == nil &&
		reflect.DeepEqual(binding.Spec.ValidationActions, []admissionv1.ValidationAction{admissionv1.Deny})
}
