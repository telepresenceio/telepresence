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
export GOEXPERIMENT := jsonv2

.PHONY: FORCE
FORCE:

# Build with CGO_ENABLED=0 on all platforms to ensure that the binary is as
# portable as possible, but we must make an exception for darwin and linux,
# because the fuseftp file system is linked into the binary and requires CGO
# (libfuse) there. On darwin CGO is also needed because the Go implementation
# of the DNS resolver doesn't work properly unless it's using clib. The fuse
# module on Windows uses winfsp through its DLL and builds without CGO.
ifeq ($(GOOS),darwin)
CGO_ENABLED ?= 1
else
ifeq ($(GOOS),linux)
CGO_ENABLED ?= 1
else
CGO_ENABLED ?= 0
endif
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

.PHONY: clean
clean:
	rm -rf $(BUILDDIR)

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
generate: protoc $(tools/go-mkopensource) $(BUILDDIR)/$(shell go env GOVERSION | grep -oE '^go[0-9]+\.[0-9]+\.[0-9]+').src.tar.gz
	cd ./rpc && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor
	cd ./pkg/vif/testdata/router && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor
	cd ./tools/src/test-report && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor
	cd ./cmd/teleroute && $(MAKE) rpc/teleroute/.rsync-stamp
	cd ./cmd/teleroute && go mod tidy
	cd ./cmd/teleroute/rpc && go mod tidy
	cd ./regression_test/testdata/echo-server && export GOFLAGS=-mod=mod && go mod tidy && go mod vendor && rm -rf vendor

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
	rsync -vc DEPENDENCY_LICENSES.md docs/licenses.md

	rm -rf vendor

# Build: artifacts that don't get checked in to Git
# =================================================

TELEPRESENCE=$(BINDIR)/telepresence$(BEXE)

generate: docs-files

.PHONY: generate-clean
generate-clean: ## (Generate) Delete generated files
	rm -rf ./rpc/vendor
	rm -rf ./vendor
	rm -f DEPENDENCIES.md
	rm -f DEPENDENCY_LICENSES.md
	rm -f docs/release-notes.md*
	rm -f docs/README.md
	rm -f docs/helm/values.schema.json
	rm -f docs/schemas/workstation-state.v1alpha1.json

CHANGELOG.yml: FORCE
	@# Check if the version is in the x.x.x format (GA release)
	if echo "$(TELEPRESENCE_VERSION)" | grep -qE 'v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo $$file; \
		sed -i.bak -r "s/date: (TBD|\(TBD\)|\"TBD\"|\"\(TBD\)\")$$/date: $$(date +'%Y-%m-%d')/" CHANGELOG.yml; \
		rm -f CHANGELOG.yml.bak; \
		git add CHANGELOG.yml; \
	fi

docs-files: docs/README.md docs/release-notes.md docs/release-notes.mdx docs/variables.yml docs/helm/values.schema.json docs/schemas/workstation-state.v1alpha1.json docs/reference/cli/telepresence.md

docs/reference/cli/telepresence.md: $(TELEPRESENCE)
	$(TELEPRESENCE) man-pages --dir $(@D)
	git add $(@D)

docs/README.md: docs/doc-links.yml $(tools/tocgen)
	$(tools/tocgen) --input $< > $@
	git add $@

docs/release-notes.md: CHANGELOG.yml $(tools/relnotesgen)
	$(tools/relnotesgen) --input $< > $@
	git add $@

docs/release-notes.mdx: CHANGELOG.yml $(tools/relnotesgen)
	$(tools/relnotesgen) --mdx --input $< > $@
	git add $@

docs/variables.yml: CHANGELOG.yml $(tools/relnotesgen)
	$(tools/relnotesgen) --variables --input $< > $@
	git add $@

docs/helm/values.schema.json: charts/telepresence-oss/values.schema.yaml $(tools/y2j)
	mkdir -p $(@D)
	$(tools/y2j) < $< > $@
	git add $@

