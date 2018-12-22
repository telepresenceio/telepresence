all: build check

include build-aux/common.mk
include build-aux/go.mk
include build-aux/kubernaut.mk

export PATH:=$(CURDIR)/bin_$(GOOS)_$(GOARCH):$(PATH)
export KUBECONFIG = $(CURDIR)/cluster.knaut

manifests: $(KUBECONFIG) bin_$(GOOS)_$(GOARCH)/kubeapply
	bin_$(GOOS)_$(GOARCH)/kubeapply -f k8s
.PHONY: manifests

other-tests: build
	go test -v $(filter-out $(go.module)/internal/pkg/nat $(go.module)/cmd/teleproxy,$(go.pkgs))

nat-tests: build
	go test -v -exec sudo $(go.module)/internal/pkg/nat

smoke-tests: build manifests
	go test -v -exec "sudo env PATH=${PATH} KUBECONFIG=${KUBECONFIG}" $(go.module)/cmd/teleproxy

sudo-tests: nat-tests smoke-tests

run-tests: sudo-tests other-tests

test-go: go-get run-tests

test-docker: build
ifneq ($(shell which docker 2>/dev/null),)
test-docker: $(addprefix bin_linux_amd64/,$(notdir $(go.bins)))
	docker build -f scripts/Dockerfile . -t teleproxy-make
	docker run --cap-add=NET_ADMIN teleproxy-make nat-tests
else
	@echo "SKIPPING DOCKER TESTS"
endif

test: test-go test-docker
check: test

clean: cluster.knaut.clean

# Utility targets

claim: $(KUBECONFIG)

shell: $(KUBECONFIG)
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash

run: build
	bin_$(GOOS)_$(GOARCH)/teleproxy
