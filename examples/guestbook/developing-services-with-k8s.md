# Developing microservices locally with Kubernetes

## TL;DR

The popular [guestbook](https://cloud.google.com/container-engine/docs/tutorials/guestbook) tutorial for Kubernetes shows how to get a simple PHP and Redis application running in Kubernetes, but doesn't explain how you might edit and test the code. We'll show you how to set up a fast, productive development environment for coding on Kubernetes.

## Microservices

Microservices are an increasingly popular design pattern for cloud applications. In a microservices architecture, an individual application is broken into many small services that can be independently developed, tested, and released. While this approach has numerous benefits, microservices can also bring additional complexity into your overall architecture and workflow.

One of the areas of complexity is setting up a productive development environment for microservices. In a traditional web application, a development environment may consist of a database and the actual web application. In a microservices cloud application, an individual service may depend on multiple other services. Moreover, the service may also utilize cloud resources such as Amazon RDS or Google Cloud Pub/Sub. Setting up and maintaining a development environment with multiple services and cloud resources can be a lot of work. While there are [multiple approaches to setting up a development environment for microservices](), this tutorial will walk through setting up local development environment for microservices with your services running on a remote Kubernetes cluster.

## Technologies used

* Kubernetes
* Google Container Engine
* Docker
* Telepresence
* PHP and Redis

## Prerequisites and setup

In order to use this demo, you're going to need:

* A local system running either Linux or Mac OS X
* Access to a Kubernetes cluster (this tutorial will walk through setting up a cluster using Google Container Engine)

### Setting up your local laptop

To set up your laptop, you'll need to install a few basic components.

First, you'll want to install Docker. If you don't have Docker installed, follow the install instructions at https://www.docker.com/community-edition.

Next, we're going to want to install the `gcloud` and `kubectl` commands. Follow the instructions at https://cloud.google.com/sdk/downloads to download and install the Cloud SDK. Then, insure `kubectl` is installed:

```
% sudo gcloud components update kubectl
```

Finally, we need to install Telepresence, which will proxy your locally running service to GKE.

```
% brew install torsocks
% curl
% chmod +x
```

### Setting up Kubernetes in Google Container Engine

Setting up a production-ready Kubernetes cluster can be [fairly complex](https://www.datawire.io/guide/setting-kubernetes-aws/), so we're going to use Google Container Engine in our example. If you already have a Kubernetes cluster handy, you can skip this section.

To set up a Kubernetes cluster in GKE, go to https://console.cloud.google.com, choose the Google Container Engine option from the menu, and then Create a Cluster.

The following gcloud command will create a small 2 node cluster in the us-central1-a region:

```
gcloud container --project "PROJECT" clusters create "EXAMPLE_NAME" --zone "us-central1-a" --machine-type "n1-standard-1" --image-type "GCI" --disk-size "100" --scopes "https://www.googleapis.com/auth/compute","https://www.googleapis.com/auth/devstorage.read_only","https://www.googleapis.com/auth/logging.write","https://www.googleapis.com/auth/monitoring","https://www.googleapis.com/auth/servicecontrol","https://www.googleapis.com/auth/service.management.readonly","https://www.googleapis.com/auth/trace.append" --num-nodes "2" --network "default" --enable-cloud-logging --enable-cloud-monitoring
```

## Installing the Guestbook application

The [Guestbook](https://cloud.google.com/container-engine/docs/tutorials/guestbook) sample application is a simple PHP application backed by a Redis database. We're going to set up Redis in the cloud, and run the PHP application locally on our laptop.

To get started, we need to authenticate to our cluster:

```
% gcloud container clusters get-credentials CLUSTER_NAME
% gcloud auth application-default login
```

### Setting up Redis

Now, let's install a Redis cluster in our Kubernetes cluster.

```
% kubectl create -f redis-master-deployment.yaml
% kubectl create -f redis-master-service.yaml
% kubectl create -f redis-slave-deployment.yaml
% kubectl create -f redis-slave-service.yaml
```

You can verify that everything is running:

```
% kubectl get pods
NAME                           READY     STATUS    RESTARTS   AGE
redis-master-343230949-lpw91   1/1       Running   0          1d
redis-slave-132015689-dpp46    1/1       Running   0          1d
redis-slave-132015689-v06md    1/1       Running   0          1d
```

### Setting up the Guestbook frontend

We're going to run the Guestbook PHP frontend locally, so the first step is to download the Docker image.

```
docker pull gcr.io/google_samples/gb-frontend:v4
```

You can quickly run the frontend service:

```
docker run -it --publish=8080:80 gcr.io/google_samples/gb-frontend:v4
```

The `--publish` option maps the container's port 80 to the host's port 8080. Visit `localhost:8080` in your browser, and you'll see the Guestbook application. You won't be able to add a Guestbook entry, because we haven't connected the PHP application to Redis yet. Exit your Docker process by typing `Ctrl-C`.

### Connecting the Guestbook frontend to Redis

We're now going to use [Telepresence](https://datawire.github.io/telepresence) to create a virtual network between your local machine and the remote Kubernetes cluster. This way, the PHP application will be able to talk to remote cloud resources, and vice versa.

First, we need to figure out the IP address of Redis:

```
% kubectl get services
NAME           CLUSTER-IP     EXTERNAL-IP   PORT(S)    AGE
redis-master   10.7.248.117   <none>        6379/TCP   1d
redis-slave    10.7.245.58    <none>        6379/TCP   1d
```

We see that the Redis master is running on `10.7.248.117` on port 6379. Now, let's fire up Telepresence. Substitute the IP address and port of your Redis instance into the command below.

```
telepresence --new-deployment php --proxy REDIS_IP:REDIS_PORT --expose 80 --docker-run --rm -i -t -publish=8080:80 --volume=/users/richard/dw/tel/:/var/www/html/:ro  gcr.io/google_samples/gb-frontend:v4
```




kubectl get pod
pick the pod matching deployment name
kubectl port-forward <pod> 8080:80
