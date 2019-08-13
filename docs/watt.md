# Watt - Watch All The Things

TODO: add more stuff here... I pruned this of stuff that was obviously stale, but now it is kinda sparse...

Watt watches resources in kubernetes and/or consul and invokes hooks
when these resources change.

# Run Watt

- Add the appropriate `-s` switches for initial sources.
- Set `--notify` to whatever makes you happy.

`bin_linux_amd64/watt -s service -s configmap -s secrets --notify printf`

# Register and Deregister Services from Consul

Make sure you're using the right `KUBECONFIG` if you switch terminals

**NOTE**: Change `-id` as needed

## Register

`kubectl exec $(kubectl get pods --selector=app=consul --output=jsonpath='{.items[0].metadata.name}') -- consul services register -name=foobar -address=10.10.0.1 -port=9000 -id fb0`

## Deregister

`kubectl exec $(kubectl get pods --selector=app=consul --output=jsonpath='{.items[0].metadata.name}') -- consul services deregister -id fb0`
