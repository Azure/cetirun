// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/brokerclient"
)

const maxResponseBytes = 256 << 10
const maxMailBodyBytes = 64 << 10
const maxItems = 100

type Client struct{ http *http.Client }
type Error struct {
	Code    string
	Unknown bool
}

func (err *Error) Error() string { return err.Code }

func New(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	configured := *client
	configured.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &configured}
}

func (c *Client) Execute(ctx context.Context, provider, tool string, arguments map[string]any, constraints brokerclient.Constraints, accessToken string) (map[string]any, error) {
	switch provider {
	case "azure":
		return c.azure(ctx, tool, arguments, constraints, accessToken)
	case "github":
		return c.github(ctx, tool, arguments, constraints, accessToken)
	case "microsoft":
		return c.microsoft(ctx, tool, arguments, constraints, accessToken)
	case "google":
		return c.google(ctx, tool, arguments, constraints, accessToken)
	default:
		return nil, &Error{Code: "permission_denied"}
	}
}

func (c *Client) azure(ctx context.Context, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	base := "https://management.azure.com"
	switch tool {
	case "azure_subscriptions_list":
		var reply struct {
			Value    []map[string]any `json:"value"`
			NextLink string           `json:"nextLink"`
		}
		if err := c.request(ctx, http.MethodGet, base+"/subscriptions?api-version=2022-12-01&$top=100", nil, token, &reply); err != nil {
			return nil, err
		}
		items := []any{}
		for _, item := range reply.Value {
			if contains(limits.SubscriptionIDs, text(item["subscriptionId"])) {
				items = append(items, pick(item, "subscriptionId", "displayName", "state", "tenantId"))
			}
		}
		return listed(items, pageTruncated(len(reply.Value), reply.NextLink != "", false)), nil
	case "azure_resource_groups_list":
		subscription := stringArg(args, "subscription_id")
		if !contains(limits.SubscriptionIDs, subscription) {
			return nil, denied()
		}
		var reply struct {
			Value    []map[string]any `json:"value"`
			NextLink string           `json:"nextLink"`
		}
		if err := c.request(ctx, http.MethodGet, base+"/subscriptions/"+url.PathEscape(subscription)+"/resourcegroups?api-version=2021-04-01&$top=100", nil, token, &reply); err != nil {
			return nil, err
		}
		return listed(pickSlice(reply.Value, "id", "name", "location"), pageTruncated(len(reply.Value), reply.NextLink != "", false)), nil
	case "azure_resources_search":
		subscription := stringArg(args, "subscription_id")
		if !contains(limits.SubscriptionIDs, subscription) {
			return nil, denied()
		}
		query := "Resources | where subscriptionId == '" + strings.ToLower(subscription) + "'"
		if resourceType, ok := args["resource_type"].(string); ok {
			query += " | where type =~ '" + resourceType + "'"
		}
		if prefix, ok := args["name_prefix"].(string); ok {
			query += " | where name startswith '" + prefix + "'"
		}
		query += " | project id, name, type, location, resourceGroup, subscriptionId | limit 100"
		body := map[string]any{"subscriptions": []string{subscription}, "query": query, "options": map[string]any{"$top": maxItems}}
		var reply struct {
			Data            []map[string]any `json:"data"`
			ResultTruncated any              `json:"resultTruncated"`
			SkipToken       string           `json:"$skipToken"`
		}
		if err := c.request(ctx, http.MethodPost, base+"/providers/Microsoft.ResourceGraph/resources?api-version=2021-03-01", body, token, &reply); err != nil {
			return nil, err
		}
		return listed(pickSlice(reply.Data, "id", "name", "type", "location", "resourceGroup", "subscriptionId"), pageTruncated(len(reply.Data), reply.SkipToken != "", boolValue(reply.ResultTruncated))), nil
	case "azure_resource_show":
		resourceID := stringArg(args, "resource_id")
		subscription, ok := canonicalAzureResourceID(resourceID)
		if !ok || !contains(limits.SubscriptionIDs, subscription) {
			return nil, denied()
		}
		query := "Resources | where id =~ '" + strings.ReplaceAll(resourceID, "'", "''") + "' | project id, name, type, location, kind, tags | limit 1"
		body := map[string]any{"subscriptions": []string{subscription}, "query": query, "options": map[string]any{"$top": 1}}
		var reply struct {
			Data []map[string]any `json:"data"`
		}
		if err := c.request(ctx, http.MethodPost, base+"/providers/Microsoft.ResourceGraph/resources?api-version=2021-03-01", body, token, &reply); err != nil {
			return nil, err
		}
		if len(reply.Data) != 1 || !strings.EqualFold(text(reply.Data[0]["id"]), resourceID) {
			return nil, denied()
		}
		return map[string]any{"resource": pick(reply.Data[0], "id", "name", "type", "location", "kind", "tags")}, nil
	default:
		return nil, denied()
	}
}

