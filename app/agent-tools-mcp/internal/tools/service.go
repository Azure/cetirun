// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/brokerclient"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/config"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/identity"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/tools/providers"
)

type Service struct {
	kubernetes kubernetesClient
	broker     brokerClient
	portalURL  string
	operations *operations
	providers  providerClient
}

type kubernetesClient interface {
	Authenticate(context.Context, string) (identity.Caller, error)
	Get(context.Context, string, any) error
	Create(context.Context, string, any, any) error
	Update(context.Context, string, any, any) error
}

type brokerClient interface {
	List(context.Context, string) (brokerclient.ListResponse, error)
	Check(context.Context, brokerclient.Request) (brokerclient.CheckResponse, error)
	Issue(context.Context, brokerclient.Request) (brokerclient.IssueResponse, error)
}

type providerClient interface {
	Execute(context.Context, string, string, map[string]any, brokerclient.Constraints, string) (map[string]any, error)
}

type ToolError struct{ Code string }

func (err *ToolError) Error() string { return err.Code }

func New(kubernetes *identity.Kubernetes, broker *brokerclient.Client, cfg config.Config) *Service {
	portal := ""
	if cfg.PortalBaseURL != nil {
		portal = cfg.PortalBaseURL.String()
	}
	return &Service{kubernetes: kubernetes, broker: broker, portalURL: portal, operations: &operations{kubernetes: kubernetes, namespace: cfg.OperationRecordsNamespace}, providers: providers.New(nil)}
}

func (s *Service) Authenticate(ctx context.Context, bearer string) (identity.Caller, error) {
	return s.kubernetes.Authenticate(ctx, bearer)
}

func (s *Service) List(ctx context.Context, caller identity.Caller) ([]Spec, error) {
	result, err := s.broker.List(ctx, caller.Proof)
	if err != nil {
		return nil, brokerError(err)
	}
	if result.InstanceUID != caller.InstanceUID || result.CatalogVersion != brokerclient.CatalogVersion {
		return nil, &ToolError{Code: "authorization_changed"}
	}
	allowed := map[string]bool{}
	for _, tool := range result.Tools {
		allowed[tool] = true
	}
	visible := []Spec{}
	for _, spec := range Catalog() {
		if allowed[spec.Name] {
			visible = append(visible, spec)
		}
	}
	return visible, nil
}

func (s *Service) Call(ctx context.Context, caller identity.Caller, tool string, arguments map[string]any) (map[string]any, error) {
	if tool == "connections_status" {
		result, err := s.broker.List(ctx, caller.Proof)
		if err != nil {
			return nil, brokerError(err)
		}
		if result.InstanceUID != caller.InstanceUID || result.CatalogVersion != brokerclient.CatalogVersion {
			return nil, &ToolError{Code: "authorization_changed"}
		}
		connections := []any{}
		for _, connection := range result.Connections {
			connections = append(connections, map[string]any{"id": connection.ID, "provider": connection.Provider, "account_display": connection.AccountDisplay, "capabilities": connection.Capabilities, "status": connection.Status, "created_at": connection.CreatedAt})
		}
		return map[string]any{"connections": connections}, nil
	}
	if tool == "connections_start_link" {
		if s.portalURL == "" {
			return nil, &ToolError{Code: "dependency_unavailable"}
		}
		return map[string]any{"actionUrl": s.portalURL}, nil
	}
	connectionID, _ := arguments["connection_id"].(string)
	check, err := s.broker.Check(ctx, brokerclient.NewRequest(caller.Proof, tool, connectionID, arguments, nil, nil))
	if err != nil {
		return nil, brokerError(err)
	}
	if !validAuthorization(check, caller, tool, connectionID) {
		return nil, &ToolError{Code: "authorization_changed"}
	}
	grantRevision, connectionRevision := check.Grant.Revision, check.Connection.Revision
	issue, err := s.broker.Issue(ctx, brokerclient.NewRequest(caller.Proof, tool, connectionID, arguments, &grantRevision, &connectionRevision))
	if err != nil {
		return nil, brokerError(err)
	}
	if !validAuthorization(issue.CheckResponse, caller, tool, connectionID) || !reflect.DeepEqual(check, issue.CheckResponse) || strings.TrimSpace(issue.AccessToken) == "" || expired(issue.ExpiresAt) {
		return nil, &ToolError{Code: "authorization_changed"}
	}

	if spec, ok := Find(tool); ok && spec.Write {
		operationID, _ := arguments["operation_id"].(string)
		existing, err := s.operations.Begin(ctx, caller.InstanceUID, connectionID, check.Grant.ID, tool, operationID, arguments)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
		result, executeErr := s.providers.Execute(ctx, check.Connection.Provider, tool, arguments, check.Grant.Constraints, issue.AccessToken)
		if executeErr != nil {
			if uncertainWrite(executeErr) {
				if err := s.operations.Finish(ctx, caller.InstanceUID, connectionID, check.Grant.ID, tool, operationID, arguments, "unknown", nil, "execution_unknown"); err != nil {
					return nil, &ToolError{Code: "execution_unknown"}
				}
				return nil, &ToolError{Code: "execution_unknown"}
			}
			code := toolCode(providerError(executeErr))
			if err := s.operations.Finish(ctx, caller.InstanceUID, connectionID, check.Grant.ID, tool, operationID, arguments, "failed", nil, code); err != nil {
				return nil, &ToolError{Code: "execution_unknown"}
			}
			return nil, &ToolError{Code: code}
		}
		if err := s.operations.Finish(ctx, caller.InstanceUID, connectionID, check.Grant.ID, tool, operationID, arguments, "succeeded", result, ""); err != nil {
			return nil, &ToolError{Code: "execution_unknown"}
		}
		return result, nil
	}
	result, err := s.providers.Execute(ctx, check.Connection.Provider, tool, arguments, check.Grant.Constraints, issue.AccessToken)
	if err != nil {
		return nil, providerError(err)
	}
	return result, nil
}

