# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for building Docker images, and for pushing them to
# kubernaut.io clusters.
#
## Inputs ##
#  - Variable: K8S_ENVS ?=
#  - Variable: K8S_DIRS ?= k8s
#  - Variable: K8S_IMAGES ?=
## Outputs ##
#  - .PHONY Target: push
#  - .PHONY Target: apply
#  - .PHONY Target: deploy
## common.mk targets ##
#  - build
#  - clean
#
# Each IMAGE in $(K8S_IMAGES) is a path to a directory containing a
# Dockerfile; each of $(addsuffix /Dockerfile,$(K8S_IMAGES)) should
# exist.
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
include $(dir $(lastword $(MAKEFILE_LIST)))docker.mk

K8S_IMAGES ?=
K8S_ENVS ?=
K8S_DIRS ?= k8s

ifneq ($(shell which docker 2>/dev/null),)
build: $(addsuffix .docker,$(K8S_IMAGES))
else
build: _build-k8s
_build-k8s:
	@echo 'SKIPPING DOCKER BUILD'
.PHONY: _build-k8s
endif
clean: $(addsuffix .docker.clean,$(K8S_IMAGES))

push: ## (Kubernaut) Push Docker images to kubernaut.io cluster
push: $(addsuffix .docker.knaut-push,$(K8S_IMAGES))
.PHONY: push

apply:  ## (Kubernaut) Apply YAML to kubernaut.io cluster, without pushing newer Docker images (this is useful for quickly deploying YAML-only changes)
deploy: ## (Kubernaut) Apply YAML to kubernaut.io cluster, pushing newer Docker images
_k8s.push = $(addsuffix .docker.knaut-push,$(K8S_IMAGES))
apply: $(filter-out $(wildcard $(_k8s.push)),$(_k8s.push))
deploy: $(_k8s.push)
apply deploy: $(KUBECONFIG) $(KUBEAPPLY) $(K8S_ENVS)
	$(if $(K8S_ENVS),set -a && $(foreach k8s_env,$(abspath $(K8S_ENVS)), . $(k8s_env) && ))$(KUBEAPPLY) $(addprefix -f ,$(K8S_DIRS))
.PHONY: apply deploy

$(KUBECONFIG).clean: _clean-k8s
_clean-k8s:
	rm -f $(addsuffix .docker.knaut-push,$(K8S_IMAGES))
.PHONY: _clean-k8s

endif
