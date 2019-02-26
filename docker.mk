# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for building Docker images, and for pushing them to
# kubernaut.io clusters.
#
## Inputs ##
#  - Variable: VERSION
#  - Variable: DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/$(notdir $*):$(or $(VERSION),latest)
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

ifeq ($(GOOS),darwin)
docker.LOCALHOST = host.docker.internal
else
docker.LOCALHOST = localhost
endif

DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/$(notdir $*):$(or $(VERSION),latest)

_docker.port-forward = $(dir $(_docker.mk))docker-port-forward

# %.docker file contents:
#
#  line 1: local tag name (version-based)
#  line 2: image hash
#  line 3: local tag name (hash-based)
#
# Note: We test for changes for CI early, but test for changes for
# cleanup late.  If we did the cleanup test early because of :latest,
# it would leave dangling untagged images.  If we did the CI test
# late, it would remove the evidence for debugging.
%.docker: %/Dockerfile
	printf '%s\n' '$(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest)' > $(@D)/.tmp.$(@F).tmp
	docker build -t "$$(sed -n 1p $(@D)/.tmp.$(@F).tmp)" $*
	docker image inspect "$$(sed -n 1p $(@D)/.tmp.$(@F).tmp)" --format='{{.Id}}' >> $(@D)/.tmp.$(@F).tmp
	printf '%s\n' '$(docker.LOCALHOST):31000/$(notdir $*)':"id-$$(sed -n '2{ s/:/-/g; p; }' $(@D)/.tmp.$(@F).tmp)" >> $(@D)/.tmp.$(@F).tmp
	@{ \
		PS4=''; set -x; \
		if cmp -s $(@D)/.tmp.$(@F).tmp $@; then \
			rm -f $(@D)/.tmp.$(@F).tmp || true; \
		else \
			$(if $(CI),if test -e $@; then false This should not happen in CI: $@ should not change; fi &&) \
			docker tag "$$(sed -n 2p $(@D)/.tmp.$(@F).tmp)" "$$(sed -n 3p $(@D)/.tmp.$(@F).tmp)" && \
			$(if $(VERSION),docker tag "$$(sed -n 2p $(@D)/.tmp.$(@F).tmp)" '$(docker.LOCALHOST):31000/$(notdir $*):latest' &&) \
			if test -e $@; then docker image rm $$(cat $@) || true; fi && \
			mv -f $(@D)/.tmp.$(@F).tmp $@; \
		fi; \
	}

%.docker.clean:
	if [ -e $*.docker ]; then docker image rm $$(cat $*.docker) || true; fi
	rm -f $*.docker
.PHONY: %.docker.clean

# %.docker.knaut-push file contents:
#
#  line 1: in-cluster tag name (hash-based)
%.docker.knaut-push: %.docker $(KUBEAPPLY) $(KUBECONFIG)
	$(KUBEAPPLY) -f $(dir $(_docker.mk))docker-registry.yaml
	{ \
	    trap "kill $$($(FLOCK) $(_docker.port-forward).lock sh -c 'kubectl port-forward --namespace=docker-registry deployment/registry 31000:5000 >$(_docker.port-forward).log 2>&1 & echo $$!')" EXIT; \
	    while ! curl -i http://localhost:31000/ 2>/dev/null; do sleep 1; done; \
	    docker push "$$(sed -n 3p $<)"; \
	}
	sed -n '3{ s/^host\.docker\.internal:/localhost:/; p; }' $< > $@

%.docker.push: %.docker
	docker tag "$$(sed -n 2p $<)" '$(DOCKER_IMAGE)'
	docker push '$(DOCKER_IMAGE)'
.PHONY: %.docker.push

_clean-docker:
	$(FLOCK) $(_docker.port-forward).lock rm $(_docker.port-forward).lock
	rm -f $(_docker.port-forward).log
	rm -f $(dir $(_docker.mk))docker-registry.yaml.o
clean: _clean-docker
.PHONY: _clean-docker

endif
