# Copyright 2018-2019 Datawire. All rights reserved.
#
# Useful bits for writing Makefiles or Makefile snippets.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Variable: export GOHOSTOS
#  - Variable: export GOHOSTARCH
#  - Variable: FLOCK
#  - Variable: NL
#  - Variable: SPACE
#  - Function: joinlist
#  - Function: path.trimprefix
#  - Function: path.addprefix
#  - Function: quote.shell
#  - Function: lazyonce
#  - .PHONY Target: FORCE
## common.mk targets ##
#  (none)
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_prelude.mk := $(lastword $(MAKEFILE_LIST))

#
# Variables

# Possible values of GOHOSTOS/GOHOSTARCH:
# https://golang.org/doc/install/source#environment
_prelude.HAVE_GO = $(call lazyonce,_prelude.HAVE_GO,$(shell which go 2>/dev/null))
export GOHOSTOS   = $(call lazyonce,GOHOSTOS  ,$(if $(_prelude.HAVE_GO),$(shell go env GOHOSTOS  ),$(shell uname -s | tr A-Z a-z)))
export GOHOSTARCH = $(call lazyonce,GOHOSTARCH,$(if $(_prelude.HAVE_GO),$(shell go env GOHOSTARCH),$(patsubst i%86,386,$(patsubst x86_64,amd64,$(shell uname -m)))))

FLOCK = $(call lazyonce,FLOCK,$(if $(shell which flock 2>/dev/null),flock,$(dir $(_prelude.mk))flock))

# NOTE: this is not a typo, this is actually how you spell newline in Make
define NL


endef

# NOTE: this is not a typo, this is actually how you spell space in Make
define SPACE
 
endef

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
