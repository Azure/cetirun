// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package identity

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/config"
)

const maxKubernetesResponse = 1 << 20

type Kubernetes struct {
	baseURL   string
	tokenFile string
	client    *http.Client
	audience  string
}

type Caller struct {
	Proof       string
	InstanceUID string
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

type objectMeta struct {
	Name              string     `json:"name"`
	Namespace         string     `json:"namespace"`
	UID               string     `json:"uid"`
	ResourceVersion   string     `json:"resourceVersion"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
}

type pod struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		ServiceAccountName string `json:"serviceAccountName"`
	} `json:"spec"`
}

type serviceAccount struct {
	Metadata objectMeta `json:"metadata"`
}

type agent struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Suspended bool `json:"suspended"`
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
	Items []agent `json:"items"`
}

func NewKubernetes(cfg config.Config) (*Kubernetes, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("Kubernetes service environment is unavailable")
	}
	if _, err := os.Stat(cfg.KubernetesTokenFile); err != nil {
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
	return &Kubernetes{
		baseURL:   "https://" + host + ":" + port,
		tokenFile: cfg.KubernetesTokenFile,
		audience:  cfg.WorkloadTokenAudience,
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
		}}},
	}, nil
}

func (k *Kubernetes) Authenticate(ctx context.Context, bearer string) (Caller, error) {
	if strings.TrimSpace(bearer) == "" {
		return Caller{}, errors.New("missing workload token")
	}
	var review tokenReview
	review.APIVersion, review.Kind = "authentication.k8s.io/v1", "TokenReview"
	review.Spec.Token = bearer
	review.Spec.Audiences = []string{k.audience}
	if err := k.request(ctx, http.MethodPost, "/apis/authentication.k8s.io/v1/tokenreviews", review, &review); err != nil {
		return Caller{}, err
	}
	if !review.Status.Authenticated || !audienceMatches(review.Status.Audiences, k.audience) {
		return Caller{}, errors.New("workload token is not authenticated")
	}
	namespace, serviceAccountName, ok := serviceAccountIdentity(review.Status.User.Username)
	if !ok {
		return Caller{}, errors.New("workload token is not a ServiceAccount")
	}
	podName, podUID := extraValue(review.Status.User.Extra, "authentication.kubernetes.io/pod-name"), extraValue(review.Status.User.Extra, "authentication.kubernetes.io/pod-uid")
	serviceAccountUID := review.Status.User.UID
	if !dnsLabel(podName) || podUID == "" || serviceAccountUID == "" {
		return Caller{}, errors.New("workload token is not Pod bound")
	}
	var currentPod pod
	if err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+namespace+"/pods/"+podName, nil, &currentPod); err != nil || currentPod.Metadata.DeletionTimestamp != nil || currentPod.Metadata.UID != podUID || currentPod.Metadata.Namespace != namespace || currentPod.Spec.ServiceAccountName != serviceAccountName {
		return Caller{}, errors.New("workload Pod binding is invalid")
	}
	var currentServiceAccount serviceAccount
	if err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+namespace+"/serviceaccounts/"+serviceAccountName, nil, &currentServiceAccount); err != nil || currentServiceAccount.Metadata.DeletionTimestamp != nil || currentServiceAccount.Metadata.UID != serviceAccountUID || currentServiceAccount.Metadata.UID != review.Status.User.UID {
		return Caller{}, errors.New("workload ServiceAccount binding is invalid")
	}
	var agents agentList
	if err := k.request(ctx, http.MethodGet, "/apis/cetirun.microsoft.com/v1alpha1/agents", nil, &agents); err != nil {
		return Caller{}, errors.New("instance binding is unavailable")
	}
	var instanceUID string
	for _, item := range agents.Items {
		if item.Metadata.DeletionTimestamp != nil || item.Spec.Suspended || item.Status.Phase != "Ready" || item.Status.Namespace != namespace || instanceServiceName(item.Metadata.Name) != serviceAccountName || item.Metadata.UID == "" {
			continue
		}
		if instanceUID != "" {
			return Caller{}, errors.New("workload maps to multiple executable instances")
		}
		instanceUID = item.Metadata.UID
	}
	if instanceUID == "" {
		return Caller{}, errors.New("workload does not map to an executable instance")
	}
	return Caller{Proof: bearer, InstanceUID: instanceUID}, nil
}

func audienceMatches(audiences []string, expected string) bool {
	for _, audience := range audiences {
		if audience == expected && expected != "" {
			return true
		}
	}
	return false
}

func (k *Kubernetes) Get(ctx context.Context, path string, result any) error {
	return k.request(ctx, http.MethodGet, path, nil, result)
}

func (k *Kubernetes) Create(ctx context.Context, path string, value, result any) error {
	return k.request(ctx, http.MethodPost, path, value, result)
}

func (k *Kubernetes) Update(ctx context.Context, path string, value, result any) error {
	return k.request(ctx, http.MethodPut, path, value, result)
}

func (k *Kubernetes) request(ctx context.Context, method, path string, body any, result any) error {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return errors.New("invalid Kubernetes endpoint")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode Kubernetes request")
		}
		reader = bytes.NewReader(encoded)
	}
	token, err := os.ReadFile(k.tokenFile)
	if err != nil || strings.TrimSpace(string(token)) == "" {
		return errors.New("Kubernetes service account token is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, method, k.baseURL+path, reader)
	if err != nil {
		return errors.New("cannot create Kubernetes request")
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := k.client.Do(request)
	if err != nil {
		return errors.New("Kubernetes API is unavailable")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxKubernetesResponse+1))
	if err != nil || len(payload) > maxKubernetesResponse {
		return errors.New("cannot read Kubernetes response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Kubernetes API returned HTTP %d", response.StatusCode)
	}
	if result != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, result); err != nil {
			return errors.New("Kubernetes API returned invalid JSON")
		}
	}
	return nil
}

func serviceAccountIdentity(username string) (string, string, bool) {
	parts := strings.Split(username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || !dnsLabel(parts[2]) || !dnsLabel(parts[3]) {
		return "", "", false
	}
	return parts[2], parts[3], true
}

func extraValue(extra map[string][]string, key string) string {
	values := extra[key]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

// instanceServiceName mirrors the Agent chart fullname convention used by the Broker.
func instanceServiceName(instanceName string) string {
	name := "openclaw-" + instanceName + "-cetirun"
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func dnsLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}
