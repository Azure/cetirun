// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package registryauth

import (
	"context"
	"errors"
	"slices"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrIsolation = errors.New("registry credential isolation could not be verified")

// CheckIsolation evaluates effective authorization, including cluster-wide
// grants and service-account groups. A permissive or unavailable authorizer is
// not treated as evidence of isolation. Platform administrators remain trusted.
func CheckIsolation(ctx context.Context, c client.Client, sourceNS, targetNS, desiredSA string, secretNames ...string) error {
	if len(validation.IsDNS1123Label(sourceNS)) != 0 || sourceNS == targetNS || targetNS == "" ||
		sourceNS == "cetirun-credentials" || sourceNS == "cetirun-system" || sourceNS == "default" || strings.HasPrefix(sourceNS, "kube-") {
		return ErrIsolation
	}
	sourcePods := &corev1.PodList{}
	if c.List(ctx, sourcePods, client.InNamespace(sourceNS)) != nil || len(sourcePods.Items) != 0 {
		return ErrIsolation
	}
	type subject struct{ namespace, name string }
	subjects := map[subject]bool{}
	runtimeSubjects := map[subject]bool{}
	terminalAgents := map[subject]*api.Agent{}
	add := func(ns, name string) {
		if name == "" {
			name = "default"
		}
		subjects[subject{ns, name}] = true
	}
	add(targetNS, desiredSA)
	runtimeSubjects[subject{targetNS, desiredSA}] = true
	agents := &api.AgentList{}
	if c.List(ctx, agents) != nil {
		return ErrIsolation
	}
	for _, agent := range agents.Items {
		if agent.Spec.Agent.RegistrySecretRef != nil {
			secretNames = append(secretNames, agent.Spec.Agent.RegistrySecretRef.Name)
		}
		if name, err := PullSecretName(agent.UID); err == nil {
			secretNames = append(secretNames, name)
		}
		ns := "cetirun-" + agent.Name
		add(ns, "default")
		add(ns, "openclaw-"+agent.Name+"-cetirun")
		runtimeSubjects[subject{ns, "default"}] = true
		runtimeSubjects[subject{ns, "openclaw-" + agent.Name + "-cetirun"}] = true
		if agent.Spec.Agent.Type == api.AgentTypeCustom {
			terminalAgents[subject{ns, terminalpolicy.WorkloadName("openclaw-" + agent.Name + "-cetirun")}] = &agent
		}
		pods := &corev1.PodList{}
		workloads := &appsv1.StatefulSetList{}
		if c.List(ctx, pods, client.InNamespace(ns)) != nil || c.List(ctx, workloads, client.InNamespace(ns)) != nil {
			return ErrIsolation
		}
		for _, pod := range pods.Items {
			add(ns, pod.Spec.ServiceAccountName)
		}
		for _, sts := range workloads.Items {
			add(ns, sts.Spec.Template.Spec.ServiceAccountName)
			runtimeSubjects[subject{ns, sts.Spec.Template.Spec.ServiceAccountName}] = true
		}
	}
	inventory, err := readRegistryRBAC(ctx, c)
	if err != nil {
		return ErrIsolation
	}
	slices.Sort(secretNames)
	secretNames = slices.Compact(secretNames)
	for subject := range subjects {
		terminalVerified := !runtimeSubjects[subject] && terminalAgents[subject] != nil && terminalExecBound(ctx, c, terminalAgents[subject])
		// Named grants (for example patching one StatefulSet, or minting a
		// token for one privileged SA in another namespace) are invisible to
		// unnamed reviews. Include the exact grant paths, then let the API
		// authorizer decide; metadata is never treated as authorization.
		extra, err := inventory.accessChecks(subject.namespace, subject.name)
		if err != nil {
			return ErrIsolation
		}
		unique := map[authorizationv1.ResourceAttributes]struct{}{}
		for _, namespace := range []string{sourceNS, targetNS} {
			checks := registryAccessChecks(namespace)
			if namespace == sourceNS {
				checks = append(checks, extra...)
			}
			for _, name := range secretNames {
				checks = append(checks, authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "secrets", Verb: "get", Name: name})
			}
			for _, access := range checks {
				unique[access] = struct{}{}
			}
		}
		group, reviewCtx := errgroup.WithContext(ctx)
		group.SetLimit(8)
		for access := range unique {
			if reviewCtx.Err() != nil {
				break
			}
			group.Go(func() error {
				review := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
					User:               "system:serviceaccount:" + subject.namespace + ":" + subject.name,
					Groups:             []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + subject.namespace},
					ResourceAttributes: &access,
				}}
				if c.Create(reviewCtx, review) != nil || review.Status.EvaluationError != "" {
					return ErrIsolation
				}
				terminalExec := terminalVerified && access.Group == "" && access.Resource == "pods" &&
					access.Subresource == "exec" && (access.Verb == "get" || access.Verb == "create") && access.Namespace == subject.namespace
				if review.Status.Allowed && !terminalExec {
					return ErrIsolation
				}
				return nil
			})
		}
		if group.Wait() != nil || ctx.Err() != nil {
			return ErrIsolation
		}
	}
	return nil
}

type registryRBAC struct {
	rules           map[string][]rbacv1.PolicyRule
	bindings        []rbacv1.RoleBinding
	clusterBindings []rbacv1.ClusterRoleBinding
}

