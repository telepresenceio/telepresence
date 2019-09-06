# Copyright 2018 Datawire. All rights reserved.
#
# Makefile snippet for calling `teleproxy`
#
## Eager inputs ##
#  - Variable: KUBECONFIG
#  - Variable: TELEPROXY_LOG ?= ./build-aux/teleproxy.log
## Lazy inputs ##
#  - Variable: KUBE_URL
## Outputs ##
#  - Executable: TELEPROXY ?= $(CURDIR)/build-aux/bin/teleproxy
#  - Variable: TELEPROXY_LOG ?= ./build-aux/teleproxy.log
#  - .PHONY Target: proxy
#  - .PHONY Target: unproxy
#  - .PHONY Target: status-proxy
## common.mk targets ##
#  - clean
## kubernaut-ui.mk targets ##
#  - $(KUBECONFIG).clean
ifeq ($(words $(filter $(abspath $(lastword $(MAKEFILE_LIST))),$(abspath $(MAKEFILE_LIST)))),1)
_teleproxy.mk := $(lastword $(MAKEFILE_LIST))
include $(dir $(_teleproxy.mk))prelude.mk

TELEPROXY_LOG ?= $(dir $(_teleproxy.mk))teleproxy.log
KUBE_URL = https://kubernetes/api/

TELEPROXY ?= $(build-aux.bindir)/teleproxy
$(eval $(call build-aux.bin-go.rule,teleproxy,github.com/datawire/teleproxy/cmd/teleproxy))
# override the rule for .teleproxy.stamp -> teleproxy
$(build-aux.bindir)/teleproxy: $(build-aux.bindir)/%: $(build-aux.bindir)/.%.stamp $(COPY_IFCHANGED)
	sudo $(COPY_IFCHANGED) $< $@
	sudo chown 0:0 $@
	sudo chmod go-w,a+sx $@

proxy: ## (Kubernaut) Launch teleproxy in the background
proxy: $(KUBECONFIG) $(TELEPROXY)
	if ! curl -sk $(KUBE_URL); then \
		$(TELEPROXY) > $(TELEPROXY_LOG) 2>&1 & \
	fi
	@for i in $$(seq 127); do \
		echo "Checking proxy ($$i): $(KUBE_URL)"; \
		if curl -sk $(KUBE_URL); then \
			exit 0; \
		fi; \
		sleep 1; \
	done; echo "ERROR: proxy did not come up"; exit 1
	@printf '\n\nProxy UP!\n'
.PHONY: proxy

unproxy: ## (Kubernaut) Shut down 'proxy'
	curl -s --connect-timeout 5 127.254.254.254/api/shutdown || true
	@sleep 1
.PHONY: unproxy

status-proxy: ## (Kubernaut) Fail if cluster connectivity is broken or Teleproxy is not running
status-proxy: status-cluster
	@if curl -o /dev/null -s --connect-timeout 1 127.254.254.254; then \
		if curl -o /dev/null -sk $(KUBE_URL); then \
			echo "Proxy okay!"; \
		else \
			echo "Proxy up but connectivity check failed."; \
			exit 1; \
		fi; \
	else \
		echo "Proxy not running."; \
		exit 1; \
	fi
.PHONY: status-proxy

$(KUBECONFIG).clean: unproxy

clean: _clean-teleproxy
_clean-teleproxy: $(if $(wildcard $(TELEPROXY_LOG)),unproxy)
	rm -f $(TELEPROXY_LOG)
# Files made by older versions.  Remove the tail of this list when the
# commit making the change gets far enough in to the past.
#
# 2018-07-01
	rm -f $(dir $(_teleproxy.mk))teleproxy
.PHONY: _clean-teleproxy

endif
