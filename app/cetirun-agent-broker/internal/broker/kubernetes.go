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
	"strings"
	"time"
)

var errConflict = errors.New("authorization record update conflict")
var errNotFound = errors.New("authorization record not found")

type secretDocument struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   secretMetadata    `json:"metadata"`
	Data       map[string]string `json:"data"`
}

type secretMetadata struct {
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	UID               string            `json:"uid,omitempty"`
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	OwnerReferences   []ownerReference  `json:"ownerReferences,omitempty"`
}

type ownerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

type secretList struct {
	Metadata struct {
		Continue string `json:"continue"`
	} `json:"metadata"`
	Items []secretDocument `json:"items"`
}

type agentDocument struct {
	Metadata struct {
		Name        string            `json:"name"`
		UID         string            `json:"uid"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		User struct {
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

type kubeClient struct {
	agentNamespaces         bool
	recordNamespaceDeleting bool
	recordAgent             *Agent

	baseURL               string
	namespace             string
	tokenFile             string
	client                *http.Client
	wrapper               keyWrapper
	blueprintClientID     string
	authorizationClientID string
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
		agentNamespaces: cfg.ConnectionBindingsEnabled,
		baseURL:         "https://" + host + ":" + port,
		namespace:       cfg.StoreNamespace,
		tokenFile:       tokenFile,
		client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		}}, Timeout: cfg.RequestTimeout},
		wrapper:               wrapper,
		blueprintClientID:     cfg.BlueprintClientID,
		authorizationClientID: cfg.AuthorizationClientID,
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
	agent := Agent{
		Name:           document.Metadata.Name,
		UID:            document.Metadata.UID,
		Namespace:      document.Status.Namespace,
		OwnerTenantID:  document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-tenant-id"],
		OwnerObjectID:  document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"],
		ServiceAccount: workloadServiceAccount(document.Metadata.Name),
	}
	if document.Spec.AgentIdentity != nil && document.Spec.AgentIdentity.Enabled && document.Status.AgentIdentity != nil {
		agent.AgentIdentityClientID = document.Status.AgentIdentity.ClientID
		agent.AgentIdentityReady = document.Status.Phase == "Ready" && document.Status.AgentIdentity.Phase == "Ready"
	}
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
		agent := Agent{
			Name:               document.Metadata.Name,
			UID:                document.Metadata.UID,
			Namespace:          document.Status.Namespace,
			OwnerTenantID:      document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-tenant-id"],
			OwnerObjectID:      document.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"],
			ServiceAccount:     workloadServiceAccount(document.Metadata.Name),
			AgentIdentityReady: document.Status.Phase == "Ready" && document.Status.AgentIdentity != nil && document.Status.AgentIdentity.Phase == "Ready",
		}
		if document.Status.AgentIdentity != nil {
			agent.AgentIdentityClientID = document.Status.AgentIdentity.ClientID
		}
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

func workloadServiceAccount(instanceName string) string {
	name := "openclaw-" + instanceName + "-cetirun"
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func recordName(uid string) string { return "agent-authorization-" + strings.ToLower(uid) }

func recordAAD(uid, tenantID, ownerID, agentIdentityClientID, blueprintClientID, authorizationClientID string, generation uint64) []byte {
	return []byte(fmt.Sprintf("v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", uid, tenantID, ownerID, agentIdentityClientID, blueprintClientID, authorizationClientID, generation))
}

func (k *kubeClient) getRecord(ctx context.Context, uid string) (*AuthorizationRecord, string, error) {
	var secret secretDocument
	endpoint := "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/secrets/" + url.PathEscape(recordName(uid))
	if _, err := k.request(ctx, http.MethodGet, endpoint, nil, &secret); err != nil {
		return nil, "", err
	}
	encoded, err := base64.StdEncoding.DecodeString(secret.Data["envelope.json"])
	if err != nil {
		return nil, "", errors.New("authorization record encoding is invalid")
	}
	generation := uint64(0)
	if raw := secret.Data["generation"]; raw != "" {
		decoded, decodeErr := base64.StdEncoding.DecodeString(raw)
		if decodeErr == nil {
			_, _ = fmt.Sscanf(string(decoded), "%d", &generation)
		}
	}
	ownerTenant := secret.Metadata.Labels["agentidentity.cetirun.microsoft.com/owner-tenant"]
	ownerObject := secret.Metadata.Labels["agentidentity.cetirun.microsoft.com/owner-object"]
	agentIdentityClientID := secret.Metadata.Labels["agentidentity.cetirun.microsoft.com/agent-client-id"]
	plaintext, err := openEnvelope(ctx, k.wrapper, encoded, recordAAD(uid, ownerTenant, ownerObject, agentIdentityClientID, k.blueprintClientID, k.authorizationClientID, generation))
	if err != nil {
		return nil, "", err
	}
	var record AuthorizationRecord
	if err := json.Unmarshal(plaintext, &record); err != nil || record.InstanceUID != uid || record.Generation != generation {
		return nil, "", errors.New("authorization record is invalid")
	}
	return &record, secret.Metadata.ResourceVersion, nil
}

func (k *kubeClient) putRecord(ctx context.Context, record *AuthorizationRecord, resourceVersion string) error {
	plaintext, err := json.Marshal(record)
	if err != nil {
		return err
	}
	aad := recordAAD(record.InstanceUID, record.OwnerTenantID, record.OwnerObjectID, record.AgentIdentityClientID, k.blueprintClientID, k.authorizationClientID, record.Generation)
	encoded, err := seal(ctx, k.wrapper, plaintext, aad)
	if err != nil {
		return err
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by":                        "cetirun-agent-broker",
		"agentidentity.cetirun.microsoft.com/instance-uid":    record.InstanceUID,
		"agentidentity.cetirun.microsoft.com/instance-name":   record.InstanceName,
		"agentidentity.cetirun.microsoft.com/owner-tenant":    record.OwnerTenantID,
		"agentidentity.cetirun.microsoft.com/owner-object":    record.OwnerObjectID,
		"agentidentity.cetirun.microsoft.com/agent-client-id": record.AgentIdentityClientID,
	}
	if record.Transaction != nil {
		labels["agentidentity.cetirun.microsoft.com/state-hash"] = record.Transaction.StateHash[:48]
	}
	secret := secretDocument{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata: secretMetadata{
			Name:            recordName(record.InstanceUID),
			Namespace:       k.namespace,
			ResourceVersion: resourceVersion,
			Labels:          labels,
			OwnerReferences: []ownerReference{{
				APIVersion: "cetirun.microsoft.com/v1alpha1",
				Kind:       "Agent",
				Name:       record.InstanceName,
				UID:        record.InstanceUID,
			}},
		},
		Data: map[string]string{
			"envelope.json": base64.StdEncoding.EncodeToString(encoded),
			"generation":    base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", record.Generation))),
		},
	}
	endpoint := "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/secrets"
	method := http.MethodPost
	if resourceVersion != "" {
		method = http.MethodPut
		endpoint += "/" + url.PathEscape(secret.Metadata.Name)
	}
	_, err = k.request(ctx, method, endpoint, secret, nil)
	return err
}

func (k *kubeClient) findRecordByState(ctx context.Context, state string) (*AuthorizationRecord, string, error) {
	digest := sha256.Sum256([]byte(state))
	hash := hex.EncodeToString(digest[:])
	selector := url.QueryEscape("agentidentity.cetirun.microsoft.com/state-hash=" + hash[:48])
	endpoint := "/api/v1/namespaces/" + url.PathEscape(k.namespace) + "/secrets?labelSelector=" + selector
	var list secretList
	if _, err := k.request(ctx, http.MethodGet, endpoint, nil, &list); err != nil {
		return nil, "", err
	}
	if len(list.Items) != 1 {
		return nil, "", errNotFound
	}
	return k.getRecord(ctx, strings.TrimPrefix(list.Items[0].Metadata.Name, "agent-authorization-"))
}

func revokeAuthorizationRecord(record *AuthorizationRecord) {
	record.Generation++
	record.Revoked = true
	record.Transaction = nil
	record.Tokens = nil
	record.AccountDisplay = ""
}

func (k *kubeClient) revokeRecord(ctx context.Context, agent Agent) error {
	for attempt := 0; attempt < 3; attempt++ {
		record, version, err := k.getRecord(ctx, agent.UID)
		if errors.Is(err, errNotFound) {
			if agent.Name == "" || agent.OwnerTenantID == "" || agent.OwnerObjectID == "" || agent.AgentIdentityClientID == "" {
				return nil
			}
			record = &AuthorizationRecord{
				InstanceUID:           agent.UID,
				InstanceName:          agent.Name,
				OwnerTenantID:         agent.OwnerTenantID,
				OwnerObjectID:         agent.OwnerObjectID,
				AgentIdentityClientID: agent.AgentIdentityClientID,
			}
		} else if err != nil {
			return err
		}
		revokeAuthorizationRecord(record)
		if err := k.putRecord(ctx, record, version); errors.Is(err, errConflict) {
			continue
		} else {
			return err
		}
	}
	return errConflict
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
