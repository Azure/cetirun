// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"strings"
	"testing"
)

func TestHermesWebappRequiresCoordinatedRuntimeProfile(t *testing.T) {
	for _, replacements := range [][2]string{
		{"0.22.7", "0.21.5"},
		{"b200f5bc60e31588ba2cb60e288029c4a54ba5fa", strings.Repeat("0", 40)},
		{"webappPort: 9120", "webappPort: 9119"},
	} {
		chrt := testChart(t)
		for _, file := range chrt.Files {
			if file.Name == "agents/hermes/profile.yaml" {
				file.Data = []byte(strings.ReplaceAll(string(file.Data), replacements[0], replacements[1]))
			}
		}
		if _, err := PrepareChart(chrt, sampleInput("hermes", "openai", false)); err == nil {
			t.Fatal("incompatible Hermes profile was accepted")
		}
	}
}
