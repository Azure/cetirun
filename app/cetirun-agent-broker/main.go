// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Azure/CetiRun/app/cetirun-agent-broker/internal/broker"
)

func main() {
	cfg, err := broker.LoadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	service, err := broker.NewServer(cfg)
	if err != nil {
		slog.Error("initialize service", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: cfg.ListenAddress, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	if cfg.TLSCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			slog.Error("load TLS certificate", "error", err)
			os.Exit(1)
		}
		clientCAPEM, err := os.ReadFile(cfg.TLSClientCAFile)
		if err != nil {
			slog.Error("read TLS client CA", "error", err)
			os.Exit(1)
		}
		clientCAs := x509.NewCertPool()
		if !clientCAs.AppendCertsFromPEM(clientCAPEM) {
			slog.Error("load TLS client CA", "error", errors.New("no certificates found"))
			os.Exit(1)
		}
		tlsServer := &http.Server{Addr: cfg.TLSListenAddress, Handler: service.TLSHandler(), TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, ClientCAs: clientCAs, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
		go func() {
			slog.Info("Agent Identity Broker TLS listener started", "address", cfg.TLSListenAddress)
			if tlsErr := tlsServer.ListenAndServeTLS("", ""); tlsErr != nil && tlsErr != http.ErrServerClosed {
				slog.Error("serve TLS", "error", tlsErr)
				os.Exit(1)
			}
		}()
	}
	slog.Info("Agent Identity Broker listening", "address", cfg.ListenAddress)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
