# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for installing `kubeapply`
#
## Eager inputs ##
#  - Variable: KUBEAPPLY ?= ./build-aux/kubeapply
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Variable: KUBEAPPLY ?= ./build-aux/kubeapply
#  - Target: $(KUBEAPPLY)
## common.mk targets ##
#  - clobber
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_kubeapply.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(lastword $(MAKEFILE_LIST)))prelude.mk

KUBEAPPLY ?= $(dir $(_kubeapply.mk))kubeapply
KUBEAPPLY_VERSION = 0.3.11

$(KUBEAPPLY): $(_kubeapply.mk)
	curl -o $@ --fail https://s3.amazonaws.com/datawire-static-files/kubeapply/$(KUBEAPPLY_VERSION)/$(GOHOSTOS)/$(GOHOSTARCH)/kubeapply
	chmod go-w,a+x $@

clobber: _clobber-kubeapply
_clobber-kubeapply:
	rm -f $(KUBEAPPLY)
.PHONY: _clobber-kubeapply

endif
