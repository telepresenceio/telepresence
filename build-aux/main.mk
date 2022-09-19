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

# DOCKER_BUILDKIT is _required_ by our Dockerfile, since we use
# Dockerfile extensions for the Go build cache.  See
# https://github.com/moby/buildkit/blob/master/frontend/dockerfile/docs/syntax.md.
export DOCKER_BUILDKIT := 1

.PHONY: FORCE
FORCE:

# Build with CGO_ENABLED=0 on all platforms to ensure that the binary is as
# portable as possible, but we must make an exception for darwin, because
# the Go implementation of the DNS resolver doesn't work properly there unless
# it's using clib
ifeq ($(GOOS),darwin)
CGO_ENABLED=1
else
CGO_ENABLED=0
endif

# Build using CGO_ENABLED=1 on all platforms except windows.
ifeq ($(GOOS),windows)
BEXE=.exe
else
BEXE=
endif

# Generate: artifacts that get checked in to Git
# ==============================================

$(BUILDDIR)/go1%.src.tar.gz:
	mkdir -p $(BUILDDIR)
	curl -o $@ --fail -L https://dl.google.com/go/$(@F)

.PHONY: generate
generate: ## (Generate) Update generated files that get checked in to Git
generate: generate-clean
generate: $(tools/protoc) $(tools/protoc-gen-go) $(tools/protoc-gen-go-grpc)
generate: $(tools/go-mkopensource) $(BUILDDIR)/$(shell go env GOVERSION).src.tar.gz
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

	export GOFLAGS=-mod=mod && go mod tidy && go mod vendor

	mkdir -p $(BUILDDIR)
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --output-format=txt --package=mod --application-type=external \
		--unparsable-packages build-aux/unparsable-packages.yaml >$(BUILDDIR)/DEPENDENCIES.txt
	sed 's/\(^.*the Go language standard library ."std".[ ]*v[1-9]\.[1-9]*\)\..../\1    /' $(BUILDDIR)/DEPENDENCIES.txt >DEPENDENCIES.md

	printf "Telepresence CLI incorporates Free and Open Source software under the following licenses:\n\n" > DEPENDENCY_LICENSES.md
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --output-format=txt --package=mod \
		--output-type=json --application-type=external --unparsable-packages build-aux/unparsable-packages.yaml > $(BUILDDIR)/DEPENDENCIES.json
	jq -r '.licenseInfo | to_entries | .[] | "* [" + .key + "](" + .value + ")"' $(BUILDDIR)/DEPENDENCIES.json > $(BUILDDIR)/LICENSES.txt
	sed -e 's/\[\([^]]*\)]()/\1/' $(BUILDDIR)/LICENSES.txt >> DEPENDENCY_LICENSES.md

	rm -rf vendor

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files
	rm -rf ./rpc/vendor
	find ./rpc -name '*.go' -delete

	rm -rf ./vendor
	rm -f DEPENDENCIES.md
	rm -f DEPENDENCY_LICENSES.md

PKG_VERSION = $(shell go list ./pkg/version)

# Build: artifacts that don't get checked in to Git
# =================================================

TELEPRESENCE=$(BINDIR)/telepresence$(BEXE)

.PHONY: build
build: $(TELEPRESENCE) ## (Build) Produce a `telepresence` binary for GOOS/GOARCH

# We might be building for arm64 on a mac that doesn't have an M1 chip
# (which is definitely the case with circle), so GOARCH may be set for that,
# but we need to ensure it's using the host's architecture so the go command runs successfully.
ifeq ($(GOHOSTOS),darwin)
	sdkroot=SDKROOT=$(shell xcrun --sdk macosx --show-sdk-path)
else
	sdkroot=
endif

FUSEFTP_VERSION=$(shell go list -m -f {{.Version}} github.com/datawire/go-fuseftp/rpc)

pkg/client/userd/fuseftp.bits: $(BUILDDIR)/fuseftp-$(GOOS)-$(GOARCH)$(BEXE) FORCE
	cp $< $@

$(BUILDDIR)/fuseftp-$(GOOS)-$(GOARCH)$(BEXE): go.mod
	mkdir -p $(BUILDDIR)
	curl --fail -L https://github.com/datawire/go-fuseftp/releases/download/$(FUSEFTP_VERSION)/fuseftp-$(GOOS)-$(GOARCH)$(BEXE) -o $@

build-deps: pkg/client/userd/fuseftp.bits

ifeq ($(GOHOSTOS),windows)
WINTUN_VERSION=0.14.1
$(BUILDDIR)/wintun-$(WINTUN_VERSION)/wintun/bin/$(GOHOSTARCH)/wintun.dll:
	mkdir -p $(BUILDDIR)
	curl --fail -L https://www.wintun.net/builds/wintun-$(WINTUN_VERSION).zip -o $(BUILDDIR)/wintun-$(WINTUN_VERSION).zip
	rm -rf  $(BUILDDIR)/wintun-$(WINTUN_VERSION)
	unzip $(BUILDDIR)/wintun-$(WINTUN_VERSION).zip -d $(BUILDDIR)/wintun-$(WINTUN_VERSION)
