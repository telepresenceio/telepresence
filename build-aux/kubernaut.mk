# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for managing kubernaut.io clusters.
#
## Eager inputs ##
#  (none)
## Lazy inputs ##
#  (none)
## Outputs ##
#  - Target       : `%.knaut`
#  - .PHONY Target: `%.knaut.clean`
## common.mk targets ##
#  - clean
#
# Creating the NAME.knaut creates the Kubernaut claim.  The file may
# be used as a KUBECONFIG file.
#
# Calling the NAME.knaut.clean file releases the claim, and removes
# the NAME.knaut file.
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
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_kubernaut.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_kubernaut.mk))prelude.mk

GUBERNAUT ?= $(build-aux.bindir)/gubernaut
$(eval $(call build-aux.bin-go.rule, gubernaut, github.com/datawire/build-aux/bin-go/gubernaut))

%.knaut.claim:
	echo $(*F)-$${USER}-$$(uuidgen) > $@
%.knaut: %.knaut.claim $(GUBERNAUT)
	$(GUBERNAUT) -release $$(cat $<)
	$(GUBERNAUT) -claim $$(cat $<) -output $@

# This `go run` bit is gross, compared to just depending on and using
# $(GUBERNAUT).  But if the user runs `make clobber`, the prelude.mk
# cleanup might delete $(GUBERNAUT) before we get to run it.
%.knaut.clean:
	if [ -e $*.knaut.claim ]; then cd $(dir $(_kubernaut.mk))bin-go/gubernaut && GO111MODULE=on go run . -release $$(cat $(abspath $*.knaut.claim)); fi
	rm -f $*.knaut $*.knaut.claim
.PHONY: %.knaut.clean

clean: $(addsuffix .clean,$(wildcard *.knaut) $(wildcard $(build-aux.dir)/*.knaut))

endif
