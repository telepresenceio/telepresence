# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet to auto-generate a `make help` rule.
#
## Inputs: ##
#   - Variable: help.body ?=
## Outputs: ##
#   - .PHONY Target: help
#
## Basic Example: ##
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
# The double "##" is important.  It is also important that there be no
# dependencies between the ":" and the "##"; any ammount of whitespace
# is acceptable, though.
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

help.body ?=

help:  ## Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@printf '%s\n' '$(call _help.escape_quotes,$(help.body))'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^#]*) *: *[#]# */\1	/p' ${MAKEFILE_LIST} | column -t -s '	' | sed 's/^/  /'
.PHONY: help

# I put this as the last line in the file because it confuses Emacs
# syntax highlighting and makes the remainder of the file difficult to
# edit.
_help.escape_squotes = $(subst ','\'',$1)
