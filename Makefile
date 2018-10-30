
include kubernaut.mk

all: test build

shell: cluster.knaut
	PATH=${PATH}:${PWD} \
	KUBECONFIG=${PWD}/cluster.knaut \
	PS1="(dev) [\W]$$ " bash

.PHONY: teleproxy
teleproxy: $(GO_FILES)
	go build cmd/teleproxy/teleproxy.go

build: teleproxy
	sudo chown root:wheel ./teleproxy && sudo chmod u+s ./teleproxy

get:
	go get -t -d ./...

other-tests:
	go test -v $(shell go list ./... | fgrep -v github.com/datawire/teleproxy/internal/pkg/nat)

nat-tests:
	go test -v -exec sudo github.com/datawire/teleproxy/internal/pkg/nat/

run-tests: nat-tests other-tests

test-go: get run-tests

test-docker:
	@if [[ "$(shell which docker)-no" != "-no" ]]; then \
		docker build -f scripts/Dockerfile . -t teleproxy-make && \
		docker run --cap-add=NET_ADMIN teleproxy-make nat-tests ; \
	else \
		echo "SKIPPING DOCKER TESTS" ; \
	fi

test: test-go test-docker

run: build
	./teleproxy

clean: cluster.knaut.clean
	rm -f ./teleproxy

clobber: clean cluster.knaut.clobber kubernaut.clobber
	rm -rf kubernaut
