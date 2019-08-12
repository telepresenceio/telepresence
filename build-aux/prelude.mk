# Copyright 2018-2019 Datawire. All rights reserved.
#
# Useful bits for writing Makefiles or Makefile snippets.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Executable: FLOCK           ?= $(CURDIR)/build-aux/bin/flock # or /usr/bin/flock
#  - Executable: WRITE_IFCHANGED ?= $(CURDIR)/build-aux/bin/write-ifchanged
#  - Executable: COPY_IFCHANGED  ?= $(CURDIR)/build-aux/bin/copy-ifchanged
#  - Executable: TAP_DRIVER      ?= $(CURDIR)/build-aux/bin/tap-driver
#
#  - Variable: build-aux.dir
#  - Variable: build-aux.bindir
#  - Variable: build-aux.go-build
#
#  - Variable: export GOHOSTOS
#  - Variable: export GOHOSTARCH
#  - Variable: NL
#  - Variable: SPACE
#
#  - Function: joinlist
#  - Function: path.trimprefix
#  - Function: path.addprefix
#  - Function: quote.shell
#  - Function: lazyonce
#  - .PHONY Target: FORCE
#
#  - Target: _prelude_clobber
## common.mk targets ##
#  - clobber
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_prelude.mk := $(lastword $(MAKEFILE_LIST))

#
# Variables

build-aux.dir = $(patsubst %/,%,$(dir $(_prelude.mk)))
build-aux.bindir = $(abspath $(build-aux.dir)/bin)

# Possible values of GOHOSTOS/GOHOSTARCH:
# https://golang.org/doc/install/source#environment
_prelude.HAVE_GO = $(call lazyonce,_prelude.HAVE_GO,$(shell which go 2>/dev/null))
export GOHOSTOS   = $(call lazyonce,GOHOSTOS  ,$(if $(_prelude.HAVE_GO),$(shell go env GOHOSTOS  ),$(shell uname -s | tr A-Z a-z)))
export GOHOSTARCH = $(call lazyonce,GOHOSTARCH,$(if $(_prelude.HAVE_GO),$(shell go env GOHOSTARCH),$(patsubst i%86,386,$(patsubst x86_64,amd64,$(shell uname -m)))))

# NOTE: this is not a typo, this is actually how you spell newline in Make
define NL


endef

# NOTE: this is not a typo, this is actually how you spell space in Make
define SPACE
 
endef

#
# Executables

FLOCK           ?= $(call lazyonce,FLOCK,$(or $(shell which flock 2>/dev/null),$(build-aux.bindir)/flock))
COPY_IFCHANGED  ?= $(build-aux.bindir)/copy-ifchanged
WRITE_IFCHANGED ?= $(build-aux.bindir)/write-ifchanged
TAP_DRIVER      ?= $(build-aux.bindir)/tap-driver

$(build-aux.bindir):
	mkdir $@

clobber: _clobber-prelude
_clobber-prelude:
	rm -rf $(build-aux.bindir)
.PHONY: _clobber-prelude

$(build-aux.bindir)/%: $(build-aux.dir)/bin-sh/%.sh | $(build-aux.bindir)
	install $< $@

# All of this funny business with locking can be ditched once we drop
# support for Go 1.11.  (When removing it, be aware that go-mod.mk
# uses `_prelude.go.*` variables).
_prelude.go.GOPATH = $(call lazyonce,$(shell go env GOPATH))
_prelude.go.goversion = $(call lazyonce,_prelude.go.goversion,$(patsubst go%,%,$(filter go1%,$(shell go version))))
_prelude.go.lock = $(if $(filter 1.11 1.11.%,$(_prelude.go.goversion)),$(FLOCK)$(if $@, $(_prelude.go.GOPATH)/pkg/mod ))
$(build-aux.bindir)/.flock.stamp: $(build-aux.bindir)/.%.stamp: $(build-aux.dir)/bin-go/%/ $(shell find $(build-aux.dir)/bin-go/ -mindepth 1) $(build-aux.dir)/go.mod | $(build-aux.bindir)
	cd $(build-aux.dir) && GO111MODULE=on go build -o $@ ./bin-go/$*
$(build-aux.bindir)/.%.stamp: $(build-aux.dir)/bin-go/%/ $(shell find $(build-aux.dir)/bin-go/ -mindepth 1) $(build-aux.dir)/go.mod $(_prelude.go.lock) | $(build-aux.bindir)
	cd $(build-aux.dir) && GO111MODULE=on $(_prelude.go.lock)go build -o $@ ./bin-go/$*
$(build-aux.bindir)/%: $(build-aux.bindir)/.%.stamp $(COPY_IFCHANGED)
	$(COPY_IFCHANGED) $< $@
build-aux.go-build = cd $(build-aux.dir) && GO111MODULE=on $(_prelude.go.lock)go build

#
# Functions

# Usage: $(call joinlist,SEPARATOR,LIST)
# Example: $(call joinlist,/,foo bar baz) => foo/bar/baz
joinlist=$(if $(word 2,$2),$(firstword $2)$1$(call joinlist,$1,$(wordlist 2,$(words $2),$2)),$2)

# Usage: $(call path.trimprefix,PREFIX,LIST)
# Example: $(call path.trimprefix,foo/bar,foo/bar foo/bar/baz) => . baz
path.trimprefix = $(patsubst $1/%,%,$(patsubst $1,$1/.,$2))

# Usage: $(call path.addprefix,PREFIX,LIST)
# Example: $(call path.addprefix,foo/bar,. baz) => foo/bar foo/bar/baz
path.addprefix = $(patsubst %/.,%,$(addprefix $1/,$2))

# Usage: $(call quote.shell,STRING)
# Example: $(call quote.shell,some'string"with`special characters) => "some'string\"with\`special characters"
#
# Based on
# https://git.lukeshu.com/autothing/tree/build-aux/Makefile.once.head/00-quote.mk?id=9384e763b00774603208b3d44977ed0e6762a09a
# but modified to make newlines work with shells other than Bash.
quote.shell = "$$(printf '%s\n' $(subst $(NL),' ','$(subst ','\'',$1)'))"

# Usage: VAR = $(call lazyonce,VAR,EXPR)
#
# Caches the value of EXPR (in case it's expensive/slow) once it is
# evaluated, but doesn't eager-evaluate it either.
lazyonce = $(eval $(strip $1) := $2)$($(strip $1))

#
# Targets

# Sometimes we have a file-target that we want Make to always try to
# re-generate (such as compiling a Go program; we would like to let
# `go install` decide whether it is up-to-date or not, rather than
# trying to teach Make how to do that).  We could mark it as .PHONY,
# but that tells Make that "this isn't a "this isn't a real file that
# I expect to ever exist", which has a several implications for Make,
# most of which we don't want.  Instead, we can have them *depend* on
# a .PHONY target (which we'll name "FORCE"), so that they are always
# considered out-of-date by Make, but without being .PHONY themselves.
.PHONY: FORCE

endif
