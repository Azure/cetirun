// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

const maxItems = 100

type Spec struct {
	Name        string
	Description string
	InputSchema map[string]any
	Write       bool
}

func Catalog() []Spec {
	connection := field("connection_id", stringSchema(1, 128, ""))
	operation := field("operation_id", stringSchema(8, 128, `^[A-Za-z0-9][A-Za-z0-9._-]*$`))
	repository := field("repository_id", stringSchema(1, 32, `^[0-9]+$`))
	calendar := field("calendar_id", stringSchema(1, 256, ""))
	azureSubscription := field("subscription_id", stringSchema(1, 64, `^[0-9a-fA-F-]+$`))
	resourceGroup := field("resource_group_name", stringSchema(1, 90, `^[A-Za-z0-9](?:[A-Za-z0-9_.()\-]*[A-Za-z0-9_()\-])?$`))
	location := field("location", stringSchema(2, 64, `^[A-Za-z0-9]+$`))
	tags := field("tags", mapSchema(1, 50, stringSchema(1, 256, `^[^\r\n]+$`)))
	return []Spec{
		{"connections_status", "List this instance's authorized connections without credentials.", object(nil), false},
		{"connections_start_link", "Return the configured Portal page for connecting accounts and managing grants.", object(nil), false},
		{"azure_subscriptions_list", "List authorized Azure subscriptions.", object([]property{connection}), false},
		{"azure_resource_groups_list", "List resource groups in one authorized subscription.", object([]property{connection, azureSubscription}), false},
		{"azure_resources_search", "Search authorized Azure resources using fixed structured filters.", object([]property{connection, azureSubscription, optional("resource_type", stringSchema(1, 160, `^[A-Za-z0-9./]+$`)), optional("name_prefix", stringSchema(1, 128, `^[A-Za-z0-9._ -]+$`))}), false},
		{"azure_resource_show", "Get one Azure resource by a canonical resource ID.", object([]property{connection, field("resource_id", stringSchema(16, 1024, `^/subscriptions/[0-9a-fA-F-]+/[A-Za-z0-9._~:/-]+$`))}), false},
		{"azure_resource_groups_create", "Create one pre-authorized Azure resource group. The resource group name and subscription must be granted explicitly.", object([]property{connection, azureSubscription, resourceGroup, location, operation}), true},
		{"azure_resource_group_tags_update", "Merge tags on one pre-authorized Azure resource group. The resource group name and subscription must be granted explicitly.", object([]property{connection, azureSubscription, resourceGroup, tags, operation}), true},
		{"github_repositories_get", "Get an authorized GitHub repository.", object([]property{connection, repository}), false},
		{"github_contents_get", "Get bounded text content from an authorized GitHub repository.", object([]property{connection, repository, field("path", stringSchema(1, 512, `^[^\\]+$`)), optional("ref", stringSchema(1, 256, ""))}), false},
		{"github_issues_list", "List issues from an authorized GitHub repository.", object([]property{connection, repository, optional("state", enumSchema("open", "closed", "all"))}), false},
		{"github_pull_requests_list", "List pull requests from an authorized GitHub repository.", object([]property{connection, repository, optional("state", enumSchema("open", "closed", "all"))}), false},
		{"github_issues_create", "Create an issue in an authorized GitHub repository.", object([]property{connection, repository, field("title", stringSchema(1, 256, "")), optional("body", stringSchema(0, 65536, "")), operation}), true},
		{"github_comments_create", "Create a comment on an issue or pull request in an authorized GitHub repository.", object([]property{connection, repository, field("issue_number", integerSchema(1, 1000000000)), field("body", stringSchema(1, 65536, "")), operation}), true},
		{"github_pull_requests_create", "Create a pull request from an existing branch without merging it.", object([]property{connection, repository, field("title", stringSchema(1, 256, "")), field("head", stringSchema(1, 256, `^[A-Za-z0-9._/-]+$`)), field("base_branch", stringSchema(1, 256, `^[A-Za-z0-9._/-]+$`)), optional("body", stringSchema(0, 65536, "")), operation}), true},
		{"mail_messages_search", "Search bounded messages in the connected mailbox.", object([]property{connection, optional("subject", stringSchema(1, 256, "")), optional("from", emailSchema()), optional("start", dateTimeSchema()), optional("end", dateTimeSchema())}), false},
		{"mail_messages_get", "Get one bounded message from the connected mailbox.", object([]property{connection, field("message_id", stringSchema(1, 512, ""))}), false},
		{"mail_send", "Send a plain text mail from the connected mailbox to authorized recipients.", object([]property{connection, field("to", emailArray()), optional("cc", emailArray()), optional("bcc", emailArray()), field("subject", stringSchema(1, 256, "")), field("body", stringSchema(1, 65536, "")), operation}), true},
		{"calendar_events_list", "List events in an authorized calendar over a bounded time window.", object([]property{connection, calendar, field("start", dateTimeSchema()), field("end", dateTimeSchema())}), false},
		{"calendar_events_create", "Create a non-recurring event in an authorized calendar.", object([]property{connection, calendar, field("start", dateTimeSchema()), field("end", dateTimeSchema()), field("subject", stringSchema(1, 256, "")), optional("body", stringSchema(0, 65536, "")), optional("attendees", emailArray()), operation}), true},
	}
}

