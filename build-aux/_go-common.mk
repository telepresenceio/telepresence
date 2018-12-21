# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet of common bits between go-mod.mk and
# go-workspace.mk.  Don't include this directly from your Makefile,
# include either go-mod.mk or go-workspace.mk!

ifeq ($(go.module),)
$(error Do not include _go-common.mk directly, include go-mod.mk or go-workspace.mk)
endif

# It would be simpler to create this list if we could use Go modules:
#
#     go.bins := $(shell $(GO) list -f='{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./...)
#
# But alas, we can't do that *even if* the build is using Go modules,
# as that would cause the module system go ahead and download
# dependencies.  We don't want Go to do that at Makefile-parse-time;
# what if we're running `make clean`?
#
# So instead, we must deal with this abomination.  At least that means
# we can share it between go-mod.mk and go-workspace.mk.
_go.submods := $(patsubst %/go.mod,%,$(shell git ls-files '*/go.mod'))
go.list = $(call path.addprefix,$(go.module),\
                                $(filter-out $(foreach d,$(_go.submods),$d $d/%),\
                                             $(call path.trimprefix,_$(CURDIR),\
                                                                    $(shell GO111MODULE=off GOCACHE=off GOPATH=$(GOPATH) go list $1))))
go.bins := $(call go.list,-f='{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./...)

#
# Rules

# go-FOO.mk is responsible for implementing go-get
go-get: ## Download Go dependencies
.PHONY: go-get

define _go.bin.rule
bin_%/$(notdir $(go.bin)): go-get FORCE
	go build -o $$@ $(go.bin)
endef
$(foreach go.bin,$(go.bins),$(eval $(_go.bin.rule)))

go-build: $(addprefix bin_$(GOOS)_$(GOARCH)/,$(notdir $(go.bins)))
.PHONY: go-build

check-go-fmt: ## Check whether the code conforms to `gofmt`
	test -z "$$(git ls-files -z '*.go' | xargs -0 gofmt -d | tee /dev/stderr)"
.PHONY: check-go-fmt

go-vet: ## Check the code with `go vet`
go-vet: go-get
	go vet $(go.pkgs)
.PHONY: go-vet

go-fmt: ## Fixup the code with `go fmt`
	go fmt ./...
.PHONY: go-fmt

#go-test: ## Check the code with `go test`
#go-test: go-build
#	go test $(go.pkgs)
#.PHONY: go-test

#
# Hook in to common.mk

build: go-build
lint: check-go-fmt go-vet
#check: go-test
format: go-fmt
