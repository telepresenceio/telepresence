# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for building Docker images, and for pushing them to
# kubernaut.io clusters.
#
## Eager inputs ##
#  - Variable: KUBECONFIG (optional)
## Lazy inputs ##
#  - Variable: VERSION (optional)
#  - Variable: DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/$(notdir $*):$(or $(VERSION),latest)
#  - Variable: DOCKER_K8S_ENABLE_PVC ?=
## Outputs ##
#  - Variable      : HAVE_DOCKER             # non-empty if true, empty if false
#  - Target        : %.docker: %/Dockerfile  # tags image as localhost:31000/$(notdir $*):$(VERSION)
#  - .PHONY Target : %.docker.clean
#  - Target        : %.docker.knaut-push     # pushes to private in-kubernaut-cluster registry
#  - .PHONY Target : %.docker.push           # pushes to $(DOCKER_REGISTRY)
## common.mk targets ##
#  - clean
#
# Note: `docker.mk` depends on `kubernaut-ui.mk`.  See the
# documentation there for more info.
#
# ## Local docker build ##
#
#    `Dockerfile`s must be in sub-directories; it doesn't support
#    having a `Dockerfile` in the root.
#
#    You can build `somedir/Dockefile` by depending on
#    `somedir.docker`.  The TL;DR of naming is that the image will be
#    named `localhost:31000/$(notdir $*):latest`, where `$*=somedir`,
#    but you should use `$$(sed -n 2p somedir.docker)` to robustly
#    refer to it in your Makefile rules.  The nuance is:
#     - It will be `host.docker.internal:31000` instead of
#       `localhost:31000` on macOS.  You can use $(docker.LOCALHOST)
#       to portably refer `host.docker.internal` on macOS, and
#       `localhost` on everything else.
#     - It actually gets tagged as 2 or 3 different things:
#        * `$(docker.LOCALHOST):31000/$(notdir $*):latest`
#        * `$(docker.LOCALHOST):31000/$(notdir $*):$(VERSION)` (if `$(VERSION)` is set))
#        * `$(docker.LOCALHOST):31000/$(notdir $*):id-sha256-...`
#     - Inside of your local Makefile rules, you should refer to by
#       its ID hash using `$$(sed -n 2p somedir.docker)`.
#
#    If you need something to be done before the `docker build`, make
#    it a dependency of `somedir.docker`.
#
#    You can untag an image by having your `clean` target depend on
#    `dir.docker.clean`
#
# ## Pushing to a private Kubernaut registry ##
#
#     > NOTE: On macOS, you will need to add
#     > host.docker.internal:31000` to Docker's list of "Insecure
#     > registries" in order to push to kubernaut.io clusters.  Ask
#     > Abhay how to do that.
#
#    You can push to kubernaut by depending on
#    `somedir.docker.knaut-push`.  It will be known in-cluster as
#    `$$(cat somedir.docker.knaut-push)`.  You will need to substitute
#    that value in your YAML (kubeapply can help with this).
#
#    The private in-kubernaut-cluster registry is known as
#    "localhost:31000" to the cluster Nodes.
#
#    As a preliminary measure for supporting this functionality on
#    non-Kubernaut clusters, if DOCKER_K8S_ENABLE_PVC is 'true', then
#    the in-cluster registry will use a PersistentVolumeClaim (instead
#    of a hostPath) for storage.  Kubernaut does not support
#    PersistentVolumeClaims, but since Kubernaut clusters only have a
#    single Node, a hostPath is an acceptable hack there; it isn't on
#    other clusters.
#
# ## Pushing to a public registry ##
#
#    You can push to the public `$(DOCKER_REGISTRY)` by depending on
#    `somedir.docker.push`.  By default, it will have the same name as
#    the local tag (sans the `localhost:31000` prefix).
#
#    You can customize the public name by adjusting the `DOCKER_IMAGE`
#    variable, which defines a mapping to publicly the pushed image
#    name/tag, it is evaluated in the context of "%.docker.push".
#
#    Example:
#        In order to to push the Ambassador Pro sidecar as
#        `ambassador_pro:amb-sidecar-$(VERSION)` instead the the
#        default `amb-sidecar:$(VERSION)`, the apro Makefile sets
#
#            DOCKER_IMAGE = $(DOCKER_REGISTRY)/ambassador_pro:$(notdir $*)-$(VERSION)
#
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_docker.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_docker.mk))prelude.mk
include $(dir $(_docker.mk))kubeapply.mk