func readRegistryRBAC(ctx context.Context, c client.Client) (*registryRBAC, error) {
	roles, clusters := &rbacv1.RoleList{}, &rbacv1.ClusterRoleList{}
	bindings, clusterBindings := &rbacv1.RoleBindingList{}, &rbacv1.ClusterRoleBindingList{}
	if c.List(ctx, roles) != nil || c.List(ctx, clusters) != nil || c.List(ctx, bindings) != nil || c.List(ctx, clusterBindings) != nil {
		return nil, ErrIsolation
	}
	roleRules := map[string][]rbacv1.PolicyRule{}
	for _, role := range roles.Items {
		roleRules[role.Namespace+"/"+role.Name] = role.Rules
	}
	for _, role := range clusters.Items {
		roleRules["/"+role.Name] = role.Rules
	}
	return &registryRBAC{rules: roleRules, bindings: bindings.Items, clusterBindings: clusterBindings.Items}, nil
}

func (inventory *registryRBAC) accessChecks(namespace, name string) ([]authorizationv1.ResourceAttributes, error) {
	matches := func(subjects []rbacv1.Subject, bindingNS string) bool {
		for _, s := range subjects {
			switch s.Kind {
			case "ServiceAccount":
				ns := s.Namespace
				if ns == "" {
					ns = bindingNS
				}
				if ns == namespace && s.Name == name {
					return true
				}
			case "User":
				if s.Name == "system:serviceaccount:"+namespace+":"+name {
					return true
				}
			case "Group":
				if slices.Contains([]string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + namespace}, s.Name) {
					return true
				}
			}
		}
		return false
	}
	checks := []authorizationv1.ResourceAttributes{}
	add := func(namespace string, ref rbacv1.RoleRef) error {
		key := namespace + "/" + ref.Name
		if ref.Kind == "ClusterRole" {
			key = "/" + ref.Name
		}
		rules, found := inventory.rules[key]
		if !found {
			return ErrIsolation
		}
		for _, rule := range rules {
			for _, access := range registryAccessChecks(namespace) {
				resource := access.Resource
				if access.Subresource != "" {
					resource += "/" + access.Subresource
				}
				contains := func(items []string, item string) bool {
					return slices.Contains(items, "*") || slices.Contains(items, item)
				}
				resourceMatch := contains(rule.Resources, resource) || (access.Subresource != "" && (slices.Contains(rule.Resources, "*/"+access.Subresource) || slices.Contains(rule.Resources, access.Resource+"/*")))
				if !contains(rule.APIGroups, access.Group) || !resourceMatch || !contains(rule.Verbs, access.Verb) {
					continue
				}
				if len(rule.ResourceNames) == 0 {
					checks = append(checks, access)
				} else {
					for _, n := range rule.ResourceNames {
						named := access
						named.Name = n
						checks = append(checks, named)
					}
				}
			}
		}
		return nil
	}
	for _, b := range inventory.bindings {
		if matches(b.Subjects, b.Namespace) {
			if err := add(b.Namespace, b.RoleRef); err != nil {
				return nil, err
			}
		}
	}
	for _, b := range inventory.clusterBindings {
		if matches(b.Subjects, "") {
			if err := add("", b.RoleRef); err != nil {
				return nil, err
			}
		}
	}
	return checks, nil
}

func registryAccessChecks(namespace string) []authorizationv1.ResourceAttributes {
	checks := []authorizationv1.ResourceAttributes{}
	for _, resource := range []struct{ group, resource, subresource string }{
		{"", "secrets", ""}, {"", "pods", ""}, {"", "pods", "ephemeralcontainers"},
		{"", "pods", "exec"}, {"", "serviceaccounts", "token"},
		{"", "replicationcontrollers", ""}, {"apps", "deployments", ""}, {"apps", "statefulsets", ""},
		{"apps", "daemonsets", ""}, {"apps", "replicasets", ""}, {"batch", "jobs", ""}, {"batch", "cronjobs", ""},
		{"rbac.authorization.k8s.io", "roles", ""}, {"rbac.authorization.k8s.io", "rolebindings", ""},
	} {
		verbs := []string{"create", "update", "patch"}
		if resource.resource == "secrets" {
			verbs = []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}
		} else if resource.subresource == "exec" {
			verbs = []string{"get", "create", "update", "patch"}
		}
		for _, verb := range verbs {
			checks = append(checks, authorizationv1.ResourceAttributes{Namespace: namespace,
				Group: resource.group, Resource: resource.resource, Subresource: resource.subresource, Verb: verb})
		}
	}
	for _, resource := range []string{"users", "groups", "serviceaccounts"} {
		checks = append(checks, authorizationv1.ResourceAttributes{Resource: resource, Verb: "impersonate"})
	}
	for _, resource := range []string{"clusterroles", "clusterrolebindings"} {
		verbs := []string{"create", "update", "patch"}
		if resource == "clusterroles" {
			verbs = slices.Concat(verbs, []string{"bind", "escalate"})
		}
		for _, verb := range verbs {
			checks = append(checks, authorizationv1.ResourceAttributes{Group: "rbac.authorization.k8s.io", Resource: resource, Verb: verb})
		}
	}
	for _, resource := range []string{"validatingadmissionpolicies", "validatingadmissionpolicybindings"} {
		for _, verb := range []string{"create", "update", "patch", "delete", "deletecollection"} {
			checks = append(checks, authorizationv1.ResourceAttributes{Group: "admissionregistration.k8s.io", Resource: resource, Verb: verb})
		}
	}
	return checks
}
