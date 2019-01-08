# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for calling `teleproxy`
#
## Inputs ##
#  - Variable: TELEPROXY     ?= ./build-aux/teleproxy
#  - Variable: TELEPROXY_LOG ?= ./build-aux/teleproxy.log
#  - Variable: KUBECONFIG
#  - Variable: KUBE_URL
## Outputs ##
#  - Target       : $(TELERPOXY)
#  - .PHONY Target: proxy
#  - .PHONY Target: unproxy
## common.mk targets ##
#  - clean
#  - clobber
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_teleproxy.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(lastword $(MAKEFILE_LIST)))common.mk

TELEPROXY ?= $(dir $(_teleproxy.mk))teleproxy
TELEPROXY_LOG ?= $(dir $(_teleproxy.mk))teleproxy.log
TELEPROXY_VERSION = 0.3.8
KUBE_URL = https://kubernetes/api/

$(TELEPROXY): $(_teleproxy.mk)
	sudo rm -f $@
	curl -o $@ https://s3.amazonaws.com/datawire-static-files/teleproxy/$(TELEPROXY_VERSION)/$(GOOS)/$(GOARCH)/teleproxy
	sudo chown root $@
	sudo chmod go-w,a+sx $@

proxy: ## Launch teleproxy in the background
proxy: $(KUBECONFIG) $(TELEPROXY) unproxy
# NB: we say KUBECONFIG=$(KUBECONFIG) here because it might not be exported
	KUBECONFIG=$(KUBECONFIG) $(TELEPROXY) > $(TELEPROXY_LOG) 2>&1 &
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

endif
