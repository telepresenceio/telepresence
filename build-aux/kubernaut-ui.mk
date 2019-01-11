# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for providing claim/unclaim/shell user-facing
# targets for interacting with kubernaut.
#
## Inputs ##
#  - Variable: export KUBECONFIG := $(or $(NAME),cluster).knaut
## Outputs ##
#  - .PHONY Target: claim
#  - .PHONY Target: unclaim
#  - .PHONY Target: shell
## common.mk targets ##
#  - clean
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
include $(dir $(lastword $(MAKEFILE_LIST)))kubernaut.mk

_KUBECONFIG := build-aux/$(or $(NAME),cluster).knaut
export KUBECONFIG = $(_KUBECONFIG)

claim: ## (Kubernaut) Obtain an ephemeral k8s cluster from kubernaut.io
claim: $(KUBECONFIG)
.PHONY: claim

unclaim: ## (Kubernaut) Release the cluster claimed by 'claim'
unclaim: $(_KUBECONFIG).clean
.PHONY: unclaim

shell: ## (Kubernaut) Run an interactive Bash shell with KUBECONFIG= set to a Kubernaut claim
shell: $(KUBECONFIG)
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash
.PHONY: shell

clean: unclaim

endif