func (c *Client) github(ctx context.Context, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	base, repository := "https://api.github.com", stringArg(args, "repository_id")
	if !safeGitHubRepositoryID(repository) || !contains(limits.RepositoryIDs, repository) {
		return nil, denied()
	}
	if tool == "github_contents_get" && !safeGitHubPath(stringArg(args, "path")) {
		return nil, &Error{Code: "invalid_request"}
	}
	header := map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	canonicalPath, resolved, err := c.githubRepository(ctx, base, repository, token, header)
	if err != nil {
		return nil, err
	}
	switch tool {
	case "github_repositories_get":
		var reply map[string]any
		if err := c.requestHeaders(ctx, http.MethodGet, base+canonicalPath, nil, token, header, &reply); err != nil {
			return nil, err
		}
		if text(reply["id"]) != repository {
			return nil, denied()
		}
		if text(reply["full_name"]) != text(resolved["full_name"]) {
			return nil, denied()
		}
		return map[string]any{"repository": pick(reply, "id", "full_name", "description", "private", "default_branch", "html_url", "updated_at")}, nil
	case "github_contents_get":
		contentPath := stringArg(args, "path")
		value := base + canonicalPath + "/contents/" + escapeGitHubPath(contentPath)
		if ref, ok := args["ref"].(string); ok {
			value += "?ref=" + url.QueryEscape(ref)
		}
		var reply map[string]any
		if err := c.requestHeaders(ctx, http.MethodGet, value, nil, token, header, &reply); err != nil {
			return nil, err
		}
		content, err := decodeGitHubContent(reply)
		if err != nil {
			return nil, &Error{Code: "invalid_request"}
		}
		return map[string]any{"path": text(reply["path"]), "sha": text(reply["sha"]), "content": content, "truncated": false}, nil
	case "github_issues_list", "github_pull_requests_list":
		resource := "/issues"
		if tool == "github_pull_requests_list" {
			resource = "/pulls"
		}
		state := "open"
		if selected, ok := args["state"].(string); ok {
			state = selected
		}
		var reply []map[string]any
		var responseHeaders http.Header
		if err := c.requestHeadersResponse(ctx, http.MethodGet, base+canonicalPath+resource+"?state="+url.QueryEscape(state)+"&per_page=100", nil, token, header, &reply, &responseHeaders); err != nil {
			return nil, err
		}
		return listed(pickSlice(reply, "number", "title", "state", "html_url", "created_at", "updated_at", "user"), pageTruncated(len(reply), githubHasNextPage(responseHeaders), false)), nil
	case "github_issues_create":
		body := map[string]any{"title": stringArg(args, "title")}
		if value, ok := args["body"].(string); ok {
			body["body"] = value
		}
		var reply map[string]any
		if err := c.requestHeaders(ctx, http.MethodPost, base+canonicalPath+"/issues", body, token, header, &reply); err != nil {
			return nil, err
		}
		return writeResult(reply, "number", "html_url", "state")
	case "github_comments_create":
		number := integerArg(args, "issue_number")
		var reply map[string]any
		if err := c.requestHeaders(ctx, http.MethodPost, base+canonicalPath+"/issues/"+strconv.FormatInt(number, 10)+"/comments", map[string]any{"body": stringArg(args, "body")}, token, header, &reply); err != nil {
			return nil, err
		}
		return writeResult(reply, "id", "html_url", "created_at")
	case "github_pull_requests_create":
		baseBranch := stringArg(args, "base_branch")
		if len(limits.BaseBranches) > 0 && !contains(limits.BaseBranches, baseBranch) {
			return nil, denied()
		}
		body := map[string]any{"title": stringArg(args, "title"), "head": stringArg(args, "head"), "base": baseBranch}
		if value, ok := args["body"].(string); ok {
			body["body"] = value
		}
		var reply map[string]any
		if err := c.requestHeaders(ctx, http.MethodPost, base+canonicalPath+"/pulls", body, token, header, &reply); err != nil {
			return nil, err
		}
		return writeResult(reply, "number", "html_url", "state")
	default:
		return nil, denied()
	}
}

