// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// recordHistoricalBootstrap converts a real common bootstrap receipt into the
// immutable mode marker written by earlier releases. It is intentionally a
// test-only compatibility fixture: new Agents retain Reconciled=true and must
// take the common lifecycle.
func recordHistoricalBootstrap(t *testing.T, f *preparationFixture) {
	t.Helper()
	// Common reconciliation may finish its initial apply before this fixture is
	// converted. What matters here is the durable, catalog-backed receipt it
	// writes; subsequent reconciliation uses the retained held lifecycle.
	_ = f.reconcile()
	receipt := bootstrapReceipt(t, f)
	var document map[string]any
	if err := json.Unmarshal([]byte(receipt.Data["bootstrap.json"]), &document); err != nil {
		t.Fatal(err)
	}
	document["reconciled"] = false
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Data["bootstrap.json"] = string(raw)
	if err := f.client.Update(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	f.reconciler.SandboxPreparation.NativeExecution = false
	prerequisites, ok := f.reconciler.SandboxPreparation.Prerequisites.(*controller.KubernetesSandboxPrerequisites)
	if !ok {
		t.Fatal("common bootstrap fixture did not install Kubernetes prerequisites")
	}
	prerequisites.NativeExecution = false
}

// commonBootstrapInstaller is deliberately small: bootstrap tests need the
// same typed common apply boundary as production, while preparationInstaller
// remains the held-lifecycle installer used by the preparation tests.
type commonBootstrapInstaller struct{ base *preparationInstaller }

func (i *commonBootstrapInstaller) Prepare(_ string, values map[string]any, version string) (*helm.PreparedRelease, error) {
	if version != i.base.chart.Metadata.Version {
		return nil, fmt.Errorf("test chart version differs")
	}
	return helm.PrepareChart(i.base.chart, values)
}

func (i *commonBootstrapInstaller) Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error) {
	return prepared.Rebuild(values)
}

func (i *commonBootstrapInstaller) InstallOrUpgrade(_ string, _ string, prepared *helm.PreparedRelease) error {
	if err := prepared.AuthorizeSandboxApply(context.Background()); err != nil {
		return err
	}
	manifest, err := prepared.SandboxManifest()
	if err != nil {
		return err
	}
	decoder := kubeyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		object := &unstructured.Unstructured{}
		err := decoder.Decode(object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(object.Object) == 0 {
			continue
		}
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(object.GroupVersionKind())
		if err := i.base.client.Get(context.Background(), client.ObjectKeyFromObject(object), current); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return err
			}
			if err := i.base.client.Create(context.Background(), object); err != nil {
				return err
			}
			continue
		}
		object.SetUID(current.GetUID())
		object.SetResourceVersion(current.GetResourceVersion())
		if err := i.base.client.Update(context.Background(), object); err != nil {
			return err
		}
	}
	i.base.stages++
	return nil
}

func (*commonBootstrapInstaller) CustomApplied(string, string, *helm.PreparedRelease) (bool, error) {
	return false, nil
}

func (*commonBootstrapInstaller) Uninstall(string, string) error { return nil }
