# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for automatically including the version number in
# Go executables.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  - Variable: VERSION ?= … (via version.mk)
## Outputs ##
#  - Variable: go.LDFLAGS += …$(VERSION)
## common.mk targets ##
#  (none)
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
include $(dir $(lastword $(MAKEFILE_LIST)))version.mk

go.LDFLAGS += -X main.Version=$(VERSION)

endif
