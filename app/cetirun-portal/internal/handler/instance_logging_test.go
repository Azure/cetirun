// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"

	cetirunv1beta1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestToInstanceResponse_TailscaleLogging(t *testing.T) {
	const secretName = "tailscale-auth-log-test"
	const authKey = "tskey-auth-log-redaction-test"
	secretResource := schema.GroupResource{Resource: "secrets"}
	tests := []struct {
		name       string
		data       map[string][]byte
		getErr     error
		wantReason string
		wantKey    string
	}{
		{
			name:    "success",
			data:    map[string][]byte{"TAILSCALE_AUTH_KEY": []byte(authKey)},
			wantKey: authKey,
		},
		{
			name:       "not found",
			getErr:     apierrors.NewNotFound(secretResource, secretName),
			wantReason: "secret_not_found",
		},
		{
			name:       "forbidden",
			getErr:     apierrors.NewForbidden(secretResource, secretName, errors.New(authKey)),
			wantReason: "secret_access_denied",
		},
		{
			name:       "upstream error",
			getErr:     errors.New("upstream response contains " + authKey + "\nprivate error detail"),
			wantReason: "secret_read_failed",
		},
		{
			name:       "missing key",
			wantReason: "secret_read_failed",
		},
		{
			name:       "empty key",
			data:       map[string][]byte{"TAILSCALE_AUTH_KEY": {}},
			wantReason: "secret_read_failed",
		},
	}

	for _, phase := range []string{"Ready", "Provisioning"} {
		for _, tt := range tests {
			t.Run(phase+"/"+tt.name, func(t *testing.T) {
				instance := sampleInstance()
				instance.Status.Phase = phase
				instance.Spec.GatewayTokenSecretRef = "gateway-token"
				instance.Spec.Tailscale = &cetirunv1beta1.TailscaleSpec{
					AuthKeySecretRef: secretName,
				}
				gatewaySecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: instance.Status.Namespace, Name: "gateway-token"},
					Data: map[string][]byte{
						"OPENCLAW_GATEWAY_TOKEN": []byte("gateway-token-test"),
						"CETIRUN_AUTH_KEY":       []byte("cetirun-auth-test"),
					},
				}
				tailscaleSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: instance.Status.Namespace, Name: secretName},
					Data:       tt.data,
				}
				fc := fake.NewClientBuilder().WithScheme(newScheme()).
					WithObjects(gatewaySecret, tailscaleSecret).
					WithInterceptorFuncs(interceptor.Funcs{
						Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							if key == client.ObjectKeyFromObject(tailscaleSecret) && tt.getErr != nil {
								return tt.getErr
							}
							return c.Get(ctx, key, obj, opts...)
						},
					}).Build()
				h := &InstanceHandler{Kube: kube.NewClient(fc)}

				var logs bytes.Buffer
				previousLogger := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(previousLogger) })

				resp, err := h.toInstanceResponse(context.Background(), instance)
				if err != nil {
					t.Fatalf("toInstanceResponse() error = %v", err)
				}
				if resp.TailscaleAuthKey != tt.wantKey {
					t.Errorf("TailscaleAuthKey = %q, want %q", resp.TailscaleAuthKey, tt.wantKey)
				}
				if resp.GatewayToken != "gateway-token-test" || resp.CetiRunAuthKey != "cetirun-auth-test" {
					t.Error("unrelated credentials changed")
				}
				if resp.AccessURL != instance.Status.AccessURL {
					t.Errorf("AccessURL = %q, want %q", resp.AccessURL, instance.Status.AccessURL)
				}

				for _, value := range []string{secretName, authKey, "private error detail"} {
					if strings.Contains(logs.String(), value) {
						t.Errorf("logs contain excluded value %q: %s", value, logs.String())
					}
				}
				warnings := 0
				decoder := json.NewDecoder(&logs)
				for decoder.More() {
					var entry map[string]any
					if err := decoder.Decode(&entry); err != nil {
						t.Fatalf("decode log: %v", err)
					}
					if entry["msg"] != "Failed to get tailscale auth key" {
						continue
					}
					warnings++
					if entry["level"] != "WARN" || entry["alias"] != instance.Spec.User.Alias || entry["reason"] != tt.wantReason {
						t.Errorf("unexpected Tailscale warning: %v", entry)
					}
					if _, ok := entry["error"]; ok {
						t.Errorf("warning contains a raw error: %v", entry)
					}
				}
				wantWarnings := 1
				if tt.wantReason == "" {
					wantWarnings = 0
				}
				if warnings != wantWarnings {
					t.Errorf("Tailscale warnings = %d, want %d", warnings, wantWarnings)
				}
			})
		}
	}
}
