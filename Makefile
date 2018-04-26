
all: test build

generate:
	test ! -e /dev/pf || go generate github.com/datawire/pf

build: generate
	go build cmd/tp2/tp2.go && sudo chown root:wheel ./tp2 && sudo chmod u+s ./tp2

test: generate
	go test -v -exec sudo github.com/datawire/tp2/internal/pkg/nat/

KUBECONFIG ?= ~/.kube/config

run: build
	./tp2 -kubeconfig ${KUBECONFIG} -dns $(shell fgrep nameserver /etc/resolv.conf | head -1 | awk '{ print $$2 }') -remote $(shell kubectl get svc tp2 -o go-template='{{with index .status.loadBalancer.ingress 0}}{{or .ip .hostname}}{{end}}')
