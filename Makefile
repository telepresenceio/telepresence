
all: test build

build:
	go build cmd/tp2/tp2.go && sudo chown root:root ./tp2 && sudo chmod u+s ./tp2

test:
	go test -v -exec sudo github.com/datawire/tp2/internal/pkg/nat/

run:
	./tp2 -kubeconfig ~/.kube/config -dns $(shell fgrep nameserver /etc/resolv.conf | head -1 | awk '{ print $$2 }') -remote $(shell kubectl get svc tp2 -o go-template='{{(index .status.loadBalancer.ingress 0).ip}}')
