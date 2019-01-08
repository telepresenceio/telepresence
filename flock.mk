# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for using the flock(1) command.
#
## Inputs ##
#  (none)
## Outputs ##
#  - Variable: FLOCK
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)

ifneq ($(shell which flock &>/dev/null),)
FLOCK = flock
else
FLOCK = GO111MODULE=off go run $(dir $(lastword $(MAKEFILE_LIST)))/flock.go
endif

endif
