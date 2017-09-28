# Docker support

In this howto you'll learn to use Telepresence to proxy a Docker container.
Processes running in the container will have transparent access to a remote Kubernetes or OpenShift cluster.

## Connecting to a remote cluster

To try this example, start by running a service in the cluster:

```console
$ kubectl run myservice --image=datawire/hello-world --port=8000 --expose
$ kubectl get service myservice
NAME        CLUSTER-IP   EXTERNAL-IP   PORT(S)    AGE
myservice   10.0.0.12    <none>        8000/TCP   1m
```

It may take a minute or two for the pod running the server to be up and running, depending on how fast your cluster is.

You can now run a Docker container using Telepresence that can access that service, even though the process is local but the service is running in the Kubernetes cluster:

```console
$ telepresence --docker-run -i -t alpine /bin/sh
alpine# apk add --no-cache curl
alpine# curl http://myservice:8000/
Hello, world!
```

(This will not work if the hello world pod hasn't started yet... if so, try again.)

## How it works

Telepresence will start a new proxy container, and then call `docker run` with whatever arguments you pass to `--docker-run` to start a container that will have its networking proxied.
All networking is proxied:

* Outgoing to Kubernetes.
* Outgoing to cloud resources added with `--also-proxy`
* Incoming connections to ports specified with `--expose`.

Volumes and environment variables from the remote `Deployment` are also available in the container.
