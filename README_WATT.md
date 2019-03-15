# Watt - Watch All The Things

## Terminal 1

1. `git clone git@github.com:datawire/teleproxy`
2. `git checkout lomb/gorgonzola`
3. `make`
4. `kubernaut claims create --name=watt --cluster-group=main`
5. `export KUBECONFIG=~/.kube/watt.yaml`
6. `bin/watt -s configmap -s services`

## Terminal 2

`make consul.local`

## Terminal 3

`make consul.attach`

## Terminal 4

Register a Consul Resolver configuration with Watt (pretend this is a CRD)

`kubectl apply -f consul-resolver.yaml`

## Terminal 3 (consul.attach)

`consul services register -name=bar -address=10.10.0.1 -port=9000 -id0`

## Terminal 4

`curl -v http://localhost:7000/snapshot`