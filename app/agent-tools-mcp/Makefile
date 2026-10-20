# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

.PHONY: build test vet chart-lint

VERSION ?= $(shell ../../scripts/cetirun-release-metadata.sh version)
LDFLAGS ?= -X github.com/Azure/CetiRun/app/agent-tools-mcp/internal/mcp.Version=$(VERSION)
HELM_ARGS ?= --set tls.secretName=agent-tools-mcp-tls --set broker.clientTLSSecretName=broker-client-tls --set broker.baseURL=https://broker.example.com --set istio.sidecarInjection=true --set istio.revision=asm-1-28

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	go test ./... -count=1

vet:
	go vet ./...

chart-lint:
	helm lint chart $(HELM_ARGS)
	helm template agent-tools-mcp chart $(HELM_ARGS) >/dev/null
