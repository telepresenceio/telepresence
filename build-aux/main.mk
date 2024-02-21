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
$(VERBOSE).SILENT:

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
BZIP=.zip
else
BEXE=
BZIP=
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
	cd ./pkg/dnet/testdata/mockserver && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor
	cd ./pkg/vif/testdata/router && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor
	cd ./tools/src/test-report && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor
	cd ./integration_test/testdata/echo-server && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor

	export GOFLAGS=-mod=mod && go mod tidy && go mod vendor

	mkdir -p $(BUILDDIR)
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --ignore-dirty --output-format=txt --package=mod --application-type=external \
		--unparsable-packages build-aux/unparsable-packages.yaml >$(BUILDDIR)/DEPENDENCIES.txt
	sed 's/\(^.*the Go language standard library ."std".[ ]*v[1-9]\.[0-9]*\)\..../\1    /' $(BUILDDIR)/DEPENDENCIES.txt >DEPENDENCIES.md

	printf "Telepresence CLI incorporates Free and Open Source software under the following licenses:\n\n" > DEPENDENCY_LICENSES.md
	$(tools/go-mkopensource) --gotar=$(filter %.src.tar.gz,$^) --ignore-dirty --output-format=txt --package=mod \
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

ifeq ($(GOOS),windows)
TELEPRESENCE_INSTALLER=$(BINDIR)/telepresence$(BZIP)
endif

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

wintun.dll: $(BINDIR)/wintun.dll

winfsp.msi:
	mkdir -p $(BUILDDIR)
	curl --fail -L https://github.com/winfsp/winfsp/releases/download/v1.11/winfsp-1.11.22176.msi -o $(BUILDDIR)/winfsp.msi

sshfs-win.msi:
	mkdir -p $(BUILDDIR)
	curl --fail -L https://github.com/billziss-gh/sshfs-win/releases/download/v3.7.21011/sshfs-win-3.7.21011-x64.msi -o $(BUILDDIR)/sshfs-win.msi
endif

$(TELEPRESENCE): build-deps FORCE
ifeq ($(GOHOSTOS),windows)
$(TELEPRESENCE): build-deps $(BINDIR)/wintun.dll FORCE
endif
	mkdir -p $(@D)
ifeq ($(DOCKER_BUILD),1)
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -tags docker -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $@ ./cmd/telepresence
else
# -buildmode=pie addresses https://github.com/datawire/telepresence2-proprietary/issues/315
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build -buildmode=pie -trimpath -ldflags=-X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -o $@ ./cmd/telepresence
endif

ifeq ($(GOOS),windows)
$(TELEPRESENCE_INSTALLER): $(TELEPRESENCE)
	bash ./packaging/windows-package.sh
endif

.PHONY: release-binary
ifeq ($(GOOS),windows)
release-binary: $(TELEPRESENCE_INSTALLER)
	mkdir -p $(RELEASEDIR)
	cp $(TELEPRESENCE_INSTALLER) $(RELEASEDIR)/telepresence-windows-amd64$(BZIP)
else
release-binary: $(TELEPRESENCE)
	mkdir -p $(RELEASEDIR)
	cp $(TELEPRESENCE) $(RELEASEDIR)/telepresence-$(GOOS)-$(GOARCH)$(BEXE)
endif

.PHONY: setup-build-dir
setup-build-dir:
	mkdir -p $(BUILDDIR)
	printf $(TELEPRESENCE_VERSION) > $(BUILDDIR)/version.txt ## Pass version in a file instead of a --build-arg to maximize cache usage

.PHONY: tel2-image
tel2-image: build-deps setup-build-dir
	$(eval PLATFORM_ARG := $(if $(TELEPRESENCE_TEL2_IMAGE_PLATFORM), --platform=$(TELEPRESENCE_TEL2_IMAGE_PLATFORM),))
	docker build $(PLATFORM_ARG) --target tel2 --tag tel2 --tag $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) -f build-aux/docker/images/Dockerfile.traffic .

.PHONY: tel2-image-x
tel2-image-x: build-deps setup-build-dir
	docker buildx build --platform=linux/amd64,linux/arm64 --build-arg TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION) --cache-to type=local,dest=$(BUILDDIR)/docker-cache -f build-aux/docker/images/Dockerfile.traffic .

.PHONY: client-image
client-image: build-deps setup-build-dir
	docker build --target telepresence --tag telepresence --tag $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) -f build-aux/docker/images/Dockerfile.client .

.PHONY: client-image-x
client-image-x: build-deps setup-build-dir
	docker buildx build --platform=linux/amd64,linux/arm64 --build-arg TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION) --cache-to type=local,dest=$(BUILDDIR)/docker-cache -f build-aux/docker/images/Dockerfile.client .

.PHONY: push-tel2-image
push-tel2-image: tel2-image ## (Build) Push the manager/agent container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: push-tel2-image-x
push-tel2-image-x: build-deps setup-build-dir
	docker buildx build --platform=linux/amd64,linux/arm64 --build-arg TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION) --cache-from type=local,src=$(BUILDDIR)/docker-cache -f build-aux/docker/images/Dockerfile.traffic --push --tag $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) .

.PHONY: push-client-image
push-client-image: client-image ## (Build) Push the client container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION))

.PHONY: push-client-image-x
push-client-image-x: build-deps setup-build-dir
	docker buildx build --platform=linux/amd64,linux/arm64 --build-arg TELEPRESENCE_VERSION=$(TELEPRESENCE_VERSION) --cache-from type=local,src=$(BUILDDIR)/docker-cache -f build-aux/docker/images/Dockerfile.client --push --tag $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) .

