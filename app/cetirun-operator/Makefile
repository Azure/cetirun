# cetirun-operator Makefile

# Image name for the operator
IMG ?= cetirun.azurecr.io/cetirun-operator:latest

# Go build settings
GOFLAGS ?=
CGO_ENABLED ?= 0

.PHONY: all build test vet fmt lint docker-build docker-push install-crd

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

## Generate CRD YAML (requires controller-gen)
manifests:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@latest \
		crd rbac:roleName=cetirun-operator-manager-role paths="./..." \
		output:crd:artifacts:config=config/crd/bases

## Clean build artifacts
clean:
	rm -rf bin/