$(BINDIR)/wintun.dll: $(BUILDDIR)/wintun-$(WINTUN_VERSION)/wintun/bin/$(GOHOSTARCH)/wintun.dll
	mkdir -p $(@D)
	cp $< $@
endif

$(TELEPRESENCE): build-deps FORCE
ifeq ($(GOHOSTOS),windows)
$(TELEPRESENCE): build-deps $(BINDIR)/wintun.dll FORCE
endif
	mkdir -p $(@D)
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $@ ./cmd/telepresence

.PHONY: tel2
tel2: build-deps
	mkdir -p $(BUILDDIR)
	printf $(TELEPRESENCE_VERSION) > $(BUILDDIR)/version.txt ## Pass version in a file instead of a --build-arg to maximize cache usage
	docker build --target $@ --tag $@ --tag $(TELEPRESENCE_REGISTRY)/$@:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) -f base-image/Dockerfile .

.PHONY: push-image
push-image: tel2 ## (Build) Push the manager/agent container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

tel2-image: tel2
	docker save $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/tel2-image.tar

.PHONY: clobber
clobber: ## (Build) Remove all build artifacts and tools
	rm -rf $(BUILDDIR)

# Release: Push the artifacts places, update pointers ot them
# ===========================================================

.PHONY: prepare-release
prepare-release: generate
	sed -i.bak "/^### $(patsubst v%,%,$(TELEPRESENCE_VERSION)) (TBD)\$$/s/TBD/$$(date +'%B %-d, %Y')/" CHANGELOG.md
	rm -f CHANGELOG.md.bak
	git add CHANGELOG.md

	go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION)
	git add go.mod

	sed -i.bak "s/^### (TBD).*/### $(TELEPRESENCE_VERSION)/" charts/telepresence/CHANGELOG.md
	rm -f charts/telepresence/CHANGELOG.md.bak
	git add charts/telepresence/CHANGELOG.md

	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,cp -a pkg/client/userd/trafficmgr/testdata/addAgentToWorkload/cur pkg/client/userd/trafficmgr/testdata/addAgentToWorkload/$(TELEPRESENCE_VERSION))
	$(if $(findstring -,$(TELEPRESENCE_VERSION)),,git add pkg/client/userd/trafficmgr/testdata/addAgentToWorkload/$(TELEPRESENCE_VERSION))

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
lint-deps: build-deps ## (QA) Everything necessary to lint
lint-deps: $(tools/golangci-lint)
lint-deps: $(tools/protolint)
lint-deps: $(tools/helm)
ifneq ($(GOHOSTOS), windows)
lint-deps: $(tools/shellcheck)
endif

.PHONY: build-tests
build-tests: build-deps ## (Test) Build (but don't run) the test suite.  Useful for pre-loading the Go build cache.
	go list ./... | xargs -n1 go test -c -o /dev/null

shellscripts += ./packaging/homebrew-package.sh
shellscripts += ./smoke-tests/run_smoke_test.sh
shellscripts += ./packaging/push_chart.sh
shellscripts += ./packaging/windows-package.sh
.PHONY: lint lint-rpc
lint: lint-rpc ## (QA) Run the linter
	CGO_ENABLED=$(CGO_ENABLED) $(tools/golangci-lint) run --timeout 8m ./...

lint-rpc: lint-deps ## (QA) Run rpc linter
	$(tools/protolint) lint rpc
ifneq ($(GOHOSTOS), windows)
	$(tools/shellcheck) $(shellscripts)
endif
	tmpdir=$$(mktemp -d) && trap 'rm -rf "$$tmpdir"' EXIT && go run ./packaging/gen_chart.go "$$tmpdir" && $(tools/helm) lint "$$tmpdir"/*.tgz --set isCI=true

.PHONY: format
format: build-deps $(tools/golangci-lint) $(tools/protolint) ## (QA) Automatically fix linter complaints
	$(tools/golangci-lint) run --fix --timeout 2m ./... || true
	$(tools/protolint) lint --fix rpc || true

.PHONY: check-all
check-all: check-integration check-unit ## (QA) Run the test suite

.PHONY: check-unit
check-unit: build-deps ## (QA) Run the test suite
	# We run the test suite with TELEPRESENCE_LOGIN_DOMAIN set to localhost since that value
	# is only used for extensions. Therefore, we want to validate that our tests, and
	# telepresence, run without requiring any outside dependencies.
	TELEPRESENCE_MAX_LOGFILES=300 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 CGO_ENABLED=$(CGO_ENABLED) go test -timeout=20m ./cmd/... ./pkg/...

.PHONY: check-integration
check-integration: build-deps $(tools/helm) ## (QA) Run the test suite
	# We run the test suite with TELEPRESENCE_LOGIN_DOMAIN set to localhost since that value
	# is only used for extensions. Therefore, we want to validate that our tests, and
	# telepresence, run without requiring any outside dependencies.
	TELEPRESENCE_MAX_LOGFILES=300 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 CGO_ENABLED=$(CGO_ENABLED) go test -v -timeout=39m ./integration_test/...

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
all:         build image     ## (ZAlias) Alias for 'build image'
test:        check-all       ## (ZAlias) Alias for 'check-all'
images:      image           ## (ZAlias) Alias for 'image'
push-images: push-image      ## (ZAlias) Alias for 'push-image'
