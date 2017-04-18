---
layout: doc
weight: 2
title: "Quickstart"
categories: getting-started
---

### Proxying from your local process to Kubernetes

We'll start out by using Telepresence with a newly created Kubernetes `Deployment`, just so it's clearer what is going on.
In the next section we'll discuss using Telepresence with an existing `Deployment` - you can [skip ahead](#using-existing-deployments) if you want.

To get started we'll use `telepresence --new-deployment quickstart` to create a new `Deployment` and matching `Service`.
The client will connect to the remote Kubernetes cluster via that `Deployment`.
You'll then use the `--run-shell` argument to start a shell that is proxied to the remote Kubernetes cluster.

Let's start a `Service` and `Deployment` in Kubernetes, and wait until it's up and running.
We'll check the current Kubernetes context and then start a new pod:

```console
host$ kubectl config current-context
yourcluster
host$ kubectl run --expose helloworld --image=nginx:alpine --port=80
# ... wait 30 seconds, make sure pod is in Running state:
host$ kubectl get pod --selector=run=helloworld
NAME                          READY     STATUS    RESTARTS   AGE
helloworld-1333052153-63kkw   1/1       Running   0          33s
```

The local process you will run via `telepresence` will get environment variables that match those in the remote deployment, including Kubernetes `Service` addresses.
It will be able to access these addresses inside Kubernetes, as well as use Kubernetes custom DNS records for `Service` instances.

Note that starting `telepresence` the first time may take a little while, since Kubernetes needs to download the server-side image.

```console
host$ telepresence --new-deployment quickstart --run-shell
@yourcluster|host$ env | grep HELLOWORLD_SERVICE
HELLOWORLD_SERVICE_HOST=10.0.0.3
HELLOWORLD_SERVICE_PORT=443
@yourcluster|host$ curl "http://${HELLOWORLD_SERVICE_HOST}:${HELLOWORLD_SERVICE_PORT}/"
<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
...
@yourcluster|host$ curl "http://helloworld:${HELLOWORLD_SERVICE_PORT}/"
<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
...
@yourcluster|host$ exit
```

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

### Proxying from Kubernetes to your local process

So far you've seen how local processes can access the remote Kubernetes cluster's services.

You can also run a local server that listens on port 8080 and it will be exposed and available inside the Kubernetes cluster.
Just pass `--expose 8080` to Telepresence so it knows it needs to expose that port to the Kubernetes cluster:

```console
host$ echo "hello world" > file.txt
host$ telepresence --new-deployment quickstart --expose 8080 --run-shell
@yourcluster|host$ ls
file.txt
@yourcluster|host$ python2 -m SimpleHTTPServer 8080
Serving HTTP on 0.0.0.0 port 8080 ...
```

If you only have Python 3 on your computer you can instead do:

```console
@yourcluster|host$ python3 -m http.server 8080
```

If you leave the `telepresence` process running your code will be accessible from inside the Kubernetes cluster:

<div class="mermaid">
graph TD
  subgraph Laptop
    code["python HTTP server on port 8080"]---client[Telepresence client]
  end
  subgraph Kubernetes in Cloud
    client-.-proxy["k8s.Pod: Telepresence proxy, listening on port 8080"]
  end
</div>

Let's send a request to the remote pod to demonstrate that.
In a different terminal we can run a pod on the Kubernetes cluster and see that it can access the code running on your personal computer, via the Telepresence-created `Service` named `quickstart`:

```console
$ kubectl run --attach -i -t test --generator=job/v1 --rm \
          --image=busybox --restart Never --command /bin/sh
k8s-pod# wget -qO- http://quickstart.default.svc.cluster.local:8080/file.txt
hello world
```
