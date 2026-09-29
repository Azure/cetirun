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

type Config struct {
	AdminUsers         []string `json:"adminUsers"`
	AdminGroups        []string `json:"adminGroups"`
	ControllerUsername string   `json:"controllerUsername"`
}

func ParseConfig(raw string) (Config, error) {
	var config Config
	if err := decodeStrict([]byte(raw), &config); err != nil {
		return config, fmt.Errorf("invalid Sandbox admission configuration: %w", err)
	}
	return config, config.Validate()
}

func (c Config) Validate() error {
	if len(c.AdminUsers)+len(c.AdminGroups) == 0 {
		return fmt.Errorf("Sandbox admission requires an explicit administrator allowlist")
	}
	for _, values := range [][]string{c.AdminUsers, c.AdminGroups} {
		for _, value := range values {
			if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "*") {
				return fmt.Errorf("Sandbox admission administrators must be exact, nonempty identities")
			}
		}
	}
	for _, group := range c.AdminGroups {
		if group == "system:authenticated" || group == "system:unauthenticated" || strings.HasPrefix(group, "system:serviceaccounts") {
			return fmt.Errorf("Sandbox admission cannot grant administration to a blanket authentication or service-account group")
		}
	}
	parts := strings.Split(c.ControllerUsername, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" ||
		len(validation.IsDNS1123Label(parts[2])) != 0 || len(validation.IsDNS1123Subdomain(parts[3])) != 0 {
		return fmt.Errorf("Sandbox admission controllerUsername must identify one namespaced service account")
	}
	return nil
}

func (c Config) administrator(user authenticationv1.UserInfo) bool {
	if user.Username == "" || user.Username == "system:anonymous" {
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

func decodeStrict(raw []byte, object any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(object); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON object")
	}
	return nil
}
