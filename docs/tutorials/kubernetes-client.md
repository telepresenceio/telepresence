---
layout: doc
weight: 2
title: "Connect to a remote Kubernetes cluster"
categories: tutorials
---

{% include install.md cluster="Kubernetes" command="kubectl" install="https://kubernetes.io/docs/tasks/tools/install-kubectl/" %}

### Connecting to a remote cluster

In this tutorial you'll see how Telepresence allows you to get transparent access to a remote cluster from a local process.
This allows you to use your local tools on your laptop to communicate with processes inside the cluster.

You should start by running a service in the cluster:

```console
$ kubectl run myservice --image=datawire/hello-world --port=8000 --expose
$ kubectl get service myservice
NAME        CLUSTER-IP   EXTERNAL-IP   PORT(S)    AGE
myservice   10.0.0.12    <none>        8000/TCP   1m
```

It may take a minute or two for the pod running the server to be up and running, depending on how fast your cluster is.

You can now run a local process using Telepresence that can access that service, even though the process is local but the service is running in the Kubernetes cluster:

```console
$ telepresence --new-deployment example --run curl http://myservice:8000/
Hello, world!
```

(This will not work if the hello world pod hasn't started yet... if so, try again.)

What's going on:

1. Telepresence creates a new `Deployment` called `example`, which runs a proxy.
2. Telepresence runs `curl` locally in a way that proxies networking through that `Deployment`.
3. The DNS lookup and HTTP request done by `curl` get routed through the proxy and transparently access the cluster... even though `curl` is running locally.

To learn more about what Telepresence proxies you can read the relevant [reference documentation](/reference/proxying.html).
