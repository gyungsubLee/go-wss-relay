GO := $(CURDIR)/.tools/go/bin/go
BUF := $(CURDIR)/.tools/bin/buf
GO_ENV := GOCACHE=$(CURDIR)/.cache/go-build GOMODCACHE=$(CURDIR)/.cache/go-mod

.PHONY: tools proto-generate proto-lint proto-breaking proto-baseline go-tidy go-test relay-build protocol-check csharp-compat

tools:
	./scripts/bootstrap-tools.sh

# Generate both versions in staging so a failed generator leaves tracked output intact.
proto-generate: tools
	@set -eu; \
	  mkdir -p "$(CURDIR)/.cache"; \
	  stage=$$(mktemp -d "$(CURDIR)/.cache/proto-generate.XXXXXX"); \
	  trap 'rm -rf -- "$$stage"' 0 1 2 15; \
	  $(BUF) generate --output "$$stage"; \
	  for version in v1 v2; do test -s "$$stage/gen/go/relay/$$version/relay.pb.go"; done; \
	  for version in v1 v2; do \
	    mkdir -p "$(CURDIR)/gen/go/relay/$$version"; \
	    mv "$$stage/gen/go/relay/$$version/relay.pb.go" "$(CURDIR)/gen/go/relay/$$version/relay.pb.go"; \
	  done

proto-lint: tools
	$(BUF) lint

proto-breaking: tools
	$(BUF) breaking --against api/relay/relay-baseline.binpb

proto-baseline: tools
	$(BUF) build -o api/relay/relay-baseline.binpb

go-tidy: tools
	$(GO_ENV) $(GO) mod tidy

go-test: tools
	$(GO_ENV) $(GO) test ./...

relay-build: tools
	mkdir -p $(CURDIR)/out
	$(GO_ENV) $(GO) build -o $(CURDIR)/out/wss-relay ./cmd/wss-relay

csharp-compat:
	dotnet restore --artifacts-path $(CURDIR)/out/dotnet --locked-mode test/compat/csharp/Relay.Protocol.Compat.csproj
	dotnet run --artifacts-path $(CURDIR)/out/dotnet --no-restore --project test/compat/csharp/Relay.Protocol.Compat.csproj -- internal/protocol/testdata/v1-golden.json

protocol-check: proto-lint proto-breaking proto-generate
	git diff --exit-code -- api/relay gen/go/relay
	test -z "$$(git ls-files --others --exclude-standard -- api/relay gen/go/relay)"
	$(GO_ENV) $(GO) test ./internal/protocol ./internal/protocolv2 ./internal/relayv2