.PHONY: save-tel2-image
save-tel2-image: tel2-image
	docker save $(TELEPRESENCE_REGISTRY)/tel2:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/tel2-image.tar

.PHONY: save-client-image
save-client-image: client-image
	docker save $(TELEPRESENCE_REGISTRY)/telepresence:$(patsubst v%,%,$(TELEPRESENCE_VERSION)) > $(BUILDDIR)/telepresence-image.tar

.PHONY: push-images
push-images: push-tel2-image push-client-image

.PHONY: push-images-x
push-images-x: push-tel2-image-x push-client-image-x

.PHONY: clobber
clobber: ## (Build) Remove all build artifacts and tools
	rm -rf $(BUILDDIR)

# Release: Push the artifacts places, update pointers ot them
# ===========================================================

.PHONY: prepare-release
prepare-release: generate wix
	sed -i.bak "/^### $(patsubst v%,%,$(TELEPRESENCE_VERSION)) (TBD)\$$/s/TBD/$$(date +'%B %-d, %Y')/" CHANGELOG.OLD.md
	rm -f CHANGELOG.OLD.md.bak
	git add CHANGELOG.OLD.md

	@# Check if the version is in the x.x.x format (GA release)
	if echo "$(TELEPRESENCE_VERSION)" | grep -qE 'v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		sed -i.bak "/date: \"*TBD\"*\$$/s/\"*TBD\"*/\"$$(date +'%Y-%m-%d')\"/" CHANGELOG.yml; \
		rm -f CHANGELOG.yml.bak; \
		git add CHANGELOG.yml; \
	fi

	go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION)
	git add go.mod

	(cd pkg/vif/testdata/router && \
	  go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION) && \
	  git add go.mod)

#sed -i.bak "s/^### (TBD).*/### $(TELEPRESENCE_VERSION)/" charts/telepresence/CHANGELOG.md
#rm -f charts/telepresence/CHANGELOG.md.bak
#git add charts/telepresence/CHANGELOG.md

	git add packaging/telepresence.wxs
	git add packaging/bundle.wxs

	git commit --signoff --message='Prepare $(TELEPRESENCE_VERSION)'

	git tag --annotate --message='$(TELEPRESENCE_VERSION)' $(TELEPRESENCE_VERSION)
	git tag --annotate --message='$(TELEPRESENCE_VERSION)' rpc/$(TELEPRESENCE_VERSION)

.PHONY: push-tags
push-tags:
	git push origin $(TELEPRESENCE_VERSION)
	git push origin rpc/$(TELEPRESENCE_VERSION)

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
lint-deps: $(tools/gosimports)
ifneq ($(GOHOSTOS), windows)
lint-deps: $(tools/shellcheck)
endif

.PHONY: build-tests
build-tests: build-deps ## (Test) Build (but don't run) the test suite.  Useful for pre-loading the Go build cache.
	go list ./... | xargs -n1 go test -c -o /dev/null

shellscripts += ./packaging/homebrew-package.sh
shellscripts += ./packaging/windows-package.sh
.PHONY: lint lint-rpc lint-go

lint: lint-rpc lint-go

lint-go: lint-deps ## (QA) Run the golangci-lint
	$(eval badimports = $(shell find cmd integration_test pkg -name '*.go' | grep -v '/mocks/' | xargs $(tools/gosimports) --local github.com/datawire/,github.com/telepresenceio/ -l))
	$(if $(strip $(badimports)), echo "The following files have bad import ordering (use make format to fix): " $(badimports) && false)
	CGO_ENABLED=$(CGO_ENABLED) $(tools/golangci-lint) run --timeout 8m ./...

lint-rpc: lint-deps ## (QA) Run rpc linter
	$(tools/protolint) lint rpc
ifneq ($(GOHOSTOS), windows)
	$(tools/shellcheck) $(shellscripts)
endif

.PHONY: format
format: lint-deps ## (QA) Automatically fix linter complaints
	find cmd integration_test pkg -name '*.go' | grep -v '/mocks/' | xargs $(tools/gosimports) --local github.com/datawire/,github.com/telepresenceio/ -w
	$(tools/golangci-lint) run --fix --timeout 2m ./... || true
	$(tools/protolint) lint --fix rpc || true

.PHONY: check-all
check-all: check-integration check-unit ## (QA) Run the test suite

.PHONY: check-unit
check-unit: build-deps $(tools/test-report) ## (QA) Run the test suite
	# We run the test suite with TELEPRESENCE_LOGIN_DOMAIN set to localhost since that value
	# is only used for extensions. Therefore, we want to validate that our tests, and
	# telepresence, run without requiring any outside dependencies.
	set -o pipefail
	TELEPRESENCE_MAX_LOGFILES=300 SCOUT_DISABLE=1 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 CGO_ENABLED=$(CGO_ENABLED) go test -json -failfast -timeout=20m ./cmd/... ./pkg/... | $(tools/test-report)

.PHONY: check-integration
ifeq ($(GOHOSTOS), linux)
check-integration: client-image $(tools/test-report) $(tools/helm) ## (QA) Run the test suite
else
check-integration: build-deps $(tools/test-report) $(tools/helm) ## (QA) Run the test suite
endif
	# We run the test suite with TELEPRESENCE_LOGIN_DOMAIN set to localhost since that value
	# is only used for extensions. Therefore, we want to validate that our tests, and
	# telepresence, run without requiring any outside dependencies.
	set -o pipefail
	TELEPRESENCE_MAX_LOGFILES=300 TELEPRESENCE_LOGIN_DOMAIN=127.0.0.1 CGO_ENABLED=$(CGO_ENABLED) go test -failfast -json -timeout=55m ./integration_test/... | $(tools/test-report)

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
