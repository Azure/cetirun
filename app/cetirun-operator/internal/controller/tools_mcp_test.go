// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	toolsMCPTestOperatorNamespace = "cetirun-system"
	toolsMCPTestSecretName        = "agent-tools-mcp-ca"
)

func TestToolsMCPOnlyDeletionBlocksUntilGrantsAreRevoked(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			instance := toolsMCPTestAgent()
			instance.UID = "11111111-1111-1111-1111-111111111111"
			instance.Finalizers = []string{api.Finalizer}
			reconciler, kube := toolsMCPTestReconciler(t, instance)
			if err := istiov1beta1.AddToScheme(reconciler.Scheme); err != nil {
				t.Fatal(err)
			}
			reconciler.EventRecorder = record.NewFakeRecorder(10)
			reconciler.HelmClient = &suspensionHelmClient{}
			calls := 0
			broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if request.Method != http.MethodDelete || request.URL.Path != "/operator/v1/instances/11111111-1111-1111-1111-111111111111/authorization" {
					t.Error("unexpected cleanup request")
				}
				writer.WriteHeader(status)
			}))
			defer broker.Close()
			reconciler.AgentIdentityBrokerURL = broker.URL
			reconciler.AgentIdentityHTTPClient = broker.Client()
			result, err := reconciler.handleDelete(context.Background(), instance)
			if err != nil || calls != 1 {
				t.Fatalf("cleanup request missing: %v, calls %d", err, calls)
			}
			var current api.Agent
			if err := kube.Get(context.Background(), types.NamespacedName{Name: instance.Name}, &current); err != nil {
				t.Fatal(err)
			}
			if status == http.StatusServiceUnavailable && (len(current.Finalizers) == 0 || result.RequeueAfter == 0) {
				t.Fatal("failed grant revocation removed finalizer")
			}
			if status == http.StatusNoContent && len(current.Finalizers) != 0 {
				t.Fatal("successful cleanup retained finalizer")
			}
		})
	}
}

func TestAgentGrantCleanupRejectsRedirects(t *testing.T) {
	redirectedCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedCalls++
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer broker.Close()
	reconciler := &AgentReconciler{AgentIdentityBrokerURL: broker.URL, AgentIdentityHTTPClient: broker.Client()}
	err := reconciler.deleteAgentAuthorization(context.Background(), "11111111-1111-1111-1111-111111111111")
	if err == nil || redirectedCalls != 0 {
		t.Fatalf("redirected cleanup = %v, target calls %d", err, redirectedCalls)
	}
}

func TestEnsureToolsMCPCACreatesOwnerBoundPublicCASecret(t *testing.T) {
	ctx := context.Background()
	ca := toolsMCPTestCA(t, 1)
	source := toolsMCPTestSource(ca)
	reconciler, kube := toolsMCPTestReconciler(t, source)
	instance := toolsMCPTestAgent()

	if err := reconciler.ensureToolsMCPCA(ctx, instance); err != nil {
		t.Fatalf("ensureToolsMCPCA() error = %v", err)
	}
	target := &corev1.Secret{}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: toolsMCPTestSecretName}
	if err := kube.Get(ctx, key, target); err != nil {
		t.Fatalf("get copied CA Secret: %v", err)
	}
	if len(target.Data) != 1 || string(target.Data["ca.crt"]) != string(ca) {
		t.Fatalf("target data = %#v, want only ca.crt", target.Data)
	}
	if len(target.OwnerReferences) != 1 || target.OwnerReferences[0].UID != instance.UID || target.OwnerReferences[0].Kind != "Agent" || target.OwnerReferences[0].Name != instance.Name {
		t.Fatalf("target owner references = %#v", target.OwnerReferences)
	}
}

