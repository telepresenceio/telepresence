all: build check

include build-aux/common.mk
include build-aux/go.mk
include build-aux/kubernaut.mk

export PATH:=$(CURDIR)/bin_$(GOOS)_$(GOARCH):$(PATH)
export KUBECONFIG = $(CURDIR)/cluster.knaut

test-cluster: $(KUBECONFIG) bin_$(GOOS)_$(GOARCH)/kubeapply
	bin_$(GOOS)_$(GOARCH)/kubeapply -f k8s
.PHONY: test-cluster

go-test-nat: build
	go test -v -exec sudo $(go.module)/internal/pkg/nat

go-test-teleproxy: build test-cluster
	go test -v -exec "sudo env PATH=${PATH} KUBECONFIG=${KUBECONFIG}" $(go.module)/cmd/teleproxy

go-test-other: build
	go test -v $(filter-out $(go.module)/internal/pkg/nat $(go.module)/cmd/teleproxy,$(go.pkgs))

go-test: go-test-nat go-test-teleproxy go-test-other

check-docker: build
ifneq ($(shell which docker 2>/dev/null),)
check-docker: $(addprefix bin_linux_amd64/,$(notdir $(go.bins)))
	docker build -f scripts/Dockerfile . -t teleproxy-make
	docker run --cap-add=NET_ADMIN teleproxy-make go-test-nat
else
	@echo "SKIPPING DOCKER TESTS"
endif

check: check-docker

clean: cluster.knaut.clean

# Utility targets

claim: $(KUBECONFIG)

shell: $(KUBECONFIG)
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash

run: build
	bin_$(GOOS)_$(GOARCH)/teleproxy
