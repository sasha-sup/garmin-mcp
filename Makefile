GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
ACTIONLINT    := go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint vuln live check all clean ci-lint

all: lint test vuln build

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/garmin-mcp ./cmd/garmin-mcp

test:
	go test -race -count=1 ./...

lint:
	$(GOLANGCI_LINT) run ./...

vuln:
	$(GOVULNCHECK) ./...

# Validates GitHub Actions workflows (uses shellcheck if installed).
ci-lint:
	$(ACTIONLINT)

# Calls every tool against the real API. Needs GARMIN_API_KEY (or .env).
live:
	GARMIN_ENV_FILE=$(CURDIR)/.env go test -tags live -run TestLive -count=1 -v ./internal/tools/

check: build
	./bin/garmin-mcp -env-file .env -check

clean:
	rm -rf bin
