// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package sandboxadmission

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestConfigurationCoordinationAPIServer(t *testing.T) {
	if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
		t.Fatal("configuration coordination tests refuse an existing cluster")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, name := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, name))
		if assets == "" || err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			t.Fatal("explicit preinstalled API-server/etcd binaries are required")
		}
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
	}
	connection, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		api.AddToScheme, corev1.AddToScheme, admissionregistrationv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	storage, err := client.NewWithWatch(connection, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := storage.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "operator-test"}}); err != nil {
		t.Fatal(err)
	}
	state, err := sandboxcoordination.NewState("operator-test", "configuration-coordination")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Create(ctx, state); err != nil {
		t.Fatal(err)
	}
	before := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "coordinated-policy"}}
	if err := storage.Create(ctx, before); err != nil {
		t.Fatal(err)
	}
	after := before.DeepCopy()
	after.Spec.Network.BlockedCIDRs = []string{"203.0.113.0/24"}
	config := testConfig()
	config.AdminGroups = []string{"system:masters"}
	config.CoordinationConfigMapName, config.CoordinationConfigMapUID = state.Name, string(state.UID)
	handler, err := NewHandler(storage, config)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(ValidationPath, &admission.Webhook{Handler: admission.HandlerFunc(handler.Validate)})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	validating, _ := renderedWebhooks(t, certificate)
	for i := range validating.Webhooks {
		hook := &validating.Webhooks[i]
		url := server.URL + *hook.ClientConfig.Service.Path
		hook.ClientConfig.Service, hook.ClientConfig.URL = nil, &url
	}
	if err := storage.Create(ctx, validating); err != nil {
		t.Fatal(err)
	}
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		err := storage.Update(ctx, after.DeepCopy(), client.DryRunAll)
		return err != nil && strings.Contains(err.Error(), "CoordinationRequired"), nil
	}); err != nil {
		t.Fatal("served configuration coordination gate did not become ready:", err)
	}
	if err := storage.Update(ctx, after.DeepCopy()); err == nil {
		t.Fatal("direct configuration update bypassed the served reservation gate")
	}
	store := sandboxcoordination.Store{
		Client: storage, APIReader: storage, Namespace: state.Namespace,
		Identity: w.ObjectIdentity{Name: state.Name, UID: state.UID},
	}
	ticket, err := store.AcquireConfigurationChange(ctx, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Update(ctx, after.DeepCopy()); err == nil {
		t.Fatal("a reserved but unsubmitted change authorized a source write")
	}
	var updates atomic.Int32
	lostReply := interceptor.NewClient(storage, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.UpdateOption) error {
			updates.Add(1)
			if err := c.Update(ctx, object, options...); err != nil {
				return err
			}
			return errors.New("synthetic lost reply after committed update")
		},
	})
	adapter, err := sandboxcoordination.NewKubernetesChangeAdapter(lostReply, storage, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileChange(ctx, ticket, adapter); err == nil {
		t.Fatal("fixture did not lose the update response")
	}
	current := &api.AgentPolicy{}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
		t.Fatal(err)
	}
	if current.ResourceVersion == before.ResourceVersion || len(current.Spec.Network.BlockedCIDRs) != 1 ||
		current.Spec.Network.BlockedCIDRs[0] != "203.0.113.0/24" {
		t.Fatal("coordinated mutation was not committed through the real admission path")
	}
	recovered, err := store.ReadConfigurationChange(ctx, before, after)
	if err != nil || recovered.Change.Phase != sandboxcoordination.Changing {
		t.Fatal("durable submission intent did not survive the lost response:", err)
	}
	if _, err := store.ReconcileChange(ctx, recovered, adapter); err != nil {
		t.Fatal("committed update did not recover by observation:", err)
	}
	if updates.Load() != 1 {
		t.Fatal("recovery replayed an already submitted configuration write")
	}
	if err := store.CheckChange(ctx, recovered); !errors.Is(err, sandboxcoordination.ErrConflict) {
		t.Fatal("observed completed mutation retained its reservation:", err)
	}

	state.Data[sandboxcoordination.DataKey] = "{}"
	if err := storage.Update(ctx, state); err == nil {
		t.Fatal("fixture unexpectedly updated a stale authority version")
	}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(state), state); err != nil {
		t.Fatal(err)
	}
	state.Data[sandboxcoordination.DataKey] = "{}"
	if err := storage.Update(ctx, state); err != nil {
		t.Fatal(err)
	}
	changed := current.DeepCopy()
	changed.Spec.Network.BlockedCIDRs = []string{"198.51.100.0/24"}
	if err := storage.Update(ctx, changed); err == nil {
		t.Fatal("corrupt authority allowed a direct configuration update")
	}
	current.Status.ObservedGeneration = current.Generation
	if err := storage.Status().Update(ctx, current); err != nil {
		t.Fatal("coordination source loss blocked an independent status update:", err)
	}
	if err := storage.Delete(ctx, current); err != nil {
		t.Fatal("coordination source loss changed existing unreferenced deletion behavior:", err)
	}
}
