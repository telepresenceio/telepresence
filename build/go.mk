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
	$(TOOLSBINDIR)/protoc --proto_path=. --go_out=. --go-grpc_out=. --go_opt=module=github.com/datawire/telepresence2 --go-grpc_opt=module=github.com/datawire/telepresence2 $(PROTO_SRCS)
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

.PHONY: image images
image images: $(tools/ko) ## (Build) Build/tag the manager/agent container image
	localname=$$(GOFLAGS="-ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION)" ko publish --local ./cmd/traffic) && \
	docker tag "$$localname" $(TELEPRESENCE_REGISTRY)/tel2:$(TELEPRESENCE_VERSION)
.PHONY: push-images
push-images: images
	docker push $(TELEPRESENCE_REGISTRY)/tel2:$(TELEPRESENCE_VERSION)

# The upload-binary target does the following:
# 1. Check that the workspace is clean
# 2. Check that the current commit is tagged
# 3. Check that the tag is a semantic version starting with a 'v'
# 4. Upload the object to S3.
#
# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: upload-binary
upload-binary: build
	git diff --quiet HEAD && \
	versionTag=`git describe --tags --exact-match` && \
	verifiedTag=`echo $$versionTag | sed -E 's|^v([0-9]+\.[0-9]+\.[0-9]+(-.*)?$$)|\1|g'` && \
	[ "$$versionTag" != "$$verifiedTag" ] && echo -n $$verifiedTag > $(BUILDDIR)/stable.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOHOSTARCH)/$$(cat $(BUILDDIR)/stable.txt)/telepresence \
		--body $(BINDIR)/telepresence
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

.PHONY: lint
lint: $(tools/golangci-lint) $(tools/protolint) ## (Lint) Run the linters (golangci-lint and protolint)
	golangci-lint run --timeout 2m ./...
	protolint lint rpc

.PHONY: format
format: $(tools/golangci-lint) $(tools/protolint) ## (Lint) Automatically fix linter complaints
	golangci-lint run --fix --timeout 2m ./... || true
	protolint lint --fix rpc || true

.PHONY: test check
test check: $(tools/ko) ## (Test) Run the test suite
	go test -v ./...

.PHONY: all
all: test
