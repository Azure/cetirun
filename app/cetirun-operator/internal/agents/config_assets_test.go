// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agents

import (
	"os"
	"path/filepath"
	"testing"
	"text/template"
)

func TestNativeConfigAssetsUseRegisteredFunctions(t *testing.T) {
	for _, harness := range []string{"openclaw", "hermes"} {
		t.Run(harness, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "cetirun-chart", "agents", harness, "config", "native.tpl")
			asset, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := template.New(harness).Funcs(nativeTemplateFunctions()).Parse(string(asset)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
