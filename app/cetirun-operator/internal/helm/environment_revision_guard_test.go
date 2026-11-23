// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/kube"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
)

type environmentGuardRenderer struct {
	run func(*bytes.Buffer) (*bytes.Buffer, error)
}

func (r environmentGuardRenderer) Run(b *bytes.Buffer) (*bytes.Buffer, error) { return r.run(b) }

type environmentGuardKube struct {
	kubefake.PrintingKubeClient
	writes int
}

func (k *environmentGuardKube) Update(current, target kube.ResourceList, force bool) (*kube.Result, error) {
	k.writes++
	return k.PrintingKubeClient.Update(current, target, force)
}

// Exercise the real Helm upgrade and the executable Python post-renderer. Only
// Kubernetes storage/objects are in-memory. In particular, a competing upgrade
// after the guard is rejected by Helm's pending-revision Create, before hooks
// and object updates, not by a post-deployment check.
func TestEnvironmentRevisionGuardFencesCompetingHelmUpgrade(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	guard, err := filepath.Abs("../../../../scripts/helm_revision_guard.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, timing := range []string{"none", "before-prepare", "after-guard"} {
		t.Run(timing, func(t *testing.T) {
			cfg := customActionConfig()
			k := &environmentGuardKube{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}}
			cfg.KubeClient = k
			ch := &chart.Chart{Metadata: &chart.Metadata{Name: "example", Version: "1.0.0", APIVersion: "v2"},
				Templates: []*chart.File{{Name: "templates/config.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example\n")}}}
			original := &release.Release{Name: "example", Namespace: "example", Version: 1, Chart: ch,
				Info: &release.Info{Status: release.StatusDeployed}, Config: map[string]interface{}{}, Manifest: ""}
			if err := cfg.Releases.Create(original); err != nil {
				t.Fatal(err)
			}
			compete := func() {
				t.Helper()
				other := *original
				other.Version = 2
				other.Info = &release.Info{Status: release.StatusDeployed, Description: "competing upgrade"}
				if err := cfg.Releases.Create(&other); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			history := filepath.Join(dir, "history.json")
			content := `[{"revision":1,"status":"deployed"}]`
			if timing == "before-prepare" {
				compete()
				content = `[{"revision":2,"status":"deployed"}]`
			}
			if err := os.WriteFile(history, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "helm"), []byte("#!/bin/sh\nexec /bin/cat \"$CETIRUN_GUARD_TEST_HISTORY\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			u := action.NewUpgrade(cfg)
			u.Namespace = "example"
			u.DisableOpenAPIValidation = true
			calls := 0
			u.PostRenderer = environmentGuardRenderer{run: func(input *bytes.Buffer) (*bytes.Buffer, error) {
				calls++
				command := exec.Command(python, guard, "--expected-revision", "1", "--release", "example", "--namespace", "example", "--context", "synthetic")
				command.Env = append(os.Environ(), "PATH="+dir, "CETIRUN_GUARD_TEST_HISTORY="+history)
				command.Stdin = bytes.NewReader(input.Bytes())
				output, err := command.Output()
				if err == nil && !bytes.Equal(output, input.Bytes()) {
					t.Fatal("post-renderer changed the manifest")
				}
				if timing == "after-guard" && err == nil {
					compete()
				}
				return bytes.NewBuffer(output), err
			}}
			_, err := u.Run("example", ch, map[string]interface{}{})
			if calls != 1 {
				t.Fatalf("guard calls = %d, want 1", calls)
			}
			if timing == "none" {
				if err != nil || k.writes != 1 {
					t.Fatalf("uncontested upgrade: err=%v writes=%d", err, k.writes)
				}
			} else {
				if err == nil || k.writes != 0 {
					t.Fatalf("competing upgrade was not fenced: err=%v writes=%d", err, k.writes)
				}
				last, err := cfg.Releases.Last("example")
				if err != nil || last.Version != 2 || last.Info.Description != "competing upgrade" {
					t.Fatalf("competing revision overwritten: %v, %v", last, err)
				}
			}
		})
	}
}
