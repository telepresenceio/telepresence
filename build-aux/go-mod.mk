# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet to build Go programs using Go 1.11 modules
#
## Inputs ##
#  - File: ./go.mod
#  - Variable: go.DISABLE_GO_TEST ?=
## Outputs ##
#  - Variable: go.module = EXAMPLE.COM/YOU/YOURREPO
#  - Variable: go.bins = List of "main" Go packages
#  - Variable: NAME ?= $(notdir $(go.module))
#  - .PHONY Target: go-get
#  - .PHONY Target: go-build
#  - .PHONY Target: check-go-fmt
#  - .PHONY Target: go-vet
#  - .PHONY Target: go-fmt
#  - .PHONY Target: go-test
## common.mk targets ##
#  - build
#  - lint
#  - check
#  - format
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
ifneq ($(go.module),)
$(error Only include one of go-mod.mk or go-workspace.mk)
endif
include $(dir $(lastword $(MAKEFILE_LIST)))/common.mk

#
# 0. configure the `go` command

export GO111MODULE = on

# .NOTPARALLEL is important, because (as of Go 1.11) `go` commands
# that write to the module cache are not safe to invoke concurrently
# (this should be fixed in Go 1.12, scheduled for February 2019[1]).
# We could try working around that with a multi-target pattern rule[2]
# and using `GOBIN=$(@D) go install` instead of `go build -o`, but you
# can't use GOBIN when cross-compiling.  So, until we can depend on Go
# 1.12, just disable parallel builds.
#
# [1]: https://tip.golang.org/doc/go1.12
# [2]: https://www.gnu.org/software/make/manual/html_node/Pattern-Examples.html#Pattern-Examples
.NOTPARALLEL:

#
# 1. Set go.module

# Why use this complex `sed` expression to parse go.mod, instead of
# just having `go list -m` do it?  Because `go list -m` will go ahead
# and download dependencies.  We don't want Go to do that at
# Makefile-parse-time; what if we're running `make clean`?
#
# See: cmd/go/internal/modfile/read.go:ModulePath()
go.module := $(strip $(shell sed -n -e 's,//.*,,' -e '/^\s*module/{s/^\s*module//;p;q;}' go.mod))
#go.module := $(shell $(GO) list -m)
ifneq ($(words $(go.module)),1)
  # Print a helpful message
  ifeq ($(wildcard go.mod),)
    $(info go-mod.mk: File `./go.mod` does not exist.)
    ifeq ($(wildcard .go-workspace/),)
      $(info go-mod.mk: Initalize it with `go mod init github.com/YOU/REPONAME`)
    else
      $(info go-mod.mk: But `./go-workspace/` does.  Did you mean to use go-workspace.mk?)
    endif
  else
    $(info go-mod.mk: File `./go.mod` seems to be malformed; could not parse.)
  endif
  # And then error out
  $(error Could not extract $$(go.module) from ./go.mod)
endif

#
# 2. Set go.pkgs

go.pkgs := ./...

#
# 3. Recipe for go-get

go-get:
	go list ./...

#
# Include _go-common.mk

include $(dir $(lastword $(MAKEFILE_LIST)))/_go-common.mk

#
endif
