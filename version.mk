# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for automatically setting VERSION.
#
## Inputs ##
#  (none)
## Outputs ##
#  - Variable: VERSION
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_version.mk := $(lastword $(MAKEFILE_LIST))

VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always))$(if $(shell git status -s),-dirty$(if $(CI),$(_version.ci_error)))

define _version.ci_error
$(warning Build is dirty:)
$(shell git add . >&2)
$(shell PAGER= git diff --cached >&2)
$(error This should not happen in CI: the build should not be dirty)
endef

endif
