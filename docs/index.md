# Telepresence Documentation

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