func (c *Client) microsoft(ctx context.Context, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	return c.graph(ctx, "https://graph.microsoft.com/v1.0", tool, args, limits, token)
}

func (c *Client) google(ctx context.Context, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	if strings.HasPrefix(tool, "mail_") {
		return c.gmail(ctx, tool, args, limits, token)
	}
	return c.googleCalendar(ctx, tool, args, limits, token)
}

func (c *Client) graph(ctx context.Context, base, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	switch tool {
	case "mail_messages_search":
		query := "$top=100&$select=id,subject,from,receivedDateTime,bodyPreview"
		filter := graphMailFilter(args)
		if filter != "" {
			query += "&$filter=" + url.QueryEscape(filter)
		}
		var reply struct {
			Value    []map[string]any `json:"value"`
			NextLink string           `json:"@odata.nextLink"`
		}
		if err := c.request(ctx, http.MethodGet, base+"/me/messages?"+query, nil, token, &reply); err != nil {
			return nil, err
		}
		return listed(pickSlice(reply.Value, "id", "subject", "from", "receivedDateTime", "bodyPreview"), pageTruncated(len(reply.Value), reply.NextLink != "", false)), nil
	case "mail_messages_get":
		var reply map[string]any
		if err := c.request(ctx, http.MethodGet, base+"/me/messages/"+url.PathEscape(stringArg(args, "message_id"))+"?$select=id,subject,from,toRecipients,receivedDateTime,bodyPreview,body", nil, token, &reply); err != nil {
			return nil, err
		}
		return map[string]any{"message": pick(reply, "id", "subject", "from", "toRecipients", "receivedDateTime", "bodyPreview", "body")}, nil
	case "mail_send":
		if !allowedRecipients(args, limits) {
			return nil, denied()
		}
		body := map[string]any{"message": map[string]any{"subject": stringArg(args, "subject"), "body": map[string]string{"contentType": "Text", "content": stringArg(args, "body")}, "toRecipients": graphRecipients(args, "to"), "ccRecipients": graphRecipients(args, "cc"), "bccRecipients": graphRecipients(args, "bcc")}, "saveToSentItems": true}
		if err := c.request(ctx, http.MethodPost, base+"/me/sendMail", body, token, nil); err != nil {
			return nil, err
		}
		return map[string]any{"status": "accepted"}, nil
	case "calendar_events_list":
		calendar := stringArg(args, "calendar_id")
		if !contains(limits.CalendarIDs, calendar) {
			return nil, denied()
		}
		query := "startDateTime=" + url.QueryEscape(stringArg(args, "start")) + "&endDateTime=" + url.QueryEscape(stringArg(args, "end")) + "&$top=100&$select=id,subject,start,end,organizer,attendees,webLink"
		var reply struct {
			Value    []map[string]any `json:"value"`
			NextLink string           `json:"@odata.nextLink"`
		}
		if err := c.request(ctx, http.MethodGet, base+"/me/calendars/"+url.PathEscape(calendar)+"/calendarView?"+query, nil, token, &reply); err != nil {
			return nil, err
		}
		return listed(pickSlice(reply.Value, "id", "subject", "start", "end", "organizer", "attendees", "webLink"), pageTruncated(len(reply.Value), reply.NextLink != "", false)), nil
	case "calendar_events_create":
		calendar := stringArg(args, "calendar_id")
		if !contains(limits.CalendarIDs, calendar) || (!limits.AllowInvites && hasArray(args, "attendees")) || (hasArray(args, "attendees") && !allowedRecipients(args, limits)) {
			return nil, denied()
		}
		body := map[string]any{"subject": stringArg(args, "subject"), "start": graphDate(stringArg(args, "start")), "end": graphDate(stringArg(args, "end")), "attendees": graphRecipients(args, "attendees")}
		if value, ok := args["body"].(string); ok {
			body["body"] = map[string]string{"contentType": "Text", "content": value}
		}
		var reply map[string]any
		if err := c.request(ctx, http.MethodPost, base+"/me/calendars/"+url.PathEscape(calendar)+"/events", body, token, &reply); err != nil {
			return nil, err
		}
		return writeResult(reply, "id", "webLink", "subject")
	default:
		return nil, denied()
	}
}

