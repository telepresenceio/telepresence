# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for providing claim/unclaim/shell user-facing
# targets for interacting with kubernaut.
#
## Eager inputs ##
#  - Variable: export KUBECONFIG := $(or $(NAME),cluster).knaut
## Lazy inputs ##
#  (none)
## Outputs ##
#  - .PHONY Target: claim
#  - .PHONY Target: unclaim
#  - .PHONY Target: shell
#  - .PHONY Target: status-cluster
#  - Variable: export KUBECONFIG := $(or $(NAME),cluster).knaut
## common.mk targets ##
#  - clean
#
# `kubernaut-ui.mk` adds the `claim`, `unclaim`, `shell`, and
# `status-cluster` user-facing Makefile targets
#
# Makefile rules that need to talk to the cluster should declare a
# dependency on `$(KUBECONFIG)`
#
# `kubernaut-ui.mk` sets `KUBECONFIG`.  The user can override this to
# point to an existing cluster (instead of a Kubernaut cluster), but
# NOT with an environment variable.  To override it, you must set it
# with a `make` argument: `make KUBECONFIG=...`
#
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_kubernaut-ui.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_kubernaut-ui.mk))kubernaut.mk

# We have a separate private _kubernaut-ui.KUBECONFIG that the user
# can't override, in order to ensure that `make unclaim` never deletes
# a non-Kubernaut-generated KUBECONFIG file.
_kubernaut-ui.KUBECONFIG := $(abspath $(dir $(_kubernaut-ui.mk))$(or $(NAME),cluster).knaut)
export KUBECONFIG = $(_kubernaut-ui.KUBECONFIG)

claim: ## (Kubernaut) Obtain an ephemeral cluster from kubernaut.io
claim: $(KUBECONFIG)
.PHONY: claim

unclaim: ## (Kubernaut) Destroy the cluster
unclaim: $(_kubernaut-ui.KUBECONFIG).clean
.PHONY: unclaim

shell: ## (Kubernaut) Run an interactive Bash shell with KUBECONFIG= set to the Kubernaut claim
shell: $(KUBECONFIG)
	+exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash
.PHONY: shell

status-cluster: ## (Kubernaut) Fail if the cluster is not reachable or not claimed
	@if [ -e $(KUBECONFIG) ] ; then \
		if kubectl --request-timeout=1 get pods connectivity-check --ignore-not-found; then \
			echo "Cluster okay!"; \
		else \
			echo "Cluster claimed but connectivity check failed."; \
			exit 1; \
		fi \
	else \
		echo "Cluster not claimed."; \
		exit 1; \
	fi
.PHONY: status-cluster

clean: unclaim

endif
