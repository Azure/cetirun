// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func compileIdentity(input CompileInput) (IdentityPlan, []ApprovedGrant, *ApprovedIdentity, error) {
	plan := IdentityPlan{ServiceAccountName: "default"}
	policy, platform := input.Resolution.AgentPolicy, input.Platform
	grants, err := approvedGrants(input.Resolution.Binding.AgentPolicyUID, policy.KubernetesAccess, platform.Grants)
	if err != nil {
		return plan, nil, nil, err
	}
	if policy.KubernetesAccess == nil && !policy.EntraID.Enabled {
		return plan, grants, nil, nil
	}
	var approval *ApprovedIdentity
	for i := range platform.Identities {
		candidate := platform.Identities[i]
		if candidate.AgentUID != input.Resolution.Binding.AgentUID {
			continue
		}
		if approval != nil {
			return plan, nil, nil, fmt.Errorf("%w: ambiguous per-Agent identity registration", ErrUnapprovedGrant)
		}
		approval = &candidate
	}
	if approval == nil || approval.Namespace != input.Namespace || approval.Revision == "" ||
		approval.ServiceAccountName == "default" ||
		len(validation.IsDNS1123Subdomain(approval.ServiceAccountName)) != 0 {
		return plan, nil, nil, fmt.Errorf("%w: a dedicated per-Agent ServiceAccount registration is required", ErrUnapprovedGrant)
	}
	if policy.KubernetesAccess != nil && (!approval.Kubernetes ||
		policy.KubernetesAccess.ServiceAccountName != approval.ServiceAccountName) {
		return plan, nil, nil, fmt.Errorf("%w: the requested Kubernetes ServiceAccount is not approved", ErrUnapprovedGrant)
	}
	if policy.EntraID.Enabled && (approval.Entra == nil || approval.Entra.ClientID == "" || approval.Entra.TenantID == "") {
		return plan, nil, nil, fmt.Errorf("%w: per-Agent Entra identity is unavailable", ErrUnapprovedGrant)
	}
	expiration, err := tokenSettings(platform)
	if err != nil {
		return plan, nil, nil, err
	}
	plan.ServiceAccountName = approval.ServiceAccountName
	automount := false
	plan.ServiceAccount = &corev1.ServiceAccount{
		TypeMeta:                     metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta:                   ownedMetadata(input, plan.ServiceAccountName, input.Namespace),
		AutomountServiceAccountToken: &automount,
	}
	if policy.KubernetesAccess != nil {
		mode := int32(0440)
		plan.Volumes = append(plan.Volumes, corev1.Volume{
			Name: "sandbox-kubernetes-api",
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: &mode,
				Sources: []corev1.VolumeProjection{
					{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Audience: platform.KubernetesAudience, ExpirationSeconds: &expiration, Path: "token",
					}},
					{ConfigMap: &corev1.ConfigMapProjection{
						LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
						Items:                []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
					}},
					{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{
						Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"},
					}}}},
				},
			}},
		})
		plan.VolumeMounts = append(plan.VolumeMounts, corev1.VolumeMount{
			Name: "sandbox-kubernetes-api", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true,
		})
		subject := rbacv1.Subject{Kind: "ServiceAccount", Name: plan.ServiceAccountName, Namespace: input.Namespace}
		for _, grant := range grants {
			grantRevision, _ := digest(grant)
			hash, _ := digest(struct {
				AgentUID  types.UID
				Namespace string
				RoleRef   rbacv1.RoleRef
			}{input.Resolution.Binding.AgentUID, grant.Namespace, grant.RoleRef})
			name := "sandbox-" + hash[:32]
			ref := BindingReference{
				Name: name, Namespace: grant.Namespace, AgentName: input.AgentName,
				AgentUID: input.Resolution.Binding.AgentUID, RoleRef: grant.RoleRef,
				ServiceAccountName: plan.ServiceAccountName, ServiceAccountNamespace: input.Namespace,
				GrantRevision: grantRevision,
			}
			metadata := ownedMetadata(input, name, grant.Namespace)
			metadata.Annotations[GrantRevisionAnnotation] = grantRevision
			if grant.Namespace == "" {
				ref.Kind = "ClusterRoleBinding"
				plan.ClusterRoleBindings = append(plan.ClusterRoleBindings, rbacv1.ClusterRoleBinding{
					TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: ref.Kind},
					ObjectMeta: metadata, RoleRef: grant.RoleRef, Subjects: []rbacv1.Subject{subject},
				})
			} else {
				ref.Kind = "RoleBinding"
				plan.RoleBindings = append(plan.RoleBindings, rbacv1.RoleBinding{
					TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: ref.Kind},
					ObjectMeta: metadata, RoleRef: grant.RoleRef, Subjects: []rbacv1.Subject{subject},
				})
			}
			plan.Bindings = append(plan.Bindings, ref)
		}
	}
	if policy.EntraID.Enabled {
		mode := int32(0440)
		plan.Volumes = append(plan.Volumes, corev1.Volume{
			Name: "sandbox-entra-exchange",
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				DefaultMode: &mode, Sources: []corev1.VolumeProjection{{
					ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Audience: EntraAudience, ExpirationSeconds: &expiration, Path: "azure-identity-token",
					},
				}},
			}},
		})
		plan.VolumeMounts = append(plan.VolumeMounts, corev1.VolumeMount{
			Name: "sandbox-entra-exchange", MountPath: "/var/run/secrets/azure/tokens", ReadOnly: true,
		})
		plan.Env = []corev1.EnvVar{
			{Name: "AZURE_CLIENT_ID", Value: approval.Entra.ClientID},
			{Name: "AZURE_TENANT_ID", Value: approval.Entra.TenantID},
			{Name: "AZURE_FEDERATED_TOKEN_FILE", Value: "/var/run/secrets/azure/tokens/azure-identity-token"},
		}
		plan.ServiceAccount.Annotations["azure.workload.identity/client-id"] = approval.Entra.ClientID
		plan.ServiceAccount.Annotations["azure.workload.identity/tenant-id"] = approval.Entra.TenantID
	}
	// Unrequested privileges are not projected or included as effective identity.
	approval.Kubernetes = policy.KubernetesAccess != nil
	if !policy.EntraID.Enabled {
		approval.Entra = nil
	}
	return plan, grants, approval, nil
}

