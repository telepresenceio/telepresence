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

# This file deals with the "main" flow of Make.  The user-facing
# targets, the generate/build/release cycle.  Try to keep boilerplate
# out of this file.  Try to keep this file simple; anything complex or
# clever should probably be factored into a separate file.

# All build artifacts that are files end up in $(BUILDDIR).
BUILDDIR=build-output

BINDIR=$(BUILDDIR)/bin

bindir ?= $(or $(shell go env GOBIN),$(shell go env GOPATH|cut -d: -f1)/bin)

# Build statically on linux platforms so that the binary can be used in
# alpine containers and the like, where libc is different.
ifeq ($(GOHOSTOS),linux)
CGO_ENABLED=0
else
CGO_ENABLED=1
endif

.PHONY: FORCE
FORCE:

# Generate: artifacts that get checked in to Git
# ==============================================

build-aux/go1%.src.tar.gz:
	curl -o $@ --fail -L https://dl.google.com/go/$(@F)

.PHONY: generate
generate: ## (Generate) Update generated files that get checked in to Git
generate: generate-clean
generate: $(tools/protoc) $(tools/protoc-gen-go) $(tools/protoc-gen-go-grpc)
generate: $(tools/go-mkopensource) build-aux/$(shell go env GOVERSION).src.tar.gz
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
	export GOFLAGS=-mod=mod && go generate ./...
	export GOFLAGS=-mod=mod && go mod tidy && go mod vendor
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --output-format=txt --package=mod >OPENSOURCE.md
	rm -rf vendor

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files that get checked in to Git
	rm -rf ./rpc/vendor
	find ./rpc -name '*.go' -delete

	rm -rf ./vendor
	find pkg cmd -name 'generated_*.go' -delete
	rm -f OPENSOURCE.md

# Build: artifacts that don't get checked in to Git
# =================================================

# We might be building for arm64 on a mac that doesn't have an M1 chip
# (which is definitely the case with circle), so GOARCH may be set for that,
# but we need to ensure it's using the host's architecture so the go command runs successfully.
pkg/install/helm/telepresence-chart.tgz: $(tools/helm) charts/telepresence FORCE
	GOOS=$(GOHOSTOS) GOARCH=$(shell go env GOHOSTARCH) go run ./build-aux/package_embedded_chart/main.go $(TELEPRESENCE_VERSION)

TELEPRESENCE_BASE_VERSION := $(firstword $(shell shasum base-image/Dockerfile))
.PHONY: base-image
base-image: base-image/Dockerfile # Intentionally not in 'make help'
	if (! docker pull $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION)) && (! docker image inspect $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION) > /dev/null); then \
	  cd base-image && docker build --pull -t $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION) .; \
	fi; \
    docker tag $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION) ko.local/tel2-base:$(TELEPRESENCE_BASE_VERSION)

PKG_VERSION = $(shell go list ./pkg/version)

ifeq ($(GOHOSTOS),darwin)
	sdkroot=SDKROOT=$(shell xcrun --sdk macosx --show-sdk-path)
else
	sdkroot=
endif

.PHONY: build
build: pkg/install/helm/telepresence-chart.tgz ## (Build) Build all the source code
	mkdir -p $(BINDIR)
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $(BINDIR) ./cmd/...

.ko.yaml: .ko.yaml.in base-image
	sed $(foreach v,TELEPRESENCE_REGISTRY TELEPRESENCE_BASE_VERSION, -e 's|@$v@|$($v)|g') <$< >$@
.PHONY: image push-image
image: .ko.yaml $(tools/ko) ## (Build) Build/tag the manager/agent container image
	localname=$$(GOFLAGS="-ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -trimpath" GOOS=linux ko publish --local ./cmd/traffic) && \
	docker tag "$$localname" $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: push-image
push-image: image ## (Build) Push the manager/agent container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TELEPRESENCE_REGISTRY)/tel2-base:$(TELEPRESENCE_BASE_VERSION) && \
	docker push $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: clean
clean: ## (Build) Remove all build artifacts
	rm -rf $(BUILDDIR) pkg/install/helm/telepresence-chart.tgz

.PHONY: clobber
clobber: clean ## (Build) Remove all build artifacts and tools
	rm -f build-aux/go1*.src.tar.gz

# Release: Push the artifacts places, update pointers ot them
# ===========================================================

