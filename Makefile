.PHONY: build test test-race check fmt tidy

VERSION := $(shell cat VERSION)

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o bin/eregion ./cmd/eregion

test:
	go test ./...

test-race:
	@echo "Requires CGO (gcc). On Linux CI: CGO_ENABLED=1 go test -race ./..."
	CGO_ENABLED=1 go test -race ./...

check:
	go test ./internal/config ./internal/protocol ./internal/worker

fmt:
	gofmt -w ./cmd ./internal ./tests

tidy:
	go mod tidy