func (c *Client) gmail(ctx context.Context, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	base := "https://gmail.googleapis.com/gmail/v1/users/me"
	switch tool {
	case "mail_messages_search":
		query := gmailQuery(args)
		var reply struct {
			Messages      []map[string]any `json:"messages"`
			NextPageToken string           `json:"nextPageToken"`
		}
		endpoint := base + "/messages?maxResults=100"
		if query != "" {
			endpoint += "&q=" + url.QueryEscape(query)
		}
		if err := c.request(ctx, http.MethodGet, endpoint, nil, token, &reply); err != nil {
			return nil, err
		}
		items := []any{}
		for _, item := range reply.Messages {
			items = append(items, pick(item, "id", "threadId"))
		}
		return listed(items, pageTruncated(len(reply.Messages), reply.NextPageToken != "", false)), nil
	case "mail_messages_get":
		var reply map[string]any
		if err := c.request(ctx, http.MethodGet, base+"/messages/"+url.PathEscape(stringArg(args, "message_id"))+"?format=full", nil, token, &reply); err != nil {
			return nil, err
		}
		message := pick(reply, "id", "threadId", "labelIds", "snippet", "internalDate")
		payload, _ := reply["payload"].(map[string]any)
		for key, value := range gmailHeaders(payload) {
			message[key] = value
		}
		body, err := gmailTextBody(payload)
		if err != nil {
			return nil, &Error{Code: "dependency_unavailable"}
		}
		message["body"] = body
		return map[string]any{"message": message}, nil
	case "mail_send":
		if !allowedRecipients(args, limits) {
			return nil, denied()
		}
		raw, err := gmailMessage(args)
		if err != nil {
			return nil, &Error{Code: "invalid_request"}
		}
		var reply map[string]any
		if err := c.request(ctx, http.MethodPost, base+"/messages/send", map[string]string{"raw": raw}, token, &reply); err != nil {
			return nil, err
		}
		return writeResult(reply, "id", "threadId", "labelIds")
	default:
		return nil, denied()
	}
}

