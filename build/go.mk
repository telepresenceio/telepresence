# .DEFAULT_GOAL = all

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

# assume first directory in path is the local go directory
GOLOCAL=$(word 1, $(subst :, ,$(GOPATH)))
GOSRC=$(GOLOCAL)/src
GOBIN=$(GOLOCAL)/bin

export PATH := $(BUILDDIR)/bin:$(PATH)

# proto/gRPC generation using protoc
pkg/%.pb.go pkg/%_grpc.pb.go: %.proto $(PROTOC) $(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc
	$(PROTOC) --proto_path=. --go_out=. --go-grpc_out=. --go_opt=module=github.com/datawire/telepresence2 --go-grpc_opt=module=github.com/datawire/telepresence2 $<

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
	go build -o $@ ./$(<D)

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

.PHONY: lint
lint: $(GOLANGCI_LINT) $(PROTOLINT) ## (Lint) runs golangci-lint and protolint
	$(GOLANGCI_LINT) run ./...
	$(PROTOLINT) lint $(shell find rpc -type f -name '*.proto')

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
