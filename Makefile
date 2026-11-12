GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint vuln live check all clean

all: lint test vuln build

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/garmin-mcp ./cmd/garmin-mcp

test:
	go test -race -count=1 ./...

lint:
	$(GOLANGCI_LINT) run ./...

vuln:
	$(GOVULNCHECK) ./...

# Calls every tool against the real API. Needs GARMIN_API_KEY (or .env).
live:
	GARMIN_ENV_FILE=$(CURDIR)/.env go test -tags live -run TestLive -count=1 -v ./internal/tools/

check: build
	./bin/garmin-mcp -env-file .env -check

clean:
	rm -rf bin
