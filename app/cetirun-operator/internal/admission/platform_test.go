// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplatform"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func platformFixtureDocument(f fixture, runtimeUID, storageUID types.UID) sandboxplatform.Configuration {
	registration := f.handler.registrations[api.SandboxKata]
	registration.Capabilities.Protocols = []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP}
	return sandboxplatform.Configuration{
		Version: "v1", Registrations: map[api.SandboxType]sandbox.Registration{api.SandboxKata: registration},
		RuntimeClasses: map[string]sandboxplatform.RuntimePrerequisite{
			"test-kata": {UID: runtimeUID, Handler: "kata"},
		},
		StorageClasses: map[string]sandboxplatform.StoragePrerequisite{
			"test-home": {UID: storageUID, Provisioner: "storage.example.com/csi"},
		},
		Platforms: map[api.SandboxType]sandboxpolicy.Platform{
			api.SandboxKata: {
				Adapter: sandboxpolicy.AdapterRegistration{
					Name: sandboxpolicy.AzureCNIOverlayCilium, Revision: "synthetic-only", Qualified: true, Available: true,
				},
				Topology: sandboxpolicy.Topology{
					Revision: "synthetic-topology", ManagedPeersComplete: true,
					PodCIDRs: []string{"10.244.0.0/16"}, ServiceCIDRs: []string{"10.96.0.0/12"},
					NodeCIDRs: []string{"192.168.0.0/16"}, ManagementCIDRs: []string{"10.1.0.0/16"},
					MetadataCIDRs: []string{"169.254.169.254/32"}, DNSResolverIPs: []string{"10.96.0.10"},
					DNS: []sandboxpolicy.Peer{{Name: "dns", Namespace: "kube-system", UID: "synthetic-dns", IPs: []string{"10.96.0.10"}}},
					Gateway: []sandboxpolicy.Destination{{
						Peer:  sandboxpolicy.Peer{Name: "gateway", Namespace: "platform", UID: "synthetic-gateway", IPs: []string{"10.96.1.50"}},
						Ports: []api.AgentPolicyPort{{Port: 8443, Protocol: corev1.ProtocolTCP}},
					}},
					Ingress: []sandboxpolicy.Destination{{
						Peer:  sandboxpolicy.Peer{Name: "ingress", Namespace: "platform", UID: "synthetic-ingress", IPs: []string{"10.96.1.60"}},
						Ports: []api.AgentPolicyPort{{Port: 8080, Protocol: corev1.ProtocolTCP}},
					}},
				},
			},
		},
	}
}

func platformFixtureConfig() Config {
	config := testConfig()
	config.PlatformNamespace, config.PlatformConfigMapName = "operator-test", "sandbox-platform"
	config.TeamAssignments = []TeamAssignment{{Team: "engineering", Users: []string{"member"}}}
	return config
}

func newPlatformFixture(t *testing.T) (fixture, *corev1.ConfigMap) {
	t.Helper()
	f := newFixture(t)
	for _, add := range []func() error{
		func() error { return corev1.AddToScheme(f.client.Scheme()) },
		func() error { return nodev1.AddToScheme(f.client.Scheme()) },
		func() error { return storagev1.AddToScheme(f.client.Scheme()) },
	} {
		if err := add(); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(platformFixtureDocument(f, "runtime-uid", "storage-uid"))
	if err != nil {
		t.Fatal(err)
	}
	config := platformFixtureConfig()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: config.PlatformConfigMapName, Namespace: config.PlatformNamespace, UID: "platform-uid"},
		Data:       map[string]string{sandboxplatform.ConfigurationKey: string(raw)},
	}
	for _, object := range []client.Object{
		cm,
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "test-kata", UID: "runtime-uid"}, Handler: "kata"},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "test-home", UID: "storage-uid"}, Provisioner: "storage.example.com/csi"},
	} {
		if err := f.client.Create(context.Background(), object); err != nil {
			t.Fatal(err)
		}
	}
	f.handler, err = NewHandler(f.client, config)
	if err != nil {
		t.Fatal(err)
	}
	return f, cm
}

