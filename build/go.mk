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
generate: generate-clean $(tools/protoc) $(tools/protoc-gen-go) $(tools/protoc-gen-go-grpc)
	rm -rf ./pkg/rpc/vendor ./vendor
	$(tools/protoc) --proto_path=. --go_out=. --go-grpc_out=. --go_opt=module=github.com/datawire/telepresence2 --go-grpc_opt=module=github.com/datawire/telepresence2 $(PROTO_SRCS)
	go generate ./...
	cd ./pkg/rpc && go mod tidy
	cd ./pkg/rpc && go mod vendor
	go mod tidy
	go mod vendor
	rm -rf ./pkg/rpc/vendor ./vendor

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files that get checked in to Git
	find pkg cmd -name 'generated_*.go' -delete
	rm -rf pkg/rpc/*/

PKG_VERSION = $(shell go list ./pkg/version)

.PHONY: build
build: ## (Build) Build all the source code
	mkdir -p $(BINDIR)
	go build -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $(BINDIR) ./cmd/...

.ko.yaml: .ko.yaml.in base-image
	sed $(foreach v,TELEPRESENCE_REGISTRY TELEPRESENCE_BASE_VERSION, -e 's|@$v@|$($v)|g') <$< >$@
.PHONY: image push-image
image: .ko.yaml $(tools/ko) ## (Build) Build/tag the manager/agent container image
	localname=$$(GOFLAGS="-ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION)" ko publish --local ./cmd/traffic) && \
	docker tag "$$localname" $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))
push-image: image
	docker push $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: images push-images
images: image
push-images: push-image

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: push-executable
push-executable: build
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOHOSTARCH)/$(patsubst v%,%,$(TELEPRESENCE_VERSION))/telepresence \
		--body $(BINDIR)/telepresence

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: promote-to-stable
promote-to-stable:
	mkdir -p $(BUILDDIR)
	echo $(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/stable.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOHOSTARCH)/stable.txt \
		--body $(BUILDDIR)/stable.txt

.PHONY: install
install:  ## (Install) installs the telepresence binary under ~/go/bin
	go install -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) ./cmd/telepresence

.PHONY: clean
clean: ## (Build) Remove all build artifacts
	rm -rf $(BUILDDIR)

.PHONY: clobber
clobber: clean ## (Build) Remove all build artifacts and tools

.PHONY: lint-deps
lint-deps: $(tools/golangci-lint) $(tools/protolint) ## (Lint) Everything nescessary to lint

.PHONY: lint
lint: lint-deps ## (Lint) Run the linters (golangci-lint and protolint)
	$(tools/golangci-lint) run --timeout 2m ./...
	$(tools/protolint) lint rpc

.PHONY: format
format: $(tools/golangci-lint) $(tools/protolint) ## (Lint) Automatically fix linter complaints
	$(tools/golangci-lint) run --fix --timeout 2m ./... || true
	$(tools/protolint) lint --fix rpc || true

.PHONY: test check
test check: $(tools/ko) ## (Test) Run the test suite
	go test ./...

.PHONY: all
all: test
