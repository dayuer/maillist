GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: test lint build upstream

test:
	go test -race -count=1 ./...

lint:
	$(GOLANGCI) run ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/mailcheap ./cmd/mailcheap

upstream:
	scripts/fetch-upstream.sh
