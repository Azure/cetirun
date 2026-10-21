// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	toolsMCPBrokerRoleName        = "cetirun-agent-tools-broker"
	toolsMCPBrokerRoleBindingName = "cetirun-agent-tools-broker"
)

// Only absence of the namespace permits the steady-state path to fall through
// to namespace creation. A missing Role or RoleBinding is not this condition.
var errToolsMCPBrokerNamespaceMissing = errors.New("Agent namespace is missing")

// ParseToolsMCPBrokerStorageConfig validates the optional external Broker
// identity that is granted access to individual Agent namespaces. The Broker
// gets no access unless both coordinates are explicitly configured.
func ParseToolsMCPBrokerStorageConfig(namespace, serviceAccount string) (bool, error) {
	if namespace == "" && serviceAccount == "" {
		return false, nil
	}
	if namespace == "" || serviceAccount == "" {
		return false, fmt.Errorf("Tools MCP Broker namespace and service account must be configured together")
	}
	if strings.TrimSpace(namespace) != namespace || len(validation.IsDNS1123Label(namespace)) != 0 {
		return false, fmt.Errorf("Tools MCP Broker namespace is invalid")
	}
	if strings.TrimSpace(serviceAccount) != serviceAccount || len(validation.IsDNS1123Subdomain(serviceAccount)) != 0 {
		return false, fmt.Errorf("Tools MCP Broker service account is invalid")
	}
	return true, nil
}

func (r *AgentReconciler) toolsMCPBrokerStorageEnabled() (bool, error) {
	return ParseToolsMCPBrokerStorageConfig(r.ToolsMCPBrokerNamespace, r.ToolsMCPBrokerServiceAccount)
}

func toolsMCPBrokerOwner(instance *api.Agent) []metav1.OwnerReference {
	return []metav1.OwnerReference{registryauth.Owner(instance)}
}

func toolsMCPBrokerOwned(obj metav1.Object, instance *api.Agent) bool {
	owners := obj.GetOwnerReferences()
	if len(owners) != 1 {
		return false
	}
	owner := owners[0]
	return owner.APIVersion == api.GroupVersion.String() && owner.Kind == "Agent" &&
		owner.Name == instance.Name && owner.UID == instance.UID
}

func (r *AgentReconciler) toolsMCPBrokerNamespaceOwned(ctx context.Context, instance *api.Agent) error {
	namespace := &corev1.Namespace{}
	key := types.NamespacedName{Name: instanceNamespace(instance.Name)}
	if err := r.Get(ctx, key, namespace); apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %w", errToolsMCPBrokerNamespaceMissing, err)
	} else if err != nil {
		return fmt.Errorf("get Agent namespace: %w", err)
	}
	if !namespace.DeletionTimestamp.IsZero() {
		return fmt.Errorf("Agent namespace %q is terminating", namespace.Name)
	}
	return validateToolsMCPBrokerNamespaceOwner(namespace, instance)
}

func validateToolsMCPBrokerNamespaceOwner(namespace *corev1.Namespace, instance *api.Agent) error {
	if !toolsMCPBrokerOwned(namespace, instance) {
		return fmt.Errorf("Agent namespace %q must be owned by Agent %q (UID %q); automatic namespace adoption is not supported; verify ownership before manual repair", namespace.Name, instance.Name, instance.UID)
	}
	return nil
}

func (r *AgentReconciler) desiredToolsMCPBrokerRBAC(instance *api.Agent) (*rbacv1.Role, *rbacv1.RoleBinding) {
	namespace := instanceNamespace(instance.Name)
	owner := toolsMCPBrokerOwner(instance)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{
		Name:            toolsMCPBrokerRoleName,
		Namespace:       namespace,
		OwnerReferences: owner,
		Labels:          map[string]string{"app.kubernetes.io/managed-by": "cetirun-operator"},
	}, Rules: []rbacv1.PolicyRule{{
		APIGroups: []string{""}, Resources: []string{"secrets"},
		Verbs: []string{"get", "list", "create", "update", "delete"},
	}}}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{
		Name:            toolsMCPBrokerRoleBindingName,
		Namespace:       namespace,
		OwnerReferences: owner,
		Labels:          map[string]string{"app.kubernetes.io/managed-by": "cetirun-operator"},
	}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: r.ToolsMCPBrokerNamespace, Name: r.ToolsMCPBrokerServiceAccount}}}
	return role, binding
}