docs/schemas/workstation-state.v1alpha1.json: pkg/client/cli/manifest/state.schema.yaml $(tools/y2j)
	mkdir -p $(@D)
	$(tools/y2j) < $< > $@
	git add $@

PKG_VERSION = $(shell go list ./pkg/version)

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

BUILD_TAGS=
build-deps:

ifeq ($(DOCKER_BUILD),1)
BUILD_TAGS=-tags docker
endif

# TELEPRESENCE_COVER=1 builds the client binary with code-coverage
# instrumentation (see regression_test/framework/rt/cover.go and `make
# rtest-coverage`).
COVER_FLAG=
ifneq ($(TELEPRESENCE_COVER),)
COVER_FLAG=-cover
endif

pkg/client/cli/docker/compose/dc-cli.json: go.mod go.mod cmd/cobraparser/main.go
	go mod tidy
	(cd cmd/cobraparser && go mod tidy) && GOOS= GOARCH= go run cmd/cobraparser/main.go docker compose > $@

build-deps: pkg/client/cli/docker/compose/dc-cli.json

ifeq ($(GOHOSTOS),windows)
WINTUN_VERSION=0.14.1
$(BUILDDIR)/wintun-$(WINTUN_VERSION)/wintun/bin/$(GOARCH)/wintun.dll:
	mkdir -p $(BUILDDIR)
	curl --fail -L https://www.wintun.net/builds/wintun-$(WINTUN_VERSION).zip -o $(BUILDDIR)/wintun-$(WINTUN_VERSION).zip
	rm -rf  $(BUILDDIR)/wintun-$(WINTUN_VERSION)
	unzip $(BUILDDIR)/wintun-$(WINTUN_VERSION).zip -d $(BUILDDIR)/wintun-$(WINTUN_VERSION)
$(BINDIR)/wintun.dll: $(BUILDDIR)/wintun-$(WINTUN_VERSION)/wintun/bin/$(GOARCH)/wintun.dll
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

HELM_VERSION = $(shell go mod edit -json | jq -r '.Require[] | select(.Path == "helm.sh/helm/v3") | .Version')
LDFLAGS := -X=$(PKG_VERSION).Version=$(TELEPRESENCE_VERSION) -X=$(PKG_VERSION).HelmVersion=$(HELM_VERSION)
ifneq ($(DEBUG),1)
  # strip debug information and dwarf
  LDFLAGS := -s -w $(LDFLAGS)
endif
$(info LDFLAGS=$(LDFLAGS))

ifeq ($(GOOS),linux)
ifeq ($(GOARCH),arm64)
ifeq ($(CGO_ENABLED),1)
	BUILD_ENV := CC=aarch64-linux-gnu-gcc
endif
endif
endif
$(info BUILD_ENV=$(BUILD_ENV))

$(TELEPRESENCE): build-deps FORCE
ifeq ($(GOHOSTOS),windows)
$(TELEPRESENCE): build-deps $(BINDIR)/wintun.dll FORCE
endif
	mkdir -p $(@D)
ifeq ($(DOCKER_BUILD),1)
	CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build $(BUILD_TAGS) $(COVER_FLAG) -trimpath -ldflags="$(LDFLAGS)" -o $@ ./cmd/telepresence
else
# -buildmode=pie enables PIE compilation for binary harderning. Default on darwin and windows (since 1.23) but not in linux.
	$(BUILD_ENV) CGO_ENABLED=$(CGO_ENABLED) $(sdkroot) go build $(BUILD_TAGS) $(COVER_FLAG) -buildmode=pie -trimpath -ldflags="$(LDFLAGS)" -o $@ ./cmd/telepresence
endif

ifeq ($(GOOS),windows)
$(TELEPRESENCE_INSTALLER): $(TELEPRESENCE)
	bash ./packaging/windows-package.sh
endif

