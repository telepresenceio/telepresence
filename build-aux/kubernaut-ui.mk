# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for providing claim/release/shell user-facing
# targets for interacting with kubernaut.
#
## Inputs ##
#  - Variable: export KUBECONFIG = $(or $(NAME),cluster).knaut
## Outputs ##
#  - .PHONY Target: claim
#  - .PHONY Target: release
#  - .PHONY Target: shell
## common.mk targets ##
#  - clean
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
include $(dir $(lastword $(MAKEFILE_LIST)))/kubernaut.mk

# We do $(or $(NAME),cluster) instead of setting NAME?=cluster, so
# that a .mk file providing automatic NAME can set `NAME?=` without
# having to force the Makefile to worry about inclusion order.
_KUBECONFIG = $(CURDIR)/$(or $(NAME),cluster).knaut
export KUBECONFIG = $(_KUBECONFIG)

claim: ## Obtain an ephemeral k8s cluster from kubernaut.io
claim: $(KUBECONFIG)
.PHONY: claim

release: ## Release the cluster claimed by 'claim'
release: $(_KUBECONFIG).clean
.PHONY: release

shell: ## Run an interactive Bash shell with KUBECONFIG= set to a Kubernaut claim
shell: $(KUBECONFIG)
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash
.PHONY: shell

clean: release

endif