func approvedGrants(policyUID types.UID, access *api.AgentPolicyAdminKubernetesAccess, inventory []ApprovedGrant) ([]ApprovedGrant, error) {
	if access == nil {
		return nil, nil
	}
	var requested []ApprovedGrant
	for _, grant := range access.RoleBindings {
		requested = append(requested, ApprovedGrant{Namespace: grant.Namespace, RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: grant.ClusterRoleName,
		}})
	}
	for _, grant := range access.ClusterRoleBindings {
		requested = append(requested, ApprovedGrant{RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: grant.ClusterRoleName,
		}})
	}
	if len(requested) == 0 {
		return nil, fmt.Errorf("%w: at least one exact grant is required", ErrUnapprovedGrant)
	}
	var approved []ApprovedGrant
	for _, request := range requested {
		var match *ApprovedGrant
		for _, candidate := range inventory {
			if candidate.PolicyUID != policyUID || candidate.Namespace != request.Namespace || candidate.RoleRef != request.RoleRef {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("%w: duplicate approval for a role and scope", ErrUnapprovedGrant)
			}
			match = &candidate
		}
		if match == nil || match.RoleUID == "" || match.Revision == "" || len(match.Rules) == 0 {
			return nil, fmt.Errorf("%w: explicit role contents and scope approval are required", ErrUnapprovedGrant)
		}
		copy := *match
		copy.Rules = slices.Clone(match.Rules)
		for i := range copy.Rules {
			rule := &copy.Rules[i]
			rule.APIGroups = sortedUnique(rule.APIGroups)
			rule.Resources = sortedUnique(rule.Resources)
			rule.ResourceNames = sortedUnique(rule.ResourceNames)
			rule.NonResourceURLs = sortedUnique(rule.NonResourceURLs)
			rule.Verbs = sortedUnique(rule.Verbs)
		}
		copy.Rules = canonicalObjects(copy.Rules)
		approved = append(approved, copy)
	}
	return canonicalObjects(approved), nil
}

func tokenSettings(platform Platform) (int64, error) {
	expiration := platform.TokenExpirationSeconds
	if expiration == 0 {
		expiration = 600
	}
	if expiration < 600 || expiration > 3600 {
		return 0, fmt.Errorf("%w: token lifetime must be between 600 and 3600 seconds", ErrUnapprovedGrant)
	}
	if platform.KubernetesAudience == "" || platform.KubernetesAudience == EntraAudience ||
		strings.TrimSpace(platform.KubernetesAudience) != platform.KubernetesAudience {
		return 0, fmt.Errorf("%w: a distinct approved Kubernetes API audience is required", ErrUnapprovedGrant)
	}
	return expiration, nil
}
