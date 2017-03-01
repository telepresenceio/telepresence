# Telepresence: local code in a remote Kubernetes cluster

## Motivation

Have you ever wanted the quick development cycle of local code while still having your code run within a remote Kubernetes cluster?
Telepresence allows you to run your code locally while still:

1. Giving your code access to Services in a remote Kubernetes cluster.
2. Giving your code access to cloud resources like AWS RDS or Google PubSub.
3. Allowing Kubernetes to access your code as if it were in a normal pod within the cluster.

Some alternatives:

* Minikube is a tool that lets you run a Kubernetes cluster locally.
  You won't have access to cloud resources, however, and your development cycle won't be as fast since access to local source code is harder.
  Finally, spinning up your full system may not be realistic if it's big enough.
* Docker Compose lets you spin up local containers, but won't match your production Kubernetes cluster.
  It also won't help you access cloud resources, you will need to emulate them.
* Pushing your code to the remote Kubernetes cluster.
  This is a somewhat slow process, and you won't be able to do the quick debug cycle you get from running code locally.

## Theory of operation

Currently Telepresence works by running your code locally in a Docker container, and forwarding requests to/from the remote Kubernetes cluster.

## How to use Telepresence

Let's assume you have a web service which listens on port 8080, and has a Dockerfile which gets built to an image called `examplecom/yourservice`.
Your Kubernetes configuration for the service looks something like this:

```yaml

```



### Local development with Docker

To make Telepresence even more useful, you might want to use a custom Dockerfile setup that allows for code changes to be reflected immediately upon editing.

For interpreted languages the typical way to do this is to mount your source code as a Docker volume, and use your web framework's ability to reload code for each request.
Here are some tutorials for various languages and frameworks:

* [Python with Flask](http://matthewminer.com/2015/01/25/docker-dev-environment-for-web-app.html)
* [Node](http://fostertheweb.com/2016/02/nodemon-inside-docker-container/)

## Help us make Telepresence work better for you

We are considering various improvements to `telepresence`, including:

* [Removing need for Kubernetes credentials](https://github.com/datawire/telepresence/issues/2)
* [Allowing running code locally without a container](https://github.com/datawire/telepresence/issues/1)

Please add comments to these tickets if you are interested in these features, and [file a new issue](https://github.com/datawire/telepresence/issues/new) if you find any bugs or have any feature requests.
