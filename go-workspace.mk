# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet to build Go programs using Go workspaces
# ("GOPATH").  Go workspaces are scheduled for deprecation in Go 1.13,
# which is scheduled for August 2019.
#
## Eager inputs ##
#  - Symlink: ./.go-workspace/src/EXAMPLE.COM/YOU/YOURREPO -> (git topdir)
#  - File: glide.yaml or Gopkg.toml (optional)
#  - Variable: go.DISABLE_GO_TEST ?=
#  - Variable: go.PLATFORMS ?= $(GOOS)_$(GOARCH)
## Lazy inputs ##
#  - Variable: go.GOBUILD ?= go build
#  - Variable: go.LDFLAGS ?=
#  - Variable: go.GOLANG_LINT_VERSION ?= …
#  - Variable: go.GOLANG_LINT_FLAGS ?= …$(wildcard .golangci.yml .golangci.toml .golangci.json)…
#  - Variable: CI
## Outputs ##
#  - Variable: NAME ?= $(notdir $(go.module))
#  - Variable: go.module = EXAMPLE.COM/YOU/YOURREPO
#  - Variable: go.bins = List of "main" Go packages
#  - Variable: go.pkgs = List of Go packages
#  - Target: vendor/ (if `./glade.yaml` is present)
#  - .PHONY Target: go-get
#  - .PHONY Target: go-build
#  - .PHONY Target: go-lint
#  - .PHONY Target: go-fmt
#  - .PHONY Target: go-test
## common.mk targets ##
#  - build
#  - lint
#  - format
#  - check
#  - clean
#  - clobber
#
# `go.PLATFORMS` is a list of OS_ARCH pairs that specifies which
# platforms `make build` should compile for.  Unlike most variables,
# it must be specified *before* including go-workspace.mk.
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
ifneq ($(go.module),)
$(error Only include one of go-mod.mk or go-workspace.mk)
endif
include $(dir $(lastword $(MAKEFILE_LIST)))common.mk

#
# 0. configure the `go` command

export GO111MODULE = off
export GOPATH = $(CURDIR)/.go-workspace

_go-clobber:
	find .go-workspace -exec chmod +w {} +
	rm -rf .go-workspace
	mkdir -p $(dir .go-workspace/src/$(go.module))
	ln -s $(call joinlist,/,$(patsubst %,..,$(subst /, ,$(dir .go-workspace/src/$(go.module))))) .go-workspace/src/$(go.module)
.PHONY: _go-clobber
clobber: _go-clobber

#
# 1. Set go.module

go.module := $(patsubst src/%,%,$(shell cd ./.go-workspace && find src \( -name '.*' -prune \) -o -type l -print))
ifneq ($(words $(go.module)),1)
  # Print a helpful message
  ifeq ($(wildcard .go-workspace/),)
    $(info go-worksapce.mk: Directory `./go-workspace/` does not exist.)
    ifeq ($(wildcard go.mod),)
      $(info go-workspace.mk: Initialize it with:)
      $(info go-workspace.mk:     $$ mkdir -p .go-workspace/src/github.com/YOU)
      $(info go-workspace.mk:     $$ ln -srT . .go-workspace/src/github.com/YOU/REPONAME)
    else
      $(info go-workspace.mk: But `./go.mod` does.  Did you mean to use go-mod.mk?)
    endif
  else
    $(info go-workspace.mk: Did not find exactly 1 symlink under `./go-workspace/`)
  endif
  # And then error out
  $(error Could not extract $$(go.module) from ./.go-workspace/src/)
endif

#
# Include _go-common.mk

include $(dir $(lastword $(MAKEFILE_LIST)))_go-common.mk

#
# 2. Set go.pkgs
#
# We do this *after* including _go-common.mk so that we can make use
# of the $(call go.list,…) function, which is defined there.

go.pkgs := $(call go.list,./...)

#
# 3. Recipe for go-get

go-get:
	go get -d $(go.bins)

ifneq ($(wildcard glide.yaml),)
vendor: glide.yaml $(wildcard glide.lock)
	rm -rf $@
	glide install || { r=$$?; rm -rf $@; exit $$r; }
go-get: vendor

_go-clobber-vendor:
	rm -rf vendor
.PHONY: _go-clobber-vendor
clobber: _go-clobber-vendor
endif

ifneq ($(wildcard Gopkg.toml),)
vendor: Gopkg.toml $(wildcard Gopkg.lock)
	rm -rf $@
	cd $(GOPATH)/src/$(go.module) && dep ensure -v -vendor-only || { r=$$?; rm -rf $@; exit $$r; }
go-get: vendor

_go-clobber-vendor:
	rm -rf vendor
.PHONY: _go-clobber-vendor
clobber: _go-clobber-vendor
endif

#
endif
