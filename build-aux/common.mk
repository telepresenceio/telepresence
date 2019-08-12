# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for bits common bits we "always" want.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Variable: GOOS
#  - Variable: GOARCH
#  - .PHONY Target: all
#  - .PHONY Target: build
#  - .PHONY Target: check
#  - .PHONY Target: lint
#  - .PHONY Target: format
#  - .PHONY Target: clean
#  - .PHONY Target: clobber
## common.mk targets ##
#  (N/A)
#
# Dependencies of `clobber` MUST NOT depend on programs in
# `$(build-aux.bindir)/`.
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_common.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_common.mk))prelude.mk

#
# Variables

# If $@ is bin_GOOS_GOARCH/BINNAME, set GOOS and GOARCH accodingly,
# otherwise inherit from the environment.
#
# Possible values of GOOS/GOARCH:
# https://golang.org/doc/install/source#environment
_GOOS   = $(call lazyonce,_GOOS,$(shell go env GOOS))
_GOARCH = $(call lazyonce,_GOARCH,$(shell go env GOARCH))
export GOOS   = $(if $(filter bin_%,$(@D)),$(word 2,$(subst _, ,$(@D))),$(_GOOS))
export GOARCH = $(if $(filter bin_%,$(@D)),$(word 3,$(subst _, ,$(@D))),$(_GOARCH))

#
# User-facing targets

# To the extent reasonable, use target names that agree with the GNU
# standards.
#
# https://www.gnu.org/prep/standards/standards.html#Makefile-Conventions

all: build
.PHONY: all

build: ## (Common) Build the software
.PHONY: build

check: ## (Common) Check whether the software works; run the tests
.PHONY: check

lint: ## (Common) Perform static analysis of the software
.PHONY: lint

format: ## (Common) Apply automatic formatting+cleanup to source code
.PHONY: format

clean: ## (Common) Delete all files that are normally created by building the software
.PHONY: clean
# XXX: Rename this to maintainer-clean, per GNU?
clobber: ## (Common) Delete all files that this Makefile can re-generate
clobber: clean
.PHONY: clobber

#
# Targets: Default behavior

clean: _common_clean
_common_clean:
	rm -rf -- bin_*
	rm -f test-suite.tap
.PHONY: _common_clean

check: lint build
	$(MAKE) test-suite.tap.summary
test-suite.tap: $(TAP_DRIVER)
	@$(TAP_DRIVER) cat $(sort $(filter %.tap,$^)) > $@

%.tap.summary: %.tap $(TAP_DRIVER)
	@$(TAP_DRIVER) summarize $<

%.tap: %.tap.gen $(TAP_DRIVER) FORCE
	@$(abspath $<) 2>&1 | tee $@ | $(TAP_DRIVER) stream -n $<
%.log: %.test FORCE
	@$(abspath $<) >$@ 2>&1; echo :exit-status: $$? >>$@
%.tap: %.log %.test $(TAP_DRIVER)
	@{ \
		printf '%s\n' 'TAP version 13' '1..1' && \
		sed 's/^/#/' < $< && \
		sed -n '$${ s/^:exit-status: 0$$/ok 1/; s/^:exit-status: 77$$/ok 1 # SKIP/; s/^:exit-status: .*/not ok 1/; p; }' < $<; \
	} | tee $@ | $(TAP_DRIVER) stream -n $*.test

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

endif
