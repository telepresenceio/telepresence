# Install tools used by the build

TOOLSDIR=tools
TOOLSBINDIR=$(TOOLSDIR)/bin

GOHOSTOS=$(shell go env GOHOSTOS)
GOHOSTARCH=$(shell go env GOHOSTARCH)

# assume first directory in path is the local go directory
GOPATH=$(shell go env GOPATH)
GOLOCAL=$(word 1, $(subst :, ,$(GOPATH)))
GOSRC=$(GOLOCAL)/src
GOBIN=$(GOLOCAL)/bin

export PATH := $(TOOLSDIR)/bin:$(GOBIN):$(PATH)

clobber: clobber-tools

.PHONY: clobber-tools

clobber-tools:
	rm -rf $(TOOLSDIR)

# Protobuf compiler
# =================

# Install protoc under $TOOLSDIR. A protoc that is already installed locally
# cannot be trusted since this must be the exact same version as used when
# running CI. If it isn't, the generate-check will fail.
PROTOC_VERSION=3.13.0
PROTOC=$(TOOLSBINDIR)/protoc
PROTOC_ZIP=protoc-$(PROTOC_VERSION)-$(subst darwin,osx,$(GOHOSTOS))-$(shell uname -m).zip
$(PROTOC):
	mkdir -p $(TOOLSBINDIR)
	curl -sfL https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/$(PROTOC_ZIP) -o $(TOOLSDIR)/$(PROTOC_ZIP)
	cd $(TOOLSDIR) && unzip -q $(PROTOC_ZIP)

# Install protoc-gen and protoc-gen-go-grpc
$(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc: go.mod
	go get github.com/golang/protobuf/protoc-gen-go google.golang.org/grpc/cmd/protoc-gen-go-grpc

# Install ko (needs to be done in /tmp to avoid conflicting updates of go.mod)
$(GOBIN)/ko: go.mod
	cd /tmp && go get github.com/google/ko/cmd/ko

# Linters
# =======

GOLANGCI_LINT=$(TOOLSBINDIR)/golangci-lint
$(GOLANGCI_LINT):
	curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(TOOLSBINDIR) latest

PROTOLINT_VERSION=0.26.0
PROTOLINT_TGZ=protolint_$(PROTOLINT_VERSION)_$(shell uname -s)_$(shell uname -m).tar.gz
PROTOLINT=$(TOOLSBINDIR)/protolint
$(PROTOLINT):
	mkdir -p $(TOOLSBINDIR)
	curl -sfL https://github.com/yoheimuta/protolint/releases/download/v$(PROTOLINT_VERSION)/$(PROTOLINT_TGZ) -o $(TOOLSDIR)/$(PROTOLINT_TGZ)
	tar -C $(TOOLSBINDIR) -zxf $(TOOLSDIR)/$(PROTOLINT_TGZ)
