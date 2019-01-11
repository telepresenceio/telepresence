# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for building Docker images, and for pushing them to
# kubernaut.io clusters.
#
## Inputs ##
#  - Variable: VERSION
#  - Variable: DOCKER_REGISTRY
## Outputs ##
#  - Target        : %.docker: %/Dockerfile  # tags image as localhost:31000/$(notdir $*):$(VERSION)
#  - .PHONY Target : %.docker.clean
#  - .PHONY Target : %.docker.knaut-push     # pushes to private in-kubernaut-cluster registry
#  - .PHONY Target : %.docker.push           # pushes to $(DOCKER_REGISTRY)
#
# The private in-kubernaut-cluster registry is known as
# "localhost:31000" to the cluster Nodes.
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_docker.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_docker.mk))kubeapply.mk
include $(dir $(_docker.mk))kubernaut-ui.mk
include $(dir $(_docker.mk))version.mk

ifeq ($(GOOS),darwin)
docker.LOCALHOST = host.docker.internal
else
docker.LOCALHOST = localhost
endif

%.docker: %/Dockerfile
	docker build -t $(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest) $*
	docker image inspect $(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest) --format='{{.Id}}' > $@

%.docker.clean:
	if [ -e $*.docker ]; then docker image rm $$(cat $*.docker); fi
	rm -f $*.docker
.PHONY: %.docker.clean

%.docker.knaut-push: %.docker $(KUBEAPPLY) $(KUBECONFIG)
	$(KUBEAPPLY) -f $(dir $(_docker.mk))docker-registry.yaml
	{ \
	    kubectl port-forward --namespace=docker-registry deployment/registry 31000:5000 & \
	    trap "kill $$!; wait" EXIT; \
	    while ! curl -i http://localhost:31000/ 2>/dev/null; do sleep 1; done; \
	    docker push $(docker.LOCALHOST):31000/$(notdir $*):$(VERSION); \
	}
.PHONY: %.docker.knaut-push

%.docker.push: %.docker
	docker tag $$(cat $<) $(DOCKER_REGISTRY)/$(notdir $*):$(VERSION)
	docker push $(DOCKER_REGISTRY)/$(notdir $*):$(VERSION)
.PHONY: %.docker.push

endif
