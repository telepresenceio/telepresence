# Telepresence: local container in a remote kubernetes cluster


## Design attempt #1: OpenVPN

### Step 1: access to remote cluster from local container, including DNS

OpenVPN server in kubernetes pod.
OpenVPN listens on tcp 1194.
client cert is stored locally inside pod.

kubectl on laptop's client copies over client cert.
kubectl forwards localhost:1194 on laptop to openvpn pod's 1194.
OpenVPN client on laptop's docker is started using client cert, connects to 1194 on host laptop (how?).

Business logic container starts with "--net container:vpn".

### Step 2: access to local container from remote cluster

kubectl runs something on pod that forwards ports from there to matching port on local docker vpn client.


### Step 3: environment variables from remote pod available to local container


### Results

* OpenVPN is very difficult to work with.
* IP ranges conflict (e.g. minikube's Docker has same IP range as Docker on my laptop).
* Very complicated.

## Design attempt #2: kubectl port-forward only

1. Set environment variables that match k8s environment.
2. For each Service, create a tunnel using `kubectl exec` of proxy on remote side (localhost:1234 to service-ip:5100).
3. For each Service, use `kubectl port-forward` in a local container to forward from localhost:1234 to 1234 on the remote container.
4. Use `docker run --add-host` option to add host entries for the services? or use a DNS server.
