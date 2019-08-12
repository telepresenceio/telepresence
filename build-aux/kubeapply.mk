# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for installing `kubeapply`
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Executable: KUBEAPPLY ?= $(CURDIR)/build-aux/bin/kubeapply
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_kubeapply.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_kubeapply.mk))prelude.mk

KUBEAPPLY ?= $(build-aux.bindir)/kubeapply
$(build-aux.bindir)/kubeapply: $(build-aux.dir)/go.mod $(_prelude.go.lock) | $(build-aux.bindir)
	$(build-aux.go-build) -o $@ github.com/datawire/teleproxy/cmd/kubeapply

clean: _clean-kubeapply
_clean-kubeapply:
# Files made by older versions.  Remove the tail of this list when the
# commit making the change gets far enough in to the past.
#
# 2018-07-01
	rm -f $(dir $(_kubeapply.mk))kubeapply
.PHONY: _clean-kubeapply

endif
