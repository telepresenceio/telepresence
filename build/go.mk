.DEFAULT_GOAL = all

# Ensure that go environment prerequisites are met
GOPATH=$(shell go env GOPATH)
GOHOSTOS=$(shell go env GOHOSTOS)
GOHOSTARCH=$(shell go env GOHOSTARCH)

BINDIR=bin_$(GOHOSTOS)_$(GOHOSTARCH)
GOBIN=$(word 1, $(subst :, ,$(GOPATH)))/bin

RED='\033[1;31m'
GRN='\033[1;32m'
BLU='\033[1;34m'
CYN='\033[1;36m'
END='\033[0m'

PROTOC_VERSION=3.13.0

# Install protoc under $GOBIN
$(GOBIN)/protoc:
	curl -sfL https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$(GOHOSTOS)-$(shell uname -m).zip -o /tmp/protoc-$(PROTOC_VERSION).zip
	cd $(dir $(GOBIN)) && unzip /tmp/protoc-$(PROTOC_VERSION).zip

# Install protoc-gen and protoc-gen-go-grpc
$(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc:
	go get github.com/golang/protobuf/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc

.PHONY: protoc-tools
protoc-tools: go.mod $(GOBIN)/protoc $(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc $(GOBIN)/protoc

# proto/gRPC generation using protoc
pkg/%.pb.go pkg/%_grpc.pb.go: %.proto
	protoc --proto_path=. --go_out=. --go-grpc_out=. $<

TP_RPC_FILES = pkg/rpc/daemon.pb.go pkg/rpc/daemon_grpc.pb.go

.PHONY: generate
generate: protoc-tools $(TP_RPC_FILES) ## (Generate) update generated files that get checked in to Git.

.PHONY: generate-clean
generate-clean: ## (Generate) delete generated files that get checked in to Git.
	rm -rf pkg/rpc/*

# pkg sources excluding rpc
TP_PKG_SOURCES = $(shell find pkg -type f -name '*.go' | grep -v '/testdata/' | grep -v '_test.go' | grep -v '/rpc/')

$(BINDIR)/telepresence: main.go $(TP_PKG_SOURCES) $(TP_RPC_FILES)
	go build -o $(BINDIR)/telepresence .

.PHONY: build
build: $(BINDIR)/telepresence  ## (Build) runs go build

.phony: clean
clean: ## (Build) cleans built artefacts
	rm -rf $(BINDIR)

$(BINDIR)/golangci-lint: ## (Lint) install golangci-lint
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(BINDIR) latest

.phony: lint
lint: $(BINDIR)/golangci-lint ## (Lint) runs golangci-lint
	$(BINDIR)/golangci-lint run ./...

.phony: test
test: build  ## (Test) runs go test
	go test .

.PHONY: all
all: test

.PHONY: help
help:  ## (Common) Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^#]*) *:[^#]* *[#]# *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
