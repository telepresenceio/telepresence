
all: test build

teleproxy:
	go build cmd/teleproxy/teleproxy.go

build: teleproxy
	sudo chown root:wheel ./teleproxy && sudo chmod u+s ./teleproxy

get:
	go get -t -d ./...

tpu-tests:
	go test -v -exec sudo github.com/datawire/teleproxy/internal/pkg/tpu/

nat-tests:
	go test -v -exec sudo github.com/datawire/teleproxy/internal/pkg/nat/

run-tests: tpu-tests nat-tests

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
