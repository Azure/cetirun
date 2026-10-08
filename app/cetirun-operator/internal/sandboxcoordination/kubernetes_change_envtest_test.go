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

// These tests exercise real API defaulting, Update, CAS and recovery only.
// No runtime, admission deployment, CSI/CNI provider or model service runs.
func TestKubernetesConfigurationAPIServer(t *testing.T) {
	if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
		t.Fatal("configuration adapter tests refuse an existing cluster")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	for _, binary := range []string{"kube-apiserver", "etcd"} {
		info, err := os.Stat(filepath.Join(assets, binary))
		if assets == "" || err != nil || info.Mode()&0111 == 0 {
			t.Fatal("explicit preinstalled Kubernetes 1.37 API-server/etcd binaries are required")
		}
	}
	environment := &envtest.Environment{BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
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
	if err := storage.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "configuration-state"}}); err != nil {
		t.Fatal(err)
	}
	newFixture := func(t *testing.T, kind, suffix string) (Store, client.Object, client.Object) {
		t.Helper()
		name := strings.ToLower(kind) + "-" + suffix
		authority, err := NewState("configuration-state", name)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Create(ctx, authority); err != nil {
			t.Fatal(err)
		}
		before := configurationObject(kind)
		before.SetName(name)
		before.SetAnnotations(map[string]string{"example.com/change": "original"})
		if err := storage.Create(ctx, before); err != nil {
			t.Fatal(err)
		}
		if before.GetUID() == "" || before.GetResourceVersion() == "" || before.GetGeneration() < 1 {
			t.Fatal("API did not issue a persisted source identity")
		}
		return Store{Client: storage, APIReader: storage, Namespace: authority.Namespace,
			Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}, before, desiredConfiguration(before)
	}

	for _, transition := range []struct {
		name                          string
		beforeGoverned, afterGoverned bool
	}{
		{"legacy-update", false, false},
		{"add-governance", false, true},
		{"remove-governance", true, false},
	} {
		t.Run(transition.name, func(t *testing.T) {
			store, original, _ := newFixture(t, "Team", transition.name)
			before := original.(*api.Team)
			governed := *before.Spec.DeepCopy()
			if !transition.beforeGoverned {
				before.Spec = api.TeamSpec{}
				if err := storage.Update(ctx, before); err != nil {
					t.Fatal(err)
				}
			}
			after := before.DeepCopy()
			if transition.afterGoverned {
				after.Spec = governed
			} else {
				after.Spec = api.TeamSpec{}
			}
			after.Annotations["example.com/change"] = "legacy-transition"
			ticket, err := store.AcquireConfigurationChange(ctx, before, after)
			if err != nil || ticket.Change.Team != nil {
				t.Fatal("legacy transition requires a whole-Team reservation:", err)
			}
			if _, err := store.ReconcileChange(ctx, ticket, configurationAdapter(t, storage, storage, before, after)); err != nil {
				t.Fatal("actual legacy Team transition failed:", err)
			}
		})
	}

	for _, kind := range []string{"Team", "Sandbox", "AgentPolicy"} {
		for _, scenario := range []string{"success", "lost-reply", "failed-request", "never-submitted"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				store, before, after := newFixture(t, kind, scenario)
				updates := 0
				writer := interceptor.NewClient(storage, interceptor.Funcs{
					Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
						updates++
						if scenario == "failed-request" {
							return errors.New("synthetic request did not reach the API")
						}
						if object.GetUID() != before.GetUID() || object.GetResourceVersion() != before.GetResourceVersion() {
							t.Fatal("adapter changed its original API preconditions")
						}
						if err := c.Update(ctx, object, opts...); err != nil {
							return err
						}
						if scenario == "lost-reply" {
							return errors.New("synthetic lost response after real API commit")
						}
						return nil
					},
				})
				ticket, err := store.AcquireConfigurationChange(ctx, before, after)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "never-submitted" {
					ticket, err = store.BeginChange(ctx, ticket)
					if err != nil {
						t.Fatal(err)
					}
				}
				ticket, err = store.ReconcileChange(ctx, ticket, configurationAdapter(t, writer, storage, before, after))
				if scenario == "success" {
					if err != nil || updates != 1 {
						t.Fatalf("real Update failed: updates=%d error=%v", updates, err)
					}
				} else {
					if err == nil || ticket.Change.Phase != Changing {
						t.Fatal("uncertain operation lost its submission intent:", err)
					}
					restartedClient, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
					if err != nil {
						t.Fatal(err)
					}
					restarted := Store{Client: restartedClient, APIReader: restartedClient, Namespace: store.Namespace, Identity: store.Identity}
					recovered, err := restarted.AcquireConfigurationChange(ctx, before, after)
					if err != nil || recovered.Change.ID != ticket.Change.ID || recovered.Change.Phase != Changing {
						t.Fatal("exact recovery with original file identities failed:", err)
					}
					_, err = restarted.ReconcileChange(ctx, recovered, configurationAdapter(t, writer, restartedClient, before, after))
					if scenario != "lost-reply" {
						if err == nil {
							t.Fatal("unchanged source was inferred aborted")
						}
						if err := store.CheckChange(ctx, ticket); err != nil {
							t.Fatal("unchanged source stopped excluding other writes:", err)
						}
						expected := 1
						if scenario == "never-submitted" {
							expected = 0
						}
						if updates != expected {
							t.Fatal("recovery retried an uncertain request")
						}
						return
					}
					if err != nil || updates != 1 {
						t.Fatal("real committed write was not recovered without resubmission:", err)
					}
				}
				current := before.DeepCopyObject().(client.Object)
				if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
					t.Fatal(err)
				}
				if current.GetUID() != before.GetUID() || current.GetResourceVersion() == before.GetResourceVersion() ||
					current.GetGeneration() != before.GetGeneration()+1 {
					t.Fatal("real API did not preserve UID and advance resourceVersion/generation")
				}
				if err := storage.Update(ctx, after.DeepCopyObject().(client.Object)); !apierrors.IsConflict(err) {
					t.Fatal("old-RV delayed request could mutate an observed committed version:", err)
				}
			})
		}
	}

	for _, kind := range []string{"Sandbox", "AgentPolicy"} {
		t.Run(kind+"/schema-defaults", func(t *testing.T) {
			store, before, after := newFixture(t, kind, "defaults")
			switch object := after.(type) {
			case *api.Sandbox:
				object.Spec.Type = ""
			case *api.AgentPolicy:
				object.Spec.Security.ReadOnlyRootFilesystem = nil
				object.Spec.Network.Ingress.Mode = ""
				object.Spec.Network.BlockedPorts = []api.AgentPolicyPort{{Port: 8080}}
				object.Spec.Network.AllowedInternalCIDRs = []api.AgentPolicyCIDRRule{
					{CIDR: "203.0.113.0/24", Ports: []api.AgentPolicyPort{{Port: 8443}}},
				}
			}
			ticket, err := store.AcquireConfigurationChange(ctx, before, after)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReconcileChange(ctx, ticket,
				configurationAdapter(t, storage, storage, before, after)); err != nil {
				t.Fatal("valid API-defaulted update could not complete:", err)
			}
			if _, err := store.ReadConfigurationChange(ctx, before, after); !errors.Is(err, ErrUnavailable) {
				t.Fatal("completed defaulted update retained its reservation:", err)
			}
		})
	}

	for _, kind := range []string{"Sandbox", "AgentPolicy"} {
		t.Run(kind+"/effective-metadata", func(t *testing.T) {
			store, before, _ := newFixture(t, kind, "metadata")
			execution := acquire(t, store, configurationIntent(t, before, "metadata"))
			after := before.DeepCopyObject().(client.Object)
			after.SetAnnotations(map[string]string{"example.com/description": "updated"})
			ticket, err := store.AcquireConfigurationChange(ctx, before, after)
			if err != nil {
				t.Fatal("metadata-only update blocked the recorded execution:", err)
			}
			if _, err := store.ReconcileChange(ctx, ticket,
				configurationAdapter(t, storage, storage, before, after)); err != nil {
				t.Fatal("actual metadata Update did not complete:", err)
			}
			current := before.DeepCopyObject().(client.Object)
			if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
				t.Fatal(err)
			}
			if current.GetUID() != before.GetUID() || current.GetResourceVersion() == before.GetResourceVersion() ||
				current.GetAnnotations()["example.com/description"] != "updated" {
				t.Fatal("metadata update was not persisted")
			}
			if err := store.Check(ctx, execution); err != nil {
				t.Fatal("actual metadata Update changed execution ownership:", err)
			}
		})
	}

	for _, scenario := range []string{"source-race", "source-replaced", "metadata-mismatch", "status-advanced"} {
		t.Run(scenario, func(t *testing.T) {
			store, before, after := newFixture(t, "Team", scenario)
			ticket := changingConfiguration(t, store, before, after)
			updates := 0
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
					updates++
					if scenario == "source-race" {
						raced := before.DeepCopyObject().(client.Object)
						raced.SetLabels(map[string]string{"example.com/race": "changed"})
						if err := c.Update(ctx, raced); err != nil {
							t.Fatal(err)
						}
					}
					return c.Update(ctx, object, opts...)
				},
			})
			adapter := configurationAdapter(t, writer, storage, before, after)
			if scenario == "source-replaced" {
				uid, rv := before.GetUID(), before.GetResourceVersion()
				if err := storage.Delete(ctx, before, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil {
					t.Fatal(err)
				}
				replacement := configurationObject("Team")
				replacement.SetName(before.GetName())
				if err := storage.Create(ctx, replacement); err != nil {
					t.Fatal(err)
				}
				if err := adapter.Submit(ctx, ticket, func(ctx context.Context) error { return store.CheckChange(ctx, ticket) }); !errors.Is(err, ErrConflict) || updates != 0 {
					t.Fatal("replacement source reached Update:", err)
				}
			} else {
				err := adapter.Submit(ctx, ticket, func(ctx context.Context) error { return store.CheckChange(ctx, ticket) })
				if scenario == "source-race" {
					if !apierrors.IsConflict(err) || updates != 1 {
						t.Fatal("API CAS conflict was retried or hidden:", err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					current := &api.Team{}
					if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
						t.Fatal(err)
					}
					if scenario == "metadata-mismatch" {
						current.Annotations["example.com/unapproved"] = "different"
						err = storage.Update(ctx, current)
					} else {
						current.Status.ObservedGeneration = current.Generation
						err = storage.Status().Update(ctx, current)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := store.ReconcileChange(ctx, ticket, configurationAdapter(t, nil, storage, before, after))
			if scenario == "status-advanced" {
				if err != nil {
					t.Fatal("read-only status evolution hid matching desired content:", err)
				}
			} else {
				if err == nil {
					t.Fatal("replacement/mismatched source was considered committed")
				}
				if err := store.CheckChange(ctx, ticket); err != nil {
					t.Fatal("unknown result released its reservation:", err)
				}
			}
		})
	}
}
