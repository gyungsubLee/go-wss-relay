GO := $(CURDIR)/.tools/go/bin/go
BUF := $(CURDIR)/.tools/bin/buf
GO_ENV := GOCACHE=$(CURDIR)/.cache/go-build GOMODCACHE=$(CURDIR)/.cache/go-mod

.PHONY: tools proto-generate proto-lint proto-breaking proto-baseline go-tidy go-test relay-build protocol-check

tools:
	./scripts/bootstrap-tools.sh

proto-generate: tools
	sh scripts/generate-protocol.sh

proto-lint: tools
	$(BUF) lint

proto-breaking: tools
	$(BUF) breaking --against api/relay/v2/relay-baseline.binpb

proto-baseline: tools
	@echo "The v2 baseline is immutable; baseline replacement requires an explicit review." >&2
	@exit 1

go-tidy: tools
	$(GO_ENV) $(GO) mod tidy

go-test: tools
	$(GO_ENV) $(GO) test ./...

relay-build: tools
	mkdir -p $(CURDIR)/out
	$(GO_ENV) $(GO) build -o $(CURDIR)/out/wss-relay ./cmd/wss-relay

protocol-check: proto-lint proto-breaking
	sh scripts/generate-protocol.sh check
	$(GO_ENV) $(GO) test ./internal/protocol ./internal/relay
