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
#  7. Use the kubernaut.clobber target to delete the kubernaut binary
#     itself:
#
#     clobber: kubernaut.clobber
#

.PRECIOUS: %.knaut.claim
.SECONDARY: %.knaut.claim

%.knaut.claim :
	@if [ -z $${CI+x} ]; then \
		echo $(@:%.knaut.claim=%)-$${USER} > $@; \
	else \
		echo $(@:%.knaut.claim=%)-$${USER}-$(shell uuidgen) > $@; \
	fi

KUBERNAUT=./gubernaut
KUBERNAUT_CLAIM_FILE=$(@:%.knaut=%.knaut.claim)
KUBERNAUT_CLAIM_NAME=$(shell cat $(KUBERNAUT_CLAIM_FILE))

%.knaut : %.knaut.claim $(KUBERNAUT)
	$(KUBERNAUT) -release $(KUBERNAUT_CLAIM_NAME)
	$(KUBERNAUT) -claim $(KUBERNAUT_CLAIM_NAME) -output $@

.PHONY: %.knaut.clean

%.knaut.clean : $(KUBERNAUT)
	if [ -e $(@:%.clean=%.claim) ]; then $(KUBERNAUT) -release $$(cat $(@:%.clean=%.claim)); fi
	rm -f $(@:%.knaut.clean=%.knaut)
	rm -f $(@:%.clean=%.claim)

gubernaut: cmd/gubernaut/gubernaut.go
	go build cmd/gubernaut/gubernaut.go

.PHONY: kubernaut.clobber

kubernaut.clobber:
	rm -f gubernaut gubernaut.go

