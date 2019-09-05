# This is part of `prelude.mk`, split out for organizational purposes.
# !!! NOTHING EAGER IS ALLOWED TO HAPPEN IN THIS FILE !!!

#
# Internal Go language support the rest of prelude.mk (and go-mod.mk)

#
# Some global constants

_prelude.go.HAVE    = $(call lazyonce,_prelude.go.HAVE,$(shell which go 2>/dev/null))
_prelude.go.GOPATH  = $(call lazyonce,_prelude.go.GOPATH,$(shell go env GOPATH))
_prelude.go.VERSION = $(call lazyonce,_prelude.go.VERSION,$(patsubst go%,%,$(filter go1%,$(shell go version))))

# Possible values of GOHOSTOS/GOHOSTARCH:
# https://golang.org/doc/install/source#environment
export GOHOSTOS   = $(call lazyonce,GOHOSTOS  ,$(if $(_prelude.go.HAVE),$(shell go env GOHOSTOS  ),$(shell uname -s | tr A-Z a-z)))
export GOHOSTARCH = $(call lazyonce,GOHOSTARCH,$(if $(_prelude.go.HAVE),$(shell go env GOHOSTARCH),$(patsubst i%86,386,$(patsubst x86_64,amd64,$(shell uname -m)))))

#
# Pure functions for working with Go version strings

# Usage: $(call _prelude.go.VERSION.fill_patch, [MAJOR[ MINOR [PATCH] [PRERELEASE]]])
#
# Given an already-split Go version string, make sure the PATCH-level
# is filled in; `go version` omits it if PATCH==0.
_prelude.go.VERSION.fill = \
    $(if $(call uint.eq,0,$(words $1)),  0  0  0,\
    $(if $(call uint.eq,1,$(words $1)),  $1 0  0,\
    $(if $(call uint.eq,2,$(words $1)),  $1    0,\
    $(if $(call uint.eq,3,$(words $1)),\
         $(if $(filter beta% rc%,$(word 3,$1)),\
              $(wordlist 1,2,$1) 0 $(word 3,$1),\
              $1),\
    $(if $(call uint.eq,4,$(words $1)),  $1     ,\
    $(error Could not parse Go version string: '$1'))))))

# Usage: $(call _prelude.go.VERSION.parse, MAJOR.MINOR[.PATCH][PRERELEASE])
#
# Given a Go version string, parse it in to 4 whitespace-separated
# segments: MAJOR MINOR PATCH PRERELEASE.  None of MAJOR, MINOR, or PATCH
# will be empty in the output.  PRERELEASE may be empty in the output.
_prelude.go.VERSION.parse = $(call _prelude.go.VERSION.fill,$(subst ., ,$(subst beta,.beta,$(subst rc,.rc,$1))))

# Usage: $(_prelude.go.VERSION.prerelease.ge,A,B) => A >= B
#
# Compare Go version PRERELEASE strings (since you can't use
# $(call uint.ge,A,B) on them).
#
#   (empty)    , X       => $(TRUE)
#   (nonempty) , (empty) => $(FALSE)
#   rcX        , betaY   => $(TRUE)
#   rcX        , rcY     => (X >= Y)
#   betaX      , betaY   => (X >= Y)
_prelude.go.VERSION.prerelease.ge = $(strip \
    $(if $(call not,$1),$(TRUE),\
    $(if $(call not,$2),$(FALSE),\
    $(if $(and $(filter rc%,$1),$(filter beta%,$2)),$(TRUE),\
    $(if $(and $(filter beta%,$1),$(filter rc%,$2)),$(FALSE),\
    $(call uint.ge,\
               $(patsubst beta%,%,$(patsubst rc%,%,$1)),\
               $(patsubst beta%,%,$(patsubst rc%,%,$2))))))))

# Usage: $(call _prelude.go.VERSION._ge, PARSED_A, PARSED_B)
_prelude.go.VERSION._ge = $(strip \
    $(if $(call uint.gt,$(word 1,$1),$(word 1,$2)),$(TRUE),\
    $(if $(call uint.lt,$(word 1,$1),$(word 1,$2)),$(FALSE),\
    $(if $(call uint.gt,$(word 2,$1),$(word 2,$2)),$(TRUE),\
    $(if $(call uint.lt,$(word 2,$1),$(word 2,$2)),$(FALSE),\
    $(if $(call uint.gt,$(word 3,$1),$(word 3,$2)),$(TRUE),\
    $(if $(call uint.lt,$(word 3,$1),$(word 3,$2)),$(FALSE),\
    $(call _prelude.go.VERSION.prerelease.ge,$(word 4,$1),$(word 4,$2)))))))))

# Usage: $(call _prelude.go.VERSION.ge, A, B)
_prelude.go.VERSION.ge = $(call _prelude.go.VERSION._ge,$(call _prelude.go.VERSION.parse,$1),$(call _prelude.go.VERSION.parse,$2))

#
# Function for doing version checks

# Usage: $(call _prelude.go.VERSION.HAVE, major.minor[.patch][prerelease])
#
# Evaluates to $(TRUE) if `go` is >= the specified version, $(FALSE)
# otherwise.
_prelude.go.VERSION.HAVE = $(if $(_prelude.go.HAVE),$(call _prelude.go.VERSION.ge,$(_prelude.go.VERSION),$1))

#
# Building Go programs for use by build-aux

_prelude.go.error_unsupported = $(error This Makefile requires Go '1.11.4' or newer; you $(if $(_prelude.go.HAVE),have '$(_prelude.go.VERSION)',do not seem to have Go))
_prelude.go.ensure = $(if $(call _prelude.go.VERSION.HAVE,1.11.4),,$(_prelude.go.error_unsupported))

# All of this funny business with locking can be ditched once we drop
# support for Go 1.11.  (When removing it, be aware that go-mod.mk
# uses `_prelude.go.*` variables).

_prelude.go.lock.unsupported = $(_prelude.go.error_unsupported)
# The second $(if) is just to make sure that any output isn't part of what we return.
_prelude.go.lock.1_11 = $(FLOCK)$(if $@, $(_prelude.go.GOPATH)/pkg/mod $(if $(shell mkdir -p $(_prelude.go.GOPATH)/pkg/mod),))
_prelude.go.lock.current =

# _prelude.go.lock is a slightly magical variable.  When evaluated as a dependency in a  
#
#     target: dependencies
#
# line, it evaluates as an "Executable" (see ./docs/conventions.md).
# When evaluated as part of a recipe, it evaluates as a command prefix
# with arguments.
_prelude.go.lock = $(_prelude.go.lock.$(strip \
    $(if $(call _prelude.go.VERSION.HAVE, 1.12beta1), current,\
    $(if $(call _prelude.go.VERSION.HAVE, 1.11.4),    1_11,\
                                                      unsupported))))