func TestEnsureToolsMCPCARotationUpdatesSecretAndHash(t *testing.T) {
	ctx := context.Background()
	firstCA, secondCA := toolsMCPTestCA(t, 1), toolsMCPTestCA(t, 2)
	reconciler, kube := toolsMCPTestReconciler(t, toolsMCPTestSource(firstCA))
	instance := toolsMCPTestAgent()
	if err := reconciler.ensureToolsMCPCA(ctx, instance); err != nil {
		t.Fatalf("initial ensureToolsMCPCA() error = %v", err)
	}
	firstHash, err := reconciler.toolsMCPCAHash(ctx)
	if err != nil || firstHash == "" {
		t.Fatalf("initial toolsMCPCAHash() = %q, %v", firstHash, err)
	}
	source := &corev1.Secret{}
	sourceKey := types.NamespacedName{Namespace: toolsMCPTestOperatorNamespace, Name: toolsMCPTestSecretName}
	if err := kube.Get(ctx, sourceKey, source); err != nil {
		t.Fatal(err)
	}
	source.Data = map[string][]byte{"ca.crt": secondCA, "tls.key": []byte("source-private-key")}
	if err := kube.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	secondHash, err := reconciler.toolsMCPCAHash(ctx)
	if err != nil || secondHash == "" || secondHash == firstHash {
		t.Fatalf("rotated toolsMCPCAHash() = %q, %v; first = %q", secondHash, err, firstHash)
	}
	if err := reconciler.ensureToolsMCPCA(ctx, instance); err != nil {
		t.Fatalf("rotated ensureToolsMCPCA() error = %v", err)
	}
	target := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: toolsMCPTestSecretName}, target); err != nil {
		t.Fatal(err)
	}
	if len(target.Data) != 1 || string(target.Data["ca.crt"]) != string(secondCA) {
		t.Fatalf("rotated target data = %#v", target.Data)
	}
}

func TestEnsureToolsMCPCARejectsUnownedTarget(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: toolsMCPTestSecretName, Namespace: instanceNamespace(instance.Name), OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: "other", UID: "other-agent"}}},
		Data:       map[string][]byte{"ca.crt": []byte("unowned")},
	}
	reconciler, kube := toolsMCPTestReconciler(t, toolsMCPTestSource(toolsMCPTestCA(t, 1)), target)
	if err := reconciler.ensureToolsMCPCA(ctx, instance); err == nil {
		t.Fatal("ensureToolsMCPCA() accepted an unowned target")
	}
	stored := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: target.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if string(stored.Data["ca.crt"]) != "unowned" {
		t.Fatalf("unowned target was modified: %#v", stored.Data)
	}
}

func TestToolsMCPCARejectsMissingAndInvalidSources(t *testing.T) {
	tests := []struct {
		name   string
		source *corev1.Secret
	}{
		{name: "missing source"},
		{name: "missing CA", source: toolsMCPTestSource(nil)},
		{name: "invalid PEM", source: toolsMCPTestSource([]byte("not a certificate"))},
		{name: "private key material", source: toolsMCPTestSource(append(toolsMCPTestCA(t, 1), []byte("-----BEGIN PRIVATE KEY-----")...))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := []client.Object{}
			if test.source != nil {
				objects = append(objects, test.source)
			}
			reconciler, kube := toolsMCPTestReconciler(t, objects...)
			if _, err := reconciler.toolsMCPCA(context.Background()); err == nil {
				t.Fatal("toolsMCPCA() accepted an unavailable or invalid source")
			}
			if err := reconciler.ensureToolsMCPCA(context.Background(), toolsMCPTestAgent()); err == nil {
				t.Fatal("ensureToolsMCPCA() accepted an unavailable or invalid source")
			}
			target := &corev1.Secret{}
			key := types.NamespacedName{Namespace: instanceNamespace(toolsMCPTestAgent().Name), Name: toolsMCPTestSecretName}
			if err := kube.Get(context.Background(), key, target); err == nil {
				t.Fatal("target Secret was created from an unavailable or invalid source")
			}
		})
	}
}

func toolsMCPTestReconciler(t *testing.T, objects ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(api.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &AgentReconciler{Client: kube, Scheme: scheme, OperatorNamespace: toolsMCPTestOperatorNamespace, ToolsMCPURL: "https://agent-tools-mcp.example.com/mcp", ToolsMCPCASecretName: toolsMCPTestSecretName}, kube
}

func toolsMCPTestSource(ca []byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPTestSecretName, Namespace: toolsMCPTestOperatorNamespace}, Data: map[string][]byte{"ca.crt": ca, "tls.key": []byte("source-private-key")}}
}

func toolsMCPTestAgent() *api.Agent {
	return &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "tools-mcp", UID: "tools-mcp-agent"}}
}

func toolsMCPTestCA(t *testing.T, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "tools-mcp-test-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
