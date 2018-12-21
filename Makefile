all: build check

include build-aux/common.mk
include build-aux/go.mk
include build-aux/kubernaut.mk

export PATH:=$(CURDIR)/bin_$(GOOS)_$(GOARCH):$(PATH)
export KUBECONFIG=${PWD}/cluster.knaut

manifests: cluster.knaut bin_$(GOOS)_$(GOARCH)/kubeapply
	bin_$(GOOS)_$(GOARCH)/kubeapply -f k8s
.PHONY: manifests

claim: cluster.knaut.clean cluster.knaut

shell: cluster.knaut
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash

other-tests:
	go test -v $(filter-out $(go.module)/internal/pkg/nat $(go.module)/cmd/teleproxy,$(go.pkgs))

nat-tests:
	go test -v -exec sudo $(go.module)/internal/pkg/nat

smoke-tests: manifests
	go test -v -exec "sudo env PATH=${PATH} KUBECONFIG=${KUBECONFIG}" $(go.module)/cmd/teleproxy

sudo-tests: nat-tests smoke-tests

run-tests: sudo-tests other-tests

test-go: go-get run-tests

ifneq ($(shell which docker 2>/dev/null),)
test-docker: $(addprefix bin_linux_amd64/,$(notdir $(go.bins)))
	docker build -f scripts/Dockerfile . -t teleproxy-make
	docker run --cap-add=NET_ADMIN teleproxy-make nat-tests
else
test-docker:
	@echo "SKIPPING DOCKER TESTS"
endif

test: test-go test-docker
check: test

run: build
	bin_$(GOOS)_$(GOARCH)/teleproxy

clean: cluster.knaut.clean
