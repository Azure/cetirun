// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

var ErrPolicyChangeRequiresSuspension = errors.New("policy assignment and budget changes require a suspended Agent")
var ErrPolicyResourceChanged = errors.New("policy resource changed or was replaced; reload before saving")

const sandboxReauthorizeAnnotation = "cetirun.microsoft.com/sandbox-reauthorize"

// InstancePolicyUpdate contains the administrator-controlled policy fields.
// The expected identity prevents a stale Portal page from modifying a replacement Agent.
type InstancePolicyUpdate struct {
	ExpectedUID             string
	ExpectedResourceVersion string
	Team                    *string
	PolicyClass             *cetirunv1alpha1.AgentPolicyClass
	SandboxRef              *string
	MonthlyBudgetUSD        *string
}

func (c *Client) ListAgentPolicies(ctx context.Context) ([]cetirunv1alpha1.AgentPolicy, error) {
	items := &cetirunv1alpha1.AgentPolicyList{}
	if err := c.client.List(ctx, items); err != nil {
		return nil, err
	}
	return items.Items, nil
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

func (c *Client) UpdateAgentPolicy(ctx context.Context, name, expectedUID, expectedResourceVersion string, spec cetirunv1alpha1.AgentPolicySpec) (*cetirunv1alpha1.AgentPolicy, error) {
	item, err := c.GetAgentPolicy(ctx, name)
	if err != nil {
		return nil, err
	}
	if string(item.UID) != expectedUID || item.ResourceVersion != expectedResourceVersion {
		return nil, ErrPolicyResourceChanged
	}
	before := item.DeepCopy()
	item.Spec = spec
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}

func (c *Client) DeleteAgentPolicy(ctx context.Context, name, expectedUID, expectedResourceVersion string) error {
	item, err := c.GetAgentPolicy(ctx, name)
	if err != nil {
		return err
	}
	if string(item.UID) != expectedUID || item.ResourceVersion != expectedResourceVersion {
		return ErrPolicyResourceChanged
	}
	uid := types.UID(expectedUID)
	return c.client.Delete(ctx, &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}}, client.Preconditions{
		UID: &uid, ResourceVersion: &expectedResourceVersion,
	})
}

func (c *Client) ListSandboxes(ctx context.Context) ([]cetirunv1alpha1.Sandbox, error) {
	items := &cetirunv1alpha1.SandboxList{}
	if err := c.client.List(ctx, items); err != nil {
		return nil, err
	}
	return items.Items, nil
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

func (c *Client) UpdateSandbox(ctx context.Context, name, expectedUID, expectedResourceVersion string, spec cetirunv1alpha1.SandboxSpec) (*cetirunv1alpha1.Sandbox, error) {
	item, err := c.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	if string(item.UID) != expectedUID || item.ResourceVersion != expectedResourceVersion {
		return nil, ErrPolicyResourceChanged
	}
	before := item.DeepCopy()
	item.Spec = spec
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
}

func (c *Client) DeleteSandbox(ctx context.Context, name, expectedUID, expectedResourceVersion string) error {
	item, err := c.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	if string(item.UID) != expectedUID || item.ResourceVersion != expectedResourceVersion {
		return ErrPolicyResourceChanged
	}
	uid := types.UID(expectedUID)
	return c.client.Delete(ctx, &cetirunv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: name}}, client.Preconditions{
		UID: &uid, ResourceVersion: &expectedResourceVersion,
	})
}

// UpdateInstancePolicy changes an Agent only while it is stopped.  Selection
// changes include an exact, fresh reauthorization intent required by admission.
func (c *Client) UpdateInstancePolicy(ctx context.Context, name string, update InstancePolicyUpdate) (*cetirunv1alpha1.Agent, error) {
	item, err := c.GetInstanceByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if string(item.UID) != update.ExpectedUID || item.ResourceVersion != update.ExpectedResourceVersion {
		return nil, ErrInstanceChanged
	}
	if !item.DeletionTimestamp.IsZero() {
		return nil, ErrInstanceDeleting
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
	if update.MonthlyBudgetUSD != nil {
		if item.Spec.ModelAccess == nil {
			item.Spec.ModelAccess = &cetirunv1alpha1.ModelAccessSpec{}
		}
		item.Spec.ModelAccess.MonthlyBudgetUSD = *update.MonthlyBudgetUSD
	}
	selectionChanged := before.Spec.Team != item.Spec.Team || before.Spec.PolicyClass != item.Spec.PolicyClass || before.Spec.SandboxRef != item.Spec.SandboxRef
	budgetChanged := monthlyBudget(before) != monthlyBudget(item)
	if (selectionChanged || budgetChanged) && !item.Spec.Suspended {
		return nil, ErrPolicyChangeRequiresSuspension
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

func monthlyBudget(item *cetirunv1alpha1.Agent) string {
	if item == nil || item.Spec.ModelAccess == nil {
		return ""
	}
	return item.Spec.ModelAccess.MonthlyBudgetUSD
}

func (c *Client) setSandboxReauthorization(ctx context.Context, item *cetirunv1alpha1.Agent) error {
	team, err := c.GetTeam(ctx, item.Spec.Team)
	if err != nil {
		return fmt.Errorf("read selected Team: %w", err)
	}
	if !slices.Contains(team.Spec.SandboxRefs, item.Spec.SandboxRef) {
		return fmt.Errorf("Sandbox %q is not permitted by Team %q", item.Spec.SandboxRef, team.Name)
	}
	sandbox, err := c.GetSandbox(ctx, item.Spec.SandboxRef)
	if err != nil {
		return fmt.Errorf("read selected Sandbox: %w", err)
	}
	class := item.Spec.PolicyClass
	if class == "" {
		class = cetirunv1alpha1.AgentPolicyClassNormal
		item.Spec.PolicyClass = class
	}
	policyName := team.Spec.NormalAgentPolicyRef
	if class == cetirunv1alpha1.AgentPolicyClassAdmin {
		policyName = team.Spec.AdminAgentPolicyRef
	}
	if policyName == "" {
		return fmt.Errorf("Team %q has no %s AgentPolicy", team.Name, class)
	}
	policy, err := c.GetAgentPolicy(ctx, policyName)
	if err != nil {
		return fmt.Errorf("read selected AgentPolicy: %w", err)
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
	intent.Binding.AgentUID, intent.Binding.TeamUID, intent.Binding.SandboxUID = string(item.UID), string(team.UID), string(sandbox.UID)
	intent.Binding.PolicyClass, intent.Binding.AgentPolicyUID = class, string(policy.UID)
	raw, err := json.Marshal(intent)
	if err != nil {
		return fmt.Errorf("encode reauthorization request: %w", err)
	}
	if item.Annotations == nil {
		item.Annotations = map[string]string{}
	}
	item.Annotations[sandboxReauthorizeAnnotation] = string(raw)
	return nil
}
