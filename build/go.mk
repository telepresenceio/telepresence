.DEFAULT_GOAL = all

# Delete implicit rules not used here (clutters debug output)
.SUFFIXES:
%:: RCS/%,v
%:: RCS/%
%:: s.%
%:: %,v
%:: SCCS/s.%

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
PROTOC_ZIP=protoc-$(PROTOC_VERSION)-$(subst darwin,osx,$(GOHOSTOS))-$(shell uname -m).zip
$(PROTOC):
	mkdir -p $(BINDIR)
	curl -sfL https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/$(PROTOC_ZIP) -o $(BUILDDIR)/$(PROTOC_ZIP)
	cd $(BUILDDIR) && unzip $(PROTOC_ZIP)

# Install protoc-gen and protoc-gen-go-grpc
$(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc: go.mod
	go get github.com/golang/protobuf/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc

# proto/gRPC generation using protoc
pkg/%.pb.go pkg/%_grpc.pb.go: %.proto $(PROTOC) $(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc
	$(PROTOC) --proto_path=$(GOPATH)/src:. --go_out=$(GOPATH)/src --go-grpc_out=$(GOPATH)/src $<

GRPC_NAMES = connector daemon manager
GRPC_PB_GO_FILES = $(foreach proto, $(GRPC_NAMES), pkg/rpc/$(proto)/$(proto)_grpc.pb.go)

PROTO_NAMES = iptables version $(GRPC_NAMES)
PB_GO_FILES = $(foreach proto, $(PROTO_NAMES), pkg/rpc/$(proto)/$(proto).pb.go)

RPC_FILES=$(PB_GO_FILES) $(GRPC_PB_GO_FILES)

.PHONY: generate
generate: $(PB_GO_FILES) $(GRPC_PB_GO_FILES) ## (Generate) update generated files that get checked in to Git.

.PHONY: generate-clean
generate-clean: ## (Generate) delete generated files that get checked in to Git.
	rm -rf pkg/rpc/*

# pkg sources excluding rpc
TP_PKG_SOURCES = $(shell find pkg -type f -name '*.go' | grep -v '_test.go' | grep -v '/rpc/')
TP_TEST_SOURCES = $(shell find cmd pkg -type f -name '*_test.go')

EXECUTABLES=$(shell ls cmd)

.PHONY: build
build: $(foreach exe, $(EXECUTABLES), $(BINDIR)/$(exe)) ## (Build) runs go build

$(BINDIR)/%: cmd/%/main.go $(TP_PKG_SOURCES) $(RPC_FILES)
	mkdir -p $(BINDIR)
	go build -o $@ $<

.PHONY: install
install: $(foreach exe, $(EXECUTABLES), $(GOBIN)/$(exe))  ## (Install) runs go install

$(GOBIN)/%: cmd/%/main.go $(TP_PKG_SOURCES) $(TP_RPC_FILES)
	cd $(<D) && go install

.PHONY: docker-build
docker-build: ## (Install) runs docker build for all executables
	for exe in $(EXECUTABLES) ; do \
		docker build --target $$exe --tag datawire/$$exe .; \
	done

.PHONY: clean
clean: ## (Build) cleans built artefacts
	rm -rf $(BUILDDIR)

$(BINDIR)/golangci-lint: ## (Lint) install golangci-lint
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(BINDIR) latest

PROTOLINT_VERSION=0.26.0
PROTOLINT_TGZ=protolint_$(PROTOLINT_VERSION)_$(shell uname -s)_$(shell uname -m).tar.gz
$(BINDIR)/protolint: ## (Lint) install protolint
	mkdir -p $(BINDIR)
	curl -sfL https://github.com/yoheimuta/protolint/releases/download/v$(PROTOLINT_VERSION)/$(PROTOLINT_TGZ) -o $(BUILDDIR)/$(PROTOLINT_TGZ)
	tar -C $(BINDIR) -zxf $(BUILDDIR)/$(PROTOLINT_TGZ)

.PHONY: lint
lint: $(BINDIR)/golangci-lint $(BINDIR)/protolint ## (Lint) runs golangci-lint and protolint
	$(BINDIR)/golangci-lint run ./...
	$(BINDIR)/protolint lint $(shell find rpc -type f -name '*.proto')

.PHONY: test
test: build $(TP_TEST_SOURCES) ## (Test) runs go test
	go test -v ./...

.PHONY: all
all: test

.PHONY: help
help:  ## (Common) Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^:]*):[^#]*## *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