.PHONY: prepare-release
prepare-release: generate ## (Release) Update nescessary files and tag the release (does not push)
	sed -i.bak "/^### $(patsubst v%,%,$(TELEPRESENCE_VERSION)) (TBD)\$$/s/TBD/$$(date +'%B %-d, %Y')/" CHANGELOG.md
	rm -f CHANGELOG.md.bak
	go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION)
	git add CHANGELOG.md go.mod
	sed -i.bak "s/^version:.*/version: $(patsubst v%,%,$(TELEPRESENCE_VERSION))/" charts/telepresence/Chart.yaml
	sed -i.bak "s/^appVersion:.*/appVersion: $(patsubst v%,%,$(TELEPRESENCE_VERSION))/" charts/telepresence/Chart.yaml
	git add charts/telepresence/Chart.yaml
	rm -f charts/telepresence/Chart.yaml.bak
	sed -i.bak "s/^### (TBD).*/### $(TELEPRESENCE_VERSION)/" charts/telepresence/CHANGELOG.md
	rm -f charts/telepresence/CHANGELOG.md.bak
	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,cp -a pkg/client/connector/userd_trafficmgr/testdata/addAgentToWorkload/cur pkg/client/connector/userd_trafficmgr/testdata/addAgentToWorkload/$(TELEPRESENCE_VERSION))
	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,git add pkg/client/connector/userd_trafficmgr/testdata/addAgentToWorkload/$(TELEPRESENCE_VERSION))
	git commit --signoff --message='Prepare $(TELEPRESENCE_VERSION)'
	git tag --annotate --message='$(TELEPRESENCE_VERSION)' $(TELEPRESENCE_VERSION)
	git tag --annotate --message='$(TELEPRESENCE_VERSION)' rpc/$(TELEPRESENCE_VERSION)

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: push-executable
push-executable: build ## (Release) Upload the executable to S3
ifeq ($(GOHOSTOS), windows)
	packaging/windows-package.sh
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOARCH)/$(patsubst v%,%,$(TELEPRESENCE_VERSION))/telepresence.zip \
		--body $(BINDIR)/telepresence.zip
else
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOARCH)/$(patsubst v%,%,$(TELEPRESENCE_VERSION))/telepresence \
		--body $(BINDIR)/telepresence
endif

.PHONY: push-chart
push-chart: $(tools/helm) ## (Release) Run script that publishes our Helm chart
	packaging/push_chart.sh

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: promote-to-stable
promote-to-stable: ## (Release) Update stable.txt in S3
	mkdir -p $(BUILDDIR)
	echo $(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/stable.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOARCH)/stable.txt \
		--body $(BUILDDIR)/stable.txt
ifeq ($(GOHOSTOS), darwin)
	packaging/homebrew-package.sh $(patsubst v%,%,$(TELEPRESENCE_VERSION)) $(GOARCH)
endif

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: promote-nightly
promote-nightly: ## (Release) Update nightly.txt in S3
	mkdir -p $(BUILDDIR)
	echo $(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/nightly.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2/$(GOHOSTOS)/$(GOARCH)/nightly.txt \
		--body $(BUILDDIR)/nightly.txt

# Quality Assurance: Make sure things are good
# ============================================

.PHONY: lint-deps
lint-deps: ## (QA) Everything necessary to lint
lint-deps: pkg/install/helm/telepresence-chart.tgz
lint-deps: $(tools/golangci-lint)
lint-deps: $(tools/protolint)
lint-deps: $(tools/shellcheck)
lint-deps: $(tools/helm)

.PHONY: build-tests
build-tests: pkg/install/helm/telepresence-chart.tgz ## (Test) Build (but don't run) the test suite.  Useful for pre-loading the Go build cache.
	go list ./... | xargs -n1 go test -c -o /dev/null

shellscripts  = ./cmd/traffic/cmd/manager/internal/watchable/generic.gen
shellscripts += ./packaging/homebrew-package.sh
shellscripts += ./smoke-tests/run_smoke_test.sh
shellscripts += ./packaging/push_chart.sh
shellscripts += ./packaging/windows-package.sh
.PHONY: lint
lint: lint-deps ## (QA) Run the linters
	GOOS=linux   $(tools/golangci-lint) run --timeout 3m ./...
	GOOS=darwin  $(tools/golangci-lint) run --timeout 3m ./...
	GOOS=windows $(tools/golangci-lint) run --timeout 3m ./...
	$(tools/protolint) lint rpc
	$(tools/shellcheck) $(shellscripts)
	$(tools/helm) lint charts/telepresence --set isCI=true

.PHONY: format
format: $(tools/golangci-lint) $(tools/protolint) ## (QA) Automatically fix linter complaints
	$(tools/golangci-lint) run --fix --timeout 2m ./... || true
	$(tools/protolint) lint --fix rpc || true

.PHONY: check
check: $(tools/ko) $(tools/helm) pkg/install/helm/telepresence-chart.tgz ## (QA) Run the test suite
	# We run the test suite with TELEPRESENCE_LOGIN_DOMAIN set to localhost since that value
	# is only used for extensions. Therefore, we want to validate that our tests, and
	# telepresence, run without requiring any outside dependencies.
	TELEPRESENCE_MAX_LOGFILES=300 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 go test -v -timeout=29m ./integration_test/...
	TELEPRESENCE_MAX_LOGFILES=300 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 go test ./cmd/... ./pkg/...

.PHONY: _login
_login:
	docker login --username "$$TELEPRESENCE_REGISTRY_USERNAME" --password "$$TELEPRESENCE_REGISTRY_PASSWORD"


# Install
# =======

.PHONY: install
install: build ## (Install) Installs the telepresence binary to $(bindir)
	install -Dm755 $(BINDIR)/telepresence $(bindir)/telepresence

# Aliases
# =======

.PHONY: all test images push-images
all:         build image ## (ZAlias) Alias for 'build image'
test:        check       ## (ZAlias) Alias for 'check'
images:      image       ## (ZAlias) Alias for 'image'
push-images: push-image  ## (ZAlias) Alias for 'push-image'
