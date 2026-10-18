# Builds are throttled: the dev box has ~2 GB RAM and compiling
# modernc.org/sqlite and gRPC in parallel runs it out of memory.
GO       ?= GOFLAGS=-p=1 GOMAXPROCS=1 go
VERSION  ?= 0.0.1
LDFLAGS  := -X github.com/nosaka4i/sentient/internal/version.Version=$(VERSION)
TOOLS    := .claudetmp/tools

.PHONY: build test cover proto tools

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/ ./cmd/sen ./cmd/sentientd

test:
	$(GO) test ./...

cover:
	$(GO) test -cover ./...

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