.PHONY: release-binary
ifeq ($(GOOS),windows)
release-binary: $(TELEPRESENCE_INSTALLER)
	mkdir -p $(RELEASEDIR)
	cp $(TELEPRESENCE_INSTALLER) $(RELEASEDIR)/telepresence-windows-$(GOARCH)$(BZIP)
else
release-binary: $(TELEPRESENCE)
	mkdir -p $(RELEASEDIR)
	cp $(TELEPRESENCE) $(RELEASEDIR)/telepresence-$(GOOS)-$(GOARCH)$(BEXE)
endif

.PHONY: setup-build-dir
setup-build-dir:
	mkdir -p $(BUILDDIR)
	printf $(TELEPRESENCE_VERSION) > $(BUILDDIR)/version.txt ## Pass version in a file instead of a --build-arg to maximize cache usage
	printf $(HELM_VERSION) > $(BUILDDIR)/helm-version.txt

TELEPRESENCE_SEMVER=$(patsubst v%,%,$(TELEPRESENCE_VERSION))
CLIENT_IMAGE_FQN=$(TELEPRESENCE_REGISTRY)/telepresence:$(TELEPRESENCE_SEMVER)
TEL2_IMAGE_FQN=$(TELEPRESENCE_REGISTRY)/tel2:$(TELEPRESENCE_SEMVER)

.PHONY: images-deps
images-deps: build-deps setup-build-dir

.PHONY: tel2-image
tel2-image: images-deps
	$(eval PLATFORM_ARG := $(if $(TELEPRESENCE_TEL2_IMAGE_PLATFORM), --platform=$(TELEPRESENCE_TEL2_IMAGE_PLATFORM),))
	$(eval COVER_BUILD_ARG := $(if $(TELEPRESENCE_COVER), --build-arg TEL_COVER=-cover,))
	docker build $(PLATFORM_ARG) $(COVER_BUILD_ARG) --target tel2 --tag tel2 --tag $(TEL2_IMAGE_FQN) -f build-aux/docker/images/Dockerfile.traffic .

.PHONY: client-image
client-image: images-deps
	docker build --target telepresence --tag telepresence --tag $(CLIENT_IMAGE_FQN) -f build-aux/docker/images/Dockerfile.client .

# load-image loads a locally-built image into the running local cluster instead
# of pushing it to a registry. The loader is chosen from the current kubectl
# context: a kind-* context uses "kind load docker-image" and a minikube context
# uses "minikube image load". Any other context is an error, since only kind and
# minikube can load images straight into the cluster.
define load-image
@ctx=$$(kubectl config current-context 2>/dev/null); case "$$ctx" in kind-*) echo "kind load docker-image $(1)"; kind load docker-image --name "$${ctx#kind-}" $(1) ;; minikube) echo "minikube image load $(1)"; minikube image load $(1) ;; *) echo "load-* targets require a kind or minikube kubectl context (current: $${ctx:-<none>})" >&2; exit 1 ;; esac
endef

.PHONY: push-tel2-image
push-tel2-image: tel2-image ## (Build) Push the manager/agent container image to $(TELEPRESENCE_REGISTRY)
	docker push $(TEL2_IMAGE_FQN)

.PHONY: load-tel2-image
load-tel2-image: tel2-image ## (Build) Load the manager/agent container image into the local cluster (kind/minikube)
	$(call load-image,$(TEL2_IMAGE_FQN))

.PHONY: save-tel2-image
save-tel2-image: tel2-image
	docker save $(TEL2_IMAGE_FQN) > $(BUILDDIR)/tel2-image.tar

.PHONY: push-client-image
push-client-image: client-image ## (Build) Push the client container image to $(TELEPRESENCE_REGISTRY)
	docker push $(CLIENT_IMAGE_FQN)

.PHONY: load-client-image
load-client-image: client-image ## (Build) Load the client container image into the local cluster (kind/minikube)
	$(call load-image,$(CLIENT_IMAGE_FQN))

