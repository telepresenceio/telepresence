# Copyright 2019 Datawire. All rights reserved.
#
# Magic for depending on the value of a variable.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Variable: var. = build-aux/.var.
#  - Targets: $(var.)%
## common.mk targets ##
#  - clobber
#
# To have a target depend on the variable FOO, just depend on "$(var.)FOO".  For example:
#
#     foo.o: foo.c $(var.)FOOFLAGS
#         $(CC) $(FOOFLAGS) -o $@ $<
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_var.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_var.mk))prelude.mk

var. = $(dir $(_var.mk)).var.
$(var.)%: FORCE $(WRITE_IFCHANGED)
	@printf '%s' $(call quote.shell,$($*)) | $(WRITE_IFCHANGED) $@

clobber: _clobber-var
_clobber-var:
	rm -f $(var.)*
.PHONY: _clobber-var

endif