func TestPlatformAdmissionProductionFactory(t *testing.T) {
	f, _ := newPlatformFixture(t)
	for _, object := range []client.Object{f.team, f.box, f.policy} {
		requireResponse(t, f.handler.Validate(context.Background(), requestFor(t, admissionv1.Create, object, nil, "administrator")), true, "")
	}
	f.agent.Spec.SandboxRef, f.agent.Spec.PolicyClass, f.agent.Spec.Team = "", "", ""
	response := f.handler.DefaultAgent(context.Background(), requestFor(t, admissionv1.Create, f.agent, nil, "member"))
	requireResponse(t, response, true, "")
	if len(response.Patches) == 0 || f.handler.platformSnapshot != nil || f.handler.registrations != nil {
		t.Fatal("production source must serve request-local decisions without persisting registration state")
	}
	requireResponse(t, f.handler.DefaultAgent(context.Background(), requestFor(t, admissionv1.Create, f.agent, nil, "outsider")), true, "legacy")
	f.agent.Spec.Team = f.team.Name
	requireResponse(t, f.handler.DefaultAgent(context.Background(), requestFor(t, admissionv1.Create, f.agent, nil, "outsider")), false, "assigned Team")
	f.agent.Spec.PolicyClass = api.AgentPolicyClassAdmin
	requireResponse(t, f.handler.DefaultAgent(context.Background(), requestFor(t, admissionv1.Create, f.agent, nil, "member")), false, "non-normal")
}

func TestPlatformAdmissionNoLastKnownGoodFallback(t *testing.T) {
	for _, failure := range []string{"missing", "malformed", "unqualified", "runtime replaced", "storage replaced"} {
		t.Run(failure, func(t *testing.T) {
			f, cm := newPlatformFixture(t)
			request := requestFor(t, admissionv1.Create, f.team, nil, "administrator")
			requireResponse(t, f.handler.Validate(context.Background(), request), true, "")
			switch failure {
			case "missing":
				if err := f.client.Delete(context.Background(), cm); err != nil {
					t.Fatal(err)
				}
			case "malformed", "unqualified":
				if failure == "malformed" {
					cm.Data[sandboxplatform.ConfigurationKey] = `{"invalid":"do-not-log-this-platform-value"}`
				} else {
					cm.Data[sandboxplatform.ConfigurationKey] = strings.ReplaceAll(cm.Data[sandboxplatform.ConfigurationKey], `"qualified":true`, `"qualified":false`)
				}
				if err := f.client.Update(context.Background(), cm); err != nil {
					t.Fatal(err)
				}
			case "runtime replaced":
				current := &nodev1.RuntimeClass{}
				key := client.ObjectKey{Name: "test-kata"}
				if err := f.client.Get(context.Background(), key, current); err != nil {
					t.Fatal(err)
				}
				current.UID = "replacement-runtime"
				if err := f.client.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			case "storage replaced":
				current := &storagev1.StorageClass{}
				if err := f.client.Get(context.Background(), client.ObjectKey{Name: "test-home"}, current); err != nil {
					t.Fatal(err)
				}
				current.UID = "replacement-storage"
				if err := f.client.Update(context.Background(), current); err != nil {
					t.Fatal(err)
				}
			}
			response := f.handler.Validate(context.Background(), request)
			requireResponse(t, response, false, "dependencies are unavailable")
			if response.Result.Code != 503 || strings.Contains(response.Result.Message, "do-not-log") {
				t.Fatal("dependency failure must be explicit and payload-free")
			}
		})
	}
}

type platformInterceptReader struct {
	client.Reader
	before func(context.Context, client.Object) error
}

func (r platformInterceptReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if r.before != nil {
		if err := r.before(ctx, object); err != nil {
			return err
		}
	}
	return r.Reader.Get(ctx, key, object, options...)
}

func TestPlatformAdmissionRechecksBeforeAuthorizing(t *testing.T) {
	f, cm := newPlatformFixture(t)
	reads := 0
	reader := platformInterceptReader{Reader: f.client, before: func(_ context.Context, object client.Object) error {
		if _, ok := object.(*corev1.ConfigMap); !ok {
			return nil
		}
		reads++
		if reads == 3 {
			cm.Annotations = map[string]string{"example.com/revision": "changed"}
			return f.client.Update(context.Background(), cm)
		}
		return nil
	}}
	var err error
	f.handler, err = NewHandler(reader, platformFixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	requireResponse(t, f.handler.Validate(context.Background(), requestFor(t, admissionv1.Create, f.team, nil, "administrator")), false, "dependencies")
	requireResponse(t, f.handler.Validate(context.Background(), requestFor(t, admissionv1.Create, f.team, nil, "administrator")), true, "")
}

func TestPlatformAdmissionOutageDoesNotBlockIndependentRequests(t *testing.T) {
	f, cm := newPlatformFixture(t)
	if err := f.client.Delete(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	legacy := f.agent.DeepCopy()
	legacy.Spec.Team, legacy.Spec.SandboxRef, legacy.Spec.PolicyClass = "", "", api.AgentPolicyClassNormal
	plain := f.policy.DeepCopy()
	plain.Name = "unreferenced"
	requests := []admission.Request{
		requestFor(t, admissionv1.Create, legacy, nil, "unassigned-member"),
		requestFor(t, admissionv1.Delete, f.agent, nil, "member"),
		requestFor(t, admissionv1.Create, plain, nil, "administrator"),
		requestFor(t, admissionv1.Delete, plain, nil, "administrator"),
		requestFor(t, admissionv1.Update, f.team, f.team.DeepCopy(), "member"),
	}
	status := requestFor(t, admissionv1.Update, f.agent, f.agent.DeepCopy(), testController)
	status.SubResource = "status"
	requests = append(requests, status)
	held := f.agent.DeepCopy()
	now := metav1.Now()
	held.DeletionTimestamp = &now
	held.Finalizers = append(held.Finalizers, sandbox.HoldFinalizer)
	deleting := f.agent.DeepCopy()
	deleting.DeletionTimestamp = &now
	cleanup := requestFor(t, admissionv1.Update, deleting, held, testController)
	requests = append(requests, cleanup)
	for i, request := range requests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			requireResponse(t, f.handler.Validate(context.Background(), request), true, "")
		})
	}
	requireResponse(t, f.handler.DefaultAgent(context.Background(), requests[0]), true, "legacy")
	requireResponse(t, f.handler.Validate(context.Background(), requestFor(t, admissionv1.Delete, f.policy, nil, "administrator")), false, "referenced")
}

