.PHONY: build dist test test-race check fmt tidy clean

VERSION := $(shell cat VERSION)
LDFLAGS := -s -w -X main.Version=$(VERSION)

DIST := dist
CHECKSUMS := $(DIST)/checksums.txt

# Frozen asset names (eregion-binary-distribution.md §3).
ASSETS := \
	eregion-linux-amd64 \
	eregion-linux-arm64 \
	eregion-darwin-amd64 \
	eregion-darwin-arm64 \
	eregion-windows-amd64.exe

build:
	go build -ldflags "$(LDFLAGS)" -o bin/eregion ./cmd/eregion

# Cross-compile the release matrix into dist/ and write SHA-256 checksums.
dist: clean-dist
	@mkdir -p $(DIST)
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/eregion-linux-amd64 ./cmd/eregion
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/eregion-linux-arm64 ./cmd/eregion
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/eregion-darwin-amd64 ./cmd/eregion
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/eregion-darwin-arm64 ./cmd/eregion
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/eregion-windows-amd64.exe ./cmd/eregion
	@cd $(DIST) && sha256sum $(ASSETS) > checksums.txt
	@echo "Wrote $(CHECKSUMS)"
	@cat $(CHECKSUMS)

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

clean-dist:
	rm -rf $(DIST)

clean: clean-dist
	rm -rf bin/
