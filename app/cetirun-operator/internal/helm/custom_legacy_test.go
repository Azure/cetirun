// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"strings"
	"testing"
)

func TestPrepareChartRejectsLegacyCustomBindings(t *testing.T) {
	_, err := PrepareChart(testChart(t), map[string]any{
		"agentType":      "custom",
		"customBindings": map[string]any{},
		"agentRequest":   map[string]any{"type": "custom"},
	})
	if err == nil || !strings.Contains(err.Error(), "Custom requires common Sandbox execution") {
		t.Fatalf("legacy Custom renderer must be rejected, got %v", err)
	}
}
