# Builds are throttled: the dev box has ~2 GB RAM and compiling
# modernc.org/sqlite and gRPC in parallel runs it out of memory.
GO       ?= GOFLAGS=-p=1 GOMAXPROCS=1 go
VERSION  ?= 0.0.2
LDFLAGS  := -X github.com/nosaka4i/sentient/internal/version.Version=$(VERSION)
TOOLS    := .claudetmp/tools

.PHONY: build install build-mac test fix jstest cover vet fmt check proto tools

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/sen ./cmd/sentientd

# Installs sen and sentientd to $GOBIN (default $GOPATH/bin), with the version
# stamped in like build does. Make sure that directory is on your PATH.
install:
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/sen ./cmd/sentientd

# PKG narrows it to one package: make test PKG=./internal/web
# Binaries for an Apple-silicon Mac, in bin/darwin-arm64/ (pure Go: no CGO,
# so they cross-compile here). Copy them to the Mac with scp or rsync.
build-mac:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -ldflags "$(LDFLAGS)" -o bin/darwin-arm64/ ./cmd/sen ./cmd/sentientd

PKG ?= ./...
test:
	$(GO) test -cover $(PKG)

# Rewrites files that aren't gofmt-formatted.
fix:
	gofmt -w cmd internal

# Tests of the prompts page's script (needs node 20+; nothing to install).
jstest:
	node --test web/test/*.test.js

cover:
	$(GO) test -cover ./...

vet:
	$(GO) vet ./...

# Fails, listing the files, if any isn't gofmt-formatted.
fmt:
	@out="$$(gofmt -l cmd internal)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# Everything to run before calling a change done.
check: fmt vet cover jstest

# Regenerate internal/api/sentientpb from sentient.proto (generated code is
# committed). Needs `make tools` once.
proto:
	PATH="$(abspath $(TOOLS)):$$PATH" buf generate

# buf plus the protoc-gen-go / protoc-gen-go-grpc plugins, local to the repo.
tools:
	mkdir -p $(TOOLS)
	curl -sSL -o $(TOOLS)/buf https://github.com/bufbuild/buf/releases/download/v1.47.2/buf-$$(uname -s)-$$(uname -m)
	chmod +x $(TOOLS)/buf
	GOBIN=$(abspath $(TOOLS)) go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
	GOBIN=$(abspath $(TOOLS)) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
