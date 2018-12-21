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
#  6. Incorporate <blah>.knaut[.clean] targets into your Makefile as
#     needed
#
#     tests: test-cluster.knaut
#             KUBECONFIG=test-cluster.knaut py.test ...
#
#     clean: test-cluster.knaut.clean
#

%.knaut.claim :
	@if [ -z $${CI+x} ]; then \
		echo $(@:%.knaut.claim=%)-$${USER} > $@; \
	else \
		echo $(@:%.knaut.claim=%)-$${USER}-$(shell uuidgen) > $@; \
	fi
.PRECIOUS: %.knaut.claim
.SECONDARY: %.knaut.claim

KUBERNAUT=go run build-aux/gubernaut.go
KUBERNAUT_CLAIM_NAME=$(shell cat $(@:%.knaut=%.knaut.claim))

%.knaut : %.knaut.claim
	$(KUBERNAUT) -release $(KUBERNAUT_CLAIM_NAME)
	$(KUBERNAUT) -claim $(KUBERNAUT_CLAIM_NAME) -output $@

%.knaut.clean :
	if [ -e $(@:%.clean=%.claim) ]; then $(KUBERNAUT) -release $$(cat $(@:%.clean=%.claim)); fi
	rm -f $(@:%.knaut.clean=%.knaut)
	rm -f $(@:%.clean=%.claim)
.PHONY: %.knaut.clean
