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
_kubeapply.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(lastword $(MAKEFILE_LIST)))common.mk

KUBEAPPLY ?= $(dir $(_kubeapply.mk))kubeapply
KUBEAPPLY_VERSION = 0.3.11

$(KUBEAPPLY): $(_kubeapply.mk)
	curl -o $@ https://s3.amazonaws.com/datawire-static-files/kubeapply/$(KUBEAPPLY_VERSION)/$(GOOS)/$(GOARCH)/kubeapply
	chmod go-w,a+x $@

clobber: _clobber-kubeapply
_clobber-kubeapply:
	rm -f $(KUBEAPPLY)
.PHONY: _clobber-kubeapply

endif
