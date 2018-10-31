## Quickstart (and pretty much everything else you need to know)
#
#  1. Put this file in your source tree and include it from your
#     Makefile, e.g.:
#
#     ...
#     include kubernaut.mk
#     ...
#
#  2. Run `make foo.knaut` to (re)acquire a cluster.
#
#  3. Use `kubectl -kubeconfig foo.knaut ...` to use a cluster.
#
#  4. Run `make foo.knaut.clean` to release the cluster.
#
#  5. If you care, the claim name is in foo.knaut.claim. This will use
#     a UUID if the CI environment variable is set.
#
#  6. Run `make foo.knaut.clobber` to discard the claim name in
#     addition to releasing the cluster.
#
#  7. Incorporate <blah>.knaut{.clean,.clobber) targets into your
#     Makefile as needed
#
#     tests: test-cluster.knaut
#             KUBECONFIG=test-cluster.knaut py.test ...
#
#     clean: test-cluster.knaut.clean
#
#
#  8. Use the kubernaut.clobber target to delete the kubernaut binary
#     itself:
#
#     clobber: test-cluster.knaut.clobber kubernaut.clobber
#

KUBERNAUT_BASE=.
KUBERNAUT_VERSION=2018.10.24-d46c1f1
KUBERNAUT=$(KUBERNAUT_BASE)/kubernaut

GOOS=$(shell go env GOOS)
GOARCH=$(shell go env GOARCH)

$(KUBERNAUT):
	mkdir -p $(shell dirname $(KUBERNAUT))
	curl -o $(KUBERNAUT) http://releases.datawire.io/kubernaut/$(KUBERNAUT_VERSION)/$(GOOS)/$(GOARCH)/kubernaut
	chmod +x $(KUBERNAUT)

.PRECIOUS: %.knaut.claim
.SECONDARY: %.knaut.claim

%.knaut.claim :
	@if [ -z $${CI+x} ]; then \
		echo $(@:%.knaut.claim=%)-$${USER} > $@; \
	else \
		echo $(@:%.knaut.claim=%)-$${USER}-$(shell uuidgen) > $@; \
	fi

KUBERNAUT_CLAIM_FILE=$(@:%.knaut=%.knaut.claim)
KUBERNAUT_CLAIM_NAME=$(shell cat $(KUBERNAUT_CLAIM_FILE))
KUBERNAUT_CLAIM=$(KUBERNAUT) claims create --name $(KUBERNAUT_CLAIM_NAME) --cluster-group main
KUBERNAUT_DISCARD=$(KUBERNAUT) claims delete $(KUBERNAUT_CLAIM_NAME)

%.knaut : %.knaut.claim $(KUBERNAUT)
	$(KUBERNAUT_DISCARD)
	$(KUBERNAUT_CLAIM)
	cp ~/.kube/$(KUBERNAUT_CLAIM_NAME).yaml $@

.PHONY: %.knaut.clean %.knaut.clobber

%.knaut.clean :
	if [ -e $(@:%.clean=%.claim) ]; then $(KUBERNAUT) claims delete $(shell cat $(@:%.clean=%.claim)); fi
	rm -f $(@:%.knaut.clean=%.knaut)

%.knaut.clobber : %.knaut.clean
	rm -f $(@:%.clobber=%.claim)

.PHONY: kubernaut.clobber

kubernaut.clobber:
	rm -f $(KUBERNAUT)
