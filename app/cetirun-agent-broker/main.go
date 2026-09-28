// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
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
	slog.Info("Agent Identity Broker listening", "address", cfg.ListenAddress)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
