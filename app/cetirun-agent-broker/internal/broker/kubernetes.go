// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

var errConflict = errors.New("record update conflict")
var errNotFound = errors.New("record not found")
var errDependencyUnavailable = errors.New("dependency unavailable")
var errWorkloadProofInvalid = errors.New("workload proof is invalid")

type secretDocument struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   secretMetadata    `json:"metadata"`
	Data       map[string]string `json:"data"`
}

type secretMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	OwnerReferences []ownerReference  `json:"ownerReferences,omitempty"`
}

type ownerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

type secretList struct {
	Items []secretDocument `json:"items"`
}

type agentDocument struct {
	Metadata struct {
		Name              string            `json:"name"`
		UID               string            `json:"uid"`
		Annotations       map[string]string `json:"annotations"`
		DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		Suspended bool `json:"suspended"`
		User      struct {
			Alias string `json:"alias"`
		} `json:"user"`
		AgentIdentity *struct {
			Enabled bool `json:"enabled"`
		} `json:"agentIdentity"`
	} `json:"spec"`
	Status struct {
		Phase         string `json:"phase"`
		Namespace     string `json:"namespace"`
		AgentIdentity *struct {
			Phase    string `json:"phase"`
			ClientID string `json:"clientId"`
		} `json:"agentIdentity"`
	} `json:"status"`
}

type agentList struct {
	Items []agentDocument `json:"items"`
}

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

type kubeClient struct {
	baseURL   string
	namespace string
	tokenFile string
	client    *http.Client
	wrapper   keyWrapper
}

func newKubeClient(cfg Config, wrapper keyWrapper) (*kubeClient, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("Kubernetes service environment is unavailable")
	}
	tokenFile := cfg.KubernetesTokenFile
	if _, err := os.Stat(tokenFile); err != nil {
		return nil, errors.New("Kubernetes service account token is unavailable")
	}
	ca, err := os.ReadFile(cfg.KubernetesCAFile)
	if err != nil {
		return nil, errors.New("Kubernetes service CA is unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("Kubernetes service CA is invalid")
	}
	return &kubeClient{
		baseURL:   "https://" + host + ":" + port,
		namespace: cfg.StoreNamespace,
		tokenFile: tokenFile,
		client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		}}, Timeout: cfg.RequestTimeout},
		wrapper: wrapper,
	}, nil
}

func (k *kubeClient) serviceAccountToken() (string, error) {
	token, err := os.ReadFile(k.tokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return "", errors.New("Kubernetes service account token is unavailable")
	}
	return strings.TrimSpace(string(token)), nil
}

