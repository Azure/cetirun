# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# cetirun-operator Makefile

# Image name for the operator
IMG ?= cetirun.azurecr.io/cetirun-operator:latest

# Go build settings
GOFLAGS ?=
CGO_ENABLED ?= 0
VERSION ?= dev
GIT_SHA ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo.Version=$(VERSION) \
	-X github.com/Azure/CetiRun/app/cetirun-operator/internal/buildinfo.Commit=$(GIT_SHA)
CONTROLLER_GEN_VERSION := v0.21.0
CONTROLLER_GEN ?= controller-gen

.PHONY: all build build-terminal test vet fmt lint docker-build docker-push install-crd \
	tools generate manifests check-controller-gen sync-crds check-crds \
	verify-generated test-envtest

CRD_SOURCE_DIR := config/crd/bases
CRD_CHART_DIR := chart/templates/crds
CRD_FILES := cetirun.microsoft.com_agents.yaml cetirun.microsoft.com_teams.yaml \
	cetirun.microsoft.com_sandboxes.yaml cetirun.microsoft.com_agentpolicies.yaml

all: build

## Build the operator binary
build:
	CGO_ENABLED=$(CGO_ENABLED) go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/manager ./cmd/main.go

## Build the independent Custom terminal entrypoint
build-terminal:
	CGO_ENABLED=$(CGO_ENABLED) go build $(GOFLAGS) -o bin/terminal ./cmd/terminal

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
	docker build -f Dockerfile -t $(IMG) \
		--build-arg VERSION=$(VERSION) --build-arg GIT_SHA=$(GIT_SHA) ../../

## Push the Docker image
docker-push:
	docker push $(IMG)

## Explicit tool setup (network access may be required; not an offline check).
tools:
	GOBIN="$(CURDIR)/bin" GOTOOLCHAIN=local go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

## Generate DeepCopy methods from API types
generate: check-controller-gen
	$(CONTROLLER_GEN) object paths="./api/..."

## Generate CRD YAML from API types; chart RBAC remains a source template
manifests: check-controller-gen
	$(CONTROLLER_GEN) crd paths="./api/..." \
		output:crd:artifacts:config=$(CRD_SOURCE_DIR)
	$(MAKE) sync-crds

## Copy generated CRDs into Helm-managed templates.
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

verify-generated: generate manifests
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go $(CRD_SOURCE_DIR) $(CRD_CHART_DIR)

## Uses only a local API server and etcd; never connects to an existing cluster.
test-envtest:
	@test -n "$(KUBEBUILDER_ASSETS)" && test -x "$(KUBEBUILDER_ASSETS)/kube-apiserver" && test -x "$(KUBEBUILDER_ASSETS)/etcd" || { echo "Set KUBEBUILDER_ASSETS to preinstalled envtest binaries"; exit 1; }
	USE_EXISTING_CLUSTER=false go test -tags=envtest ./test/integration ./internal/helm ./internal/admission ./internal/controller -count=1 -v

## Clean build artifacts
clean:
	rm -rf bin/
