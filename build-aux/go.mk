# Copyright 2020-2021 Datawire.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Delete implicit rules not used here (clutters debug output)
MAKEFLAGS += --no-builtin-rules

# All build artifacts that are files end up in $(BUILDDIR).
BUILDDIR=build-output

BINDIR=$(BUILDDIR)/bin

# proto/gRPC generation using protoc

.PHONY: generate
generate: ## (Generate) Update generated files that get checked in to Git
generate: generate-clean $(tools/protoc) $(tools/protoc-gen-go) $(tools/protoc-gen-go-grpc)
	rm -rf ./rpc/vendor
	find ./rpc -name '*.go' -delete
	$(tools/protoc) \
	  \
	  --go_out=./rpc \
	  --go_opt=module=github.com/telepresenceio/telepresence/rpc/v2 \
	  \
	  --go-grpc_out=./rpc \
	  --go-grpc_opt=module=github.com/telepresenceio/telepresence/rpc/v2 \
	  \
	  --proto_path=. \
	  $$(find ./rpc/ -name '*.proto')
	cd ./rpc && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor

	rm -rf ./vendor
	go generate ./...
	export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files that get checked in to Git
	rm -rf ./rpc/vendor
	find ./rpc -name '*.go' -delete

	rm -rf ./vendor
	find pkg cmd -name 'generated_*.go' -delete

PKG_VERSION = $(shell go list ./pkg/version)

.PHONY: build
build: ## (Build) Build all the source code
	mkdir -p $(BINDIR)
	CGO_ENABLED=0 go build -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $(BINDIR) ./cmd/...

.ko.yaml: .ko.yaml.in base-image
	sed $(foreach v,TELEPRESENCE_REGISTRY TELEPRESENCE_BASE_VERSION, -e 's|@$v@|$($v)|g') <$< >$@
.PHONY: image push-image
image: .ko.yaml $(tools/ko) ## (Build) Build/tag the manager/agent container image
	localname=$$(GOFLAGS="-ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -trimpath" ko publish --local ./cmd/traffic) && \
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
ifeq ($(GOHOSTOS), darwin)
	packaging/homebrew-package.sh $(patsubst v%,%,$(TELEPRESENCE_VERSION))
endif

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

.PHONY: check
check: $(tools/ko) ## (Test) Run the test suite
	go test ./...

.PHONY: all
all: build ## (ZAlias) Alias for 'build'

.PHONY: test
test: check ## (ZAlias) Alias for 'check'
