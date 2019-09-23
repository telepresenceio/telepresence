# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet to build Go programs using Go 1.11 modules.
#
## Eager inputs ##
#  - File: ./go.mod
#  - Variable: go.DISABLE_GO_TEST ?=
#  - Variable: go.PLATFORMS ?= $(GOOS)_$(GOARCH)
#
## Lazy inputs ##
#  - Variable: go.GOBUILD ?= go build
#  - Variable: go.LDFLAGS ?=
#  - Variable: go.GOLANG_LINT_FLAGS ?= …$(wildcard .golangci.yml .golangci.toml .golangci.json)…
#  - Variable: CI ?=
#
## Outputs ##
#  - Executable: GOTEST2TAP    ?= $(CURDIR)/build-aux/bin/gotest2tap
#  - Executable: GOLANGCI_LINT ?= $(CURDIR)/build-aux/bin/golangci-lint
#
#  - Variable: export GO111MODULE = on
#  - Variable: NAME ?= $(notdir $(go.module))
#
#  - Variable: go.goversion = $(patsubst go%,%,$(filter go1%,$(shell go version)))
#  - Variable: go.lock = $(FLOCK)                   # if nescessary, in dependencies
#  -                or = $(FLOCK) $(GOPATH)/pkg/mod # if nescessary, in recipes
#  - Variable: go.module = EXAMPLE.COM/YOU/YOURREPO
#  - Variable: go.bins = List of "main" Go packages
#  - Variable: go.pkgs ?= ./...
#
#  - Function: go.list = like $(shell go list $1), but ignores nested Go modules and doesn't download things
#  - Function: go.bin.rule = Only use this if you know what you are doing
#
#  - Targets: bin_$(OS)_$(ARCH)/$(CMD)
#  - Targets: bin_$(OS)_$(ARCH)/$(CMD).opensource.tar.gz
#  - .PHONY Target: go-get
#  - .PHONY Target: go-build
#  - .PHONY Target: go-lint
#  - .PHONY Target: go-fmt
#  - .PHONY Target: go-test
#
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
_go-mod.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_go-mod.mk))common.mk

#
# Configure the `go` command

go.goversion = $(_prelude.go.VERSION)
go.lock = $(_prelude.go.lock)

export GO111MODULE = on

#
# Set default values for input variables

go.GOBUILD ?= go build
go.DISABLE_GO_TEST ?=
go.LDFLAGS ?=
go.PLATFORMS ?= $(GOOS)_$(GOARCH)
go.GOLANG_LINT_FLAGS ?= $(if $(wildcard .golangci.yml .golangci.toml .golangci.json),,--disable-all --enable=gofmt --enable=govet)
CI ?=

#
# Set output variables and functions

GOTEST2TAP       ?= $(build-aux.bindir)/gotest2tap
GOLANGCI_LINT    ?= $(build-aux.bindir)/golangci-lint
_go.mkopensource  = $(build-aux.bindir)/go-mkopensource

$(eval $(call build-aux.bin-go.rule, gotest2tap     , github.com/datawire/build-aux/bin-go/gotest2tap      ))
$(eval $(call build-aux.bin-go.rule, golangci-lint  , github.com/golangci/golangci-lint/cmd/golangci-lint  ))
$(eval $(call build-aux.bin-go.rule, go-mkopensource, github.com/datawire/build-aux/bin-go/go-mkopensource ))

NAME ?= $(notdir $(go.module))

go.module := $(_prelude.go.ensure)$(shell GO111MODULE=on go mod edit -json | jq -r .Module.Path)
ifneq ($(words $(go.module)),1)
  $(error Could not extract $$(go.module) from ./go.mod)
endif

