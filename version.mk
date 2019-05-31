# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for automatically setting VERSION.
#
## Eager inputs ##
#  - Variable: CIRCLE_TAG (optional)
#  - Variable: TRAVIS_TAG (optional)
## Lazy inputs ##
#  - Variable: CI (optional)
#  - Variable: VERSION ?= …
## Outputs ##
#  - Variable: VERSION ?= …
## common.mk targets ##
#  (none)
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)

VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always))$(if $(shell git status -s),-dirty$(if $(CI),$(_version.ci_error)))

define _version.ci_error
$(warning Build is dirty:)
$(shell git add . >&2)
$(shell PAGER= git diff --cached >&2)
$(error This should not happen in CI: the build should not be dirty)
endef

ifneq ($(CIRCLE_TAG),)
  ifneq ($(patsubst v%,%,$(CIRCLE_TAG)),$(VERSION))
    $(error This should not happen: CIRCLE_TAG={$(patsubst v%,%,$(CIRCLE_TAG))} and VERSION={$(VERSION)} disagree)
  endif
endif

ifneq ($(TRAVIS_TAG),)
  ifneq ($(patsubst v%,%,$(TRAVIS_TAG)),$(VERSION))
    $(error This should not happen: TRAVIS_TAG={$(patsubst v%,%,$(TRAVIS_TAG))} and VERSION={$(VERSION)} disagree)
  endif
endif

endif
