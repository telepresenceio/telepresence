
all: test build

build:
	go build cmd/teleproxy/teleproxy.go && sudo chown root:wheel ./teleproxy && sudo chmod u+s ./teleproxy

test:
	go test -v -exec sudo github.com/datawire/teleproxy/internal/pkg/nat/

KUBECONFIG ?= ~/.kube/config

run: build
	./teleproxy -kubeconfig ${KUBECONFIG} -dns $(shell fgrep nameserver /etc/resolv.conf | head -1 | awk '{ print $$2 }') -remote $(shell kubectl get svc teleproxy -o go-template='{{with index .status.loadBalancer.ingress 0}}{{or .ip .hostname}}{{end}}')
