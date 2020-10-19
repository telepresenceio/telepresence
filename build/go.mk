.DEFAULT_GOAL = all

# All build artifacts end up here except go packages. Their destination is controlled by the go environment
BUILDDIR=build-output
BINDIR=$(BUILDDIR)/bin

# Ensure that go environment prerequisites are met
GOPATH=$(shell go env GOPATH)
GOHOSTOS=$(shell go env GOHOSTOS)
GOHOSTARCH=$(shell go env GOHOSTARCH)
GOBIN=$(word 1, $(subst :, ,$(GOPATH)))/bin

export PATH := $(BUILDDIR)/bin:$(PATH)

# Install protoc under $BUILDDIR. A protoc that is already installed locally cannot be trusted since this must be the exact
# same version as used when running CI. If it isn't, the generate-check will fail.
PROTOC_VERSION=3.13.0
PROTOC=$(BINDIR)/protoc
$(PROTOC):
	mkdir -p $(BINDIR)
	curl -sfL https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$(GOHOSTOS)-$(shell uname -m).zip -o $(BUILDDIR)/protoc-$(PROTOC_VERSION).zip
	cd $(BUILDDIR) && unzip protoc-$(PROTOC_VERSION).zip

# Install protoc-gen and protoc-gen-go-grpc
$(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc: go.mod
	go get github.com/golang/protobuf/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc

# proto/gRPC generation using protoc
pkg/%.pb.go pkg/%_grpc.pb.go: %.proto $(PROTOC) $(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc $(GOBIN)/protoc
	$(PROTOC) --proto_path=. --go_out=. --go-grpc_out=. $<

TP_RPC_FILES = pkg/rpc/daemon.pb.go pkg/rpc/daemon_grpc.pb.go

.PHONY: generate
generate: $(TP_RPC_FILES) ## (Generate) update generated files that get checked in to Git.

.PHONY: generate-clean
generate-clean: ## (Generate) delete generated files that get checked in to Git.
	rm -rf pkg/rpc/*

# pkg sources excluding rpc
TP_PKG_SOURCES = $(shell find pkg -type f -name '*.go' | grep -v '/testdata/' | grep -v '_test.go' | grep -v '/rpc/')

$(BINDIR)/telepresence: main.go $(TP_PKG_SOURCES) $(TP_RPC_FILES)
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/telepresence .

.PHONY: build
build: $(BINDIR)/telepresence  ## (Build) runs go build

.phony: clean
clean: ## (Build) cleans built artefacts
	rm -rf $(BUILDDIR)

$(BINDIR)/golangci-lint: ## (Lint) install golangci-lint
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(BINDIR) latest

.phony: lint
lint: $(BINDIR)/golangci-lint ## (Lint) runs golangci-lint
	$(BINDIR)/golangci-lint run ./...

.phony: test
test: build ## (Test) runs go test
	go test .

.PHONY: all
all: test

.PHONY: help
help:  ## (Common) Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^:]*):[^#]*## *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
