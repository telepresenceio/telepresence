# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for calling `teleproxy`
#
## Inputs ##
#  - Variable: TELEPROXY     ?= ./build-aux/teleproxy
#  - Variable: TELEPROXY_LOG ?= ./build-aux/teleproxy.log
#  - Variable: CLUSTER
#  - Variable: KUBE_URL
## Outputs ##
#  - Target       : $(TELERPOXY)
#  - .PHONY Target: proxy
#  - .PHONY Target: unproxy
## common.mk targets ##
#  - clean
#  - clobber

TELEPROXY ?= $(dir $(lastword $(MAKEFILE_LIST)))/teleproxy
TELEPROXY_LOG ?= $(dir $(lastword $(MAKEFILE_LIST)))/teleproxy.log
TELEPROXY_VERSION = 0.3.2
KUBE_URL = https://kubernetes/api/

$(TELEPROXY):
	curl -o $(TELEPROXY) https://s3.amazonaws.com/datawire-static-files/teleproxy/$(TELEPROXY_VERSION)/$(GOOS)/$(GOARCH)/teleproxy
	sudo chown root $(TELEPROXY)
	sudo chmod go-w,a+sx $(TELEPROXY)

proxy: ## Launch teleproxy in the background
proxy: $(CLUSTER) $(TELEPROXY) unproxy
	KUBECONFIG=$(CLUSTER) $(TELEPROXY) > $(TELEPROXY_LOG) 2>&1 &
	@for i in 1 2 4 8 16 32 64 x; do \
		if [ "$$i" == "x" ]; then echo "ERROR: proxy did not come up"; exit 1; fi; \
		echo "Checking proxy: $(KUBE_URL)"; \
		if curl -sk $(KUBE_URL); then \
			echo -e "\n\nProxy UP!"; \
			break; \
		fi; \
		echo "Waiting $$i seconds..."; \
		sleep $$i; \
	done
.PHONY: proxy

unproxy: ## Shut down 'proxy'
	curl -s 127.254.254.254/api/shutdown || true
	@sleep 1
.PHONY: unproxy

clean: _clean-teleproxy
_clean-teleproxy: $(if $(wildcard $(TELEPROXY_LOG)),unproxy)
	rm -f $(TELEPROXY_LOG)
.PHONY: _clean-teleproxy

clobber: _clobber-teleproxy
_clobber-teleproxy:
	rm -f $(TELEPROXY)
.PHONY: _clobber-teleproxy