func (k *kubeClient) request(ctx context.Context, method, endpoint string, body any, result any) (int, error) {
	token, err := k.serviceAccountToken()
	if err != nil {
		return 0, err
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.baseURL+endpoint, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, errors.New("Kubernetes API is unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, errors.New("cannot read Kubernetes API response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusNotFound {
			return resp.StatusCode, errNotFound
		}
		if resp.StatusCode == http.StatusConflict {
			return resp.StatusCode, errConflict
		}
		return resp.StatusCode, fmt.Errorf("Kubernetes API returned HTTP %d", resp.StatusCode)
	}
	if result != nil && len(data) > 0 {
		if err := json.Unmarshal(data, result); err != nil {
			return resp.StatusCode, errors.New("Kubernetes API returned invalid JSON")
		}
	}
	return resp.StatusCode, nil
}

func (k *kubeClient) getAgent(ctx context.Context, name string) (Agent, error) {
	if !dnsLabel(name) {
		return Agent{}, errors.New("instance name is invalid")
	}
	var document agentDocument
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

func (k *kubeClient) getAgentByWorkload(ctx context.Context, namespace, serviceAccount string) (Agent, error) {
	if !dnsLabel(namespace) || !dnsLabel(serviceAccount) {
		return Agent{}, errors.New("workload identity is invalid")
	}
	var list agentList
	if _, err := k.request(ctx, http.MethodGet, "/apis/cetirun.microsoft.com/v1alpha1/agents", nil, &list); err != nil {
		return Agent{}, err
	}
	var match *Agent
	for _, document := range list.Items {
		if document.Status.Namespace != namespace || workloadServiceAccount(document.Metadata.Name) != serviceAccount {
			continue
		}
		agent, err := k.getAgent(ctx, document.Metadata.Name)
		if err != nil {
			return Agent{}, err
		}
		if match != nil {
			return Agent{}, errors.New("workload identity maps to multiple instances")
		}
		match = &agent
	}
	if match == nil {
		return Agent{}, errNotFound
	}
	return *match, nil
}

func (k *kubeClient) getAgentByUID(ctx context.Context, uid string) (Agent, error) {
	if !uuidPattern.MatchString(uid) {
		return Agent{}, errors.New("instance UID is invalid")
	}
	var list agentList
	if _, err := k.request(ctx, http.MethodGet, "/apis/cetirun.microsoft.com/v1alpha1/agents", nil, &list); err != nil {
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
	agent, err := k.getAgentByWorkload(ctx, parts[2], parts[3])
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

func agentFromDocument(document agentDocument) Agent {
	agent := Agent{
		Name:           document.Metadata.Name,
		UID:            document.Metadata.UID,
		Namespace:      document.Status.Namespace,
		OwnerTenantID:  document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-tenant-id"],
		OwnerObjectID:  document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"],
		ServiceAccount: workloadServiceAccount(document.Metadata.Name),
		WorkloadReady:  document.Status.Phase == "Ready" && !document.Spec.Suspended && document.Metadata.DeletionTimestamp == nil,
	}
	if document.Spec.AgentIdentity != nil && document.Spec.AgentIdentity.Enabled && document.Status.AgentIdentity != nil {
		agent.AgentIdentityClientID = document.Status.AgentIdentity.ClientID
		agent.AgentIdentityReady = agent.WorkloadReady && document.Status.AgentIdentity.Phase == "Ready" && agent.AgentIdentityClientID != ""
	}
	return agent
}

func firstExtra(extra map[string][]string, key string) string {
	if len(extra[key]) != 1 {
		return ""
	}
	return extra[key][0]
}

func workloadServiceAccount(instanceName string) string {
	name := "openclaw-" + instanceName + "-cetirun"
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func stateHash(state string) string {
	digest := sha256.Sum256([]byte(state))
	return hex.EncodeToString(digest[:])
}

func dnsLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (char == '-' && index > 0 && index < len(value)-1) {
			continue
		}
		return false
	}
	return true
}

func toolRecordName(kind, id string) string { return "agent-tools-" + kind + "-" + id }

func connectionAAD(connection Connection) []byte {
	return []byte(fmt.Sprintf("agent-tools-connection-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", connection.ID, connection.Owner.TenantID, connection.Owner.ObjectID, connection.Provider, connection.AccountID, connection.Revision))
}

func grantAAD(grant AgentGrant) []byte {
	return []byte(fmt.Sprintf("agent-tools-grant-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", grant.ID, grant.Owner.TenantID, grant.Owner.ObjectID, grant.InstanceUID, grant.ConnectionID, grant.Revision))
}

func recordRevision(data map[string]string) uint64 {
	raw, err := base64.StdEncoding.DecodeString(data["revision"])
	if err != nil {
		return 0
	}
	var revision uint64
	_, _ = fmt.Sscanf(string(raw), "%d", &revision)
	return revision
}

func (k *kubeClient) getToolSecret(ctx context.Context, kind, id string) (secretDocument, error) {
	var secret secretDocument
	_, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets/"+url.PathEscape(toolRecordName(kind, id)), nil, &secret)
	return secret, err
}

func (k *kubeClient) putToolSecret(ctx context.Context, kind, id string, labels map[string]string, data map[string]string, resourceVersion string) error {
	labels["app.kubernetes.io/managed-by"] = "cetirun-agent-broker"
	labels["agenttools.cetirun.microsoft.com/record-type"] = kind
	secret := secretDocument{APIVersion: "v1", Kind: "Secret", Metadata: secretMetadata{Name: toolRecordName(kind, id), Namespace: k.namespace, ResourceVersion: resourceVersion, Labels: labels}, Data: data}
	endpoint, method := "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets", http.MethodPost
	if resourceVersion != "" {
		endpoint, method = endpoint+"/"+url.PathEscape(secret.Metadata.Name), http.MethodPut
	}
	_, err := k.request(ctx, method, endpoint, secret, nil)
	return err
}

func (k *kubeClient) encodeConnection(ctx context.Context, record *connectionRecord) (map[string]string, error) {
	plain, err := json.Marshal(storedConnectionRecord{connectionRecord: record, OwnerTenantID: record.Connection.Owner.TenantID, OwnerObjectID: record.Connection.Owner.ObjectID, AccountID: record.Connection.AccountID, UpdatedAt: record.Connection.UpdatedAt})
	if err != nil {
		return nil, err
	}
	envelope, err := seal(ctx, k.wrapper, plain, connectionAAD(record.Connection))
	if err != nil {
		return nil, err
	}
	return map[string]string{"envelope.json": base64.StdEncoding.EncodeToString(envelope), "revision": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", record.Connection.Revision)))}, nil
}

type storedConnectionRecord struct {
	*connectionRecord
	OwnerTenantID string    `json:"ownerTenantId"`
	OwnerObjectID string    `json:"ownerObjectId"`
	AccountID     string    `json:"accountId"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (k *kubeClient) decodeConnection(ctx context.Context, secret secretDocument) (*connectionRecord, error) {
	id, ownerTenant, ownerObject, provider := secret.Metadata.Labels["agenttools.cetirun.microsoft.com/id"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-tenant"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-object"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/provider"]
	revision := recordRevision(secret.Data)
	if id == "" || ownerTenant == "" || ownerObject == "" || provider == "" || revision == 0 {
		return nil, errors.New("connection record metadata is invalid")
	}
	envelope, err := base64.StdEncoding.DecodeString(secret.Data["envelope.json"])
	if err != nil {
		return nil, errors.New("connection record encoding is invalid")
	}
	stub := Connection{ID: id, Owner: Owner{TenantID: ownerTenant, ObjectID: ownerObject}, Provider: provider, AccountID: secret.Metadata.Labels["agenttools.cetirun.microsoft.com/account-id"], Revision: revision}
	plain, err := openEnvelope(ctx, k.wrapper, envelope, connectionAAD(stub))
	if err != nil {
		return nil, err
	}
	stored := storedConnectionRecord{connectionRecord: &connectionRecord{}}
	if json.Unmarshal(plain, &stored) != nil {
		return nil, errors.New("connection record is invalid")
	}
	record := stored.connectionRecord
	record.Connection.Owner = Owner{TenantID: stored.OwnerTenantID, ObjectID: stored.OwnerObjectID}
	record.Connection.AccountID, record.Connection.UpdatedAt = stored.AccountID, stored.UpdatedAt
	if record.Connection.ID != id || record.Connection.Revision != revision || !sameOwner(record.Connection.Owner, stub.Owner) || record.Connection.Provider != provider || record.Connection.AccountID != stub.AccountID {
		return nil, errors.New("connection record is invalid")
	}
	return record, nil
}

func (k *kubeClient) getConnection(ctx context.Context, id string) (*connectionRecord, string, error) {
	secret, err := k.getToolSecret(ctx, "connection", id)
	if err != nil {
		return nil, "", err
	}
	record, err := k.decodeConnection(ctx, secret)
	return record, secret.Metadata.ResourceVersion, err
}

func (k *kubeClient) putConnection(ctx context.Context, record *connectionRecord, resourceVersion string) error {
	data, err := k.encodeConnection(ctx, record)
	if err != nil {
		return err
	}
	labels := map[string]string{
		"agenttools.cetirun.microsoft.com/id":           record.Connection.ID,
		"agenttools.cetirun.microsoft.com/owner-tenant": record.Connection.Owner.TenantID,
		"agenttools.cetirun.microsoft.com/owner-object": record.Connection.Owner.ObjectID,
		"agenttools.cetirun.microsoft.com/provider":     record.Connection.Provider,
		"agenttools.cetirun.microsoft.com/account-id":   record.Connection.AccountID,
	}
	if record.Transaction != nil {
		labels["agenttools.cetirun.microsoft.com/state-hash"] = record.Transaction.StateHash[:48]
	}
	return k.putToolSecret(ctx, "connection", record.Connection.ID, labels, data, resourceVersion)
}

func (k *kubeClient) connectionRecords(ctx context.Context, selector string) ([]struct {
	record  *connectionRecord
	version string
}, error) {
	endpoint := "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/secrets"
	if selector != "" {
		endpoint += "?labelSelector=" + url.QueryEscape(selector)
	}
	var list secretList
	if _, err := k.request(ctx, http.MethodGet, endpoint, nil, &list); err != nil {
		return nil, err
	}
	result := make([]struct {
		record  *connectionRecord
		version string
	}, 0, len(list.Items))
	for _, secret := range list.Items {
		if secret.Metadata.Labels["agenttools.cetirun.microsoft.com/record-type"] != "connection" {
			continue
		}
		record, err := k.decodeConnection(ctx, secret)
		if err != nil {
			return nil, err
		}
		result = append(result, struct {
			record  *connectionRecord
			version string
		}{record, secret.Metadata.ResourceVersion})
	}
	return result, nil
}

func (k *kubeClient) listConnections(ctx context.Context, owner Owner) ([]Connection, error) {
	records, err := k.connectionRecords(ctx, "agenttools.cetirun.microsoft.com/owner-tenant="+owner.TenantID+",agenttools.cetirun.microsoft.com/owner-object="+owner.ObjectID)
	if err != nil {
		return nil, err
	}
	connections := make([]Connection, 0, len(records))
	for _, item := range records {
		if !item.record.Deleted && sameOwner(item.record.Connection.Owner, owner) {
			connections = append(connections, item.record.Connection)
		}
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].CreatedAt.Before(connections[j].CreatedAt) })
	return connections, nil
}

func (k *kubeClient) findConnectionByState(ctx context.Context, state string) (*connectionRecord, string, error) {
	hash := stateHash(state)
	records, err := k.connectionRecords(ctx, "agenttools.cetirun.microsoft.com/state-hash="+hash[:48])
	if err != nil {
		return nil, "", err
	}
	if len(records) != 1 || records[0].record.Deleted || records[0].record.Transaction == nil || !strings.EqualFold(records[0].record.Transaction.StateHash, hash) {
		return nil, "", errNotFound
	}
	return records[0].record, records[0].version, nil
}

func (k *kubeClient) encodeGrant(ctx context.Context, record *grantRecord) (map[string]string, error) {
	plain, err := json.Marshal(storedGrantRecord{grantRecord: record, OwnerTenantID: record.Grant.Owner.TenantID, OwnerObjectID: record.Grant.Owner.ObjectID, Revoked: record.Grant.Revoked, CreatedAt: record.Grant.CreatedAt, UpdatedAt: record.Grant.UpdatedAt})
	if err != nil {
		return nil, err
	}
	envelope, err := seal(ctx, k.wrapper, plain, grantAAD(record.Grant))
	if err != nil {
		return nil, err
	}
	return map[string]string{"envelope.json": base64.StdEncoding.EncodeToString(envelope), "revision": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", record.Grant.Revision)))}, nil
}

type storedGrantRecord struct {
	*grantRecord
	OwnerTenantID string    `json:"ownerTenantId"`
	OwnerObjectID string    `json:"ownerObjectId"`
	Revoked       bool      `json:"revoked"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func (k *kubeClient) decodeGrant(ctx context.Context, secret secretDocument) (*grantRecord, error) {
	id, ownerTenant, ownerObject, instanceUID, connectionID := secret.Metadata.Labels["agenttools.cetirun.microsoft.com/id"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-tenant"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-object"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/instance-uid"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/connection-id"]
	stub := AgentGrant{ID: id, Owner: Owner{TenantID: ownerTenant, ObjectID: ownerObject}, InstanceUID: instanceUID, ConnectionID: connectionID, Revision: recordRevision(secret.Data)}
	if id == "" || ownerTenant == "" || ownerObject == "" || instanceUID == "" || connectionID == "" || stub.Revision == 0 {
		return nil, errors.New("grant record metadata is invalid")
	}
	envelope, err := base64.StdEncoding.DecodeString(secret.Data["envelope.json"])
	if err != nil {
		return nil, errors.New("grant record encoding is invalid")
	}
	plain, err := openEnvelope(ctx, k.wrapper, envelope, grantAAD(stub))
	if err != nil {
		return nil, err
	}
	stored := storedGrantRecord{grantRecord: &grantRecord{}}
	if json.Unmarshal(plain, &stored) != nil {
		return nil, errors.New("grant record is invalid")
	}
	record := stored.grantRecord
	record.Grant.Owner = Owner{TenantID: stored.OwnerTenantID, ObjectID: stored.OwnerObjectID}
	record.Grant.Revoked, record.Grant.CreatedAt, record.Grant.UpdatedAt = stored.Revoked, stored.CreatedAt, stored.UpdatedAt
	if record.Grant.ID != id || record.Grant.Revision != stub.Revision || !sameOwner(record.Grant.Owner, stub.Owner) || record.Grant.InstanceUID != instanceUID || record.Grant.ConnectionID != connectionID {
		return nil, errors.New("grant record is invalid")
	}
	return record, nil
}

func (k *kubeClient) getGrant(ctx context.Context, id string) (*grantRecord, string, error) {
	secret, err := k.getToolSecret(ctx, "grant", id)
	if err != nil {
		return nil, "", err
	}
	record, err := k.decodeGrant(ctx, secret)
	return record, secret.Metadata.ResourceVersion, err
}

func (k *kubeClient) putGrant(ctx context.Context, record *grantRecord, resourceVersion string) error {
	data, err := k.encodeGrant(ctx, record)
	if err != nil {
		return err
	}
	grant := record.Grant
	labels := map[string]string{
		"agenttools.cetirun.microsoft.com/id":            grant.ID,
		"agenttools.cetirun.microsoft.com/owner-tenant":  grant.Owner.TenantID,
		"agenttools.cetirun.microsoft.com/owner-object":  grant.Owner.ObjectID,
		"agenttools.cetirun.microsoft.com/instance-uid":  grant.InstanceUID,
		"agenttools.cetirun.microsoft.com/connection-id": grant.ConnectionID,
	}
	return k.putToolSecret(ctx, "grant", grant.ID, labels, data, resourceVersion)
}

func (k *kubeClient) grantRecords(ctx context.Context, selector string) ([]struct {
	record  *grantRecord
	version string
}, error) {
	endpoint := "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/secrets"
	if selector != "" {
		endpoint += "?labelSelector=" + url.QueryEscape(selector)
	}
	var list secretList
	if _, err := k.request(ctx, http.MethodGet, endpoint, nil, &list); err != nil {
		return nil, err
	}
	result := make([]struct {
		record  *grantRecord
		version string
	}, 0, len(list.Items))
	for _, secret := range list.Items {
		if secret.Metadata.Labels["agenttools.cetirun.microsoft.com/record-type"] != "grant" {
			continue
		}
		record, err := k.decodeGrant(ctx, secret)
		if err != nil {
			return nil, err
		}
		result = append(result, struct {
			record  *grantRecord
			version string
		}{record, secret.Metadata.ResourceVersion})
	}
	return result, nil
}

func (k *kubeClient) listGrants(ctx context.Context, owner Owner, instanceUID string) ([]AgentGrant, error) {
	selector := "agenttools.cetirun.microsoft.com/owner-tenant=" + owner.TenantID + ",agenttools.cetirun.microsoft.com/owner-object=" + owner.ObjectID
	if instanceUID != "" {
		selector += ",agenttools.cetirun.microsoft.com/instance-uid=" + instanceUID
	}
	records, err := k.grantRecords(ctx, selector)
	if err != nil {
		return nil, err
	}
	grants := make([]AgentGrant, 0, len(records))
	for _, item := range records {
		if sameOwner(item.record.Grant.Owner, owner) {
			grants = append(grants, item.record.Grant)
		}
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].CreatedAt.Before(grants[j].CreatedAt) })
	return grants, nil
}

func sameOwner(left, right Owner) bool {
	return sameIdentityID(left.TenantID, right.TenantID) && sameIdentityID(left.ObjectID, right.ObjectID)
}

func activeGrant(grant AgentGrant, now time.Time) bool {
	return !grant.Revoked && now.Before(grant.ExpiresAt)
}

func (k *kubeClient) revokeGrantsForInstance(ctx context.Context, instanceUID string) error {
	records, err := k.grantRecords(ctx, "agenttools.cetirun.microsoft.com/instance-uid="+instanceUID)
	if err != nil {
		return err
	}
	for _, item := range records {
		if item.record.Grant.Revoked {
			continue
		}
		updated := item.record
		updated.Grant.Revoked = true
		updated.Grant.Revision++
		updated.Grant.UpdatedAt = time.Now().UTC()
		if err := k.putGrant(ctx, updated, item.version); err != nil {
			if errors.Is(err, errConflict) {
				return errConflict
			}
			return err
		}
	}
	return nil
}
