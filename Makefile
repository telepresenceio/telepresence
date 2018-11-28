
all: test build

pkg = github.com/datawire/teleproxy
bins = teleproxy kubeapply

include build-aux/go.mk
include build-aux/kubernaut.mk

export GOPATH
export GOBIN
export PATH:=$(GOBIN):$(PATH)
export KUBECONFIG=${PWD}/cluster.knaut

manifests: cluster.knaut kubeapply
	./kubeapply -f k8s
.PHONY: manifests

claim: cluster.knaut.clean cluster.knaut

shell: cluster.knaut
	@exec env -u MAKELEVEL PS1="(dev) [\W]$$ " bash

other-tests:
	$(GO) test -v $(shell $(GO) list ./.go-workspace/src/$(pkg)/... \
		| fgrep -v github.com/datawire/teleproxy/internal/pkg/nat \
		| fgrep -v github.com/datawire/teleproxy/cmd/teleproxy)

nat-tests:
	$(GO) test -v -exec sudo github.com/datawire/teleproxy/internal/pkg/nat/

smoke-tests: manifests
	$(GO) test -v -exec "sudo env PATH=${PATH} KUBECONFIG=${KUBECONFIG}" github.com/datawire/teleproxy/cmd/teleproxy

sudo-tests: nat-tests smoke-tests

run-tests: sudo-tests other-tests

test-go: get run-tests

test-docker:
	@if [[ "$(shell which docker)-no" != "-no" ]]; then \
		docker build -f scripts/Dockerfile . -t teleproxy-make && \
		docker run --cap-add=NET_ADMIN teleproxy-make nat-tests ; \
	else \
		echo "SKIPPING DOCKER TESTS" ; \
	fi

test: test-go test-docker

format:
	gofmt -w -s cmd internal pkg

run: build
	./teleproxy

clean: cluster.knaut.clean
	rm -f ./teleproxy ./kubeapply
