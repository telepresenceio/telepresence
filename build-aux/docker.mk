# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for building Docker images, and for pushing them to
# kubernaut.io clusters.
#
## Inputs ##
#  - Variable: VERSION
#  - Variable: DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/$(notdir $*):$(VERSION)
## Outputs ##
#  - Target        : %.docker: %/Dockerfile  # tags image as localhost:31000/$(notdir $*):$(VERSION)
#  - .PHONY Target : %.docker.clean
#  - Target        : %.docker.knaut-push     # pushes to private in-kubernaut-cluster registry
#  - .PHONY Target : %.docker.push           # pushes to $(DOCKER_REGISTRY)
## common.mk targets ##
#  - clean
#
# The private in-kubernaut-cluster registry is known as
# "localhost:31000" to the cluster Nodes.
#
# DOCKER_IMAGE defines the mapping to publicly the pushed image
# name/tag, it is evaluated in the context of "%.docker.push"
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_docker.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_docker.mk))flock.mk
include $(dir $(_docker.mk))kubeapply.mk
include $(dir $(_docker.mk))kubernaut-ui.mk
include $(dir $(_docker.mk))version.mk

ifeq ($(GOOS),darwin)
docker.LOCALHOST = host.docker.internal
else
docker.LOCALHOST = localhost
endif

DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/$(notdir $*):$(VERSION)

_docker.port-forward = $(dir $(_docker.mk))docker-port-forward

%.docker: %/Dockerfile
	docker build -t $(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest) $*
ifneq ($(CI),)
	docker image inspect $(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest) --format='{{.Id}}' > $(@D)/.tmp.$(@F).tmp
	if test -e $@; then cmp -s $(@D)/.tmp.$(@F).tmp $@; fi
	rm -f $(@D)/.tmp.$(@F).tmp
endif
	docker image inspect $(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest) --format='{{.Id}}' > $@

%.docker.clean:
	if [ -e $*.docker ]; then docker image rm $$(cat $*.docker); fi
	rm -f $*.docker
.PHONY: %.docker.clean

%.docker.knaut-push: %.docker $(KUBEAPPLY) $(KUBECONFIG)
	$(KUBEAPPLY) -f $(dir $(_docker.mk))docker-registry.yaml
	{ \
	    $(FLOCK) $(_docker.port-forward).lock kubectl port-forward --namespace=docker-registry deployment/registry 31000:5000 >$(_docker.port-forward).log 2>&1 & \
	    trap "kill $$!; wait" EXIT; \
	    while ! curl -i http://localhost:31000/ 2>/dev/null; do sleep 1; done; \
	    docker push $(docker.LOCALHOST):31000/$(notdir $*):$(VERSION); \
	}
	echo localhost:31000/$(notdir $*):$(VERSION) > $@

%.docker.push: %.docker
	docker tag $$(cat $<) $(DOCKER_IMAGE)
	docker push $(DOCKER_IMAGE)
.PHONY: %.docker.push

_clean-docker:
	$(FLOCK) $(_docker.port-forward).lock rm $(_docker.port-forward).lock
	rm -f $(_docker.port-forward).log
clean: _clean-docker
.PHONY: _clean-docker

endif
