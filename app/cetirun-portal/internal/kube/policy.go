// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

const sandboxReauthorizeAnnotation = "cetirun.microsoft.com/sandbox-reauthorize"

type InstancePolicyUpdate struct {
	ExpectedUID             string
	ExpectedResourceVersion string
	Team                    *string
	PolicyClass             *cetirunv1alpha1.AgentPolicyClass
	SandboxRef              *string
	MonthlyBudgetUSD        *string
	TeamAuthorization       *cetirunv1alpha1.TeamAuthorization
}

// UpdateInstanceLifecycle applies an administrator's direct suspend or resume
// decision using both UID and resourceVersion to reject stale or replaced Agents.
func (c *Client) UpdateInstanceLifecycle(ctx context.Context, name, expectedUID, expectedResourceVersion string, suspended bool) (*cetirunv1alpha1.Agent, error) {
	item, err := c.GetInstanceByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if string(item.UID) != expectedUID || item.ResourceVersion != expectedResourceVersion {
		return nil, ErrInstanceChanged
	}
	if !item.DeletionTimestamp.IsZero() {
		return nil, ErrInstanceDeleting
	}
	before := item.DeepCopy()
	item.Spec.Suspended = suspended
	if err := c.client.Patch(ctx, item, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}), client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, err
	}
	return item, nil
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

func (c *Client) UpdateInstancePolicy(ctx context.Context, name string, update InstancePolicyUpdate) (*cetirunv1alpha1.Agent, error) {
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
	if update.MonthlyBudgetUSD != nil {
		if item.Spec.ModelAccess == nil {
			item.Spec.ModelAccess = &cetirunv1alpha1.ModelAccessSpec{}
		}
		item.Spec.ModelAccess.MonthlyBudgetUSD = *update.MonthlyBudgetUSD
	}
	if update.TeamAuthorization != nil {
		item.Spec.TeamAuthorization = *update.TeamAuthorization.DeepCopy()
	}
	selectionChanged := before.Spec.Team != item.Spec.Team || before.Spec.PolicyClass != item.Spec.PolicyClass || before.Spec.SandboxRef != item.Spec.SandboxRef
	policyChanged := !reflect.DeepEqual(before.Spec.TeamAuthorization, item.Spec.TeamAuthorization) || !reflect.DeepEqual(before.Spec.ModelAccess, item.Spec.ModelAccess)
	if (selectionChanged || policyChanged) && !item.Spec.Suspended {
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