type property struct {
	name     string
	schema   map[string]any
	required bool
}

func field(name string, schema map[string]any) property {
	return property{name: name, schema: schema, required: true}
}
func optional(name string, schema map[string]any) property {
	return property{name: name, schema: schema}
}

func object(properties []property) map[string]any {
	values, required := map[string]any{}, []string{}
	for _, property := range properties {
		values[property.name] = property.schema
		if property.required {
			required = append(required, property.name)
		}
	}
	return map[string]any{"type": "object", "properties": values, "required": required, "additionalProperties": false}
}

func stringSchema(minimum, maximum int, pattern string) map[string]any {
	schema := map[string]any{"type": "string", "minLength": minimum, "maxLength": maximum}
	if pattern != "" {
		schema["pattern"] = pattern
	}
	return schema
}
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func integerSchema(minimum, maximum int64) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
}
func dateTimeSchema() map[string]any {
	return map[string]any{"type": "string", "format": "date-time", "minLength": 20, "maxLength": 64}
}
func emailSchema() map[string]any {
	return map[string]any{"type": "string", "format": "email", "minLength": 3, "maxLength": 254}
}
func emailArray() map[string]any {
	return map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": emailSchema()}
}
func mapSchema(minimum, maximum int, values map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": values, "minProperties": minimum, "maxProperties": maximum}
}

