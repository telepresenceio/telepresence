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

# pkg sources excluding rpc
TP_PKG_SOURCES = $(shell find pkg -type f -name '*.go' | grep -v '_test.go' | grep -v '/rpc/')
TP_TEST_SOURCES = $(shell find cmd pkg -type f -name '*_test.go')

EXECUTABLES=$(shell ls cmd)

.PHONY: build
build: ## (Build) Build all the source code
	mkdir -p $(BINDIR)
	go build -o $(BINDIR) ./cmd/...

.PHONY: install
install:  ## (Install) runs go install -- what is this for
	go install ./cmd/...

.PHONY: docker-build
docker-build: ## (Install) runs docker build for all executables
	for exe in $(EXECUTABLES) ; do \
		docker build --target $$exe --tag datawire/$$exe .; \
	done

.PHONY: clean
clean: ## (Build) Remove all build artifacts
	rm -rf $(BUILDDIR)

.PHONY: clobber
clobber: clean ## (Build) Remove all build artifacts and tools
	rm -rf $(BUILDDIR)

.PHONY: lint
lint: $(GOLANGCI_LINT) $(PROTOLINT) ## (Lint) Run the linters (golangci-lint and protolint)
	$(GOLANGCI_LINT) run ./...
	$(PROTOLINT) lint $(shell find rpc -type f -name '*.proto')

.PHONY: test
test: build ## (Test) Run the Go tests
	go test -v ./...

.PHONY: check
check: test ## (Test) Run the full test suite

.PHONY: all
all: test

.PHONY: help
help:  ## (Common) Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^:]*):[^#]*## *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
