BINARY      := tyr
PKG         := github.com/noahzmr/tyr
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) \
               -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) -X $(PKG)/internal/buildinfo.Date=$(DATE)
GOFLAGS     := -trimpath
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
DIST        := dist

export CGO_ENABLED=0

.PHONY: all build run demo test test-short live lint fmt fixtures golden cross clean

FIXTURE ?= server-degraded

all: lint test build

build:
	@mkdir -p $(DIST)
	@if [ -d cmd/tyr ]; then go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY) ./cmd/tyr; \
	else echo "cmd/tyr not implemented yet (step 3)"; fi

# Live TUI on this machine (sudo for dmidecode, /dev/kmsg, IPMI).
run: build
	./$(DIST)/$(BINARY)

# TUI against a fixture: make demo FIXTURE=server-healthy
demo: build
	./$(DIST)/$(BINARY) --fixtures testdata/fixtures/$(FIXTURE)

test:
	go test ./...

test-short:
	go test -short ./...

# Collect from this machine (read-only).
live:
	TYR_LIVE=1 go test -count=1 -run TestLiveHost -v ./internal/collector/...

lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt needed" && exit 1)
	go vet ./...
	go run $(STATICCHECK) ./...

fmt:
	gofmt -w .

# Regenerate synthetic server fixtures.
fixtures:
	go run ./tools/genfixture -out testdata/fixtures

# Regenerate the workload golden table (only after changing a kernel!).
golden:
	go test ./internal/workload -run TestGoldenTable -update

cross:
	@mkdir -p $(DIST)
	@for arch in amd64 arm64; do \
		echo "linux/$$arch"; \
		GOOS=linux GOARCH=$$arch go vet ./... || exit 1; \
		if [ -d cmd/tyr ]; then GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o $(DIST)/$(BINARY)-linux-$$arch ./cmd/tyr || exit 1; \
		else GOOS=linux GOARCH=$$arch go build ./... || exit 1; fi; \
	done

clean:
	rm -rf $(DIST)
