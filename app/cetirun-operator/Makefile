# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# cetirun-operator Makefile

# Image name for the operator
IMG ?= cetirun.azurecr.io/cetirun-operator:latest

# Go build settings
GOFLAGS ?=
CGO_ENABLED ?= 0
CONTROLLER_GEN_VERSION := v0.21.0
CONTROLLER_GEN ?= controller-gen

.PHONY: all build test vet fmt lint docker-build docker-push install-crd generate manifests check-controller-gen sync-crds check-crds

CRD_SOURCE_DIR := config/crd/bases
CRD_CHART_DIR := chart/crds
CRD_FILES := cetirun.microsoft.com_agents.yaml cetirun.microsoft.com_teams.yaml

all: build

## Build the operator binary
build:
	CGO_ENABLED=$(CGO_ENABLED) go build $(GOFLAGS) -o bin/manager ./cmd/main.go

## Run unit tests
test:
	go test ./... -v -count=1

## Run go vet
vet:
	go vet ./...

## Run gofmt
fmt:
	gofmt -w .

## Download dependencies
deps:
	go mod download
	go mod tidy

## Build the Docker image
docker-build:
	docker build -f Dockerfile -t $(IMG) ../../

## Push the Docker image
docker-push:
	docker push $(IMG)

## Generate DeepCopy methods from API types
generate: check-controller-gen
	$(CONTROLLER_GEN) object paths="./api/..."

## Generate CRD YAML from API types; chart RBAC remains a source template
manifests: check-controller-gen
	$(CONTROLLER_GEN) crd paths="./api/..." \
		output:crd:artifacts:config=$(CRD_SOURCE_DIR)
	$(MAKE) sync-crds

## Copy generated CRDs into the Helm chart's standard CRD directory.
sync-crds:
	mkdir -p $(CRD_CHART_DIR)
	cp $(addprefix $(CRD_SOURCE_DIR)/,$(CRD_FILES)) $(CRD_CHART_DIR)/

## Verify that the chart packages the generated CRDs without drift.
check-crds:
	@set -e; for file in $(CRD_FILES); do \
		diff -u $(CRD_SOURCE_DIR)/$$file $(CRD_CHART_DIR)/$$file; \
	done

check-controller-gen:
	@test "$$($(CONTROLLER_GEN) --version)" = "Version: $(CONTROLLER_GEN_VERSION)" || \
		{ echo "Install controller-gen $(CONTROLLER_GEN_VERSION) or set CONTROLLER_GEN to its path" >&2; exit 1; }

## Clean build artifacts
clean:
	rm -rf bin/
