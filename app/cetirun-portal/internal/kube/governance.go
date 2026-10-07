// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

const sandboxReauthorizeAnnotation = "cetirun.microsoft.com/sandbox-reauthorize"

type InstanceGovernanceUpdate struct {
	ExpectedUID             string
	ExpectedResourceVersion string
	Team                    *string
	PolicyClass             *cetirunv1alpha1.AgentPolicyClass
	SandboxRef              *string
	GatewayBudgetUSD        *string
	Suspended               *bool
	TeamAuthorization       *cetirunv1alpha1.TeamAuthorization
}

func (c *Client) ListAgentPolicies(ctx context.Context) ([]cetirunv1alpha1.AgentPolicy, error) {
	list := &cetirunv1alpha1.AgentPolicyList{}
	if err := c.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *Client) GetAgentPolicy(ctx context.Context, name string) (*cetirunv1alpha1.AgentPolicy, error) {
	item := &cetirunv1alpha1.AgentPolicy{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: name}, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (c *Client) CreateAgentPolicy(ctx context.Context, item *cetirunv1alpha1.AgentPolicy) (*cetirunv1alpha1.AgentPolicy, error) {
	if err := c.client.Create(ctx, item, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}

func (c *Client) UpdateAgentPolicy(ctx context.Context, name string, spec cetirunv1alpha1.AgentPolicySpec) (*cetirunv1alpha1.AgentPolicy, error) {
	item, err := c.GetAgentPolicy(ctx, name)
	if err != nil {
		return nil, err
	}
	before := item.DeepCopy()
	item.Spec = spec
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}

func (c *Client) DeleteAgentPolicy(ctx context.Context, name string) error {
	return c.client.Delete(ctx, &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}})
}

func (c *Client) ListSandboxes(ctx context.Context) ([]cetirunv1alpha1.Sandbox, error) {
	list := &cetirunv1alpha1.SandboxList{}
	if err := c.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}
func (c *Client) GetSandbox(ctx context.Context, name string) (*cetirunv1alpha1.Sandbox, error) {
	item := &cetirunv1alpha1.Sandbox{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: name}, item); err != nil {
		return nil, err
	}
	return item, nil
}
func (c *Client) CreateSandbox(ctx context.Context, item *cetirunv1alpha1.Sandbox) (*cetirunv1alpha1.Sandbox, error) {
	if err := c.client.Create(ctx, item, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}
func (c *Client) UpdateSandbox(ctx context.Context, name string, spec cetirunv1alpha1.SandboxSpec) (*cetirunv1alpha1.Sandbox, error) {
	item, err := c.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	before := item.DeepCopy()
	item.Spec = spec
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}
func (c *Client) DeleteSandbox(ctx context.Context, name string) error {
	return c.client.Delete(ctx, &cetirunv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: name}})
}

func (c *Client) ListGovernanceActions(ctx context.Context) ([]cetirunv1alpha1.GovernanceAction, error) {
	list := &cetirunv1alpha1.GovernanceActionList{}
	if err := c.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}
