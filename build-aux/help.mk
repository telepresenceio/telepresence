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

# If a target doesn't specify a category name, then we use $(NAME) as
# the category name.  We used to use "-", but the en_US.UTF-8 locale
# ignores non-letter characters.  So showing "-" in the category
# column for non-categorized targets meant that `sort` would be
# looking at the second column for those rows, which would,
# potentially split the non-categorized targets:
#
#    TARGETS:
#      -            check-e2e        Check: oauth e2e tests
#      -            check-intercept  Check: apictl traffic intercept
#      (Common)     build            Build the software
#      (Common)     check            Check whether the software works; run the tests
#      (Common)     clean            Delete all files that are normally created by building the software
#      (Common)     clobber          Delete all files that this Makefile can re-generate
#      (Common)     format           Apply automatic formatting+cleanup to source code
#      (Common)     help             Show this message
#      (Common)     lint             Perform static analysis of the software
#      (Go)         go-fmt           Fixup the code with `go fmt`
#      (Go)         go-get           Download Go dependencies
#      (Go)         go-lint          Check the code with `golangci-lint`
#      (Go)         go-test          Check the code with `go test`
#      (Kubernaut)  apply            Apply YAML to the cluster, WITHOUT pushing newer Docker images
#      (Kubernaut)  claim            Obtain an ephemeral cluster from kubernaut.io
#      (Kubernaut)  deploy           Apply YAML to the cluster, pushing newer Docker images
#      (Kubernaut)  proxy            Launch teleproxy in the background
#      (Kubernaut)  push             Push Docker images to the cluster
#      (Kubernaut)  shell            Run an interactive Bash shell with KUBECONFIG= set to the Kubernaut claim
#      (Kubernaut)  unclaim          Destroy the cluster
#      (Kubernaut)  unproxy          Shut down 'proxy'
#      -            release-bin      Upload binaries to S3
#      -            release          Cut a release; upload binaries to S3 and Docker images to Quay
#      -            release-docker   Upload Docker images to Quay
#
# Using $(NAME) (falling back to "this project", since `help.mk`
# doesn't assume you set NAME) as the default category name solves
# this, and makes it clear what no-category means (since all
# build-aux.git targets now declare a category).

help:  ## (Common) Show this message
	@echo 'Usage: make [TARGETS...]'
	@echo
	@printf '%s\n' $(call quote.shell,$(help.body)) | sed -e 's/^# //' -e 's/^#//'
	@echo
	@echo TARGETS:
	@sed -En 's/^([^#]*) *: *[#]# *(\([^)]*\))? */\2	\1	/p' $(sort $(abspath $(MAKEFILE_LIST))) | sed 's/^	/($(or $(NAME),this project))&/' | column -t -s '	' | sed 's/^/  /' | sort
.PHONY: help

endif