# It would be simpler to create this list if we could use module-aware
# `go list`:
#
#     go.bins := $(shell GO111MODULE=on go list -f='{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./...)
#
# But alas, we can't do that, as that would cause the module system go
# ahead and download dependencies.  We don't want Go to do that at
# Makefile-parse-time; what if we're running `make clean`?
#
# So instead, we must deal with this abomination.
  # "General" functions
    # Usage: $(call _go.file2dirs,FILE)
    # Example: $(call _go.file2dirs,foo/bar/baz) => foo foo/bar foo/bar/baz
    _go.file2dirs = $(if $(findstring /,$1),$(call _go.file2dirs,$(patsubst %/,%,$(dir $1)))) $1
    # Usage: $(call _go.files2dirs,FILE_LIST)
    # Example: $(call _go.files2dirs,foo/bar/baz foo/baz/qux) => foo foo/bar foo/bar/baz foo/baz foo/baz/qux
    _go.files2dirs = $(sort $(foreach f,$1,$(call _go.file2dirs,$f)))
  # Without pruning sub-module packages (relative to ".", without "./" prefix")
    # Use pwd(1) instead of $(CURDIR) because $(CURDIR) behaves differently than `go` in the presence of symlinks.
    _go.raw.cwd := $(shell pwd)
    # Usage: $(call _go.raw.list,ARGS)
    _go.raw.list = $(call path.trimprefix,_$(_go.raw.cwd),$(shell GOPATH=/bogus GO111MODULE=off go list $1))
    _go.raw.pkgs := $(call _go.raw.list,./... 2>/dev/null)
    _go.raw.submods := $(filter-out .,$(patsubst %/go.mod,%,$(wildcard $(addsuffix /go.mod,$(call _go.files2dirs,$(_go.raw.pkgs))))))
  # With pruning sub-module packages (relative to ".", without "./" prefix")
    _go.pkgs = $(filter-out $(foreach m,$(_go.raw.submods),$m $m/%),$(_go.raw.pkgs))
    # Usage: $(call _go.list,ARGS)
    _go.list = $(filter-out $(foreach m,$(_go.raw.submods),$m $m/%),$(_go.raw.list))
  # With pruning sub-module packages (qualified)
    # Usage: $(call go.list,ARGS)
    go.list = $(call path.addprefix,$(go.module),$(_go.list))
    go.bins := $(call go.list,-f='{{if eq .Name "main"}}{{.ImportPath}}{{end}}' $(addprefix ./,$(_go.pkgs)))

go.pkgs ?= ./...

#
# Rules

go-get: ## (Go) Download Go dependencies
go-get: $(go.lock)
	$(go.lock)go mod download
.PHONY: go-get

vendor: go-get FORCE
vendor: $(go.lock)
	$(go.lock)go mod vendor
	@test -d $@ || test "$$(go mod edit -json|jq '.Require|length')" -eq 0

$(dir $(_go-mod.mk))go1%.src.tar.gz:
	curl -o $@ --fail https://dl.google.com/go/$(@F)

# Usage: $(eval $(call go.bin.rule,BINNAME,GOPACKAGE))
define go.bin.rule
bin_%/.$1.stamp: go-get $$(go.lock) FORCE
	$$(go.lock)$$(go.GOBUILD) $$(if $$(go.LDFLAGS),--ldflags $$(call quote.shell,$$(go.LDFLAGS))) -o $$@ $2
bin_%/$1: bin_%/.$1.stamp $$(COPY_IFCHANGED)
	$$(COPY_IFCHANGED) $$< $$@

bin_%/$1.opensource.tar.gz: bin_%/$1 vendor $$(_go.mkopensource) $$(dir $$(_go-mod.mk))go$$(go.goversion).src.tar.gz $$(WRITE_IFCHANGED) $$(go.lock)
	bash -o pipefail -c '$$(go.lock)$$(_go.mkopensource) --output-name=$1.opensource --package=$2 --gotar=$$(dir $$(_go-mod.mk))go$$(go.goversion).src.tar.gz | $$(WRITE_IFCHANGED) $$@'
endef