ROUTECONTROLLER_IMAGE_FQN=$(TELEPRESENCE_REGISTRY)/route-controller:$(TELEPRESENCE_SEMVER)

.PHONY: routecontroller-image
routecontroller-image: images-deps  ## (Build) Build the route-controller DaemonSet image
	$(eval PLATFORM_ARG := $(if $(TELEPRESENCE_ROUTECONTROLLER_IMAGE_PLATFORM), --platform=$(TELEPRESENCE_ROUTECONTROLLER_IMAGE_PLATFORM),))
	docker build $(PLATFORM_ARG) --target routecontroller --tag route-controller --tag $(ROUTECONTROLLER_IMAGE_FQN) \
	    -f build-aux/docker/images/Dockerfile.routecontroller .

.PHONY: push-routecontroller-image
push-routecontroller-image: routecontroller-image ## (Build) Push the route-controller DaemonSet image to $(TELEPRESENCE_REGISTRY)
	docker push $(ROUTECONTROLLER_IMAGE_FQN)

.PHONY: load-routecontroller-image
load-routecontroller-image: routecontroller-image ## (Build) Load the route-controller DaemonSet image into the local cluster (kind/minikube)
	$(call load-image,$(ROUTECONTROLLER_IMAGE_FQN))

.PHONY: push-images
push-images: push-tel2-image push-client-image push-routecontroller-image

.PHONY: load-images
load-images: load-tel2-image load-client-image load-routecontroller-image ## (Build) Load all images into the local cluster (kind/minikube)

.PHONY: helm-chart
helm-chart: $(BUILDDIR)/telepresence-oss-chart.tgz

$(BUILDDIR)/telepresence-oss-chart.tgz: $(wildcard charts/**/*)
	mkdir -p $(BUILDDIR)
	go run packaging/helmpackage.go -o $@ -v $(TELEPRESENCE_SEMVER)

.PHONY: clobber
clobber:  clobber-tools generate-clean ## (Build) Remove all build artifacts and tools
	rm -rf $(BUILDDIR)
	rm -rf cmd/teleroute/rpc
	rm -rf cmd/teleroute/build-output
	rm -f pkg/client/cli/docker/compose/dc-cli.json
	rm -f docs/helm/values.schema.json
	rm -f docs/schemas/workstation-state.v1alpha1.json

# Release: Push the artifacts places, update pointers ot them
# ===========================================================

.PHONY: prepare-release
prepare-release: generate
	go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION)
	git add go.mod

	(cd pkg/vif/testdata/router && \
	  go mod edit -require=github.com/telepresenceio/telepresence/rpc/v2@$(TELEPRESENCE_VERSION) && \
	  git add go.mod)

	git commit --signoff --message='Prepare $(TELEPRESENCE_VERSION)' || true

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
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/$(TELEPRESENCE_SEMVER)/telepresence.zip \
		--body $(BINDIR)/telepresence.zip
	AWS_PAGER="" aws s3api put-object \
		--region us-east-1 \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/$(TELEPRESENCE_SEMVER)/telepresence-setup.exe \
		--body $(BINDIR)/telepresence-setup.exe
else
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/$(TELEPRESENCE_SEMVER)/telepresence \
		--body $(BINDIR)/telepresence
endif

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: promote-to-stable
promote-to-stable: ## (Release) Update stable.txt in S3
	mkdir -p $(BUILDDIR)
	echo $(TELEPRESENCE_SEMVER) > $(BUILDDIR)/stable.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/stable.txt \
		--body $(BUILDDIR)/stable.txt
ifeq ($(GOHOSTOS), darwin)
# Since the enterprise version is built from a different makefile, we only use the oss target here. Ref: https://github.com/telepresenceio/telepresence/pull/3626#issuecomment-2200150895
	packaging/homebrew-package.sh $(TELEPRESENCE_SEMVER)
endif

