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

VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always))$(if $(shell git status -s),-dirty$(_version.dirty_hash))

_version.dirty_hash = $(shell GO111MODULE=off go run $(dir $(_version.mk))version.go)

endif