_go.bin.name = $(notdir $(_go.bin))
_go.bin.pkg = $(_go.bin)
$(foreach _go.bin,$(go.bins),$(eval $(call go.bin.rule,$(_go.bin.name),$(_go.bin.pkg))))
go-build: $(foreach _go.PLATFORM,$(go.PLATFORMS),$(foreach _go.bin,$(go.bins), bin_$(_go.PLATFORM)/$(_go.bin.name)                   ))
build:    $(foreach _go.PLATFORM,$(go.PLATFORMS),$(foreach _go.bin,$(go.bins), bin_$(_go.PLATFORM)/$(_go.bin.name).opensource.tar.gz ))

go-build: ## (Go) Build the code with `go build`
.PHONY: go-build

go-lint: ## (Go) Check the code with `golangci-lint`
go-lint: $(GOLANGCI_LINT) go-get $(go.lock)
	$(go.lock)$(GOLANGCI_LINT) run $(go.GOLANG_LINT_FLAGS) $(go.pkgs)
.PHONY: go-lint

go-fmt: ## (Go) Fixup the code with `go fmt`
go-fmt: go-get $(go.lock)
	$(go.lock)go fmt $(go.pkgs)
.PHONY: go-fmt

go-test: ## (Go) Check the code with `go test`
go-test: go-build
ifeq ($(go.DISABLE_GO_TEST),)
	$(MAKE) $(dir $(_go-mod.mk))go-test.tap.summary
endif

$(dir $(_go-mod.mk))go-test.tap: $(GOTEST2TAP) $(TAP_DRIVER) $(go.lock) FORCE
	@{ $(go.lock)go test -json $(go.pkgs) || true; } 2>&1 | $(GOTEST2TAP) | tee $@ | $(TAP_DRIVER) stream -n go-test

#
# go-doc

go-doc: ## (Go) Run a `godoc -http` server
go-doc: $(dir $(_go-mod.mk))gopath
	{ \
		while sleep 1; do \
			$(MAKE) --quiet $(dir $(_go-mod.mk))gopath/src/$(go.module); \
		done & \
		trap "kill $$!" EXIT; \
		GOPATH=$(dir $(_go-mod.mk))gopath godoc -http :8080; \
	}
.PHONY: go-doc

$(dir $(_go-mod.mk))gopath: FORCE vendor
	mkdir -p $(dir $(_go-mod.mk))gopath/src
	echo 'module bogus' > $(dir $(_go-mod.mk))gopath/go.mod
	rsync --archive --delete vendor/ $(dir $(_go-mod.mk))gopath/src/
	$(MAKE) $(dir $(_go-mod.mk))gopath/src/$(go.module)
$(dir $(_go-mod.mk))gopath/src/$(go.module): $(go.lock) FORCE
	mkdir -p $@
	$(go.lock)go list ./... | sed -e 's,^$(go.module),,' -e 's,$$,/*.go,' | rsync --archive --prune-empty-dirs --delete-excluded --include='*/' --include-from=/dev/stdin --exclude='*' ./ $@/

#
# Hook in to common.mk

build: go-build
lint: go-lint
format: go-fmt
test-suite.tap: $(if $(go.DISABLE_GO_TEST),,$(dir $(_go-mod.mk))go-test.tap)

clean: _go-clean
_go-clean:
	rm -f $(dir $(_go-mod.mk))go-test.tap
	rm -rf $(dir $(_go-mod.mk))gopath/ vendor/
# Files made by older versions.  Remove the tail of this list when the
# commit making the change gets far enough in to the past.
#
# 2018-07-03
	rm -f vendor.hash
# 2018-07-01
	rm -f $(dir $(_go-mod.mk))golangci-lint
# 2019-02-06
	rm -f $(dir $(_go-mod.mk))patter.go $(dir $(_go-mod.mk))patter.go.tmp
.PHONY: _go-clean

clobber: _go-clobber
_go-clobber:
	rm -f $(dir $(_go-mod.mk))go1*.src.tar.gz
.PHONY: _go-clobber

endif
