// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type armAuthorizationClient struct{ client *http.Client }

func newARMAuthorizationClient(timeout time.Duration) *armAuthorizationClient {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &armAuthorizationClient{client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// verify binds direct role assignments to their role definitions and checks
// the caller's effective ARM permissions using the Agent token. Both sources
// must permit every requested catalog action. Azure still applies deny
// assignments, conditions and revocation on every API.
func (c *armAuthorizationClient) verify(ctx context.Context, token, principalID string, subscriptions, requiredActions []string) ([]string, []string, error) {
	if !uuidPattern.MatchString(principalID) || len(subscriptions) == 0 || len(subscriptions) > 20 {
		return nil, nil, errDownstreamForbidden
	}
	if !validRequiredARMActions(requiredActions) {
		return nil, nil, errDownstreamForbidden
	}
	scopes, ids := []string{}, []string{}
	for _, sub := range uniqueStrings(subscriptions) {
		if !uuidPattern.MatchString(sub) {
			return nil, nil, errDownstreamForbidden
		}
		scope := "/subscriptions/" + sub
		// Require Azure's own assignment evidence for this exact Agent principal.
		// This first contract accepts direct, unconditional subscription grants;
		// inherited/group/conditional grants remain unverified.
		assignmentData, assignmentID, err := c.get(ctx, token, scope+"/providers/Microsoft.Authorization/roleAssignments?api-version=2022-04-01&$filter="+url.QueryEscape("principalId eq '"+principalID+"'"))
		if err != nil {
			return nil, nil, err
		}
		var assignments struct {
			Value []struct {
				Properties struct {
					PrincipalID      string `json:"principalId"`
					PrincipalType    string `json:"principalType"`
					Scope            string `json:"scope"`
					RoleDefinitionID string `json:"roleDefinitionId"`
					Condition        string `json:"condition"`
				} `json:"properties"`
			} `json:"value"`
			NextLink string `json:"nextLink"`
		}
		if json.Unmarshal(assignmentData, &assignments) != nil || assignments.NextLink != "" {
			return nil, nil, errors.New("incomplete role assignment evidence")
		}
		directPermissions := []armPermission{}
		for _, assignment := range assignments.Value {
			p := assignment.Properties
			if sameIdentityID(p.PrincipalID, principalID) && p.PrincipalType == "ServicePrincipal" && strings.EqualFold(p.Scope, scope) && p.Condition == "" && p.RoleDefinitionID != "" {
				// Effective permissions alone may come from an unrelated inherited
				// or group role. Prove the direct assignment itself permits reads.
				definitionPath := p.RoleDefinitionID
				roleID := definitionPath[strings.LastIndex(definitionPath, "/")+1:]
				if !uuidPattern.MatchString(roleID) ||
					(!strings.EqualFold(definitionPath, scope+"/providers/Microsoft.Authorization/roleDefinitions/"+roleID) &&
						!strings.EqualFold(definitionPath, "/providers/Microsoft.Authorization/roleDefinitions/"+roleID)) {
					continue
				}
				definitionData, definitionRequestID, err := c.get(ctx, token, definitionPath+"?api-version=2022-04-01")
				if err != nil {
					return nil, nil, err
				}
				var definition struct {
					ID         string `json:"id"`
					Properties struct {
						Permissions []armPermission `json:"permissions"`
					} `json:"properties"`
				}
				if json.Unmarshal(definitionData, &definition) != nil || !strings.EqualFold(definition.ID, definitionPath) {
					return nil, nil, errors.New("invalid role definition evidence")
				}
				directPermissions = append(directPermissions, definition.Properties.Permissions...)
				if definitionRequestID != "" {
					ids = append(ids, definitionRequestID)
				}
			}
		}
		if !permissionsAllowActions(directPermissions, requiredActions) {
			return nil, nil, errDownstreamForbidden
		}
		if assignmentID != "" {
			ids = append(ids, assignmentID)
		}
		data, requestID, err := c.get(ctx, token, scope+"/providers/Microsoft.Authorization/permissions?api-version=2022-04-01")
		if err != nil {
			return nil, nil, err
		}
		var permissions struct {
			Value    []armPermission `json:"value"`
			NextLink string          `json:"nextLink"`
		}
		if json.Unmarshal(data, &permissions) != nil || permissions.NextLink != "" {
			return nil, nil, errors.New("incomplete ARM permissions evidence")
		}
		if !permissionsAllowActions(permissions.Value, requiredActions) {
			return nil, nil, errDownstreamForbidden
		}
		if requestID != "" {
			ids = append(ids, requestID)
		}
		// Confirm an actual read operation in addition to the permission document.
		_, requestID, err = c.get(ctx, token, scope+"/resourcegroups?api-version=2021-04-01&$top=1")
		if err != nil {
			return nil, nil, err
		}
		if requestID != "" {
			ids = append(ids, requestID)
		}
		scopes = append(scopes, scope)
	}
	return scopes, ids, nil
}

type armPermission struct {
	Actions    []string `json:"actions"`
	NotActions []string `json:"notActions"`
}

func requiredARMActions(tools []string) ([]string, error) {
	if len(tools) == 0 {
		return nil, errors.New("Azure tools are required")
	}
	actions := []string{"*/read"}
	for _, tool := range tools {
		switch tool {
		case "azure_subscriptions_list", "azure_resource_groups_list", "azure_resources_search", "azure_resource_show":
		case "azure_resource_groups_create":
			actions = append(actions, "Microsoft.Resources/subscriptions/resourceGroups/write")
		case "azure_resource_group_tags_update":
			actions = append(actions, "Microsoft.Resources/tags/write")
		default:
			return nil, errors.New("unknown Azure tool")
		}
	}
	return uniqueStrings(actions), nil
}

func validRequiredARMActions(actions []string) bool {
	if len(actions) == 0 {
		return false
	}
	for _, action := range actions {
		if action != "*/read" && action != "Microsoft.Resources/subscriptions/resourceGroups/write" && action != "Microsoft.Resources/tags/write" {
			return false
		}
	}
	return contains(actions, "*/read")
}

func permissionsAllowActions(permissions []armPermission, requiredActions []string) bool {
	if len(permissions) == 0 || !validRequiredARMActions(requiredActions) {
		return false
	}
	for _, required := range requiredActions {
		allowed := false
		for _, permission := range permissions {
			if permissionAllowsAction(permission, required) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func permissionAllowsAction(permission armPermission, required string) bool {
	// The catalog's read contract intentionally requires all ARM reads. A
	// wildcard role with even one excluded read action cannot establish that
	// stronger property. A separate Reader permission can still satisfy this
	// requirement while this permission authorizes a concrete write action.
	if required == "*/read" {
		if len(permission.NotActions) != 0 {
			return false
		}
		for _, action := range permission.Actions {
			if action == "*" || strings.EqualFold(action, "*/read") {
				return true
			}
		}
		return false
	}
	for _, denied := range permission.NotActions {
		if armActionMatches(denied, required) {
			return false
		}
	}
	for _, action := range permission.Actions {
		if armActionMatches(action, required) {
			return true
		}
	}
	return false
}

func armActionMatches(pattern, action string) bool {
	pattern, action = strings.ToLower(pattern), strings.ToLower(action)
	expression := "^" + strings.Join(strings.Split(regexp.QuoteMeta(pattern), "\\*"), ".*") + "$"
	matched, err := regexp.MatchString(expression, action)
	return err == nil && matched
}

func (c *armAuthorizationClient) get(ctx context.Context, token, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://management.azure.com"+path, nil)
	if err != nil {
		return nil, "", errors.New("invalid ARM verification request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", errors.New("ARM verification is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return nil, "", errDownstreamForbidden
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", errors.New("ARM verification failed")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) {
		return nil, "", errors.New("invalid ARM verification evidence")
	}
	id := resp.Header.Get("x-ms-request-id")
	if len(id) > 128 || strings.ContainsAny(id, "\r\n") {
		id = ""
	}
	return body, id, nil
}
