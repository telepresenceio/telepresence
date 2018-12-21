# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for managing kubernaut.io clusters.
#
## Inputs ##
#   - Variable: GUBERNAUT ?= gubernaut
## Outputs ##
#   - Target       : `%.knaut`
#   - .PHONY Target: `%.knaut.clean`
## common.mk targets ##
#   - clobber
#
# Creating the NAME.knaut creates the Kubernaut claim.  The file may
# be used as a KUBECONFIG file.
#
# Calling the NAME.knaut.clean file releases the claim, and removes
# the NAME.knaut file.
#
# The GUBERNAUT variable may be used to adjust the gubernaut command
# called; by default it looks up 'gubernaut' in $PATH.
#
## Quickstart ##
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

GUBERNAUT = GO111MODULE=off go run build-aux/gubernaut.go

%.knaut.claim:
	echo $(subst /,_,$*)-$${USER}-$$(uuidgen) > $@
%.knaut: %.knaut.claim
	$(GUBERNAUT) -release $$(cat $<)
	$(GUBERNAUT) -claim $$(cat $<) -output $@

%.knaut.clean:
	if [ -e $*.claim ]; then $(GUBERNAUT) -release $$(cat $*.claim); fi
	rm -f $*.knaut $*.knaut.claim
.PHONY: %.knaut.clean

clobber: $(addsuffix .clean,$(wildcard *.knaut))
