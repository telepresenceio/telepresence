# Copyright 2019 Datawire. All rights reserved.
#
# Makefile snippet for pushing Docker images to a private in-cluster
# Docker registry.
#
## Eager inputs ##
#  - Variable: KUBECONFIG # (optional)
## Lazy inputs ##
#  - Variable: DOCKER_K8S_ENABLE_PVC ?=
## Outputs ##
#  - Target        : %.docker.tag.cluster   # tags as $(docker.LOCALHOST):31000/$(notdir $*):IMAGE-ID
#  - Target        : %.docker.push.cluster  # pushes that tag to a private in-cluster registry
## common.mk targets ##
#  - clean
#
# ## Pushing to a private in-cluster registry ##
#
#     > NOTE: On macOS, you will need to add
#     > `host.docker.internal:31000` to Docker's list of "Insecure
#     > registries" in order to push to private in-cluster registries.
#     >
#     >   Screenshot: ./docs/docker-cluster-macos-insecure-registries.png
#     >   Docs:       https://docs.docker.com/registry/insecure/#deploy-a-plain-http-registry
#     >
#     > The docs say you need to do it through the GUI, but I've heard
#     > rumors that `~/.docker/daemon.json` can be used for this.
#     > Maybe it's sensitive to your version of Docker for Desktop, or
#     > your version of macOS?
#
#    You can push to a private in-cluster registry by depending on
#    `SOMEDIR.docker.push.cluster`.  It will be known in-cluster as
#    `$$(sed 1d SOMEDIR.docker.push.cluster)`.  You will need to
#    substitute that value in your YAML (kubeapply can help with
#    this).
#
#    The private in-cluster registry is known as "127.0.0.1:31000" to
#    the cluster Nodes.
#
#    As a preliminary measure for supporting this functionality on
#    non-Kubernaut clusters, if DOCKER_K8S_ENABLE_PVC is 'true', then
#    the in-cluster registry will use a PersistentVolumeClaim (instead
#    of a hostPath) for storage.  Kubernaut does not support
#    PersistentVolumeClaims, but since Kubernaut clusters only have a
#    single Node, a hostPath is an acceptable hack there; it isn't on
#    other clusters.
#
# ## Wait, how is including this different than setting docker.mk's `docker.tag.cluster`? ##
#
#    docker.mk allows you to set `docker.tag.GROUP = EXPR` variables
#    that add %.docker.push.GROUP targets.  This docker-cluster.mk
#    snippet "essentially" sets `docker.tag.cluster = ...`, but with
#    some magic:
#
#     - The in-cluster tag-name is different than the local tag-name (if
#       $(docker.LOCALHOST) != "localhost"), so some special logic is
#       needed for the %.docker.tag.cluster target.
#
#     - The in-cluster registry needs to be set up, and port-forwarded to
#       the cluster, so some special logic is needed for the
#       %.docker.push.cluster target.
#
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_docker-cluster.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_docker-cluster.mk))kubeapply.mk

_docker.clean.groups += cluster
include $(dir $(_docker-cluster.mk))docker.mk

DOCKER_K8S_ENABLE_PVC ?=

_docker.port-forward = $(dir $(_docker-cluster.mk))docker-port-forward

# file contents:
#   line 1: image ID
#   line 2: local tag name (hash-based)
%.docker.tag.cluster: %.docker $(WRITE_DOCKERTAGFILE) FORCE
	printf '%s\n' $$(cat $<) $(docker.LOCALHOST):31000/$(notdir $*):$$(sed -n '1{ s/:/-/g; p; }' $<) | $(WRITE_DOCKERTAGFILE) $@

# file contents:
#   line 1: image ID
#   line 2: in-cluster tag name (hash-based)
%.docker.push.cluster: %.docker.tag.cluster $(KUBEAPPLY) $(FLOCK) $(KUBECONFIG)
# the FLOCK for KUBEAPPLY is to work around https://github.com/datawire/teleproxy/issues/77
	DOCKER_K8S_ENABLE_PVC=$(DOCKER_K8S_ENABLE_PVC) $(FLOCK) $(_docker.port-forward).lock $(KUBEAPPLY) -f $(dir $(_docker-cluster.mk))docker-registry.yaml
	{ \
	    trap "kill $$($(FLOCK) $(_docker.port-forward).lock sh -c 'kubectl port-forward --namespace=docker-registry $(if $(filter true,$(DOCKER_K8S_ENABLE_PVC)),statefulset,deployment)/registry 31000:5000 >$(_docker.port-forward).log 2>&1 & echo $$!')" EXIT; \
	    while ! curl -i http://localhost:31000/ 2>/dev/null; do sleep 1; done; \
	    sed 1d $< | xargs -n1 docker push; \
	}
	sed '2{ s/^[^:]*:/127.0.0.1:/; }' $< > $@

%.docker.clean.cluster:
	if [ -e $*.docker.tag.cluster ]; then docker image rm $$(cat $*.docker.tag.cluster) || true; fi
	rm -f $*.docker.tag.cluster $*.docker.push.cluster
.PHONY: %.docker.clean.cluster

# This `go run` bit is gross, compared to just depending on and using
# $(FLOCK).  But if the user runs `make clobber`, the prelude.mk
# cleanup might delete $(FLOCK) before we get to run it.
_clean-docker-cluster:
	cd $(dir $(_docker-cluster.mk))bin-go/flock && GO111MODULE=on go run . $(abspath $(_docker.port-forward).lock) rm $(abspath $(_docker.port-forward).lock)
	rm -f $(_docker.port-forward).log
	rm -f $(dir $(_docker-cluster.mk))docker-registry.yaml.o
clean: _clean-docker-cluster
.PHONY: _clean-docker-cluster

endif
