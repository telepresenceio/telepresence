# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for using the flock(1) command.
#
## Inputs ##
#  (none)
## Outputs ##
#  - Variable: FLOCK
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)

ifneq ($(shell which flock 2>/dev/null),)
FLOCK = flock
else
FLOCK := $(dir $(lastword $(MAKEFILE_LIST)))flock
endif

endif
