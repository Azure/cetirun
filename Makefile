# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

PYTHON ?= python3
.DEFAULT_GOAL := help

.PHONY: help check test lint docs-check license-check ci-test python-test \
	python-check go-test go-lint portal-test portal-lint chart-check \
	bicep-check shell-check workflow-check

help:
	@echo "Offline validation (install tools/dependencies separately):"
	@echo "  make check       Run lint and unit tests; no cluster/cloud or downloads"
	@echo "  make test        Go, portal, auth, Teams, and CI helper unit tests"
	@echo "  make lint        Headers, Go vet, TypeScript, Python, Helm, Bicep, shell, workflows"
	@echo "  make docs-check  Run make -C site check (separate from check)"
	@echo "Prerequisites: Go from .go-version, Node 24 + npm ci, scripts/ci/requirements.txt,"
	@echo "  helm, bicep, shellcheck, actionlint, scripts/check-license-headers.py."
	@echo "Workflow lint covers six validation workflows; publisher/E2E workflows are excluded."
	@echo "See python3 scripts/ci/validate.py --help for individual gates."

check: lint test

test: ci-test python-test go-test portal-test

lint: license-check python-check go-lint portal-lint chart-check bicep-check shell-check workflow-check

docs-check license-check ci-test python-test python-check go-test go-lint portal-test portal-lint chart-check bicep-check shell-check workflow-check:
	$(PYTHON) -B scripts/ci/validate.py $@