ifeq ($(GOHOSTOS),darwin)
docker.LOCALHOST = host.docker.internal
else
docker.LOCALHOST = localhost
endif

DOCKER_IMAGE ?= $(DOCKER_REGISTRY)/$(notdir $*):$(or $(VERSION),latest)
DOCKER_K8S_ENABLE_PVC ?=

HAVE_DOCKER = $(call lazyonce,HAVE_DOCKER,$(shell which docker 2>/dev/null))

_docker.port-forward = $(dir $(_docker.mk))docker-port-forward

# %.docker file contents:
#
#  line 1: local tag name (version-based)
#  line 2: image hash
#  line 3: local tag name (hash-based)
#  line 4: local tag name (latest, optional)
#
# Note: We test for changes for CI early, but test for changes for
# cleanup late.  If we did the cleanup test early because of :latest,
# it would leave dangling untagged images.  If we did the CI test
# late, it would remove the evidence for debugging.
%.docker: %/Dockerfile
	printf '%s\n' '$(docker.LOCALHOST):31000/$(notdir $*):$(or $(VERSION),latest)' > $(@D)/.tmp.$(@F).tmp
	docker build -t "$$(sed -n 1p $(@D)/.tmp.$(@F).tmp)" $*
	docker image inspect "$$(sed -n 1p $(@D)/.tmp.$(@F).tmp)" --format='{{.Id}}' >> $(@D)/.tmp.$(@F).tmp
	printf '%s\n' '$(docker.LOCALHOST):31000/$(notdir $*)':"id-$$(sed -n '2{ s/:/-/g; p; }' $(@D)/.tmp.$(@F).tmp)" $(if $(VERSION),'$(docker.LOCALHOST):31000/$(notdir $*):latest') >> $(@D)/.tmp.$(@F).tmp
	@{ \
		PS4=''; set -x; \
		if cmp -s $(@D)/.tmp.$(@F).tmp $@; then \
			rm -f $(@D)/.tmp.$(@F).tmp || true; \
		else \
			$(if $(CI),if test -e $@; then false This should not happen in CI: $@ should not change; fi &&) \
			                docker tag "$$(sed -n 2p $(@D)/.tmp.$(@F).tmp)" "$$(sed -n 3p $(@D)/.tmp.$(@F).tmp)" && \
			$(if $(VERSION),docker tag "$$(sed -n 2p $(@D)/.tmp.$(@F).tmp)" "$$(sed -n 4p $(@D)/.tmp.$(@F).tmp)" &&) \
			if test -e $@; then docker image rm $$(grep -vFx -f $(@D)/.tmp.$(@F).tmp $@) || true; fi && \
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
	DOCKER_K8S_ENABLE_PVC=$(DOCKER_K8S_ENABLE_PVC) $(KUBEAPPLY) -f $(dir $(_docker.mk))docker-registry.yaml
	{ \
	    trap "kill $$($(FLOCK) $(_docker.port-forward).lock sh -c 'kubectl port-forward --namespace=docker-registry $(if $(filter true,$(DOCKER_K8S_ENALBE_PVC)),statefulset,deployment)/registry 31000:5000 >$(_docker.port-forward).log 2>&1 & echo $$!')" EXIT; \
	    while ! curl -i http://localhost:31000/ 2>/dev/null; do sleep 1; done; \
	    docker push "$$(sed -n 3p $<)"; \
	}
	sed -n '3{ s/^[^:]*:/127.0.0.1:/; p; }' $< > $@
.NOTPARALLEL: # work around https://github.com/datawire/teleproxy/issues/77

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
