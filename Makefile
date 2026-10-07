VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -X github.com/rapando/groundwork/internal/cli.Version=$(VERSION) -X github.com/rapando/groundwork/internal/cli.Commit=$(COMMIT)

.PHONY: web build test lint e2e run golden integration perf fuzz release-snapshot demo
# The SPA is built into internal/server/dist, which is committed and embedded.
web:
	cd web && npm ci && npm run build
build: web
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/groundwork ./cmd/groundwork
test:
	go test ./...
	cd web && npm test
lint:
	golangci-lint run
# Browser tests drive the installed Chrome against the built binary.
e2e: build
	cd web && GW_BIN=$(CURDIR)/bin/groundwork npx playwright test
# Real terraform/tflint/yamllint/ansible-lint (whatever is installed). GW_TEST_NETWORK=1 adds provider downloads.
integration:
	go test -tags integration ./...
# Accept new detection output after a deliberate change.
golden:
	go test ./internal/workspace -update
run: build
	./bin/groundwork
# Timings on a generated 5,000-file repository.
perf:
	GW_PERF=1 go test ./internal/perf -run TestLargeRepo -v -count=1
# Each fuzz target for FUZZTIME (default 30s).
FUZZTIME ?= 30s
fuzz:
	@set -e; for t in files:FuzzSafePath terraform:FuzzParseLine ansible:FuzzParseEvent ansible:FuzzInventoryAndPatterns \
		redact:FuzzRedactsRegisteredSecret vars:FuzzScan vars:FuzzLiteralRoundTrip checks:FuzzToolParsers \
		config:FuzzParse graph:FuzzParseDOT diagnostics:FuzzMatchLog; do \
		pkg=$${t%%:*}; fn=$${t##*:}; echo "== $$fn"; go test ./internal/$$pkg -run '^$$' -fuzz "^$$fn\$$" -fuzztime $(FUZZTIME); done
# Release archives in dist/ without publishing (needs goreleaser on PATH).
release-snapshot:
	goreleaser release --snapshot --clean --skip=publish
# Install from local release archives and drive the demo end to end (see scripts/demo.sh).
demo: release-snapshot
	scripts/demo.sh
