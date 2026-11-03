// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
)

const oldExecTemplateBlock = `  matchConditions:
    - name: "exclude-adminclaw"
      expression: >-
        request.userInfo.username != "system:serviceaccount:cetirun-admin:openclaw-admin-cetirun"
`

func legacyExecChart(t *testing.T) *chart.Chart {
	t.Helper()
	ch := testChart(t)
	for _, template := range ch.Templates {
		if template.Name != "templates/deny-exec-policy.yaml" {
			continue
		}
		anchor := []byte("  failurePolicy: Fail\n")
		if bytes.Count(template.Data, anchor) != 1 {
			t.Fatal("legacy fixture requires the exact exec policy anchor")
		}
		template.Data = bytes.Replace(template.Data, anchor, append(append([]byte(nil), anchor...), oldExecTemplateBlock...), 1)
		// Exact policy source at a86fbcd24e9d6fe02b6f33bc6a651957e888a9b7.
		if fmt.Sprintf("%x", sha256.Sum256(template.Data)) != "f4d8af2b56d766d0f006aa5d62c9c9d362ad6b5f53195cca6a2909979cb65c98" {
			t.Fatal("legacy exec fixture differs from its original source")
		}
		ch.Metadata.Version = "0.1.0"
		return ch
	}
	t.Fatal("legacy exec template missing")
	return nil
}

func TestLegacyExecCustomMaintenanceRestoresWithoutPolicyWrites(t *testing.T) {
	ch := legacyExecChart(t)
	input := customInput(8080)
	previous, err := prepareChart(ch, input, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, kube, _ := sandboxActionConfig()
	cfg.KubeClient = &maintenanceNamespaceKube{sandboxActionKube: kube}
	install := action.NewInstall(cfg)
	install.Namespace, install.ReleaseName, install.DisableHooks = maintenanceNamespace, maintenanceReleaseName, true
	old, err := install.Run(ch, previous.Values)
	if err != nil {
		t.Fatal(err)
	}
	old.Info.Status = release.StatusFailed
	if err := cfg.Releases.Update(old); err != nil {
		t.Fatal(err)
	}
	for key, obj := range kube.objects {
		if obj.GetKind() == "ConfigMap" {
			delete(kube.objects, key)
		}
		if obj.GetKind() == "ValidatingAdmissionPolicy" {
			// Parent's exec synchronizer has already migrated the actual policy.
			delete(object(obj.Object["spec"]), "matchConditions")
		}
	}
	input["replicaCount"] = 0
	next, err := PrepareChart(ch, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.prepareCustomAction(cfg, maintenanceNamespace, maintenanceReleaseName); err != nil {
		t.Fatal(err)
	}
	next.namespace, next.apiValidated = maintenanceNamespace, true
	kube.writes = nil
	if err := maintenanceApply(context.Background(), next, cfg, 1, allowMaintenance); err != nil {
		t.Fatal(err)
	}
	for _, written := range kube.writes {
		if written.GetKind() != "StatefulSet" && written.GetKind() != "ConfigMap" && written.GetKind() != "Service" {
			t.Fatal("maintenance wrote a protected policy or identity")
		}
	}
	current, _ := cfg.Releases.Last(maintenanceReleaseName)
	retained, _ := cfg.Releases.Get(maintenanceReleaseName, 1)
	if strings.Contains(current.Manifest, "exclude-adminclaw") || retained.Manifest != old.Manifest || current.Chart.Metadata.Version != "0.1.0" {
		t.Fatal("maintenance restored an exception, rewrote history, or changed the pin")
	}
}

func TestLegacyExecCustomPinIsNormalizedBeforeApply(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			ch := legacyExecChart(t)
			input := terminalInput(8080)
			input["ttyd"] = map[string]any{"enabled": terminal}
			// Seed the pre-migration release, not already-normalized output.
			old, err := prepareChart(ch, input, false)
			if err != nil {
				t.Fatal(err)
			}
			cfg := customActionConfig()
			install := action.NewInstall(cfg)
			install.Namespace, install.ReleaseName, install.DisableHooks = "cetirun-example", old.releaseName, true
			previous, err := install.Run(ch, old.Values)
			if err != nil {
				t.Fatal(err)
			}
			original := previous.Manifest
			if !strings.Contains(original, "exclude-adminclaw") {
				t.Fatal("fixture lacks the old deployed exception")
			}
			next, err := PrepareChart(ch, input)
			if err != nil {
				t.Fatal("old pin stopped working after platform upgrade", err)
			}
			if applied, err := next.customApplied(cfg, "cetirun-example", next.releaseName); err != nil || applied {
				t.Fatal("legacy stored output was mistaken for completed migration", err)
			}
			if err := next.prepareCustomAction(cfg, "cetirun-example", next.releaseName); err != nil {
				t.Fatal(err)
			}
			if err := next.installOrUpgradeCustom(cfg, "cetirun-example", next.releaseName); err != nil {
				t.Fatal(err)
			}
			current, err := cfg.Releases.Last(next.releaseName)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(current.Manifest, "exclude-adminclaw") || strings.Contains(current.Manifest, "matchConditions") || len(current.Hooks) != 0 {
				t.Fatal("old exception or hooks reached the applied manifest")
			}
			if current.Chart.Metadata.Version != "0.1.0" || next.ChartVersion != "0.1.0" || ch.Metadata.Version != "0.1.0" {
				t.Fatal("compatibility changed the Agent's chart pin")
			}
			retained, err := cfg.Releases.Get(next.releaseName, 1)
			if err != nil || retained.Manifest != original {
				t.Fatal("migration rewrote old history", err)
			}
			if applied, err := next.customApplied(cfg, "cetirun-example", next.releaseName); err != nil || !applied {
				t.Fatal("normalized applied output was not recognized", err)
			}
			if err := next.validateCustomManifest("cetirun-example", next.releaseName, []byte(original)); err == nil {
				t.Fatal("sealed validation accepted a reintroduced legacy exception")
			}
			if _, err := (customPostRenderer{next, "cetirun-example", next.releaseName}).Run(bytes.NewBufferString(original)); err == nil {
				t.Fatal("post-render validation performed a second untrusted conversion")
			}
			for _, template := range ch.Templates {
				if template.Name == "templates/deny-exec-policy.yaml" && !bytes.Contains(template.Data, []byte(oldExecTemplateBlock)) {
					t.Fatal("normalization edited the source chart")
				}
			}
		})
	}
}

