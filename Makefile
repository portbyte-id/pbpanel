GO      ?= go
NPM     ?= npm
BIN     ?= panel
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/portbyte-id/server-panel/internal/version.Version=$(VERSION)

.PHONY: all build web test vet lint fmt clean

all: lint test web build

## build: static Go binary (no cgo, so the release artefact runs on any Linux host)
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/panel

## web: reproducible frontend build (npm ci, not npm install), then its unit tests
web:
	cd web && $(NPM) ci && $(NPM) run build && $(NPM) test

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

## lint: convention greps plus golangci-lint when it is installed
lint:
	sh scripts/lint-conventions.sh
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found, skipped"; \
	fi

fmt:
	$(GO) fmt ./...

clean:
	rm -rf bin web/dist
