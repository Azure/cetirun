// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	ValidationCompatible = "compatible"
	ValidationStrict     = "strict"
)

type Config struct {
	ValidationMode            string   `json:"validationMode,omitempty"`
	AdminUsers                []string `json:"adminUsers"`
	AdminGroups               []string `json:"adminGroups"`
	ControllerUsername        string   `json:"controllerUsername"`
	PlatformNamespace         string   `json:"platformNamespace,omitempty"`
	PlatformConfigMapName     string   `json:"platformConfigMapName,omitempty"`
	PlacementConfigMapName    string   `json:"placementConfigMapName,omitempty"`
	CoordinationConfigMapName string   `json:"coordinationConfigMapName,omitempty"`
	CoordinationConfigMapUID  string   `json:"coordinationConfigMapUID,omitempty"`
}

func ParseConfig(raw string) (Config, error) {
	var config Config
	if err := decodeStrict([]byte(raw), &config); err != nil {
		return config, fmt.Errorf("invalid Sandbox admission configuration: %w", err)
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if c.ValidationMode != "" && c.ValidationMode != ValidationCompatible && c.ValidationMode != ValidationStrict {
		return fmt.Errorf("Sandbox admission validationMode must be compatible or strict")
	}
	if c.PlacementConfigMapName != "" && len(validation.IsDNS1123Subdomain(c.PlacementConfigMapName)) != 0 {
		return fmt.Errorf("Sandbox placement requires an explicit valid ConfigMap name")
	}
	if len(c.AdminUsers)+len(c.AdminGroups) == 0 {
		return fmt.Errorf("Sandbox admission requires an explicit administrator allowlist")
	}
	for _, values := range [][]string{c.AdminUsers, c.AdminGroups} {
		for _, value := range values {
			if !exactIdentity(value) {
				return fmt.Errorf("Sandbox admission administrators must be exact, nonempty identities")
			}
		}
	}
	for _, group := range c.AdminGroups {
		if blanketGroup(group) {
			return fmt.Errorf("Sandbox admission cannot grant administration to a blanket authentication or service-account group")
		}
	}
	parts := strings.Split(c.ControllerUsername, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" ||
		len(validation.IsDNS1123Label(parts[2])) != 0 || len(validation.IsDNS1123Subdomain(parts[3])) != 0 {
		return fmt.Errorf("Sandbox admission controllerUsername must identify one namespaced service account")
	}
	if c.PlatformNamespace != "" || c.PlatformConfigMapName != "" {
		if len(validation.IsDNS1123Label(c.PlatformNamespace)) != 0 ||
			len(validation.IsDNS1123Subdomain(c.PlatformConfigMapName)) != 0 ||
			c.PlatformNamespace != parts[2] {
			return fmt.Errorf("Sandbox admission platform source requires an explicit ConfigMap name in the controller namespace")
		}
	}
	if c.CoordinationConfigMapName != "" || c.CoordinationConfigMapUID != "" {
		if c.ValidationMode != ValidationStrict ||
			len(validation.IsDNS1123Subdomain(c.CoordinationConfigMapName)) != 0 ||
			c.CoordinationConfigMapName == c.PlatformConfigMapName ||
			c.CoordinationConfigMapName == c.PlacementConfigMapName ||
			len(c.CoordinationConfigMapUID) == 0 || len(c.CoordinationConfigMapUID) > 128 {
			return fmt.Errorf("Sandbox configuration coordination requires strict validation and a separate ConfigMap name with its exact UID")
		}
		for _, character := range c.CoordinationConfigMapUID {
			if character < '!' || character > '~' {
				return fmt.Errorf("Sandbox coordination ConfigMap UID must be a bounded printable identity without whitespace")
			}
		}
	}
	return nil
}

func (c Config) administrator(user authenticationv1.UserInfo) bool {
	if !authenticated(user) {
		return false
	}
	if slices.Contains(c.AdminUsers, user.Username) {
		return true
	}
	for _, group := range user.Groups {
		if slices.Contains(c.AdminGroups, group) {
			return true
		}
	}
	return false
}

func authenticated(user authenticationv1.UserInfo) bool {
	return exactIdentity(user.Username) && !slices.Contains(user.Groups, "system:unauthenticated")
}

func exactIdentity(value string) bool {
	return value != "" && value != "system:anonymous" && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "*\r\n\t")
}

func blanketGroup(value string) bool {
	return value == "system:authenticated" || value == "system:unauthenticated" || strings.HasPrefix(value, "system:serviceaccounts")
}

func decodeStrict(raw []byte, object any) error {
	return decodeJSON(raw, object, true)
}

func decodeJSON(raw []byte, object any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(object); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON object")
	}
	return nil
}