func (c *Client) GetGovernanceAction(ctx context.Context, name string) (*cetirunv1alpha1.GovernanceAction, error) {
	item := &cetirunv1alpha1.GovernanceAction{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: name}, item); err != nil {
		return nil, err
	}
	return item, nil
}
func (c *Client) CreateGovernanceAction(ctx context.Context, item *cetirunv1alpha1.GovernanceAction) (*cetirunv1alpha1.GovernanceAction, error) {
	if err := c.client.Create(ctx, item, client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}
func (c *Client) ApproveGovernanceAction(ctx context.Context, name string, approval cetirunv1alpha1.GovernanceActionApproval) (*cetirunv1alpha1.GovernanceAction, error) {
	item, err := c.GetGovernanceAction(ctx, name)
	if err != nil {
		return nil, err
	}
	before := item.DeepCopy()
	item.Spec.Approval = &approval
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}

func (c *Client) UpdateInstanceGovernance(ctx context.Context, name string, update InstanceGovernanceUpdate) (*cetirunv1alpha1.Agent, error) {
	item, err := c.GetInstanceByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if string(item.UID) != update.ExpectedUID || item.ResourceVersion != update.ExpectedResourceVersion {
		return nil, ErrInstanceChanged
	}
	before := item.DeepCopy()
	if update.Team != nil {
		item.Spec.Team = *update.Team
	}
	if update.PolicyClass != nil {
		item.Spec.PolicyClass = *update.PolicyClass
	}
	if update.SandboxRef != nil {
		item.Spec.SandboxRef = *update.SandboxRef
	}
	if update.GatewayBudgetUSD != nil {
		item.Spec.Governance.GatewayBudgetUSD = *update.GatewayBudgetUSD
	}
	if update.TeamAuthorization != nil {
		item.Spec.Governance.TeamAuthorization = *update.TeamAuthorization.DeepCopy()
	}
	if update.Suspended != nil {
		item.Spec.Suspended = *update.Suspended
	}
	selectionChanged := before.Spec.Team != item.Spec.Team || before.Spec.PolicyClass != item.Spec.PolicyClass || before.Spec.SandboxRef != item.Spec.SandboxRef
	governanceChanged := before.Spec.Governance != item.Spec.Governance
	if (selectionChanged || governanceChanged) && (!before.Spec.Suspended || !item.Spec.Suspended) {
		return nil, ErrGovernanceRequiresSuspension
	}
	if selectionChanged {
		if err := c.setSandboxReauthorization(ctx, item); err != nil {
			return nil, err
		}
	}
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}

// setSandboxReauthorization creates the exact, fresh UID-bound intent consumed
// by the mutating admission webhook.  Portal never authors the protected
// authorization snapshot itself.
func (c *Client) setSandboxReauthorization(ctx context.Context, item *cetirunv1alpha1.Agent) error {
	team := &cetirunv1alpha1.Team{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: item.Spec.Team}, team); err != nil {
		return fmt.Errorf("read target Team: %w", err)
	}
	sandbox := &cetirunv1alpha1.Sandbox{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: item.Spec.SandboxRef}, sandbox); err != nil {
		return fmt.Errorf("read target Sandbox: %w", err)
	}
	policyName := team.Spec.NormalAgentPolicyRef
	if item.Spec.PolicyClass == cetirunv1alpha1.AgentPolicyClassAdmin {
		policyName = team.Spec.AdminAgentPolicyRef
	}
	policy := &cetirunv1alpha1.AgentPolicy{}
	if policyName == "" {
		return fmt.Errorf("target Team has no policy for policyClass %q", item.Spec.PolicyClass)
	}
	if err := c.client.Get(ctx, client.ObjectKey{Name: policyName}, policy); err != nil {
		return fmt.Errorf("read target AgentPolicy: %w", err)
	}
	random := make([]byte, 16)
	if _, err := cryptorand.Read(random); err != nil {
		return fmt.Errorf("create reauthorization request ID: %w", err)
	}
	intent := struct {
		RequestID string `json:"requestID"`
		Binding   struct {
			AgentUID       string                           `json:"agentUID"`
			TeamUID        string                           `json:"teamUID"`
			SandboxUID     string                           `json:"sandboxUID"`
			PolicyClass    cetirunv1alpha1.AgentPolicyClass `json:"policyClass"`
			AgentPolicyUID string                           `json:"agentPolicyUID"`
		} `json:"binding"`
	}{RequestID: hex.EncodeToString(random)}
	intent.Binding.AgentUID = string(item.UID)
	intent.Binding.TeamUID = string(team.UID)
	intent.Binding.SandboxUID = string(sandbox.UID)
	intent.Binding.PolicyClass = item.Spec.PolicyClass
	intent.Binding.AgentPolicyUID = string(policy.UID)
	raw, err := json.Marshal(intent)
	if err != nil {
		return fmt.Errorf("encode Sandbox reauthorization: %w", err)
	}
	if item.Annotations == nil {
		item.Annotations = map[string]string{}
	}
	item.Annotations[sandboxReauthorizeAnnotation] = string(raw)
	return nil
}

// ReviewServiceAccountToken validates the projected workload JWT directly
// through the Kubernetes API and constrains it to the dedicated audience.
func (c *Client) ReviewServiceAccountToken(ctx context.Context, token, audience string) (string, bool, error) {
	review := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token, Audiences: []string{audience}}}
	if err := c.client.Create(ctx, review); err != nil {
		return "", false, err
	}
	return review.Status.User.Username, review.Status.Authenticated && slices.Contains(review.Status.Audiences, audience), nil
}
