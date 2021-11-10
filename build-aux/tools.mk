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

# This file deals with installing programs used by the build.

TOOLSDIR=tools
TOOLSBINDIR=$(TOOLSDIR)/bin
TOOLSSRCDIR=$(TOOLSDIR)/src

GOHOSTOS ?= $(shell go env GOHOSTOS)
GOHOSTARCH ?= $(shell go env GOHOSTARCH)

# GOARCH defaults to GOHOSTARCH but can also be set by the caller of make.
GOARCH?=$(GOHOSTARCH)

export PATH := $(abspath $(TOOLSBINDIR)):$(PATH)

go-mod-tidy: $(patsubst $(TOOLSSRCDIR)/%/go.mod,go-mod-tidy/tools/%,$(wildcard $(TOOLSSRCDIR)/*/go.mod))

.PHONY: go-mod-tidy/tools/%
go-mod-tidy/tools/%:
	rm -f $(TOOLSSRCDIR)/$*/go.sum
	cd $(TOOLSSRCDIR)/$* && GOFLAGS=-mod=mod go mod tidy
	cd $(TOOLSSRCDIR)/$* && GOFLAGS=-mod=mod go mod vendor
	rm -rf $(TOOLSSRCDIR)/$*/vendor

clobber: clobber-tools

.PHONY: clobber-tools
clobber-tools:
	rm -rf $(TOOLSBINDIR) $(TOOLSDIR)/include $(TOOLSDIR)/*.*


# Protobuf compiler
# =================
#
# Install protoc under $TOOLSDIR. A protoc that is already installed locally
# cannot be trusted since this must be the exact same version as used when
# running CI. If it isn't, the generate-check will fail.
tools/protoc = $(TOOLSBINDIR)/protoc
PROTOC_VERSION=3.17.3
PROTOC_ZIP=protoc-$(PROTOC_VERSION)-$(subst darwin,osx,$(GOHOSTOS))-$(shell uname -m).zip
$(TOOLSDIR)/$(PROTOC_ZIP):
	mkdir -p $(@D)
	curl -sfL https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/$(PROTOC_ZIP) -o $@
%/bin/protoc %/include %/readme.txt: %/$(PROTOC_ZIP)
	cd $* && unzip -q -o -DD $(<F)

# Protobuf linter
# ===============
#
tools/protolint = $(TOOLSBINDIR)/protolint
PROTOLINT_VERSION=0.32.0
PROTOLINT_TGZ=protolint_$(PROTOLINT_VERSION)_$(shell uname -s)_$(shell uname -m).tar.gz
$(TOOLSDIR)/$(PROTOLINT_TGZ):
	mkdir -p $(@D)
	curl -sfL https://github.com/yoheimuta/protolint/releases/download/v$(PROTOLINT_VERSION)/$(PROTOLINT_TGZ) -o $@
%/bin/protolint %/bin/protoc-gen-protolint: %/$(PROTOLINT_TGZ)
	mkdir -p $(@D)
	tar -C $(@D) -zxmf $< protolint protoc-gen-protolint

# Shellcheck
# ==========
#
tools/shellcheck = $(TOOLSBINDIR)/shellcheck
SHELLCHECK_VERSION=0.7.2
SHELLCHECK_ARCH=$(shell uname -m)
# shellcheck uses the same binary on Intel and Apple Silicon macs
ifeq ($(GOHOSTOS),darwin)
SHELLCHECK_ARCH=x86_64
endif
SHELLCHECK_TXZ = https://github.com/koalaman/shellcheck/releases/download/v$(SHELLCHECK_VERSION)/shellcheck-v$(SHELLCHECK_VERSION).$(GOHOSTOS).$(SHELLCHECK_ARCH).tar.xz
$(TOOLSDIR)/$(notdir $(SHELLCHECK_TXZ)):
	mkdir -p $(@D)
	curl -sfL $(SHELLCHECK_TXZ) -o $@
%/bin/shellcheck: %/$(notdir $(SHELLCHECK_TXZ))
	mkdir -p $(@D)
	tar -C $(@D) -Jxmf $< --strip-components=1 shellcheck-v$(SHELLCHECK_VERSION)/shellcheck

# Helm
# ====
#
ifeq ($(GOHOSTOS),windows)
SUFFIX=.exe
else
SUFFIX=
endif
tools/helm = $(TOOLSBINDIR)/helm
HELM_VERSION=$(shell go mod edit -json | jq -r '.Require[] | select (.Path == "helm.sh/helm/v3") | .Version')
HELM_TGZ = https://get.helm.sh/helm-$(HELM_VERSION)-$(GOHOSTOS)-$(GOHOSTARCH).tar.gz
$(TOOLSDIR)/$(notdir $(HELM_TGZ)):
	mkdir -p $(@D)
	curl -sfL $(HELM_TGZ) -o $@
%/bin/helm: %/$(notdir $(HELM_TGZ))
	mkdir -p $(@D)
	tar -C $(@D) -zxmf $< --strip-components=1 $(GOHOSTOS)-$(GOHOSTARCH)/helm$(SUFFIX)

# `go get`-able things
# ====================
#
# Install the all under $TOOLSDIR. Versions that are already in $GOBIN
# cannot be trusted since this must be the exact same version as used
# when running CI. If it isn't the generate-check will fail.
#
# Instead of having "VERSION" variables here, the versions are
# controlled by `tools/src/${thing}/go.mod` files.  Having those in
# separate per-tool go.mod files avoids conflicts between tools and
# avoid them poluting our main go.mod file.
tools/protoc-gen-go      = $(TOOLSBINDIR)/protoc-gen-go
tools/protoc-gen-go-grpc = $(TOOLSBINDIR)/protoc-gen-go-grpc
tools/ko                 = $(TOOLSBINDIR)/ko
tools/golangci-lint      = $(TOOLSBINDIR)/golangci-lint
tools/go-mkopensource    = $(TOOLSBINDIR)/go-mkopensource
$(TOOLSBINDIR)/%: $(TOOLSSRCDIR)/%/go.mod $(TOOLSSRCDIR)/%/pin.go
	cd $(<D) && GOOS= GOARCH= go build -o $(abspath $@) $$(sed -En 's,^import "(.*)".*,\1,p' pin.go)
