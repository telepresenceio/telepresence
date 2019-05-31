# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet to auto-generate a `make help` rule.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  - Variable: help.body ?= …
## Outputs ##
#  - .PHONY Target: help
## common.mk targets ##
#  (none)
#
## Basic Example ##
#
#     # Copyright 2018 Datawire. All rights reserved.
#
#     .DEFAULT_GOAL = help
#     include build-aux/help.mk
#
#     my-rule: ## A description of my-rule
#     my-rule: dep1 dep2
#             recipe
#     .PHONY: my-rule
#
#     my-other-rule: ## (Category) A description of my-other-rule
#     my-other-rule: dep1 dep2
#             recipe
#     .PHONY: my-other-rule
#
# The double "##" is important.  It is also important that there be no
# dependencies between the ":" and the "##"; any ammount of whitespace
# is acceptable, though.  The "##" may optionally be followed by a
# category in parenthesis.
#
## Advanced example ##
#
#     # Copyright 2018 Datawire. All rights reserved.
#
#     .DEFAULT_GOAL = help
#     include build-aux/help.mk
#
#     define help.body
#     This is a short little paragraph that goes between the "Usage:"
#     header and the "TARGETS:" footer that can give more information.
#     It can contain any kind of 'quotes' or shell
#     "meta"-`characters`.  Make $(variables) do get expanded, though.
#     endef
#
#     my-rule: ## A description of my-rule
#     my-rule: dep1 dep2
#             recipe
#     .PHONY: my-rule
#
# Because your editor's syntax-highlighting might be unhappy with
# things inside of help.body, you may prefix lines with "#" or "# ".
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
include $(dir $(lastword $(MAKEFILE_LIST)))prelude.mk

# Usage: $(call _help.genbody.line,VAR)
_help.genbody.line = $(if $(filter-out undefined,$(origin $1)),  $1 = $($1))

# Usage: $(call _help.genbody,CUR,VARSLEFT)
_help.genbody = $(if $2,$(call _help.genbody,$1$(if $(and $1,$(call _help.genbody.line,$(firstword $2))),$(NL))$(call _help.genbody.line,$(firstword $2)),$(wordlist 2,$(words $2),$2)),$1)

help.body.vars = NAME VERSION KUBECONFIG
help.body ?= $(call _help.genbody,,$(help.body.vars))

help:  ## (Common) Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@printf '%s\n' $(call quote.shell,$(help.body)) | sed -e 's/^# //' -e 's/^#//'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^#]*) *: *[#]# *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
.PHONY: help

endif
