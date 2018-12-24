# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for installing `kubeapply`
#
## Inputs ##
#  - Variable: KUBEAPPLY ?= ./build-aux/kubeapply
## Outputs ##
#  - Target: $(KUBEAPPLY)
## common.mk targets ##
#  - clobber
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)

KUBEAPPLY ?= $(dir $(lastword $(MAKEFILE_LIST)))/kubeapply
KUBEAPPLY_VERSION=0.3.5

include $(dir $(lastword $(MAKEFILE_LIST)))/common.mk

$(KUBEAPPLY):
	curl -o $(KUBEAPPLY) https://s3.amazonaws.com/datawire-static-files/kubeapply/$(KUBEAPPLY_VERSION)/$(GOOS)/$(GOARCH)/kubeapply
	chmod go-w,a+x $(KUBEAPPLY)

clobber: _clobber-kubeapply
_clobber-kubeapply:
	rm -f $(KUBEAPPLY)
.PHONY: _clobber-kubeapply

endif
