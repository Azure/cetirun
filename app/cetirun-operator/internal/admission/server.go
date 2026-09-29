// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type ServerOptions struct {
	CertDirectory  string
	Host           string
	Port           int
	MetricsAddress string
	ProbeAddress   string
}

// Run serves only admission. It never starts runtime reconcilers, participates
// in controller leader election, or creates/renews serving certificates.
func Run(ctx context.Context, restConfig *rest.Config, config Config, options ServerOptions) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if options.CertDirectory == "" || options.Port < 1 || options.Port > 65535 {
		return fmt.Errorf("Sandbox admission requires an explicit certificate directory and valid serving port")
	}
	if err := checkServingCertificate(options.CertDirectory, time.Now()); err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	server := webhook.NewServer(webhook.Options{
		Host: options.Host, Port: options.Port, CertDir: options.CertDirectory,
		TLSOpts: []func(*tls.Config){func(config *tls.Config) {
			config.MinVersion = tls.VersionTLS12
			config.NextProtos = []string{"http/1.1"}
			// Read the projected pair on each handshake. Rotation and readiness
			// must not depend on a background filesystem watch staying healthy.
			config.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return loadServingCertificate(options.CertDirectory, time.Now())
			}
		}},
	})
	manager, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme, LeaderElection: false, WebhookServer: server,
		Metrics:                metricsserver.Options{BindAddress: options.MetricsAddress},
		HealthProbeBindAddress: options.ProbeAddress,
	})
	if err != nil {
		return fmt.Errorf("configure Sandbox admission server: %w", err)
	}
	server = manager.GetWebhookServer()
	handler, err := NewHandler(manager.GetAPIReader(), config)
	if err != nil {
		return err
	}
	server.Register(ValidationPath, &admission.Webhook{Handler: admission.HandlerFunc(handler.Validate)})
	server.Register(DefaultingPath, &admission.Webhook{Handler: admission.HandlerFunc(handler.DefaultAgent)})
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("webhook", server.StartedChecker()); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("serving-certificate", func(*http.Request) error {
		return checkServingCertificate(options.CertDirectory, time.Now())
	}); err != nil {
		return err
	}
	return manager.Start(ctx)
}

func checkServingCertificate(directory string, now time.Time) error {
	_, err := loadServingCertificate(directory, now)
	return err
}

func loadServingCertificate(directory string, now time.Time) (*tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(directory, "tls.crt"), filepath.Join(directory, "tls.key"))
	if err != nil {
		return nil, fmt.Errorf("load platform-issued Sandbox admission certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse Sandbox admission certificate: %w", err)
	}
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, fmt.Errorf("Sandbox admission serving certificate is outside its validity period")
	}
	return &pair, nil
}
