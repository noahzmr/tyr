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

.PHONY: all build install uninstall run demo test test-short live lint fmt fixtures golden ll-table cross iso iso-arm64 diagrams clean \
	release-notes release-snapshot publish publish-retry

PREFIX  ?= /usr/local
ARCHIFY ?= $(HOME)/src/archify

FIXTURE ?= server-degraded

all: lint test build

build:
	@mkdir -p $(DIST)
	@if [ -d cmd/tyr ]; then go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY) ./cmd/tyr; \
	else echo "cmd/tyr not implemented yet (step 3)"; fi

# Static binary into $(PREFIX)/bin (sudo asks for the password).
install:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/tyr
	chmod +x $(BINARY)
	sudo mv $(BINARY) $(PREFIX)/bin/
	@echo "installed $(PREFIX)/bin/$(BINARY)"; $(PREFIX)/bin/$(BINARY) version

uninstall:
	sudo rm -f $(PREFIX)/bin/$(BINARY)

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

# Regenerate the torture test's residues (only after changing its cases;
# exact arithmetic with math/big, about a minute).
ll-table:
	go test ./internal/workload -run TestLLTable -update -timeout 30m

# Static binaries to copy to other hosts: dist/tyr-linux-{amd64,arm64}
# plus dist/SHA256SUMS (see README "Run on a remote host").
cross:
	@mkdir -p $(DIST)
	@for arch in amd64 arm64; do \
		echo "linux/$$arch"; \
		GOOS=linux GOARCH=$$arch go vet ./... || exit 1; \
		if [ -d cmd/tyr ]; then GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o $(DIST)/$(BINARY)-linux-$$arch ./cmd/tyr || exit 1; \
		else GOOS=linux GOARCH=$$arch go build ./... || exit 1; fi; \
	done
	@cd $(DIST) && sha256sum $(BINARY)-linux-* > SHA256SUMS && cat SHA256SUMS

# Releases (DEVELOPER.md "Releasing"): BUMP=patch|minor|major or RELEASE=vX.Y.Z;
# the first release is v1.0.0. publish needs GITHUB_TOKEN.
release-notes:
	@scripts/release.sh notes $(or $(RELEASE),$(BUMP))

release-snapshot:
	scripts/release.sh snapshot

publish:
	scripts/release.sh publish $(or $(RELEASE),$(BUMP))

publish-retry:
	scripts/release.sh retry

# Bootable live ISO that runs `tyr accept` (docs/acceptance.md); needs Docker.
iso:
	iso/build.sh amd64

iso-arm64:
	iso/build.sh arm64

# Re-render the archify diagrams (docs/diagrams/*.json → .html) and export
# the dual-theme SVGs the docs embed. ARCHIFY points at an archify checkout
# (https://github.com/tt-a1i/archify).
diagrams:
	@for f in docs/diagrams/*.json; do \
		name=$$(basename $$f .json); type=$${name##*.}; base=$${name%.*}; \
		node $(ARCHIFY)/bin/archify.mjs render $$type $$f docs/diagrams/$$base.html >/dev/null || exit 1; \
		node $(ARCHIFY)/bin/archify.mjs check docs/diagrams/$$base.html >/dev/null || exit 1; \
		python3 scripts/export-diagram.py docs/diagrams/$$base.html docs/images/$$base.svg || exit 1; \
		echo "docs/images/$$base.svg"; \
	done

clean:
	rm -rf $(DIST)
