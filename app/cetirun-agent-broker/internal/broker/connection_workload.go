// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var errDependencyUnavailable = errors.New("dependency unavailable")
var errWorkloadProofInvalid = errors.New("workload proof is invalid")

type tokenReview struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Token     string   `json:"token"`
		Audiences []string `json:"audiences"`
	} `json:"spec"`
	Status struct {
		Authenticated bool     `json:"authenticated"`
		Audiences     []string `json:"audiences"`
		User          struct {
			Username string              `json:"username"`
			UID      string              `json:"uid"`
			Extra    map[string][]string `json:"extra"`
		} `json:"user"`
	} `json:"status"`
}

type workloadIdentity struct {
	Agent  Agent
	PodUID string
	SAUID  string
}

// Service lookups are kept separate while the legacy OBO endpoints coexist.
func (k *kubeClient) getServiceAgent(ctx context.Context, name string) (Agent, error) {
	if !dnsLabel(name) {
		return Agent{}, errors.New("instance name is invalid")
	}
	var document serviceAgentDocument
	_, err := k.request(ctx, http.MethodGet, "/apis/cetirun.microsoft.com/v1alpha1/agents/"+url.PathEscape(name), nil, &document)
	if err != nil {
		return Agent{}, err
	}
	agent := agentFromDocument(document)
	if agent.UID == "" || agent.Namespace == "" {
		return Agent{}, errors.New("Agent resource is not ready")
	}
	return agent, nil
}

func (k *kubeClient) getServiceAgentByWorkload(ctx context.Context, namespace, serviceAccount string) (Agent, error) {
	if !dnsLabel(namespace) || !dnsLabel(serviceAccount) {
		return Agent{}, errors.New("workload identity is invalid")
	}
	var ns struct {
		Metadata secretMetadata `json:"metadata"`
	}
	if _, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(namespace), nil, &ns); err != nil {
		return Agent{}, err
	}
	if ns.Metadata.Name != namespace || ns.Metadata.DeletionTimestamp != nil {
		return Agent{}, errNotFound
	}
	var owner ownerReference
	foundOwner := false
	for _, ref := range ns.Metadata.OwnerReferences {
		if ref.Kind == "Agent" && ref.APIVersion == "cetirun.microsoft.com/v1alpha1" {
			if foundOwner {
				return Agent{}, errNotFound
			}
			owner, foundOwner = ref, true
		}
	}
	if !dnsLabel(owner.Name) || !uuidPattern.MatchString(owner.UID) {
		return Agent{}, errNotFound
	}
	agent, err := k.getServiceAgent(ctx, owner.Name)
	if err != nil {
		return Agent{}, err
	}
	if agent.Name != owner.Name || agent.UID != owner.UID || agent.Namespace != namespace || workloadServiceAccount(agent.Name) != serviceAccount {
		return Agent{}, errNotFound
	}
	return agent, nil
}

func (k *kubeClient) getServiceAgentByUID(ctx context.Context, uid string) (Agent, error) {
	if !uuidPattern.MatchString(uid) {
		return Agent{}, errors.New("instance UID is invalid")
	}
	list, err := k.listAgents(ctx)
	if err != nil {
		return Agent{}, err
	}
	var match *Agent
	for _, document := range list.Items {
		if document.Metadata.UID != uid {
			continue
		}
		agent := agentFromDocument(document)
		if match != nil {
			return Agent{}, errors.New("instance UID maps to multiple resources")
		}
		match = &agent
	}
	if match == nil {
		return Agent{}, errNotFound
	}
	return *match, nil
}