func Find(name string) (Spec, bool) {
	for _, spec := range Catalog() {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

func DecodeArguments(raw json.RawMessage, schema map[string]any) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil || arguments == nil {
		return nil, errors.New("arguments must be one JSON object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("arguments must contain one JSON object")
	}
	if err := validate(arguments, schema, "arguments"); err != nil {
		return nil, err
	}
	return arguments, nil
}

func ValidateToolArguments(tool string, arguments map[string]any) error {
	switch tool {
	case "azure_resource_groups_create", "azure_resource_group_tags_update":
		if !safeAzureSubscriptionID(stringArgument(arguments, "subscription_id")) || !safeResourceGroupName(stringArgument(arguments, "resource_group_name")) || !safeOperationID(stringArgument(arguments, "operation_id")) {
			return errors.New("Azure write arguments have invalid format")
		}
		if tool == "azure_resource_groups_create" && !safeAzureLocation(stringArgument(arguments, "location")) {
			return errors.New("location has invalid format")
		}
		if tool == "azure_resource_group_tags_update" && !safeTags(arguments["tags"]) {
			return errors.New("tags must be a bounded flat string map")
		}
	}
	start, hasStart := arguments["start"].(string)
	end, hasEnd := arguments["end"].(string)
	if !hasStart && !hasEnd {
		return nil
	}
	startAt, startErr := time.Parse(time.RFC3339, start)
	endAt, endErr := time.Parse(time.RFC3339, end)
	if startErr != nil || endErr != nil || !endAt.After(startAt) {
		return errors.New("end must be after start")
	}
	if tool == "calendar_events_list" && endAt.Sub(startAt) > 31*24*time.Hour {
		return errors.New("calendar query range must not exceed 31 days")
	}
	return nil
}

func validate(value any, schema map[string]any, path string) error {
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		objectValue, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		if minimum, ok := schema["minProperties"].(int); ok && len(objectValue) < minimum {
			return fmt.Errorf("%s has too few properties", path)
		}
		if maximum, ok := schema["maxProperties"].(int); ok && len(objectValue) > maximum {
			return fmt.Errorf("%s has too many properties", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)
		for _, required := range required {
			if _, found := objectValue[required]; !found {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		for key, nested := range objectValue {
			nestedSchema, found := properties[key]
			if !found {
				additional, additionalFound := schema["additionalProperties"]
				if !additionalFound || additional == false {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				var allowed bool
				nestedSchema, allowed = additional.(map[string]any)
				if !allowed {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
			}
			if err := validate(nested, nestedSchema.(map[string]any), path+"."+key); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if len(text) < schema["minLength"].(int) || len(text) > schema["maxLength"].(int) {
			return fmt.Errorf("%s has invalid length", path)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, _ := regexp.MatchString(pattern, text)
			if !matched {
				return fmt.Errorf("%s has invalid format", path)
			}
		}
		if values, ok := schema["enum"].([]string); ok {
			found := false
			for _, allowed := range values {
				found = found || text == allowed
			}
			if !found {
				return fmt.Errorf("%s is not an allowed value", path)
			}
		}
		if schema["format"] == "date-time" && !validDateTime(text) {
			return fmt.Errorf("%s must use RFC3339 with an offset", path)
		}
		if schema["format"] == "email" && !validEmail(text) {
			return fmt.Errorf("%s must be an email address", path)
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be an integer", path)
		}
		integer, err := number.Int64()
		if err != nil || integer < schema["minimum"].(int64) || integer > schema["maximum"].(int64) {
			return fmt.Errorf("%s has invalid value", path)
		}
	case "array":
		items, ok := value.([]any)
		if !ok || len(items) < schema["minItems"].(int) || len(items) > schema["maxItems"].(int) {
			return fmt.Errorf("%s has invalid item count", path)
		}
		for index, item := range items {
			if err := validate(item, schema["items"].(map[string]any), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid fixed schema")
	}
	return nil
}

func validDateTime(value string) bool {
	if !strings.Contains(value, "T") || !(strings.HasSuffix(value, "Z") || regexp.MustCompile(`[+-][0-9]{2}:[0-9]{2}$`).MatchString(value)) {
		return false
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}
func validEmail(value string) bool {
	if strings.ContainsAny(value, "\r\n<>") || strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && strings.Count(value, "@") == 1 && strings.LastIndex(value, ".") > strings.LastIndex(value, "@")+1
}

func stringArgument(arguments map[string]any, key string) string {
	value, _ := arguments[key].(string)
	return value
}

func safeAzureSubscriptionID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F' || character == '-') {
			return false
		}
	}
	return true
}

func safeResourceGroupName(value string) bool {
	if len(value) == 0 || len(value) > 90 || strings.Contains(value, "..") || strings.HasSuffix(value, ".") {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.()-", character) {
			if index != 0 || (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
				continue
			}
		}
		return false
	}
	return true
}

func safeAzureLocation(value string) bool {
	if len(value) < 2 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func safeOperationID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			continue
		}
		if index > 0 && strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func safeTags(value any) bool {
	tags, ok := value.(map[string]any)
	if !ok || len(tags) == 0 || len(tags) > 50 {
		return false
	}
	for key, value := range tags {
		if len(key) == 0 || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
			return false
		}
		text, ok := value.(string)
		if !ok || len(text) == 0 || len(text) > 256 || strings.ContainsAny(text, "\r\n") {
			return false
		}
	}
	return true
}
