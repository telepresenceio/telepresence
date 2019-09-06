# Copyright 2018-2019 Datawire. All rights reserved.
#
# Useful bits for writing Makefiles or Makefile snippets.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#
#  Boolean support:
#  - Variable: TRUE  = T
#  - Variable: FALSE =
#  - Function: not
#
#  String support:
#  - Variable: export NL
#  - Variable:        SPACE
#  - Function: str.eq
#
#  Unsigned integer support:
#  - Function: uint.max
#  - Function: uint.min
#  - Function: uint.eq
#  - Function: uint.ge
#  - Function: uint.le
#  - Function: uint.gt
#  - Function: uint.lt
#
#  Path support:
#  - Function: path.trimprefix
#  - Function: path.addprefix
#
#  Build tool support:
#  - Variable: export GOHOSTOS
#  - Variable: export GOHOSTARCH
#  - Variable: build-aux.dir
#  - Variable: build-aux.bindir
#  - Function: build-aux.bin-go.rule
#  - Executable: FLOCK           ?= $(CURDIR)/build-aux/bin/flock # or /usr/bin/flock
#  - Executable: WRITE_IFCHANGED ?= $(CURDIR)/build-aux/bin/write-ifchanged
#  - Executable: COPY_IFCHANGED  ?= $(CURDIR)/build-aux/bin/copy-ifchanged
#  - Executable: MOVE_IFCHANGED  ?= $(CURDIR)/build-aux/bin/move-ifchanged
#  - Executable: TAP_DRIVER      ?= $(CURDIR)/build-aux/bin/tap-driver
#
#  Other support:
#  - Function: joinlist
#  - Function: quote.shell
#  - Function: lazyonce
#  - .PHONY Target: FORCE
#
#  Internal use:
#  - Variable: _prelude.go.VERSION      (exposed as go-mod.mk:go.goversion)
#  - Variable: _prelude.go.lock         (exposed as go-mod.mk:go.lock)
#  - Variable: _prelude.go.ensure       (used by go-mod.mk)
#
## common.mk targets ##
#  - clobber
#
# `include`ing this file on its own does not introduce a dependency on
# `go`, but:
#   - Calling the `build-aux.bin-go.rule` function introduces a hard
#     dependency Go 1.11.4+.
#   - Using the $(FLOCK) executable introduces a hard dependency on Go
#     1.11+ on systems that don't have a native `flock(1)` program
#     (macOS).
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_prelude.mk := $(lastword $(MAKEFILE_LIST))

# For my own sanity with organization, I've split out several "groups"
# of functionality from this file.  Maybe that's a sign that this has
# grown too complex.  Maybe we should stop fighting it and just use
# [GMSL](https://gmsl.sourceforge.io/).  Absolutely nothing in any of
# the `prelude_*.mk` files is allowed to be eager, so ordering doesn't
# matter.  Anything eager must go in this main `prelude.mk` file.
include $(dir $(_prelude.mk))prelude_bool.mk
include $(dir $(_prelude.mk))prelude_str.mk
include $(dir $(_prelude.mk))prelude_uint.mk
include $(dir $(_prelude.mk))prelude_path.mk
include $(dir $(_prelude.mk))prelude_go.mk

#
# Functions

# Usage: $(call joinlist,SEPARATOR,LIST)
# Example: $(call joinlist,/,foo bar baz) => foo/bar/baz
joinlist=$(if $(word 2,$2),$(firstword $2)$1$(call joinlist,$1,$(wordlist 2,$(words $2),$2)),$2)

# Usage: $(call quote.shell,STRING)
# Example: $(call quote.shell,some'string"with`special characters) => "some'string\"with\`special characters"
#
# Based on
# https://git.lukeshu.com/autothing/tree/build-aux/Makefile.once.head/00-quote.mk?id=9384e763b00774603208b3d44977ed0e6762a09a
# but modified to make newlines work with shells other than Bash.
quote.shell = $(subst $(NL),'"$${NL}"','$(subst ','\'',$1)')

# Usage: VAR = $(call lazyonce,VAR,EXPR)
#
# Caches the value of EXPR (in case it's expensive/slow) once it is
# evaluated, but doesn't eager-evaluate it either.
lazyonce = $(eval $(strip $1) := $2)$2
_lazyonce.disabled = $(FALSE)

ifeq ($(MAKE_VERSION),3.81)
  define _lazyonce.print_warning
    $(warning The 'lazyonce' function is known to trigger a memory corruption bug in GNU Make 3.81)
    $(warning Disabling the 'lazyonce' function; upgrade your copy of GNU Make for faster builds)
    $(eval _lazyonce.need_warning = $(FALSE))
  endef
  _lazyonce.need_warning = $(TRUE)
  # The second $(if) is just so that the evaluated result output of
  # _lazyonce.print_warning isn't part of the returned value.
  lazyonce = $(if $(_lazyonce.need_warning),$(if $(_lazyonce.print_warning),))$2
  _lazyonce.disabled = $(TRUE)

  # These are use a lot, so go ahead and eager-evaluate them to speed
  # things up.
  _prelude.go.HAVE := $(_prelude.go.HAVE)
  _prelude.go.VERSION := $(_prelude.go.VERSION)
endif

#
# Variable constants

build-aux.dir = $(patsubst %/,%,$(dir $(_prelude.mk)))
build-aux.bindir = $(abspath $(build-aux.dir)/bin)

#
# Executables
#
# Have this section toward the end, so that it can eagerly use stuff
# defined above.

FLOCK           ?= $(call lazyonce,FLOCK,$(or $(shell which flock 2>/dev/null),$(_prelude.go.ensure)$(build-aux.bindir)/flock))
COPY_IFCHANGED  ?= $(build-aux.bindir)/copy-ifchanged
MOVE_IFCHANGED  ?= $(build-aux.bindir)/move-ifchanged
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

# Usage: $(eval $(call build-aux.bin-go.rule,BINARY_NAME,GO_PACKAGE))
define build-aux.bin-go.rule
$$(build-aux.bindir)/.$(strip $1).stamp: $$(build-aux.bindir)/.%.stamp: $$(build-aux.dir)/bin-go/%/go.mod $$(_prelude.go.lock) FORCE | $$(build-aux.bindir)
	cd $$(<D) && GO111MODULE=on $$(_prelude.go.lock)go build -o $$(abspath $$@) $2
endef
$(build-aux.bindir)/%: $(build-aux.bindir)/.%.stamp $(COPY_IFCHANGED)
	$(COPY_IFCHANGED) $< $@

# bin/.flock.stamp doesn't use build-aux.bin-go.rule, because bootstrapping
$(build-aux.bindir)/.flock.stamp: $(build-aux.bindir)/.%.stamp: $(build-aux.dir)/bin-go/%/go.mod $(shell find $(build-aux.dir)/bin-go/flock) | $(build-aux.bindir)
	cd $(<D) && GO111MODULE=on go build -o $(abspath $@) github.com/datawire/build-aux/bin-go/flock

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
