# Fast development workflow with Docker and Kubernetes

Keeping development environments in sync is a constant pain. Containerizing your development environment enables your service to run in the exact same environment everywhere: from your laptop to production (for more details on the benefits of a container native development workflow, see [this post by Matt Butcher](https://open.microsoft.com/2018/04/23/5-reasons-you-should-be-doing-container-native-development/).)

Telepresence, in conjunction with a containerized development environment, gives the developer a fast development workflow in developing a multi-container application on Kubernetes.  Telepresence lets you run a Docker container locally, while proxying it to your Kubernetes cluster.

In this HOWTO, we'll walk through how to use Telepresence with a containerized Docker environment to build a fast development workflow.

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

## Quick example

We'll start with a quick example. Start by running a service in the cluster:

```console
$ kubectl run qotm --image=datawire/qotm:1.3 --port=5000 --expose
$ kubectl get service qotm
NAME        CLUSTER-IP   EXTERNAL-IP   PORT(S)    AGE
qotm        10.0.0.12    <none>        8000/TCP   1m
```

It may take a minute or two for the pod running the server to be up and running, depending on how fast your cluster is.

You can now run a Docker container using Telepresence that can access that service, even though the process is local but the service is running in the Kubernetes cluster:

```console
$ telepresence --docker-run -i -t alpine /bin/sh
alpine# apk add --no-cache curl
alpine# curl http://qotm:5000/
{
  "hostname": "qotm-1536849512-ckf1v",
  "ok": true,
  "quote": "Nihilism gambles with lives, happiness, and even destiny itself!",
  "time": "2017-10-25T15:28:51.712799",
  "version": "1.3"
}
```

(This will not work if the QOTM pod hasn't started yet... If so, try again.)

## Setting up a development environment in Docker

So how would we use Telepresence to do actual *development* of the QOTM service? We'll set up a local Dockerized development environment for QOTM. Clone the QOTM repo:

```
$ git clone https://github.com/datawire/qotm.git
```

In the repository is a [Dockerfile](https://github.com/datawire/qotm/blob/master/Dockerfile) that builds a runtime environment for the QOTM service.

Build the runtime environment:

```
$ cd qotm
$ docker build -t qotm-dev .
```

We'll use Telepresence to swap the QOTM deployment with the local Docker image. Behind the scenes, Telepresence invokes `docker run`, so it supports any arguments you can pass to `docker run`. In this case, we're going to also mount our local directory to `/service` in your Docker container. Make sure your current working directory is the `qotm` diretory, since we're going to mount that directly into the container.

```
$ telepresence --swap-deployment qotm --docker-run \
  --rm -it -v $(pwd):/service qotm-dev:latest
```

We can test this out. In another terminal, we'll start a pod remotely on the Kubernetes cluster.

```
$ kubectl run -i --tty alpine --image=alpine -- sh
/ # apk add --no-cache curl
...
/ # curl http://qotm:5000
{
  "hostname": "8b4faa7e175c",
  "ok": true,
  "quote": "The last sentence you read is often sensible nonsense.",
  "time": "2017-10-25T19:28:41.038335",
  "version": "1.3"
}

```

Let's change the version in `qotm.py`. Run the following:

```
sed -i -e 's@1.3@'"1.4"'@' qotm/qotm.py
```

Rerun the `curl` command from your remote pod:

```
/ # curl http://qotm:5000
{
  "hostname": "8b4faa7e175c",
  "ok": true,
  "quote": "The last sentence you read is often sensible nonsense.",
  "time": "2017-10-25T19:28:41.038335",
  "version": "1.4"
}
```

And notice how the code has changed, live. Congratulations! You've now:

* Routed the QOTM service to the Docker container running locally
* Configured your Docker service to pick up changes from your local filesystem
* Made a live code edit and see it immediately reflected in production

## How it works

Telepresence will start a new proxy container, and then call `docker run` with whatever arguments you pass to `--docker-run` to start a container that will have its networking proxied. All networking is proxied:

* Outgoing to Kubernetes.
* Outgoing to cloud resources added with `--also-proxy`
* Incoming connections to ports specified with `--expose`.

Volumes and environment variables from the remote `Deployment` are also available in the container.

{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "bottom") }}

{{ macros.tutorialFooter(page.title, file.path, book['baseUrl']) }}
