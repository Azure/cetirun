# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

.PHONY: build test vet chart-lint

VERSION ?= $(shell ../../scripts/cetirun-release-metadata.sh version)
LDFLAGS ?= -X main.Version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" ./...

test:
	go test ./... -count=1

vet:
	go vet ./...

chart-lint:
	helm lint chart -f chart/ci/render-values.yaml
	helm template cetirun-skill-marketplace chart -f chart/ci/render-values.yaml >/dev/null
