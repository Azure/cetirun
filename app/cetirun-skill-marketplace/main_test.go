// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-skill-marketplace/internal/skills"
)

// required are the flags without a default, with synthetic values.
var required = []string{
	"--repository-url=https://git.example.invalid/example/skills.git",
	"--storage-resource-group=example-rg", "--storage-account=exampleskills",
	"--storage-container=marketplace", "--catalog-namespace=example-system",
}

func TestParseLimitsDefaultToTheBuiltInOnes(t *testing.T) {
	o, err := parse(required)
	if err != nil {
		t.Fatal(err)
	}
	if o.limits != skills.DefaultLimits {
		t.Fatalf("limits = %+v, want %+v", o.limits, skills.DefaultLimits)
	}
	if o.maxRepository != 256<<20 {
		t.Fatalf("repository limit = %d", o.maxRepository)
	}
}

func TestParseLimits(t *testing.T) {
	o, err := parse(append(required, "--max-repository-size=1Gi", "--max-skill-files=1000",
		"--max-skill-size=20Mi", "--max-skill-md-size=512Ki", "--max-skill-depth=12", "--max-description-length=2048"))
	if err != nil {
		t.Fatal(err)
	}
	want := skills.Limits{MaxFiles: 1000, MaxBytes: 20 << 20, MaxSkillMD: 512 << 10, MaxDepth: 12, MaxDescription: 2048}
	if o.limits != want || o.maxRepository != 1<<30 {
		t.Fatalf("limits = %+v, repository %d", o.limits, o.maxRepository)
	}
	// The synchronizer validates the repository with these limits.
	if syncer := newSyncer(o, nil, nil, nil, nil); syncer.Limits != want || syncer.MaxBytes != 1<<30 {
		t.Fatalf("synchronizer limits = %+v, repository %d", syncer.Limits, syncer.MaxBytes)
	}
}

func TestParseRefusesUnusableLimits(t *testing.T) {
	for flag, want := range map[string]string{
		"--max-repository-size=0":      "--max-repository-size must be a positive size",
		"--max-skill-size=-1Mi":        "--max-skill-size must be a positive size",
		"--max-skill-md-size=large":    "--max-skill-md-size must be a positive size",
		"--max-skill-files=0":          "--max-skill-files must be at least 1",
		"--max-skill-depth=-2":         "--max-skill-depth must be at least 1",
		"--max-description-length=0":   "--max-description-length must be at least 1",
		"--max-skill-md-size=6Mi":      "--max-skill-md-size cannot be larger than --max-skill-size",
		"--max-skill-files=five":       "invalid value",
		"--max-description-length=1.5": "invalid value",
	} {
		t.Run(flag, func(t *testing.T) {
			if _, err := parse(append(append([]string{}, required...), flag)); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error %v, want %q", err, want)
			}
		})
	}
}