# Prerequisites:
# The awscli command must be installed and configured with credentials to upload
# to the datawire-static-files bucket.
.PHONY: promote-nightly
promote-nightly: ## (Release) Update nightly.txt in S3
	mkdir -p $(BUILDDIR)
	echo $(TELEPRESENCE_SEMVER) > $(BUILDDIR)/nightly.txt
	AWS_PAGER="" aws s3api put-object \
		--bucket datawire-static-files \
		--key tel2-oss/$(GOHOSTOS)/$(GOARCH)/nightly.txt \
		--body $(BUILDDIR)/nightly.txt

# Quality Assurance: Make sure things are good
# ============================================

.PHONY: lint-deps
lint-deps: build-deps ## (QA) Everything necessary to lint
lint-deps: $(tools/protolint)
ifneq ($(GOHOSTOS), windows)
lint-deps: $(tools/shellcheck)
endif

.PHONY: build-tests
build-tests: build-deps ## (Test) Build (but don't run) the test suite.  Useful for pre-loading the Go build cache.
	go list ./... | xargs -n1 go test -c -o /dev/null

shellscripts += ./packaging/homebrew-package.sh
shellscripts += ./packaging/windows-package.sh
.PHONY: lint lint-rpc lint-go lint-docs

lint: lint-rpc lint-go lint-docs

lint-docs: $(tools/docslint) ## (QA) Lint the documentation
	$(tools/docslint) docs
	docker run --rm -v $$(pwd):/docs -w /docs jdkato/vale:latest docs

lint-go: lint-deps ## (QA) Run the golangci-lint
ifeq ($(GOOS),windows)
	@ver=$$(curl -fsSL 'https://api.github.com/repos/golangci/golangci-lint/releases/latest' | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4) && \
	docker run -e GOOS=$(GOOS) -e GOEXPERIMENT=$(GOEXPERIMENT) --rm -v $$(pwd):/app -v ~/.cache/golangci-lint/$$ver:/root/.cache -w /app golangci/golangci-lint:$$ver golangci-lint \
	run --timeout 8m ./cmd/cobraparser/... ./cmd/telepresence/... ./pkg/...
else
	# libfuse-dev provides fuse.h, which cgofuse needs to typecheck the linked
	# fuseftp file system on Linux.
	@ver=$$(curl -fsSL 'https://api.github.com/repos/golangci/golangci-lint/releases/latest' | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4) && \
	docker run -e GOOS=$(GOOS) -e GOEXPERIMENT=$(GOEXPERIMENT) --rm -v $$(pwd):/app -v ~/.cache/golangci-lint/$$ver:/root/.cache -w /app --entrypoint bash golangci/golangci-lint:$$ver \
	-c "apt-get update -qq && apt-get install -y -qq libfuse-dev && golangci-lint run --timeout 8m ./..."
endif

lint-rpc: lint-deps ## (QA) Run rpc linter
	$(tools/protolint) lint rpc
ifneq ($(GOHOSTOS), windows)
	$(tools/shellcheck) $(shellscripts)
endif

.PHONY: format
format: lint-deps ## (QA) Automatically fix linter complaints
ifeq ($(GOHOSTOS),windows)
	@ver=$$(curl -fsSL 'https://api.github.com/repos/golangci/golangci-lint/releases/latest' | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4) && \
	docker run -e GOOS=$(GOOS) -e GOEXPERIMENT=$(GOEXPERIMENT) --rm -v $$(pwd):/app -v ~/.cache/golangci-lint/$$ver:/root/.cache -w /app golangci/golangci-lint:$$ver golangci-lint \
	run --timeout 8m --fix ./cmd/telepresence/... ./pkg/...
else
	@ver=$$(curl -fsSL 'https://api.github.com/repos/golangci/golangci-lint/releases/latest' | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4) && \
	docker run -e GOOS=$(GOOS) -e GOEXPERIMENT=$(GOEXPERIMENT) --rm -v $$(pwd):/app -v ~/.cache/golangci-lint/$$ver:/root/.cache -w /app golangci/golangci-lint:$$ver golangci-lint \
	run --timeout 8m --fix ./...