func (c *Client) googleCalendar(ctx context.Context, tool string, args map[string]any, limits brokerclient.Constraints, token string) (map[string]any, error) {
	calendar := stringArg(args, "calendar_id")
	if !contains(limits.CalendarIDs, calendar) {
		return nil, denied()
	}
	base := "https://www.googleapis.com/calendar/v3/calendars/" + url.PathEscape(calendar) + "/events"
	switch tool {
	case "calendar_events_list":
		endpoint := base + "?singleEvents=true&orderBy=startTime&maxResults=100&timeMin=" + url.QueryEscape(stringArg(args, "start")) + "&timeMax=" + url.QueryEscape(stringArg(args, "end"))
		var reply struct {
			Items         []map[string]any `json:"items"`
			NextPageToken string           `json:"nextPageToken"`
		}
		if err := c.request(ctx, http.MethodGet, endpoint, nil, token, &reply); err != nil {
			return nil, err
		}
		return listed(pickSlice(reply.Items, "id", "summary", "start", "end", "organizer", "attendees", "htmlLink"), pageTruncated(len(reply.Items), reply.NextPageToken != "", false)), nil
	case "calendar_events_create":
		if (!limits.AllowInvites && hasArray(args, "attendees")) || (hasArray(args, "attendees") && !allowedRecipients(args, limits)) {
			return nil, denied()
		}
		body := map[string]any{"summary": stringArg(args, "subject"), "start": map[string]string{"dateTime": stringArg(args, "start")}, "end": map[string]string{"dateTime": stringArg(args, "end")}, "attendees": googleAttendees(args)}
		if value, ok := args["body"].(string); ok {
			body["description"] = value
		}
		var reply map[string]any
		if err := c.request(ctx, http.MethodPost, base+"?sendUpdates=all", body, token, &reply); err != nil {
			return nil, err
		}
		return writeResult(reply, "id", "htmlLink", "summary")
	default:
		return nil, denied()
	}
}