func TestPlatformAdmissionSuspensionOnly(t *testing.T) {
	for _, mode := range []string{ValidationStrict, ValidationCompatible} {
		for _, failure := range []string{"missing", "malformed"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				f, cm := newPlatformFixture(t)
				f.handler.config.ValidationMode = mode
				stampFixtureAuthorization(t, f)
				f.agent.Finalizers = append(f.agent.Finalizers, sandbox.HoldFinalizer, sandbox.PreparationFinalizer)
				suspended := f.agent.DeepCopy()
				suspended.Spec.Suspended = true
				suspended.Generation++
				suspended.ManagedFields = []metav1.ManagedFieldsEntry{{
					Manager: "synthetic-client", Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: api.GroupVersion.String(),
				}}
				reauthorized := suspended.DeepCopy()
				requestReauthorization(t, f.handler, reauthorized, "synthetic-suspension-request")
				if failure == "missing" {
					if err := f.client.Delete(context.Background(), cm); err != nil {
						t.Fatal(err)
					}
				} else {
					cm.Data[sandboxplatform.ConfigurationKey] = `{"invalid":"synthetic-invalid-platform"}`
					if err := f.client.Update(context.Background(), cm); err != nil {
						t.Fatal(err)
					}
				}
				source, reads := f.handler.platformSource, 0
				f.handler.platformSource = platformSnapshotFunc(func(ctx context.Context) (sandboxplatform.Snapshot, error) {
					reads++
					return source.Snapshot(ctx)
				})
				for _, user := range []string{"administrator", "member", "outsider", testController, "system:anonymous"} {
					for _, explicitFalse := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/explicit-false=%v", user, explicitFalse), func(t *testing.T) {
							request := requestFor(t, admissionv1.Update, suspended, f.agent, user)
							if explicitFalse {
								addUnknownField(t, &request.OldObject, "spec", "suspended", false)
							}
							if mode == ValidationCompatible {
								for _, raw := range []*runtime.RawExtension{&request.Object, &request.OldObject} {
									for _, section := range []string{"spec", "status"} {
										addUnknownField(t, raw, section, "futureCounter", json.Number("9007199254740992"))
									}
								}
							}
							allowed := user == "administrator" || user == "member"
							message := "authenticated assigned Team member"
							if allowed {
								message = "suspension-only"
								mutation := f.handler.DefaultAgent(context.Background(), request)
								requireResponse(t, mutation, true, "")
								if len(mutation.Patches) != 0 {
									t.Fatal("suspension must not change defaults or authorization")
								}
							}
							before := reads
							requireResponse(t, f.handler.Validate(context.Background(), request), allowed, message)
							if reads != before {
								t.Fatal("suspension or caller-authority checks consulted platform inventory")
							}
						})
					}
				}
				for _, test := range []struct {
					name string
					edit func(*admission.Request)
				}{
					{"creation", func(r *admission.Request) {
						r.Operation, r.OldObject = admissionv1.Create, runtime.RawExtension{}
					}},
					{"resume", func(r *admission.Request) {
						addUnknownField(t, &r.OldObject, "spec", "suspended", true)
						addUnknownField(t, &r.Object, "spec", "suspended", false)
					}},
					{"already suspended", func(r *admission.Request) {
						addUnknownField(t, &r.OldObject, "spec", "suspended", true)
					}},
					{"no transition", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "suspended", false)
					}},
					{"Team binding", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "team", "another-team")
					}},
					{"Sandbox binding", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "sandboxRef", "another-sandbox")
					}},
					{"policy class", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "policyClass", api.AgentPolicyClassAdmin)
					}},
					{"model", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "model", "another-model")
					}},
					{"Secret reference", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "gatewayTokenSecretRef", "another-secret")
					}},
					{"labels", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "labels", map[string]string{"example.com/note": "changed"})
					}},
					{"annotations", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "annotations", map[string]string{
							sandboxbinding.AuthorizationAnnotation: f.agent.Annotations[sandboxbinding.AuthorizationAnnotation],
							"example.com/note":                     "changed",
						})
					}},
					{"finalizers", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "finalizers", append(suspended.GetFinalizers(), "example.com/other"))
					}},
					{"owner references", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "ownerReferences", []metav1.OwnerReference{{
							APIVersion: api.GroupVersion.String(), Kind: "Team", Name: f.team.Name, UID: f.team.UID,
						}})
					}},
					{"authorization record", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "annotations", map[string]string{
							sandboxbinding.AuthorizationAnnotation: "synthetic-invalid-authorization",
						})
					}},
					{"reauthorization", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "annotations", reauthorized.Annotations)
					}},
					{"UID", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "uid", "replacement-agent")
					}},
					{"resourceVersion", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "metadata", "resourceVersion", "2")
					}},
					{"status", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "status", "phase", api.PhaseReady)
					}},
					{"future nested spec", func(r *admission.Request) {
						addUnknownField(t, &r.Object, "spec", "agent", map[string]any{
							"type": api.AgentTypeHermes, "futureCapability": true,
						})
					}},
				} {
					t.Run(test.name, func(t *testing.T) {
						request := requestFor(t, admissionv1.Update, suspended, f.agent, "administrator")
						test.edit(&request)
						requireResponse(t, f.handler.Validate(context.Background(), request), false, "")
					})
				}
				for _, section := range []string{"spec", "metadata", "status", "futureSection"} {
					t.Run("changed future field/"+section, func(t *testing.T) {
						request := requestFor(t, admissionv1.Update, suspended, f.agent, "administrator")
						addUnknownField(t, &request.OldObject, section, "futureCounter", json.Number("9007199254740992"))
						addUnknownField(t, &request.Object, section, "futureCounter", json.Number("9007199254740993"))
						requireResponse(t, f.handler.Validate(context.Background(), request), false, "")
					})
				}
			})
		}
	}
}

