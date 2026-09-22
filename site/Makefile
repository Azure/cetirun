# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
HUGO_VERSION := 0.164.0
HUGO ?= $(CURDIR)/.bin/hugo
CACHE_HOME ?= $(if $(XDG_CACHE_HOME),$(XDG_CACHE_HOME),$(HOME)/.cache)
HUGO_CACHEDIR ?= $(CACHE_HOME)/cetirun/hugo
BASE_URL ?= https://azure.github.io/cetirun/
PORT ?= 1313
BIND ?= 127.0.0.1
POLICY_ROOT ?= $(abspath ..)
export HUGO_CACHEDIR
export CETIRUN_POLICY_ROOT := $(POLICY_ROOT)

.PHONY: help deps build check test headers serve preflight

help:
	@echo "make check  Check headers, build, links, navigation, content, and accessibility"
	@echo "make test   Run offline validator regression tests (Python 3.11+)"
	@echo "make serve  Preview at http://127.0.0.1:$(PORT)/cetirun/"
	@echo "Overrides: HUGO=/path/to/hugo BASE_URL=https://host/path/ PORT=1313"

$(CURDIR)/.bin/hugo: go.mod
	@if test -x "$@" && "$@" version | grep -Eq 'v$(HUGO_VERSION)\+extended([ /]|$$)'; then \
		echo "Reusing Hugo $(HUGO_VERSION) extended"; \
	else \
		mkdir -p "$(CURDIR)/.bin"; \
		CGO_ENABLED=1 GOBIN="$(CURDIR)/.bin" go install -tags extended github.com/gohugoio/hugo@v$(HUGO_VERSION); \
	fi

node_modules/.package-lock.json: package.json package-lock.json
	npm ci --ignore-scripts --no-audit --no-fund

preflight:
	@case "$(abspath $(HUGO_CACHEDIR))/" in "$(abspath ..)"/*) \
		echo "HUGO_CACHEDIR must be outside the worktree" >&2; exit 1;; esac

deps: preflight $(HUGO) node_modules/.package-lock.json
	@$(HUGO) version | grep -Eq 'v$(HUGO_VERSION)\+extended([ /]|$$)' || \
		{ echo "Hugo $(HUGO_VERSION) extended is required; set HUGO=/path/to/hugo" >&2; exit 1; }

build: deps
	$(HUGO) --gc --minify --cleanDestinationDir --baseURL "$(BASE_URL)"

test:
	python3 -m unittest discover -s tests -v

headers:
	python3 ../scripts/check-license-headers.py --root ..

check: test headers build
	python3 scripts/check_site.py --public public --base-url "$(BASE_URL)" --policy-root "$(POLICY_ROOT)"

serve: deps
	$(HUGO) server --renderToMemory --disableFastRender --bind "$(BIND)" --port "$(PORT)" \
		--baseURL "http://localhost:$(PORT)/cetirun/"
