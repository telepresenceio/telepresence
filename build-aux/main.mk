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

RELEASEDIR=$(BUILDDIR)/release

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

.PHONY: protoc-clean
protoc-clean:
	find ./rpc -name '*.go' -delete

.PHONY: protoc
protoc: protoc-clean $(tools/protoc) $(tools/protoc-gen-go) $(tools/protoc-gen-go-grpc)
	$(tools/protoc) \
	  -I rpc \
	  \
	  --go_out=./rpc \
	  --go_opt=module=github.com/telepresenceio/telepresence/rpc/v2 \
	  \
	  --go-grpc_out=./rpc \
	  --go-grpc_opt=module=github.com/telepresenceio/telepresence/rpc/v2 \
	  \
	  --proto_path=. \
	  $$(find ./rpc/ -name '*.proto')

.PHONY: generate
generate: ## (Generate) Update generated files that get checked in to Git
generate: generate-clean
generate: protoc $(tools/go-mkopensource) $(BUILDDIR)/$(shell go env GOVERSION).src.tar.gz
	cd ./rpc && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor

	export GOFLAGS=-mod=mod && go mod tidy && go mod vendor

	mkdir -p $(BUILDDIR)
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --output-format=txt --package=mod --application-type=external \
		--unparsable-packages build-aux/unparsable-packages.yaml >$(BUILDDIR)/DEPENDENCIES.txt
	sed 's/\(^.*the Go language standard library ."std".[ ]*v[1-9]\.[0-9]*\)\..../\1    /' $(BUILDDIR)/DEPENDENCIES.txt >DEPENDENCIES.md

	printf "Telepresence CLI incorporates Free and Open Source software under the following licenses:\n\n" > DEPENDENCY_LICENSES.md
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --output-format=txt --package=mod \
		--output-type=json --application-type=external --unparsable-packages build-aux/unparsable-packages.yaml > $(BUILDDIR)/DEPENDENCIES.json
	jq -r '.licenseInfo | to_entries | .[] | "* [" + .key + "](" + .value + ")"' $(BUILDDIR)/DEPENDENCIES.json > $(BUILDDIR)/LICENSES.txt
	sed -e 's/\[\([^]]*\)]()/\1/' $(BUILDDIR)/LICENSES.txt >> DEPENDENCY_LICENSES.md

	rm -rf vendor

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files
	rm -rf ./rpc/vendor
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

ifeq ($(DOCKER_BUILD),1)
build-deps:
else
FUSEFTP_VERSION=$(shell go list -m -f {{.Version}} github.com/datawire/go-fuseftp/rpc)

$(BUILDDIR)/fuseftp-$(GOOS)-$(GOARCH)$(BEXE): go.mod
	mkdir -p $(BUILDDIR)
	curl --fail -L https://github.com/datawire/go-fuseftp/releases/download/$(FUSEFTP_VERSION)/fuseftp-$(GOOS)-$(GOARCH)$(BEXE) -o $@

pkg/client/remotefs/fuseftp.bits: $(BUILDDIR)/fuseftp-$(GOOS)-$(GOARCH)$(BEXE) FORCE
	cp $< $@

build-deps: pkg/client/remotefs/fuseftp.bits
endif

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
ifeq ($(DOCKER_BUILD),1)
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -tags docker -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $@ ./cmd/telepresence
else
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $@ ./cmd/telepresence
endif

# Make local authenticator. This is for test only as it's really only intended to run from within a container
.PHONY: authenticator
authenticator:
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -trimpath -o $(BINDIR)/$@ ./cmd/$@

.PHONY: release-binary
release-binary: $(TELEPRESENCE)
	mkdir -p $(RELEASEDIR)
	cp $(TELEPRESENCE) $(RELEASEDIR)/telepresence-$(GOOS)-$(GOARCH)$(BEXE)

.PHONY: tel2-image
tel2-image: build-deps
	mkdir -p $(BUILDDIR)
	printf $(TELEPRESENCE_VERSION) > $(BUILDDIR)/version.txt ## Pass version in a file instead of a --build-arg to maximize cache usage
	docker build --target tel2 --tag tel2 --tag $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) -f build-aux/docker/images/Dockerfile.traffic .

.PHONY: client-image
client-image: build-deps
	mkdir -p $(BUILDDIR)
	printf $(TELEPRESENCE_VERSION) > $(BUILDDIR)/version.txt ## Pass version in a file instead of a --build-arg to maximize cache usage
	docker build --target telepresence --tag telepresence --tag $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) -f build-aux/docker/images/Dockerfile.client .

.PHONY: push-tel2-image
push-tel2-image: tel2-image ## (Build) Push the manager/agent container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: push-client-image
push-client-image: client-image ## (Build) Push the client container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: save-tel2-image
save-tel2-image: tel2-image
	docker save $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/tel2-image.tar

.PHONY: save-client-image
save-client-image: client-image
	docker save $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/telepresence-image.tar

.PHONY: push-images
push-images: push-tel2-image push-client-image