func (k *kubeClient) verifyWorkloadProof(ctx context.Context, proof, audience string) (workloadIdentity, error) {
	if strings.TrimSpace(proof) == "" || strings.TrimSpace(audience) == "" {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	review := tokenReview{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}
	review.Spec.Token, review.Spec.Audiences = proof, []string{audience}
	if _, err := k.request(ctx, http.MethodPost, "/apis/authentication.k8s.io/v1/tokenreviews", review, &review); err != nil {
		return workloadIdentity{}, fmt.Errorf("%w: token review: %v", errDependencyUnavailable, err)
	}
	if !review.Status.Authenticated || !tokenReviewAudienceAccepted(review.Status.Audiences, audience) {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	parts := strings.Split(review.Status.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || !dnsLabel(parts[2]) || !dnsLabel(parts[3]) {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	saUID := review.Status.User.UID
	podUID := firstExtra(review.Status.User.Extra, "authentication.kubernetes.io/pod-uid")
	podName := firstExtra(review.Status.User.Extra, "authentication.kubernetes.io/pod-name")
	if saUID == "" || podUID == "" || podName == "" || !dnsLabel(podName) {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	var serviceAccount struct {
		Metadata struct {
			UID               string     `json:"uid"`
			DeletionTimestamp *time.Time `json:"deletionTimestamp,omitempty"`
		} `json:"metadata"`
	}
	if _, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(parts[2])+"/serviceaccounts/"+url.PathEscape(parts[3]), nil, &serviceAccount); err != nil {
		if errors.Is(err, errNotFound) {
			return workloadIdentity{}, errWorkloadProofInvalid
		}
		return workloadIdentity{}, fmt.Errorf("%w: service account lookup: %v", errDependencyUnavailable, err)
	} else if serviceAccount.Metadata.UID != saUID || serviceAccount.Metadata.DeletionTimestamp != nil {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	var pod struct {
		Metadata struct {
			UID               string     `json:"uid"`
			DeletionTimestamp *time.Time `json:"deletionTimestamp,omitempty"`
		} `json:"metadata"`
		Spec struct {
			ServiceAccountName string `json:"serviceAccountName"`
		} `json:"spec"`
	}
	if _, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(parts[2])+"/pods/"+url.PathEscape(podName), nil, &pod); err != nil {
		if errors.Is(err, errNotFound) {
			return workloadIdentity{}, errWorkloadProofInvalid
		}
		return workloadIdentity{}, fmt.Errorf("%w: pod lookup: %v", errDependencyUnavailable, err)
	} else if pod.Metadata.UID != podUID || pod.Spec.ServiceAccountName != parts[3] || pod.Metadata.DeletionTimestamp != nil {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	agent, err := k.getServiceAgentByWorkload(ctx, parts[2], parts[3])
	if err != nil {
		if errors.Is(err, errNotFound) {
			return workloadIdentity{}, errWorkloadProofInvalid
		}
		return workloadIdentity{}, fmt.Errorf("%w: instance lookup: %v", errDependencyUnavailable, err)
	}
	if !agent.WorkloadReady || !uuidPattern.MatchString(agent.UID) {
		return workloadIdentity{}, errWorkloadProofInvalid
	}
	return workloadIdentity{Agent: agent, PodUID: podUID, SAUID: saUID}, nil
}

func tokenReviewAudienceAccepted(audiences []string, expected string) bool {
	return len(audiences) == 1 && audiences[0] == expected
}

func agentFromDocument(document serviceAgentDocument) Agent {
	agent := Agent{
		Deleting: document.Metadata.DeletionTimestamp != nil, RuntimeType: document.Spec.Agent.Type,
		Name:               document.Metadata.Name,
		UnsupportedRuntime: agentDocumentSandboxRequested(document) || (document.Spec.Agent.Type != "hermes" && document.Spec.Agent.Type != "openclaw"),
		UID:                document.Metadata.UID,
		Namespace:          document.Status.Namespace,
		OwnerTenantID:      document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-tenant-id"],
		OwnerObjectID:      document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"],
		ServiceAccount:     workloadServiceAccount(document.Metadata.Name),
		WorkloadReady:      document.Status.Phase == "Ready" && !document.Spec.Suspended && document.Metadata.DeletionTimestamp == nil,
	}
	if document.Spec.AgentIdentity != nil && document.Spec.AgentIdentity.Enabled && document.Status.AgentIdentity != nil {
		agent.AgentIdentityClientID = document.Status.AgentIdentity.ClientID
		agent.AgentIdentityObjectID = document.Status.AgentIdentity.ObjectID
		agent.AgentIdentityReady = agent.WorkloadReady && document.Status.AgentIdentity.Phase == "Ready" && agent.AgentIdentityClientID != ""
	}
	return agent
}

func agentDocumentSandboxRequested(document serviceAgentDocument) bool {
	for _, key := range []string{"cetirun.microsoft.com/sandbox-authorization", "cetirun.microsoft.com/sandbox-reauthorize"} {
		if _, present := document.Metadata.Annotations[key]; present {
			return true
		}
	}
	for _, finalizer := range document.Metadata.Finalizers {
		switch finalizer {
		case "cetirun.microsoft.com/sandbox-hold", "cetirun.microsoft.com/sandbox-preparation", "cetirun.microsoft.com/sandbox-bootstrap":
			return true
		}
	}
	if document.Spec.SandboxRef != "" ||
		(document.Spec.ModelAccess != nil && document.Spec.ModelAccess.MonthlyBudgetUSD != "") ||
		document.Status.Policy != nil || document.Status.Sandbox != nil ||
		(document.Status.AIGateway != nil && document.Status.AIGateway.PolicyRevision != "") {
		return true
	}
	for _, condition := range document.Status.Conditions {
		if condition.Type == "SandboxReady" {
			return true
		}
	}
	return false
}

func firstExtra(extra map[string][]string, key string) string {
	if len(extra[key]) != 1 {
		return ""
	}
	return extra[key][0]
}

func (k *kubeClient) listAgents(ctx context.Context) (serviceAgentList, error) {
	result := serviceAgentList{}
	continuation := ""
	seen := map[string]bool{}
	for {
		query := url.Values{"limit": {"50"}}
		if continuation != "" {
			query.Set("continue", continuation)
		}
		var page serviceAgentList
		if _, err := k.request(ctx, http.MethodGet, "/apis/cetirun.microsoft.com/v1alpha1/agents?"+query.Encode(), nil, &page); err != nil {
			return serviceAgentList{}, err
		}
		result.Items = append(result.Items, page.Items...)
		if page.Metadata.Continue == "" {
			return result, nil
		}
		if seen[page.Metadata.Continue] {
			return serviceAgentList{}, errors.New("Kubernetes pagination did not advance")
		}
		seen[page.Metadata.Continue] = true
		continuation = page.Metadata.Continue
	}
}

type serviceAgentDocument struct {
	Metadata struct {
		Name              string            `json:"name"`
		UID               string            `json:"uid"`
		Annotations       map[string]string `json:"annotations"`
		DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
		Finalizers        []string          `json:"finalizers"`
	} `json:"metadata"`
	Spec struct {
		Suspended   bool   `json:"suspended"`
		SandboxRef  string `json:"sandboxRef"`
		ModelAccess *struct {
			MonthlyBudgetUSD string `json:"monthlyBudgetUSD"`
		} `json:"modelAccess"`
		Agent struct {
			Type string `json:"type"`
		} `json:"agent"`
		User struct {
			Alias string `json:"alias"`
		} `json:"user"`
		AgentIdentity *struct {
			Enabled bool `json:"enabled"`
		} `json:"agentIdentity"`
	} `json:"spec"`
	Status struct {
		Phase     string           `json:"phase"`
		Namespace string           `json:"namespace"`
		Policy    *json.RawMessage `json:"policy"`
		Sandbox   *json.RawMessage `json:"sandbox"`
		AIGateway *struct {
			PolicyRevision string `json:"policyRevision"`
		} `json:"aiGateway"`
		Conditions []struct {
			Type string `json:"type"`
		} `json:"conditions"`
		AgentIdentity *struct {
			Phase    string `json:"phase"`
			ClientID string `json:"clientId"`
			ObjectID string `json:"objectId"`
		} `json:"agentIdentity"`
	} `json:"status"`
}

type serviceAgentList struct {
	Metadata struct {
		Continue string `json:"continue"`
	} `json:"metadata"`
	Items []serviceAgentDocument `json:"items"`
}