func TestPlatformAdmissionReadsRespectTotalDeadline(t *testing.T) {
	f, _ := newPlatformFixture(t)
	reader := platformInterceptReader{Reader: f.client, before: func(ctx context.Context, object client.Object) error {
		if _, ok := object.(*corev1.ConfigMap); ok {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}}
	handler, err := NewHandler(reader, platformFixtureConfig())
	if err != nil {
		t.Fatal(err)
	}
	handler.requestTimeout = 10 * time.Millisecond
	begin := time.Now()
	requireResponse(t, handler.Validate(context.Background(), requestFor(t, admissionv1.Create, f.team, nil, "administrator")), false, "dependencies")
	if time.Since(begin) > time.Second {
		t.Fatal("source reads exceeded the request budget")
	}
}

type platformSnapshotFunc func(context.Context) (sandboxplatform.Snapshot, error)

func (f platformSnapshotFunc) Snapshot(ctx context.Context) (sandboxplatform.Snapshot, error) {
	return f(ctx)
}

func TestPlatformAdmissionRejectsStaleAndFutureSnapshots(t *testing.T) {
	for _, observed := range []time.Time{{}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)} {
		f, _ := newPlatformFixture(t)
		source := f.handler.platformSource
		f.handler.platformSource = platformSnapshotFunc(func(ctx context.Context) (sandboxplatform.Snapshot, error) {
			snapshot, err := source.Snapshot(ctx)
			snapshot.ObservedAt = observed
			return snapshot, err
		})
		requireResponse(t, f.handler.Validate(context.Background(), requestFor(t, admissionv1.Create, f.team, nil, "administrator")), false, "dependencies")
	}
}

func TestPlatformAdmissionConcurrentRequestsDoNotShareSnapshots(t *testing.T) {
	f, _ := newPlatformFixture(t)
	request := requestFor(t, admissionv1.Create, f.team, nil, "administrator")
	var group sync.WaitGroup
	responses := make(chan admission.Response, 12)
	for range cap(responses) {
		group.Go(func() { responses <- f.handler.Validate(context.Background(), request) })
	}
	group.Wait()
	close(responses)
	for response := range responses {
		requireResponse(t, response, true, "")
	}
	if f.handler.platformSnapshot != nil {
		t.Fatal("request snapshot escaped to the shared handler")
	}
}