func (r *AgentReconciler) ensureToolsMCPBrokerRBAC(ctx context.Context, instance *api.Agent) error {
	if err := r.toolsMCPBrokerNamespaceOwned(ctx, instance); err != nil {
		return err
	}
	desiredRole, desiredBinding := r.desiredToolsMCPBrokerRBAC(instance)
	role := &rbacv1.Role{}
	roleKey := types.NamespacedName{Namespace: desiredRole.Namespace, Name: desiredRole.Name}
	if err := r.Get(ctx, roleKey, role); apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desiredRole); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create Broker Role: %w", err)
		}
		if err := r.Get(ctx, roleKey, role); err != nil {
			return fmt.Errorf("read Broker Role after create: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get Broker Role: %w", err)
	}
	if !toolsMCPBrokerOwned(role, instance) {
		return fmt.Errorf("Broker Role ownership is invalid")
	}
	if !equality.Semantic.DeepEqual(role.Rules, desiredRole.Rules) || !equality.Semantic.DeepEqual(role.Labels, desiredRole.Labels) {
		role.Rules = desiredRole.Rules
		role.Labels = desiredRole.Labels
		if err := r.Update(ctx, role); err != nil {
			return fmt.Errorf("update Broker Role: %w", err)
		}
	}
	binding := &rbacv1.RoleBinding{}
	bindingKey := types.NamespacedName{Namespace: desiredBinding.Namespace, Name: desiredBinding.Name}
	if err := r.Get(ctx, bindingKey, binding); apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desiredBinding); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create Broker RoleBinding: %w", err)
		}
		if err := r.Get(ctx, bindingKey, binding); err != nil {
			return fmt.Errorf("read Broker RoleBinding after create: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get Broker RoleBinding: %w", err)
	}
	if !toolsMCPBrokerOwned(binding, instance) {
		return fmt.Errorf("Broker RoleBinding ownership is invalid")
	}
	if !equality.Semantic.DeepEqual(binding.RoleRef, desiredBinding.RoleRef) {
		return fmt.Errorf("Broker RoleBinding role reference is invalid")
	}
	if !equality.Semantic.DeepEqual(binding.Subjects, desiredBinding.Subjects) || !equality.Semantic.DeepEqual(binding.Labels, desiredBinding.Labels) {
		binding.Subjects = desiredBinding.Subjects
		binding.Labels = desiredBinding.Labels
		if err := r.Update(ctx, binding); err != nil {
			return fmt.Errorf("update Broker RoleBinding: %w", err)
		}
	}
	return nil
}

// reconcileToolsMCPBrokerRBAC grants the Broker access to Agent-scoped records
// in a normal built-in Agent namespace. This is independent of whether that
// Agent runtime receives a Tools MCP endpoint: the Agent finalizer still relies
// on the Broker to clean existing records before the Agent-owned namespace and
// its RBAC are garbage collected. A later disable is deliberately a no-op.
func (r *AgentReconciler) reconcileToolsMCPBrokerRBAC(ctx context.Context, instance *api.Agent, namespaceEligible bool) error {
	configured, err := r.toolsMCPBrokerStorageEnabled()
	if err != nil {
		return err
	}
	if !configured || !namespaceEligible {
		return nil
	}
	return r.ensureToolsMCPBrokerRBAC(ctx, instance)
}

// ensureToolsMCPBrokerCleanupRBAC establishes cleanup access without creating
// or adopting a namespace solely for deletion. A live Agent-owned namespace can
// receive the narrowly scoped grant before the Broker is asked to remove its
// records. A namespace already terminating is read-only: existing RBAC must be
// correct, but the finalizer never races namespace garbage collection by
// creating or changing objects there. A missing namespace is an idempotent
// cleanup success because it cannot hold Agent-scoped Secrets.
func (r *AgentReconciler) ensureToolsMCPBrokerCleanupRBAC(ctx context.Context, instance *api.Agent) error {
	configured, err := r.toolsMCPBrokerStorageEnabled()
	if err != nil || !configured {
		return err
	}
	namespace := &corev1.Namespace{}
	key := types.NamespacedName{Name: instanceNamespace(instance.Name)}
	if err := r.Get(ctx, key, namespace); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("get Agent namespace for Broker cleanup: %w", err)
	}
	if err := validateToolsMCPBrokerNamespaceOwner(namespace, instance); err != nil {
		return err
	}
	if namespace.DeletionTimestamp.IsZero() {
		return r.ensureToolsMCPBrokerRBAC(ctx, instance)
	}
	return r.verifyToolsMCPBrokerRBAC(ctx, instance)
}

func (r *AgentReconciler) verifyToolsMCPBrokerRBAC(ctx context.Context, instance *api.Agent) error {
	desiredRole, desiredBinding := r.desiredToolsMCPBrokerRBAC(instance)
	role := &rbacv1.Role{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: desiredRole.Namespace, Name: desiredRole.Name}, role); err != nil {
		return fmt.Errorf("get Broker Role during namespace deletion: %w", err)
	}
	if !toolsMCPBrokerOwned(role, instance) || !equality.Semantic.DeepEqual(role.Rules, desiredRole.Rules) {
		return fmt.Errorf("Broker Role is invalid during namespace deletion")
	}
	binding := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: desiredBinding.Namespace, Name: desiredBinding.Name}, binding); err != nil {
		return fmt.Errorf("get Broker RoleBinding during namespace deletion: %w", err)
	}
	if !toolsMCPBrokerOwned(binding, instance) ||
		!equality.Semantic.DeepEqual(binding.RoleRef, desiredBinding.RoleRef) ||
		!equality.Semantic.DeepEqual(binding.Subjects, desiredBinding.Subjects) {
		return fmt.Errorf("Broker RoleBinding is invalid during namespace deletion")
	}
	return nil
}
