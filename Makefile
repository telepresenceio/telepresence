
all: test build

build:
	go build cmd/teleproxy/teleproxy.go && sudo chown root:wheel ./teleproxy && sudo chmod u+s ./teleproxy

get:
	go get -t -d ./...

run-tests:
	go test -v -exec sudo github.com/datawire/teleproxy/internal/pkg/nat/

test-go: get run-tests

test-docker:
	docker build -f scripts/Dockerfile . -t teleproxy-make
	docker run --cap-add=NET_ADMIN teleproxy-make run-tests

test: test-go test-docker

KUBECONFIG ?= ~/.kube/config

run: build
	./teleproxy -kubeconfig ${KUBECONFIG} -dns $(shell fgrep nameserver /etc/resolv.conf | head -1 | awk '{ print $$2 }') -remote $(shell kubectl get svc teleproxy -o go-template='{{with index .status.loadBalancer.ingress 0}}{{or .ip .hostname}}{{end}}')
