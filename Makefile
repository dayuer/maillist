GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test lint build upstream

test:
	# -p 1: packages run one at a time. The slow-input tests measure elapsed time, and the packages
	# that feed them adversarial bodies are the same ones whose abandoned parses would otherwise be
	# eating the machine while another package is being timed.
	go test -race -count=1 -p 1 ./...

lint:
	$(GOLANGCI) run ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/nibwire ./cmd/nibwire

upstream:
	scripts/fetch-upstream.sh
