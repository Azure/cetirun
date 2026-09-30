# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

PYTHON ?= python3
REGISTRY ?=
VERSION ?= $(shell scripts/cetirun-release-metadata.sh version)
GIT_SHA ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERIFY_PULL ?= true
.DEFAULT_GOAL := help

.PHONY: help check test lint docs-check license-check ci-test python-test \
	python-check go-test go-lint portal-test portal-lint chart-check \
	bicep-check shell-check workflow-check version-check set-version \
	build-images publish-images publish-charts publish require-registry

.NOTPARALLEL: publish publish-images publish-charts

help:
	@echo "Offline validation (install tools/dependencies separately):"
	@echo "  make check       Run lint and unit tests; no cluster/cloud or downloads"
	@echo "  make test        Go, portal, auth, Teams, and CI helper unit tests"
	@echo "  make lint        Headers, Go vet, TypeScript, Python, Helm, Bicep, shell, workflows"
	@echo "  make docs-check  Run make -C site check (separate from check)"
	@echo "Release:"
	@echo "  make set-version VERSION=<version>  (e.g. 0.1.0-dev.1)"
	@echo "  make build-images REGISTRY=<registry> [VERSION=X.Y.Z]"
	@echo "  make publish REGISTRY=<registry> [VERSION=X.Y.Z]"
	@echo "Prerequisites: Go from .go-version, Node 24 + npm ci, scripts/ci/requirements.txt,"
	@echo "  helm, bicep, shellcheck, actionlint, scripts/check-license-headers.py."
	@echo "Workflow lint covers seven validation workflows; publisher/E2E workflows are excluded."
	@echo "See python3 scripts/ci/validate.py --help for individual gates."

check: lint test

test: ci-test python-test go-test portal-test

lint: version-check license-check python-check go-lint portal-lint chart-check bicep-check shell-check workflow-check

docs-check license-check ci-test python-test python-check go-test go-lint portal-test portal-lint chart-check bicep-check shell-check workflow-check:
	$(PYTHON) -B scripts/ci/validate.py $@

require-registry:
	@test -n "$(REGISTRY)" || { echo "REGISTRY is required" >&2; exit 1; }

version-check:
	$(PYTHON) -B scripts/cetirun-version.py check "$(VERSION)"

set-version:
	@command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
	$(PYTHON) -B scripts/cetirun-version.py set "$(VERSION)"
	helm dependency update --skip-refresh app/cetirun-platform
	$(PYTHON) -B scripts/cetirun-version.py check "$(VERSION)"

build-images:
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=false BUILD_HERMES=true scripts/cetirun-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=false scripts/operator-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=false bash scripts/terminal-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=false scripts/portal-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=false scripts/ai-gateway-docker-build.sh

publish-images: require-registry version-check
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=true BUILD_HERMES=true scripts/cetirun-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=true scripts/operator-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=true bash scripts/terminal-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=true scripts/portal-docker-build.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" GIT_SHA="$(GIT_SHA)" PUSH=true scripts/ai-gateway-docker-build.sh

publish-charts: require-registry version-check
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" VERIFY_PULL="$(VERIFY_PULL)" scripts/cetirun-helm-release.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" VERIFY_PULL="$(VERIFY_PULL)" scripts/operator-helm-release.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" VERIFY_PULL="$(VERIFY_PULL)" scripts/portal-helm-release.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" VERIFY_PULL="$(VERIFY_PULL)" scripts/platform-helm-release.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" VERIFY_PULL="$(VERIFY_PULL)" scripts/istio-helm-release.sh
	REGISTRY="$(REGISTRY)" VERSION="$(VERSION)" VERIFY_PULL="$(VERIFY_PULL)" scripts/ai-gateway-helm-release.sh

publish: publish-images publish-charts
