// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package sandboxcoordination

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestKubernetesUpdateFenceRecoveryAPIServer proves that a status fence uses
// Kubernetes resource-version CAS to exclude a delayed full-object update. It
// runs only against an isolated API server, never an existing cluster.
func TestKubernetesUpdateFenceRecoveryAPIServer(t *testing.T) {
	if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
		t.Fatal("update fence recovery test refuses an existing cluster")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, binary := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, binary))
		if assets == "" || err != nil || info.Mode()&0111 == 0 {
			t.Fatal("explicit preinstalled Kubernetes 1.37 API-server/etcd binaries are required")
		}
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
	}
	config, err := environment.Start()
	if err != nil {
		if stopErr := environment.Stop(); stopErr != nil {
			t.Error("stop failed local API startup:", stopErr)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := url.Parse(config.Host)
	if err != nil || endpoint.Scheme != "https" ||
		(endpoint.Hostname() != "127.0.0.1" && endpoint.Hostname() != "localhost" && endpoint.Hostname() != "::1") {
		t.Fatal("envtest must provide its own HTTPS loopback endpoint")
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	storage, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := storage.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "update-fence-state"}}); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"Team", "Sandbox", "AgentPolicy"} {
		t.Run(kind, func(t *testing.T) {
			name := "fence-" + strings.ToLower(kind)
			authority, err := NewState("update-fence-state", name)
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.Create(ctx, authority); err != nil {
				t.Fatal(err)
			}
			before := configurationObject(kind)
			before.SetName(name)
			if err := storage.Create(ctx, before); err != nil {
				t.Fatal(err)
			}
			persisted := before.DeepCopyObject().(client.Object)
			if err := storage.Get(ctx, client.ObjectKeyFromObject(before), persisted); err != nil {
				t.Fatal(err)
			}
			setPreservedConfigurationStatus(persisted)
			if err := storage.Status().Update(ctx, persisted); err != nil {
				t.Fatal(err)
			}
			before = persisted
			after := desiredConfiguration(before)
			store := Store{Client: storage, APIReader: storage, Namespace: authority.Namespace,
				Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}
			ticket := changingConfiguration(t, store, before, after)
			_, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
			if err != nil || outcome == nil || !outcome.Aborted || !outcome.Superseded || outcome.Applied || !outcome.NoPendingSubmissions {
				t.Fatalf("unchanged update was not fenced: outcome=%+v err=%v", outcome, err)
			}
			if err := store.CheckChange(ctx, ticket); !errors.Is(err, ErrConflict) {
				t.Fatal("fenced update retained its reservation:", err)
			}
			if err := storage.Update(ctx, after.DeepCopyObject().(client.Object)); !apierrors.IsConflict(err) {
				t.Fatalf("delayed stale update was not rejected by API-server CAS: %v", err)
			}
			current := before.DeepCopyObject().(client.Object)
			if err := storage.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil {
				t.Fatal(err)
			}
			if err := hasConfigurationFence(current, ticket.Change.ID); err != nil {
				t.Fatal(err)
			}
			if !hasConfigurationCondition(current, "PreservedStatus") {
				t.Fatal("status fence discarded an existing condition")
			}
		})
	}

	t.Run("original-update-wins", func(t *testing.T) {
		authority, err := NewState("update-fence-state", "fence-original-wins")
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Create(ctx, authority); err != nil {
			t.Fatal(err)
		}
		before := configurationObject("Team")
		before.SetName("fence-original-wins")
		if err := storage.Create(ctx, before); err != nil {
			t.Fatal(err)
		}
		after := desiredConfiguration(before)
		store := Store{Client: storage, APIReader: storage, Namespace: authority.Namespace,
			Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}
		ticket := changingConfiguration(t, store, before, after)
		statusWrites := 0
		store.Client = interceptor.NewClient(storage, interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, name string, object client.Object, _ ...client.SubResourceUpdateOption) error {
				if name != "status" {
					return errors.New("unexpected subresource")
				}
				statusWrites++
				if err := c.Update(ctx, after.DeepCopyObject().(client.Object)); err != nil {
					return err
				}
				return c.Status().Update(ctx, object)
			},
		})
		_, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
		if err != nil || outcome == nil || !outcome.Applied || outcome.Aborted || statusWrites != 1 {
			t.Fatalf("original update did not win the CAS race: outcome=%+v writes=%d err=%v", outcome, statusWrites, err)
		}
		if err := store.CheckChange(ctx, ticket); !errors.Is(err, ErrConflict) {
			t.Fatal("applied update retained reservation:", err)
		}
		current := &api.Team{}
		if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
			t.Fatal(err)
		}
		if current.Spec.DefaultSandboxRef != after.(*api.Team).Spec.DefaultSandboxRef || hasConfigurationCondition(current, ConfigurationChangeFencedCondition) {
			t.Fatal("rejected status fence changed the applied source")
		}
	})

	t.Run("lost-fence-response-restarts", func(t *testing.T) {
		authority, err := NewState("update-fence-state", "fence-lost-response")
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Create(ctx, authority); err != nil {
			t.Fatal(err)
		}
		before := configurationObject("Team")
		before.SetName("fence-lost-response")
		if err := storage.Create(ctx, before); err != nil {
			t.Fatal(err)
		}
		after := desiredConfiguration(before)
		readAfterFence := false
		reader := interceptor.NewClient(storage, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
				if readAfterFence && key.Namespace == "" && key.Name == before.GetName() {
					return errors.New("synthetic post-fence read failure")
				}
				return c.Get(ctx, key, object, opts...)
			},
		})
		store := Store{Client: storage, APIReader: reader, Namespace: authority.Namespace,
			Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}
		ticket := changingConfiguration(t, store, before, after)
		store.Client = interceptor.NewClient(storage, interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, name string, object client.Object, opts ...client.SubResourceUpdateOption) error {
				if err := c.Status().Update(ctx, object, opts...); err != nil {
					return err
				}
				readAfterFence = true
				return errors.New("synthetic lost status response")
			},
		})
		if _, outcome, err := store.RecoverConfigurationChange(ctx, before, after); outcome != nil || err == nil {
			t.Fatalf("unconfirmed lost fence response was released: outcome=%+v err=%v", outcome, err)
		}
		if err := store.CheckChange(ctx, ticket); err != nil {
			t.Fatal("unconfirmed lost fence response lost reservation:", err)
		}
		restarted := Store{Client: storage, APIReader: storage, Namespace: authority.Namespace,
			Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}
		_, outcome, err := restarted.RecoverConfigurationChange(ctx, before, after)
		if err != nil || outcome == nil || !outcome.Aborted || !outcome.Superseded {
			t.Fatalf("restart did not safely recover committed fence: outcome=%+v err=%v", outcome, err)
		}
	})
}

func setPreservedConfigurationStatus(object client.Object) {
	condition := metav1.Condition{Type: "PreservedStatus", Status: metav1.ConditionTrue, Reason: "Synthetic", Message: "preserve", LastTransitionTime: metav1.Now()}
	switch object := object.(type) {
	case *api.Team:
		object.Status.Conditions = []metav1.Condition{condition}
	case *api.Sandbox:
		object.Status.Conditions = []metav1.Condition{condition}
	case *api.AgentPolicy:
		object.Status.Conditions = []metav1.Condition{condition}
	}
}

func hasConfigurationCondition(object client.Object, conditionType string) bool {
	var conditions []metav1.Condition
	switch object := object.(type) {
	case *api.Team:
		conditions = object.Status.Conditions
	case *api.Sandbox:
		conditions = object.Status.Conditions
	case *api.AgentPolicy:
		conditions = object.Status.Conditions
	}
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}