func (c *Client) request(ctx context.Context, method, endpoint string, body any, token string, output any) error {
	return c.requestHeaders(ctx, method, endpoint, body, token, nil, output)
}
func (c *Client) requestHeaders(ctx context.Context, method, endpoint string, body any, token string, headers map[string]string, output any) error {
	return c.requestHeadersResponse(ctx, method, endpoint, body, token, headers, output, nil)
}
func (c *Client) requestHeadersResponse(ctx context.Context, method, endpoint string, body any, token string, headers map[string]string, output any, responseHeaders *http.Header) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &Error{Code: "invalid_request"}
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return &Error{Code: "dependency_unavailable", Unknown: method != http.MethodGet}
	}
	defer response.Body.Close()
	if responseHeaders != nil {
		*responseHeaders = response.Header.Clone()
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return &Error{Code: "dependency_unavailable", Unknown: method != http.MethodGet}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return &Error{Code: "throttled"}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return &Error{Code: "permission_denied"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{Code: "dependency_unavailable", Unknown: method != http.MethodGet}
	}
	if output != nil {
		if len(payload) == 0 {
			return &Error{Code: "dependency_unavailable", Unknown: method != http.MethodGet}
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		if err := decoder.Decode(output); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return &Error{Code: "dependency_unavailable", Unknown: method != http.MethodGet}
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func denied() error { return &Error{Code: "permission_denied"} }
func text(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	case float64:
		return strconv.FormatInt(int64(item), 10)
	default:
		return ""
	}
}
func stringArg(args map[string]any, key string) string { value, _ := args[key].(string); return value }
func integerArg(args map[string]any, key string) int64 {
	value, _ := args[key].(json.Number)
	integer, _ := value.Int64()
	return integer
}
func pick(input map[string]any, keys ...string) map[string]any {
	output := map[string]any{}
	for _, key := range keys {
		if value, found := input[key]; found {
			output[key] = value
		}
	}
	return output
}
func pickSlice(input []map[string]any, keys ...string) []any {
	output := make([]any, 0, len(input))
	for _, item := range input {
		output = append(output, pick(item, keys...))
	}
	return output
}
func listed(items []any, truncated bool) map[string]any {
	if len(items) > maxItems {
		items, truncated = items[:maxItems], true
	}
	return map[string]any{"items": items, "truncated": truncated, "next_cursor": nil}
}
func writeResult(reply map[string]any, keys ...string) (map[string]any, error) {
	result := pick(reply, keys...)
	if len(reply) == 0 || len(result) == 0 || text(reply[keys[0]]) == "" {
		return nil, &Error{Code: "dependency_unavailable", Unknown: true}
	}
	return map[string]any{"status": "succeeded", "result": result}, nil
}

func canonicalAzureResourceID(resourceID string) (string, bool) {
	parts := strings.Split(resourceID, "/")
	if len(parts) < 7 || parts[0] != "" || !strings.EqualFold(parts[1], "subscriptions") || !safeARMIDSegment(parts[2]) {
		return "", false
	}
	providers := 3
	if len(parts) > 3 && strings.EqualFold(parts[3], "resourceGroups") {
		if len(parts) < 9 || !safeARMIDSegment(parts[4]) {
			return "", false
		}
		providers = 5
	}
	if len(parts) < providers+4 || !strings.EqualFold(parts[providers], "providers") || !safeARMIDSegment(parts[providers+1]) || (len(parts)-(providers+2))%2 != 0 {
		return "", false
	}
	for _, part := range parts[1:] {
		if !safeARMIDSegment(part) {
			return "", false
		}
	}
	return parts[2], true
}
func safeARMIDSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._~-:", character)) {
			return false
		}
	}
	return true
}
func escapeGitHubPath(path string) string {
	parts := strings.Split(path, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}
func safeGitHubPath(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
func decodeGitHubContent(reply map[string]any) (string, error) {
	if text(reply["encoding"]) != "base64" {
		return "", errors.New("unexpected content encoding")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(text(reply["content"]), "\n", ""))
	if err != nil || len(decoded) > maxResponseBytes {
		return "", errors.New("invalid content")
	}
	return string(decoded), nil
}
func (c *Client) githubRepository(ctx context.Context, base, repository, token string, headers map[string]string) (string, map[string]any, error) {
	var reply map[string]any
	if err := c.requestHeaders(ctx, http.MethodGet, base+"/repositories/"+url.PathEscape(repository), nil, token, headers, &reply); err != nil {
		return "", nil, err
	}
	fullName := text(reply["full_name"])
	if text(reply["id"]) != repository || !safeGitHubRepository(fullName) {
		return "", nil, denied()
	}
	parts := strings.Split(fullName, "/")
	return "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), reply, nil
}
func safeGitHubRepository(fullName string) bool {
	parts := strings.Split(fullName, "/")
	return len(parts) == 2 && safeGitHubPath(parts[0]) && safeGitHubPath(parts[1])
}
func safeGitHubRepositoryID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
func pageTruncated(count int, hasNext, providerTruncated bool) bool {
	return providerTruncated || hasNext || count >= maxItems
}
func boolValue(value any) bool {
	if boolean, ok := value.(bool); ok {
		return boolean
	}
	textValue := strings.TrimSpace(strings.ToLower(text(value)))
	return textValue == "true"
}
func githubHasNextPage(headers http.Header) bool {
	for _, value := range headers.Values("Link") {
		for _, link := range strings.Split(value, ",") {
			if strings.Contains(link, "rel=\"next\"") || strings.Contains(link, "rel=next") {
				return true
			}
		}
	}
	return false
}
func hasArray(args map[string]any, key string) bool {
	values, ok := args[key].([]any)
	return ok && len(values) > 0
}
func emails(args map[string]any, key string) []string {
	values, _ := args[key].([]any)
	output := make([]string, 0, len(values))
	for _, value := range values {
		if address, ok := value.(string); ok {
			output = append(output, strings.ToLower(address))
		}
	}
	return output
}
func allowedRecipients(args map[string]any, limits brokerclient.Constraints) bool {
	for _, key := range []string{"to", "cc", "bcc", "attendees"} {
		for _, address := range emails(args, key) {
			at := strings.LastIndex(address, "@")
			if at <= 0 || (!contains(limits.RecipientAddresses, address) && !contains(limits.RecipientDomains, address[at+1:])) {
				return false
			}
		}
	}
	return true
}
func graphRecipients(args map[string]any, key string) []map[string]any {
	output := []map[string]any{}
	for _, address := range emails(args, key) {
		output = append(output, map[string]any{"emailAddress": map[string]string{"address": address}})
	}
	return output
}
func googleAttendees(args map[string]any) []map[string]string {
	output := []map[string]string{}
	for _, address := range emails(args, "attendees") {
		output = append(output, map[string]string{"email": address})
	}
	return output
}
func graphDate(value string) map[string]string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return map[string]string{"dateTime": value, "timeZone": "UTC"}
	}
	return map[string]string{"dateTime": parsed.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"}
}
func graphMailFilter(args map[string]any) string {
	values := []string{}
	if subject, ok := args["subject"].(string); ok {
		values = append(values, "contains(subject,'"+strings.ReplaceAll(subject, "'", "''")+"')")
	}
	if from, ok := args["from"].(string); ok {
		values = append(values, "from/emailAddress/address eq '"+strings.ReplaceAll(from, "'", "''")+"'")
	}
	if start, ok := args["start"].(string); ok {
		values = append(values, "receivedDateTime ge "+start)
	}
	if end, ok := args["end"].(string); ok {
		values = append(values, "receivedDateTime le "+end)
	}
	return strings.Join(values, " and ")
}
func gmailQuery(args map[string]any) string {
	values := []string{}
	if subject, ok := args["subject"].(string); ok {
		values = append(values, "subject:"+strconv.Quote(subject))
	}
	if from, ok := args["from"].(string); ok {
		values = append(values, "from:"+from)
	}
	if start, ok := args["start"].(string); ok {
		values = append(values, "after:"+start[:10])
	}
	if end, ok := args["end"].(string); ok {
		values = append(values, "before:"+end[:10])
	}
	return strings.Join(values, " ")
}
func gmailHeaders(payload map[string]any) map[string]any {
	output := map[string]any{}
	headers, _ := payload["headers"].([]any)
	decoder := new(mime.WordDecoder)
	for _, header := range headers {
		value, _ := header.(map[string]any)
		name, content := strings.ToLower(text(value["name"])), text(value["value"])
		if name != "subject" && name != "from" && name != "to" {
			continue
		}
		if decoded, err := decoder.DecodeHeader(content); err == nil {
			content = decoded
		}
		output[name] = content
	}
	return output
}
func gmailTextBody(payload map[string]any) (string, error) {
	if strings.EqualFold(strings.Split(text(payload["mimeType"]), ";")[0], "text/plain") && text(payload["filename"]) == "" {
		body, _ := payload["body"].(map[string]any)
		data := text(body["data"])
		if data != "" {
			decoded, err := base64.RawURLEncoding.DecodeString(data)
			if err != nil {
				decoded, err = base64.URLEncoding.DecodeString(data)
			}
			if err != nil || len(decoded) > maxMailBodyBytes {
				return "", errors.New("invalid mail body")
			}
			return string(decoded), nil
		}
	}
	parts, _ := payload["parts"].([]any)
	for _, part := range parts {
		value, _ := part.(map[string]any)
		body, err := gmailTextBody(value)
		if err != nil {
			return "", err
		}
		if body != "" {
			return body, nil
		}
	}
	return "", nil
}
func gmailMessage(args map[string]any) (string, error) {
	subject := stringArg(args, "subject")
	if strings.ContainsAny(subject, "\r\n") {
		return "", errors.New("invalid subject")
	}
	lines := []string{"To: " + strings.Join(emails(args, "to"), ", ")}
	if cc := emails(args, "cc"); len(cc) > 0 {
		lines = append(lines, "Cc: "+strings.Join(cc, ", "))
	}
	if bcc := emails(args, "bcc"); len(bcc) > 0 {
		lines = append(lines, "Bcc: "+strings.Join(bcc, ", "))
	}
	lines = append(lines, "Subject: "+mime.QEncoding.Encode("UTF-8", subject), "MIME-Version: 1.0", "Content-Type: text/plain; charset=utf-8", "", stringArg(args, "body"))
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(lines, "\r\n"))), nil
}