endif
	$(tools/protolint) lint --fix rpc || true

.PHONY: check-all
check-all: check-regression check-unit ## (QA) Run the test suite

.PHONY: check-unit
check-unit: build-deps $(tools/test-report) ## (QA) Run the test suite
	set -o pipefail
ifeq ($(GOOS),linux)
	CGO_ENABLED=$(CGO_ENABLED) go test -json -failfast -timeout=20m ./cmd/... ./pkg/... | $(tools/test-report)
else
	CGO_ENABLED=$(CGO_ENABLED) go test -json -failfast -timeout=20m ./pkg/... | $(tools/test-report)
endif

.PHONY: check-regression
check-regression: build-deps ## (QA) Run the regression-test framework suite (plain output)
	go test -count=1 -timeout=60m ./regression_test/...

.PHONY: rtest-clean
rtest-clean: ## (QA) Remove regression-test resources left in the cluster
	go run ./regression_test/framework/rtclean

RTEST_COVERAGE_DIR=$(BUILDDIR)/rtest/coverage

.PHONY: rtest-coverage
rtest-coverage: ## (QA) Merge client+manager coverage from a RTEST_COVER=1 run and print/export a profile
	$(eval RTEST_COVERAGE_DIRS := $(shell find $(RTEST_COVERAGE_DIR) -mindepth 1 -maxdepth 1 -type d 2>/dev/null | paste -sd, -))
	@if [ -z "$(RTEST_COVERAGE_DIRS)" ]; then \
		echo "no coverage data under $(RTEST_COVERAGE_DIR); run 'make check-regression' with RTEST_COVER=1 first" >&2; \
		exit 1; \
	fi
	go tool covdata percent -i=$(RTEST_COVERAGE_DIRS)
	go tool covdata textfmt -i=$(RTEST_COVERAGE_DIRS) -o=$(RTEST_COVERAGE_DIR)/coverage.out

.PHONY: perf
perf: ## (QA) Run the QUIC performance experiments (needs a cluster; see perf/README.md)
	# Behind the 'perf' build tag so it never runs in check-unit/check-regression.
	# Reinstalls the traffic-manager and needs a QUIC-reachable endpoint plus, for
	# the head-of-line result, PERF_NETEM_IFACE and passwordless 'sudo tc'.
	go test -tags perf -count=1 -v -timeout=30m ./perf/... $(if $(TEST_NAME),-run '$(TEST_NAME)')

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

# Aliases
# =======

.PHONY: test save-image push-image
test:        check-all       ## (ZAlias) Alias for 'check-all'
save-image: save-tel2-image
push-image: push-tel2-image

.PHONY: push-test-images
push-test-images: push-echo-server push-udp-echo

.PHONY: push-echo-server
push-echo-server:
	(cd regression_test/testdata/echo-server && \
 		docker buildx build --platform=linux/amd64,linux/arm64 --push \
 		 --tag ghcr.io/telepresenceio/echo-server:latest \
 		 --tag ghcr.io/telepresenceio/echo-server:0.3.1 .)

.PHONY: push-udp-echo
push-udp-echo:
	(cd regression_test/testdata/udp-echo && \
		docker buildx build --platform=linux/amd64,linux/arm64 --push \
		 --tag ghcr.io/telepresenceio/udp-echo:latest \
		 --tag ghcr.io/telepresenceio/udp-echo:0.1.0 .)


K8S_VERSION ?= $(shell go list -m k8s.io/client-go | awk '{print $$2}' | sed -e 's/v0./v1./')
charts/telepresence-oss/k8s-defs.json: go.mod
	curl -o $@ --fail -L https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/$(K8S_VERSION)-standalone/_definitions.json

build-deps: charts/telepresence-oss/k8s-defs.json