func brokerError(err error) error {
	if typed, ok := err.(*brokerclient.Error); ok {
		return &ToolError{Code: typed.Code}
	}
	return &ToolError{Code: "dependency_unavailable"}
}
func providerError(err error) error {
	if typed, ok := err.(*providers.Error); ok {
		return &ToolError{Code: typed.Code}
	}
	return &ToolError{Code: "dependency_unavailable"}
}
func toolCode(err error) string {
	if typed, ok := err.(*ToolError); ok {
		return safeOperationCode(typed.Code)
	}
	return "dependency_unavailable"
}
func uncertainWrite(err error) bool {
	typed, ok := err.(*providers.Error)
	return !ok || typed.Unknown || typed.Code == "dependency_unavailable"
}
func validAuthorization(check brokerclient.CheckResponse, caller identity.Caller, tool, connectionID string) bool {
	return check.ValidCatalog() == nil && connectionID != "" && check.InstanceUID == caller.InstanceUID && check.Connection.ID == connectionID && check.Connection.Status == "connected" && providerAllowed(tool, check.Connection.Provider) && containsTool(check.Connection.Capabilities, tool) && check.Grant.ID != "" && check.Grant.InstanceUID == caller.InstanceUID && check.Grant.ConnectionID == connectionID && containsTool(check.Grant.Tools, tool) && !expired(check.Grant.ExpiresAt)
}
func containsTool(tools []string, tool string) bool {
	for _, item := range tools {
		if item == tool {
			return true
		}
	}
	return false
}
func providerAllowed(tool, provider string) bool {
	switch {
	case strings.HasPrefix(tool, "azure_"):
		return provider == "azure"
	case strings.HasPrefix(tool, "github_"):
		return provider == "github"
	case strings.HasPrefix(tool, "mail_") || strings.HasPrefix(tool, "calendar_"):
		return provider == "microsoft" || provider == "google"
	default:
		return false
	}
}
func expired(value string) bool {
	expiresAt, err := time.Parse(time.RFC3339, value)
	return err != nil || !expiresAt.After(time.Now().Add(5*time.Second))
}

type operations struct {
	kubernetes operationKubernetes
	namespace  string
}
type operationKubernetes interface {
	Get(context.Context, string, any) error
	Create(context.Context, string, any, any) error
	Update(context.Context, string, any, any) error
}
type operationSecret struct {
	Metadata   operationMetadata `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
	StringData map[string]string `json:"stringData,omitempty"`
}
type operationMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}
type operationRecord struct {
	InstanceUID  string         `json:"instanceUid"`
	ConnectionID string         `json:"connectionId"`
	GrantID      string         `json:"grantId"`
	Tool         string         `json:"tool"`
	OperationID  string         `json:"operationId"`
	Digest       string         `json:"digest"`
	Status       string         `json:"status"`
	ExpiresAt    string         `json:"expiresAt"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	Result       map[string]any `json:"result,omitempty"`
}

