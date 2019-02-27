# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for managing pid file cleanup.
#
## Inputs ##
#  (none)
## Outputs ##
#  - .PHONY Target: %.pid.clean
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)

%.pid.clean:
	if [ -e $*.pid ]; then kill $$(cat $*.pid) || true; fi
	rm -f $*.pid
.PHONY: %.pid.clean

endif
