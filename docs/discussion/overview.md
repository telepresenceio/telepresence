# Introduction to Telepresence

Telepresence is an open source tool that lets you run a single service locally, while connecting that service to a remote Kubernetes cluster. This lets developers working on multi-service applications to:

1. Do fast local development of a single service, even if that service depends on other services in your cluster. Make a change to your service, save, and you can immediately see the new service in action.

2. Use any tool installed locally to test/debug/edit your service. For example, you can use a debugger or IDE!

3. Make your local development machine operate as if it's part of your Kubernetes cluster. If you've got an application on your machine that you want to run against a service in the cluster -- it's easy to do.

Telepresence works on both Mac OS X and Linux, with [OS-native packages](/reference/install.html).

## How it works

Telepresence deploys a two-way network proxy in a pod running in your Kubernetes cluster. This pod proxies data from your Kubernetes environment (e.g., TCP connections, environment variables, volumes) to the local process. The local process has its networking transparently overridden so that DNS calls and TCP connections are routed over the proxy to the remote Kubernetes cluster.

This approach gives:

* your local service full access to other services in the remote cluster
* your local service full access to Kubernetes environment variables, secrets, and ConfigMaps
* your remote services full access to your local service

How Telepresence works is discussed in more detail [here](/discussion/how-it-works.html).

## Alternatives to Telepresence

Typical alternatives to Telepresence include:

* running your entire multi-service application locally via Docker Compose. This gives you a fast dev/debug cycle. However, it's less realistic since you're not running your services actually inside Kubernetes, and there are cloud services you might use (e.g., a database) that might not be easy to use locally.
* minikube. You can't do live coding/debugging with minikube by itself, but you can with Telepresence. The two work well together.
* run everything in a remote Kubernetes cluster. Again, you can't do live coding/debugging in a remote Kubernetes cluster ... but you can with Telepresence.

## Getting started

Telepresence offers a broad set of [proxying options](/reference/methods.html) which have different strengths and weaknesses. Generally speaking, we recommend you:

* Start with the container method, which provides the most consistent environment for your code. Here is a [container quick start](/tutorials/docker.html).
* Use the vpn-tcp method, which lets you use an IDE or debugger with your code. Here is a [quick start that uses the vpn-tcp method](/tutorials/kubernetes-rapid.html)
