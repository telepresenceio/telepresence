# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for running a shell with KUBECONFIG set.
#
# Depends on kubernaut.mk
#
## Inputs ##
#  - Variable: CLUSTER ?= $(NAME).knaut
## Outputs ##
#  - .PHONY Target: claim
#  - .PHONY Target: release
#  - .PHONY Target: shell

CLUSTER ?= $(NAME).knaut

claim: ## Obtain an ephemeral k8s cluster from kubernaut.io
claim: $(CLUSTER)
.PHONY: claim

release: ## Release the cluster claimed by 'claim'
release: $(CLUSTER).clean
.PHONY: release

shell: ## Run an interactive Bash shell with KUBECONFIG=$(CLUSTER) set to an Kubernaut claim
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " KUBECONFIG=$(CLUSTER) bash
.PHONY: shell