.PHONY: clobber
clobber: ## (Build) Remove all build artifacts and tools
	rm -rf $(BUILDDIR)

# Release: Push the artifacts places, update pointers ot them
# ===========================================================

.PHONY: prepare-release
prepare-release: generate wix
	sed -i.bak "/^### $(patsubst v%,%,$(TELEPRESENCE_VERSION)) (TBD)\$$/s/TBD/$$(date +'%B %-d, %Y')/" CHANGELOG.md
	rm -f CHANGELOG.md.bak
	git add CHANGELOG.md

	go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION)
	git add go.mod

	sed -i.bak "s/^### (TBD).*/### $(TELEPRESENCE_VERSION)/" charts/telepresence/CHANGELOG.md
	rm -f charts/telepresence/CHANGELOG.md.bak
	git add charts/telepresence/CHANGELOG.md

	git add packaging/telepresence.wxs
	git add packaging/bundle.wxs

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
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/$(patsubst v%,%,$(TELEPRESENCE_VERSION))/telepresence.zip \
		--body $(BINDIR)/telepresence.zip
	AWS_PAGER="" aws s3api put-object \
		--region us-east-1 \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/$(patsubst v%,%,$(TELEPRESENCE_VERSION))/telepresence-setup.exe \
		--body $(BINDIR)/telepresence-setup.exe
else
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/$(patsubst v%,%,$(TELEPRESENCE_VERSION))/telepresence \
		--body $(BINDIR)/telepresence
endif

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: promote-to-stable
promote-to-stable: ## (Release) Update stable.txt in S3
	mkdir -p $(BUILDDIR)
	echo $(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/stable.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/stable.txt \
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
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/nightly.txt \
		--body $(BUILDDIR)/nightly.txt

# Quality Assurance: Make sure things are good
# ============================================

.PHONY: lint-deps
lint-deps: build-deps ## (QA) Everything necessary to lint
lint-deps: $(tools/golangci-lint)
lint-deps: $(tools/protolint)
ifneq ($(GOHOSTOS), windows)
lint-deps: $(tools/shellcheck)
endif

.PHONY: build-tests
build-tests: build-deps ## (Test) Build (but don't run) the test suite.  Useful for pre-loading the Go build cache.
	go list ./... | xargs -n1 go test -c -o /dev/null

shellscripts += ./packaging/homebrew-package.sh
shellscripts += ./packaging/windows-package.sh
.PHONY: lint lint-rpc
lint: lint-rpc ## (QA) Run the linter
	CGO_ENABLED=$(CGO_ENABLED) $(tools/golangci-lint) run --timeout 8m ./...

lint-rpc: lint-deps ## (QA) Run rpc linter
	$(tools/protolint) lint rpc
ifneq ($(GOHOSTOS), windows)
	$(tools/shellcheck) $(shellscripts)
endif

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
ifeq ($(GOHOSTOS), linux)
check-integration: client-image $(tools/helm) ## (QA) Run the test suite
else
check-integration: build-deps $(tools/helm) ## (QA) Run the test suite
endif
	# We run the test suite with TELEPRESENCE_LOGIN_DOMAIN set to localhost since that value
	# is only used for extensions. Therefore, we want to validate that our tests, and
	# telepresence, run without requiring any outside dependencies.
	TELEPRESENCE_MAX_LOGFILES=300 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 CGO_ENABLED=$(CGO_ENABLED) go test -v -timeout=55m ./integration_test/...

.PHONY: _login
_login:
	docker login --username "$$TELEPRESENCE_REGISTRY_USERNAME" --password "$$TELEPRESENCE_REGISTRY_PASSWORD"


# Install
# =======

.PHONY: install
install: build ## (Install) Installs the telepresence binary to $(bindir)
	install -Dm755 $(BINDIR)/telepresence $(bindir)/telepresence

.PHONY: private-registry
private-registry: $(tools/helm) ## (Test) Add a private docker registry to the current k8s cluster and make it available on localhost:5000.
	mkdir -p $(BUILDDIR)
	$(tools/helm) repo add twuni https://helm.twun.io
	$(tools/helm) repo update
	$(tools/helm) install docker-registry twuni/docker-registry
	kubectl apply -f k8s/private-reg-proxy.yaml
	kubectl rollout status -w daemonset/private-registry-proxy
	sleep 5
	kubectl wait --for=condition=ready pod --all
	kubectl port-forward daemonset/private-registry-proxy 5000:5000 > /dev/null &

WIX_VERSION = $(shell echo $(TELEPRESENCE_VERSION) | sed 's/v//;s/-.*//')
.PHONY: wix
wix:
	sed s/TELEPRESENCE_VERSION/$(WIX_VERSION)/ packaging/telepresence.wxs.in > packaging/telepresence.wxs
	sed s/TELEPRESENCE_VERSION/$(WIX_VERSION)/ packaging/bundle.wxs.in > packaging/bundle.wxs

# Aliases
# =======

.PHONY: test save-image push-image
test:        check-all       ## (ZAlias) Alias for 'check-all'
save-image: save-tel2-image
push-image: push-tel2-image