func (o *operations) Begin(ctx context.Context, instanceUID, connectionID, grantID, tool, operationID string, arguments map[string]any) (map[string]any, error) {
	digest := operationDigest(arguments)
	name := operationName(instanceUID, connectionID, tool, operationID)
	record := operationRecord{InstanceUID: instanceUID, ConnectionID: connectionID, GrantID: grantID, Tool: tool, OperationID: operationID, Digest: digest, Status: "started", ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)}
	encoded, _ := json.Marshal(record)
	secret := operationSecret{Metadata: operationMetadata{Name: name, Namespace: o.namespace, Labels: map[string]string{"app.kubernetes.io/name": "agent-tools-mcp", "cetirun.microsoft.com/operation-record": "true"}}, StringData: map[string]string{"record.json": string(encoded)}}
	err := o.kubernetes.Create(ctx, "/api/v1/namespaces/"+o.namespace+"/secrets", secret, nil)
	if err == nil {
		return nil, nil
	}
	if !strings.Contains(err.Error(), "HTTP 409") {
		return nil, &ToolError{Code: "dependency_unavailable"}
	}
	existing, resourceVersion, err := o.load(ctx, name)
	if err != nil {
		return nil, &ToolError{Code: "dependency_unavailable"}
	}
	if existing.Digest != digest {
		return nil, &ToolError{Code: "invalid_request"}
	}
	if !existing.matches(instanceUID, connectionID, grantID, tool, operationID) {
		return nil, &ToolError{Code: "execution_unknown"}
	}
	switch existing.Status {
	case "succeeded":
		if expired(existing.ExpiresAt) && !terminalResult(existing.Result) {
			existing.Result = map[string]any{"status": "succeeded"}
			if err := o.save(ctx, name, existing, resourceVersion); err != nil {
				return nil, &ToolError{Code: "execution_unknown"}
			}
		}
		return existing.Result, nil
	case "failed":
		return nil, &ToolError{Code: safeOperationCode(existing.ErrorCode)}
	case "unknown":
		return nil, &ToolError{Code: "execution_unknown"}
	case "started":
		if expired(existing.ExpiresAt) {
			if err := o.Finish(ctx, instanceUID, connectionID, grantID, tool, operationID, arguments, "unknown", nil, "execution_unknown"); err != nil {
				return nil, &ToolError{Code: "execution_unknown"}
			}
		}
		return nil, &ToolError{Code: "execution_unknown"}
	}
	return nil, &ToolError{Code: "execution_unknown"}
}

func (o *operations) Finish(ctx context.Context, instanceUID, connectionID, grantID, tool, operationID string, arguments map[string]any, status string, result map[string]any, errorCode string) error {
	name := operationName(instanceUID, connectionID, tool, operationID)
	record, resourceVersion, err := o.load(ctx, name)
	if err != nil || record.Digest != operationDigest(arguments) || !record.matches(instanceUID, connectionID, grantID, tool, operationID) || record.Status != "started" {
		return errors.New("operation record changed")
	}
	record.Status, record.ErrorCode = status, ""
	if status == "succeeded" {
		record.Result = safeResult(result)
	} else {
		record.Result = nil
		record.ErrorCode = safeOperationCode(errorCode)
	}
	return o.save(ctx, name, record, resourceVersion)
}

func (o *operations) save(ctx context.Context, name string, record operationRecord, resourceVersion string) error {
	encoded, _ := json.Marshal(record)
	secret := operationSecret{Metadata: operationMetadata{Name: name, Namespace: o.namespace, ResourceVersion: resourceVersion, Labels: map[string]string{"app.kubernetes.io/name": "agent-tools-mcp", "cetirun.microsoft.com/operation-record": "true"}}, StringData: map[string]string{"record.json": string(encoded)}}
	if err := o.kubernetes.Update(ctx, "/api/v1/namespaces/"+o.namespace+"/secrets/"+name, secret, nil); err != nil {
		return errors.New("operation record update failed")
	}
	return nil
}

func (o *operations) load(ctx context.Context, name string) (operationRecord, string, error) {
	var secret operationSecret
	if err := o.kubernetes.Get(ctx, "/api/v1/namespaces/"+o.namespace+"/secrets/"+name, &secret); err != nil {
		return operationRecord{}, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(secret.Data["record.json"])
	if err != nil {
		return operationRecord{}, "", err
	}
	var record operationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return operationRecord{}, "", err
	}
	return record, secret.Metadata.ResourceVersion, nil
}

func operationDigest(arguments map[string]any) string {
	encoded, _ := json.Marshal(arguments)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func operationName(instanceUID, connectionID, tool, operationID string) string {
	sum := sha256.Sum256([]byte(instanceUID + "\x00" + connectionID + "\x00" + tool + "\x00" + operationID))
	return "agent-tool-operation-" + hex.EncodeToString(sum[:])[:32]
}
func (r operationRecord) matches(instanceUID, connectionID, grantID, tool, operationID string) bool {
	return r.InstanceUID == instanceUID && r.ConnectionID == connectionID && r.GrantID == grantID && r.Tool == tool && r.OperationID == operationID
}
func safeOperationCode(code string) string {
	allowed := map[string]bool{"permission_denied": true, "throttled": true, "invalid_request": true, "dependency_unavailable": true, "execution_unknown": true}
	if allowed[code] {
		return code
	}
	return "dependency_unavailable"
}
func safeResult(result map[string]any) map[string]any {
	output := map[string]any{"status": textValue(result["status"])}
	if output["status"] == "" {
		output["status"] = "succeeded"
	}
	if value, found := result["result"].(map[string]any); found {
		output["result"] = value
	}
	return output
}
func terminalResult(result map[string]any) bool {
	return len(result) == 1 && textValue(result["status"]) == "succeeded"
}
func textValue(value any) string { output, _ := value.(string); return output }