func TestLegacyExecConversionStillRunsFullCustomValidation(t *testing.T) {
	for _, scenario := range []string{"username", "name", "expression", "extra condition", "extra field", "selector", "validation", "owner", "duplicate JSON", "extra object"} {
		t.Run(scenario, func(t *testing.T) {
			ch := legacyExecChart(t)
			for _, template := range ch.Templates {
				if template.Name != "templates/deny-exec-policy.yaml" {
					continue
				}
				text := string(template.Data)
				switch scenario {
				case "username":
					text = strings.Replace(text, "cetirun-admin:openclaw-admin-cetirun", "another:admin", 1)
				case "name":
					text = strings.Replace(text, "exclude-adminclaw", "exclude-anybody", 1)
				case "expression":
					text = strings.Replace(text, "request.userInfo.username !=", "false && request.userInfo.username !=", 1)
				case "extra condition":
					text = strings.Replace(text, "  matchConstraints:", "    - name: skip\n      expression: 'false'\n  matchConstraints:", 1)
				case "extra field":
					text = strings.Replace(text, "      expression: >-", "      extra: true\n      expression: >-", 1)
				case "selector":
					text = strings.Replace(text, "kubernetes.io/metadata.name:", "example.com/skip:", 1)
				case "validation":
					text = strings.Replace(text, `expression: "false"`, `expression: "true"`, 1)
				case "owner":
					text = strings.Replace(text, "uid: {{ .agentUID | quote }}", "uid: foreign-uid", 1)
				case "duplicate JSON":
					text = strings.Replace(text, "  matchConditions:", "  matchConditions: []\n  matchConditions:", 1)
				case "extra object":
					text += "\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: unapproved\n"
				}
				template.Data = []byte(text)
			}
			if _, err := PrepareChart(ch, customInput(8080)); err == nil {
				t.Fatal("unrecognized or broadened legacy policy passed full validation")
			}
		})
	}
}
