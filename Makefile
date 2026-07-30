SHELL := /bin/sh

VERSION_FILE := VERSION
VERSION ?= $(strip $(shell cat $(VERSION_FILE)))
COMMIT ?= dev
BUILD_TIME ?= 1970-01-01T00:00:00Z
NPM_AUDIT_REGISTRY ?= https://registry.npmjs.org
NPM_CI_FLAGS ?=
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)

.PHONY: version bootstrap brand-assets icons fmt lint webui test test-race build fpk-files fpk checksums artifact-check verify clean

version:
	@printf '%s\n' "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z]+)*$$' || { echo "VERSION must contain a release version such as 0.1.0" >&2; exit 1; }

bootstrap: version
	@go version | grep 'go1.26'
	@node --version | grep '^v24\.'
	@npm --version
	cd web && npm ci --ignore-scripts $(NPM_CI_FLAGS)
	@command -v fnpack >/dev/null 2>&1 || echo "fnpack: not found; deterministic static FPK builder will be used"

brand-assets:
	go run ./scripts/generate-brand-assets

icons: brand-assets
	go run ./scripts/generate-web-icons

fmt: icons
	@test -z "$$(gofmt -l cmd internal scripts)" || { gofmt -l cmd internal scripts; exit 1; }
	cd web && npm run format:check

lint: webui
	sh -n scripts/*.sh packaging/fnos/common/cmd/*
	go vet ./cmd/... ./internal/... ./scripts/...
	cd web && npm run lint
	cd web && npm run typecheck
	cd web && npm audit --audit-level=high --registry=$(NPM_AUDIT_REGISTRY)

webui: icons
	cd web && npm run build

test: webui
	go test ./cmd/... ./internal/... ./scripts/...
	cd web && npm test -- --run

test-race:
	go test -race ./internal/app ./internal/config ./internal/http

build: webui
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dockfn ./cmd/dockfn
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dockfn-linux-amd64 ./cmd/dockfn
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/dockfn-linux-arm64 ./cmd/dockfn

fpk-files: build
	sh scripts/build-fpk.sh $(VERSION)

checksums: fpk-files
	@find dist/fpk -maxdepth 1 -type f -name '*.fpk' -print | LC_ALL=C sort | xargs sha256sum >dist/SHA256SUMS
	@sha256sum -c dist/SHA256SUMS

artifact-check: checksums
	go run ./scripts/verify-artifacts -version $(VERSION)

verify: bootstrap fmt lint test test-race
	@echo "verify: all automated DockFN tests passed"

fpk: bootstrap artifact-check
	@echo "fpk: DockFN packages built and verified"

clean:
	rm -rf bin dist internal/webui/dist web/dist web/node_modules web/test-results web/playwright-report coverage
