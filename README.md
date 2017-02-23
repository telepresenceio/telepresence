# Telepresence: local container in a remote kubernetes cluster

## Step 1: access to remote cluster from local container, including DNS

OpenVPN server in kubernetes pod.
OpenVPN listens on tcp 1194.
client cert is stored locally inside pod.

kubectl on laptop's client copies over client cert.
kubectl forwards localhost:1194 on laptop to openvpn pod's 1194.
OpenVPN client on laptop's docker is started using client cert, connects to 1194 on host laptop (how?).

Business logic container starts with "--net container:vpn".

## Step 2: access to local container from remote cluster

kubectl runs something on pod that forwards ports from there to matching port on local docker vpn client.


## Step 3: environment variables from remote pod available to local container

## Step 4: documentation
