// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agents

import (
	"fmt"
	"slices"
	"strings"
)

func Resolve(request Request, profile Profile) (ResolvedAgent, error) {
	if profile.ID == "" || profile.Image == "" || profile.Port < 1 || profile.Port > 65535 {
		return ResolvedAgent{}, fmt.Errorf("incomplete runtime profile")
	}
	if request.Username == "" {
		request.Username = "vibe"
	}
	if request.Username != "vibe" {
		return ResolvedAgent{}, fmt.Errorf("built-in profile requires username vibe")
	}
	if request.Image == "" {
		request.Image = profile.Image
	}
	if request.Image != profile.Image && !slices.Contains(profile.QualifiedImages, request.Image) {
		return ResolvedAgent{}, fmt.Errorf("spec.agent.image is not qualified for the selected harness profile")
	}
	if strings.ContainsAny(request.Image, " \t\r\n") {
		return ResolvedAgent{}, fmt.Errorf("invalid profile image")
	}
	if request.Port == 0 {
		request.Port = profile.Port
	}
	if request.Port < 1 || request.Port > 65535 {
		return ResolvedAgent{}, fmt.Errorf("spec.agent.port must be between 1 and 65535")
	}
	return ResolvedAgent{
		Type: request.Type, Image: request.Image, Username: request.Username,
		HomeDir: "/home/" + request.Username, Port: request.Port,
	}, nil
}

func Validate(agent ResolvedAgent, bindings Bindings, descriptor Descriptor) error {
	if agent.Type != descriptor.Type || bindings.ProfileID == "" {
		return fmt.Errorf("harness profile/type mismatch")
	}
	if agent.Username != "vibe" || agent.HomeDir != "/home/vibe" || agent.Image == "" {
		return fmt.Errorf("invalid resolved built-in identity or image")
	}
	ports := append([]PortBinding{{Name: descriptor.PortName, Number: agent.Port}}, bindings.ReservedPorts...)
	ports = append(ports, PortBinding{Name: "cetirun-auth", Number: 18790})
	if descriptor.DashboardPort != 0 {
		ports = append(ports, PortBinding{Name: "dashboard", Number: descriptor.DashboardPort})
	}
	seen := map[int32]string{}
	for _, port := range ports {
		if port.Number < 1 || port.Number > 65535 {
			return fmt.Errorf("%s port must be between 1 and 65535", port.Name)
		}
		if previous, ok := seen[port.Number]; ok {
			return fmt.Errorf("%s port conflicts with %s port (%d)", port.Name, previous, port.Number)
		}
		seen[port.Number] = port.Name
	}
	if bindings.Model.Name == "" {
		return fmt.Errorf("model name is required")
	}
	switch bindings.Model.Provider {
	case "github_copilot", "openai", "anthropic":
	default:
		return fmt.Errorf("unsupported model provider")
	}
	if bindings.ConfigTemplate == "" {
		return fmt.Errorf("native config asset is missing")
	}
	return nil
}
