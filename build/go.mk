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

# proto/gRPC generation using protoc

PROTO_SRCS = $(shell echo rpc/*/*.proto)

.PHONY: generate
generate: ## (Generate) Update generated files that get checked in to Git
generate: $(PROTOC) $(GOBIN)/protoc-gen-go $(GOBIN)/protoc-gen-go-grpc
	$(PROTOC) --proto_path=. --go_out=. --go-grpc_out=. --go_opt=module=github.com/datawire/telepresence2 --go-grpc_opt=module=github.com/datawire/telepresence2 $(PROTO_SRCS)

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files that get checked in to Git
	rm -rf pkg/rpc/*

PKG_VERSION = $(shell go list ./pkg/version)

.PHONY: build
build: ## (Build) Build all the source code
	mkdir -p $(BINDIR)
	go build -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $(BINDIR) ./cmd/...

.PHONY: image images
image images: $(GOBIN)/ko ## (Build) Build/tag the manager/agent container image
	docker tag $(shell env GOFLAGS="-ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION)" ko publish --local ./cmd/traffic) $(TELEPRESENCE_REGISTRY)/tel2:$(TELEPRESENCE_VERSION)

.PHONY: install
install:  ## (Install) installs the telepresence binary under ~/go/bin
	go install -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) ./cmd/telepresence

.PHONY: clean
clean: ## (Build) Remove all build artifacts
	rm -rf $(BUILDDIR)

.PHONY: clobber
clobber: clean ## (Build) Remove all build artifacts and tools

.PHONY: lint
lint: $(GOLANGCI_LINT) $(PROTOLINT) ## (Lint) Run the linters (golangci-lint and protolint)
	$(GOLANGCI_LINT) run --timeout 2m ./...
	$(PROTOLINT) lint $(shell find rpc -type f -name '*.proto')

.PHONY: test check
test check: $(GOBIN)/ko ## (Test) Run the test suite
	go test -v ./...

.PHONY: all
all: test
