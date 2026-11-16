MODULE   := github.com/luthermonson/litecone
BIN      := litecone
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test vet cross clean install tidy pack-check check

build: ## build ./bin/litecone for the host
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/litecone

install: ## go install
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags '$(LDFLAGS)' ./cmd/litecone

test: ## unit tests
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

check: vet test build pack-check

# Cross-compile every target into dist/. Static (CGO_ENABLED=0) so each
# artifact is one self-contained file.
cross: ## cross-compile for linux/darwin/windows x amd64/arm64
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  out=dist/$(BIN)_$(VERSION)_$${os}_$${arch}$$ext; \
	  echo "  $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $$out ./cmd/litecone || exit 1; \
	done

pack-check: ## validate the pack skeleton (TOML, step graph, skills, doctor)
	go test ./internal/packcheck/

clean:
	rm -rf bin dist
