GO := $(CURDIR)/.tools/go/bin/go
BUF := $(CURDIR)/.tools/bin/buf
GO_ENV := GOCACHE=$(CURDIR)/.cache/go-build GOMODCACHE=$(CURDIR)/.cache/go-mod

.PHONY: tools proto-generate proto-lint proto-breaking proto-baseline go-tidy go-test relay-build protocol-check

tools:
	./scripts/bootstrap-tools.sh

# Generate the v2 schema in staging so a failed generator leaves tracked output intact.
proto-generate: tools
	@set -eu; \
	  mkdir -p "$(CURDIR)/.cache"; \
	  stage=$$(mktemp -d "$(CURDIR)/.cache/proto-generate.XXXXXX"); \
	  trap 'rm -rf -- "$$stage"' 0 1 2 15; \
	  $(BUF) generate --output "$$stage"; \
	  test -s "$$stage/gen/go/relay/v2/relay.pb.go"; \
	  mkdir -p "$(CURDIR)/gen/go/relay/v2"; \
	  mv "$$stage/gen/go/relay/v2/relay.pb.go" "$(CURDIR)/gen/go/relay/v2/relay.pb.go"

proto-lint: tools
	$(BUF) lint

proto-breaking: tools
	$(BUF) breaking --against api/relay/v2/relay-baseline.binpb

proto-baseline: tools
	$(BUF) build -o api/relay/v2/relay-baseline.binpb

go-tidy: tools
	$(GO_ENV) $(GO) mod tidy

go-test: tools
	$(GO_ENV) $(GO) test ./...

relay-build: tools
	mkdir -p $(CURDIR)/out
	$(GO_ENV) $(GO) build -o $(CURDIR)/out/wss-relay ./cmd/wss-relay

protocol-check: proto-lint proto-breaking proto-generate
	git diff --exit-code -- api/relay gen/go/relay
	test -z "$$(git ls-files --others --exclude-standard -- api/relay gen/go/relay)"
	$(GO_ENV) $(GO) test ./internal/protocol ./internal/relay
