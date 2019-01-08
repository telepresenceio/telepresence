# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for bits common bits we always want.
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)

#
# Variables

# If $@ is bin_GOOS_GOARCH/BINNAME, set GOOS and GOARCH accodingly,
# otherwise inherit from the environment.
#
# Possible values of GOOS/GOARCH:
# https://golang.org/doc/install/source#environment
export GOOS   = $(if $(filter bin_%,$(@D)),$(word 2,$(subst _, ,$(@D))),$(shell go env GOOS))
export GOARCH = $(if $(filter bin_%,$(@D)),$(word 3,$(subst _, ,$(@D))),$(shell go env GOARCH))

# NOTE: this is not a typo, this is actually how you spell newline in Make
define NL


endef

# NOTE: this is not a typo, this is actually how you spell space in Make
define SPACE
 
endef

#
# Targets

# To the extent reasonable, use target names that agree with the GNU
# standards.
#
# https://www.gnu.org/prep/standards/standards.html#Makefile-Conventions

all: build
.PHONY: all

build: ## Build the software
.PHONY: build

check: ## Check whether the software works; run the tests
check: lint
.PHONY: check

lint: ## Perform static analysis of the software
.PHONY: lint

format: ## Apply automatic formatting+cleanup to source code
.PHONY: format

clean: ## Delete all files that are normally created by building the software
.PHONY: clean

# XXX: Rename this to maintainer-clean, per GNU?
clobber: ## Delete all files that this Makefile can re-generate
clobber: clean
.PHONY: clobber

#
# Targets: Default behavior

clean: _common_clean
_common_clean:
	rm -rf -- bin_*
.PHONY: _common_clean

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

#
# Configure how Make works

# Turn off .INTERMEDIATE file removal by marking all files as
# .SECONDARY.  .INTERMEDIATE file removal is a space-saving hack from
# a time when drives were small; on modern computers with plenty of
# storage, it causes nothing but headaches.
#
# https://news.ycombinator.com/item?id=16486331
.SECONDARY:

# If a recipe errors, remove the target it was building.  This
# prevents outdated/incomplete results of failed runs from tainting
# future runs.  The only reason .DELETE_ON_ERROR is off by default is
# for historical compatibility.
#
# If for some reason this behavior is not desired for a specific
# target, mark that target as .PRECIOUS.
.DELETE_ON_ERROR:

# Sometimes we have a file-target that we want Make to always try to
# re-generate (such as compiling a Go program; we would like to let
# `go install` decide whether it is up-to-date or not, rather than
# trying to teach Make how to do that).  We could mark it as .PHONY,
# but that tells make that "this isn't a "this isn't a real file that
# I expect to ever exist", which has a several implications for Make,
# most of which we don't want.  Instead, we can have them *depend* on
# a .PHONY target (which we'll name "FORCE"), so that they are always
# considered out-of-date by Make, but without being .PHONY themselves.
.PHONY: FORCE

endif
