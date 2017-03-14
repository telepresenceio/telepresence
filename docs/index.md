Have you ever wanted the quick development cycle of local code while still having your code run within a remote Kubernetes cluster?
Telepresence allows you to run your code locally while still:

1. Giving your code access to Services in a remote Kubernetes cluster.
2. Giving your code access to cloud resources like AWS RDS or Google PubSub.
3. Allowing Kubernetes to access your code as if it were in a normal pod within the cluster.

**IMPORTANT:** Telepresence is currently in the prototyping stage, and we expect it to change rapidly based on user feedback.
Please [file bugs and feature requests](https://github.com/datawire/telepresence/issues) or come [talk to us on Slack](http://blackbird-oss.herokuapp.com/).

## Telepresence makes development faster

Let's assume you have a web service which listens on port 8080, and has a Dockerfile which gets built to an image called `examplecom/servicename`.
Your service depends on other Kubernetes `Service` instances (`thing1` and `thing2`), and on a cloud database.

The Kubernetes production environment looks like this:

<div class="mermaid">
graph LR
  subgraph Kubernetes in Cloud
    code["k8s.Pod: servicename"]
    s1["k8s.Service: servicename"]---code
    code---s2["k8s.Service: thing1"]
    code---s3["k8s.Service: thing2"]
    code---c1>"Cloud Database (AWS RDS)"]
  end
</div>

### Status quo: a slow development/live test cycle

If you need that cloud database and those two services to directly test your software, you will need to do the following to test a change:

1. Change your code.
2. Build a Docker image.
3. Push the Docker image to a Docker registry in the cloud.
4. Update the Kubernetes cluster to use your new image.
5. Wait for the image to download.

This is slow.

<div class="mermaid">
graph TD
  subgraph Laptop
    code["Source code for servicename"]==>local["Docker image"]
    kubectl
  end
  subgraph Kubernetes in Cloud
    local==>registry["Docker registry"]
    registry==>deployment["k8s.Deployment: servicename"]
    kubectl==>deployment
    s1["k8s.Service: servicename"]---deployment
    deployment---s2["k8s.Service: thing1"]
    deployment---s3["k8s.Service: thing2"]
    deployment---c1>"Cloud Database (AWS RDS)"]
  end
</div>

### Telepresence: a fast development/live test cycle

Telepresence works by running your code *locally* in a Docker container, and forwarding requests to/from the remote Kubernetes cluster.

(Future versions may allow you to run your code locally directly, without a local container.
[Let us know](https://github.com/datawire/telepresence/issues/1) if this a feature you want.)

This means development is fast: you only have to change your code and rebuild your Docker image.
Even better, you can use a development-oriented Docker image or configuration, with live reloads of code changes or the ability to attach a debugger.
This means an even faster development cycle.

<div class="mermaid">
graph TD
  subgraph Laptop
    code["Source code for servicename"]==>local["servicename, in container"]
    local---client[Telepresence client]
  end
  subgraph Kubernetes in Cloud
    client-.-proxy["k8s.Pod: Telepresence proxy"]
    s1["k8s.Service: servicename"]---proxy
    proxy---s2["k8s.Service: thing1"]
    proxy---s3["k8s.Service: thing2"]
    proxy---c1>"Cloud Database (AWS RDS)"]
  end
</div>

## Installing

You will need the following available on your machine:

* Docker.
* Python (2 or 3). This should be available on any Linux or OS X machine.
* Access to your Kubernetes cluster, with local credentials on your machine.
  You can do this test by running `kubectl get pod` - if this works you're all set.

In order to install, run the following command:

```
curl -L https://github.com/datawire/telepresence/raw/{{ site.data.version.version }}/cli/telepresence -o telepresence
chmod +x telepresence
```

Then move telepresence to somewhere in your `$PATH`, e.g.:

```
mv telepresence /usr/local/bin
```

## Quickstart

We'll start out by using Telepresence with a newly created Kubernetes `Deployment`, just so it's clearer what is going on.
In the next section we'll discuss using Telepresence with an existing `Deployment` - you can [skip ahead](#in-depth-usage) if you want.

To get started we'll use `telepresence --new-deployment quickstart` to create a new `Deployment` and matching `Service`.
The client will connect to the remote Kubernetes cluster via that `Deployment` and then run a local Docker container that is proxied into the remote cluster.
You'll also use the `--docker-run` argument to specify how that local container should be created: these arguments will match those passed to `docker run`.

The Docker container you run will get environment variables that match those in the remote deployment, including Kubernetes `Service` addresses.
We can see this by running the `env` command inside an Alpine Linux image:

```console
host$ telepresence --new-deployment quickstart --docker-run \
      --rm alpine env
KUBERNETES_SERVICE_HOST=127.0.0.1
KUBERNETES_SERVICE_PORT=60001
...
```
You can send a request to the Kubernetes API service and it will get proxied, and you can also use the special hostnames Kubernetes creates for `Services`.
(You'll get "unauthorized" in the response because you haven't provided credentials.)

```console
host$ telepresence --new-deployment quickstart --docker-run \
      --rm -i -t alpine /bin/sh
localcontainer$ apk add --no-cache curl  # install curl
localcontainer$ curl -k -v \
    "https://${KUBERNETES_SERVICE_HOST}:${KUBERNETES_SERVICE_PORT}/"
> GET / HTTP/1.1
> User-Agent: curl/7.38.0
> Host: 10.0.0.1
> Accept: */*
> 
< HTTP/1.1 401 Unauthorized
< Content-Type: text/plain; charset=utf-8
< X-Content-Type-Options: nosniff
< Date: Mon, 06 Mar 2017 19:19:44 GMT
< Content-Length: 13
Unauthorized
localcontainer$ curl -k "https://kubernetes.default.svc.cluster.local:${KUBERNETES_SERVICE_PORT}/"
Unauthorized
localcontainer$ exit
host$
```

You've sent a request to the Kubernetes API service, but you could similarly talk to any `Service` in the remote Kubernetes cluster, even though the container is running locally.

Finally, since we exposed port 8080 on the remote cluster, we can run a local server (within the container) that listens on port 8080 and it will be exposed via port 8080 inside the Kubernetes pods we've created.
Let's say we want to serve some static files from your local machine.
We can mount the current directory as a Docker volume, run a webserver on port 8080, and pass `--expose 8080` to Telepresence so it knows it needs to expose that port to the Kubernetes cluster:

```console
host$ echo "hello!" > file.txt
host$ telepresence --new-deployment quickstart --expose 8080 --docker-run \
      -v $PWD:/files -i -t python:3-slim /bin/sh
localcontainer$ cd /files
localcontainer$ ls
file.txt
localcontainer$ python3 -m http.server 8080
Serving HTTP on 0.0.0.0 port 8080 ...
```

If you leave the `telepresence` process running your code will be accessible from inside the Kubernetes cluster:

<div class="mermaid">
graph TD
  subgraph Laptop
    code["python HTTP server on port 8080, in container"]---client[Telepresence client]
  end
  subgraph Kubernetes in Cloud
    client-.-proxy["k8s.Pod: Telepresence proxy, listening on port 8080"]
  end
</div>

Let's send a request to the remote pod to demonstrate that.
In a different terminal we can run a pod on the Kubernetes cluster and see that it can access the code running on your personal computer, via the Telepresence-created `Service` named `quickstart`:

```console
$ kubectl run --attach -i -t test --generator=job/v1 --rm \
          --image=alpine --restart Never --command /bin/sh
k8s-pod# apk add --no-cache curl
k8s-pod# curl http://quickstart.default.svc.cluster.local:8080/file.txt
hello!
```

## In-depth usage

Let's look in a bit more detail at using Telepresence when you have an existing `Deployment`.

Your Kubernetes configuration will typically have a `Service`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: servicename-service
spec:
  ports:
    - port: 8080
      protocol: TCP
      targetPort: 8080
  selector:
    name: servicename
```

You will also have a `Deployment` that actually runs your code, with labels that match the `Service` `selector`:

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: servicename-deployment
spec:
  replicas: 3
  template:
    metadata:
      labels:
        name: servicename
    spec:
      containers:
      - name: servicename
        image: examplecom/servicename:1.0.2
        ports:
        - containerPort: 8080
      - env:
        - name: YOUR_DATABASE_HOST
          value: somewhere.someplace.cloud.example.com
```

In order to run Telepresence you will need to do three things:

1. Replace your production `Deployment` with a custom `Deployment` that runs the Telepresence proxy.
2. Run the Telepresence client locally in Docker.
3. Run your own code in its own Docker container, hooked up to the Telepresence client.

Let's go through these steps one by one.

### 1. Run the Telepresence proxy in Kubernetes

Instead of running the production `Deployment` above, you will need to run a different one that runs the Telepresence proxy instead.
It should only have 1 replica, and it will use a different image, but it should have the same environment variables since you want those available to your local code.

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: servicename-deployment
spec:
  replicas: 1  # <-- only one replica
  template:
    metadata:
      labels:
        name: servicename
    spec:
      containers:
      - name: servicename
        image: datawire/telepresence-k8s:{{ site.data.version.version }}  # <-- new image
        ports:
        - containerPort: 8080
      - env:
        - name: YOUR_DATABASE_HOST
          value: somewhere.someplace.cloud.example.com
```

You should apply this file to your cluster:

```console
$ kubectl apply -f telepresence-deployment.yaml
```

### 2. Run the local Telepresence client on your machine

You want to do the following:

1. Expose port 8080 in your code to Kubernetes.
2. Proxy `somewhere.someplace.cloud.example.com` port 5432 via Kubernetes, since it's probably not accessible outside of your cluster.
3. Connect specifically to the `servicename-deployment` pod you created above, in case there are multiple Telepresence users in the cluster.
4. Run a local container in the above setup.

Services `thing1` and `thing2` will be available to your code automatically so no special parameters are needed for them.
You can do so with the following command line, which uses `--deployment` instead of `--new-deployment` since there is an existing `Deployment` object:

```console
$ telepresence --deployment servicename-deployment \
               --proxy somewhere.someplace.cloud.example.com:5432 \
               --expose 8080 \
               --docker-run \
               examplecom/servicename:localsnapshot
```

You are now running your own code locally inside Docker, attaching it to the network stack of the Telepresence client and using the environment variables Telepresence client extracted.
Your code is connected to the remote Kubernetes cluster.

### 4. (Optional) Better local development with Docker

To make Telepresence even more useful, you might want to use a custom Dockerfile setup that allows for code changes to be reflected immediately upon editing.

For interpreted languages the typical way to do this is to mount your source code as a Docker volume, and use your web framework's ability to reload code for each request.
Here are some tutorials for various languages and frameworks:

* [Python with Flask](http://matthewminer.com/2015/01/25/docker-dev-environment-for-web-app.html)
* [Node](http://fostertheweb.com/2016/02/nodemon-inside-docker-container/)

## What Telepresence proxies

Telepresence currently proxies the following:

* The [special environment variables](https://kubernetes.io/docs/user-guide/services/#environment-variables) that expose the addresses of `Service` instances.
  E.g. `REDIS_MASTER_SERVICE_HOST`.
  These will be modified with new values based on the proxying logic, but that should be transparent to the application.
* The standard [DNS entries for services](https://kubernetes.io/docs/user-guide/services/#dns).
  E.g. `redis-master` and `redis-master.default.svc.cluster.local` will resolve to a working IP address.
* TCP connections to other `Service` instances that existed when the proxy was supported.
* Any additional environment variables that a normal pod would have, with the exception of a few environment variables that are different in the local environment.
  E.g. UID and HOME.
* TCP connections to specific hostname/port combinations specified on the command line.
  Typically this would be used for cloud resources, e.g. a AWS RDS database.
* TCP connections *from* Kubernetes to your local code, for ports specified on the command line.

Currently unsupported:

* TCP connections, environment variables, DNS records for `Service` instances created *after* Telepresence is started.
* SRV DNS records matching `Services`, e.g. `_http._tcp.redis-master.default`.
* UDP messages in any direction.
* For proxied addresses, only one destination per specific port number is currently supported.
  E.g. you can't proxy `remote1.example.com:5432` and `remote2.example.com:5432` at the same time.
* Access to volumes, including those for `Secret` and `ConfigMap` Kubernetes objects.

## Help us improve Telepresence!

We are considering various improvements to Telepresence, including:

* [Removing need for Kubernetes credentials](https://github.com/datawire/telepresence/issues/2)
* [Allowing running code locally without a container](https://github.com/datawire/telepresence/issues/1)
* Implementing any of the unsupported features mentioned above.

Please add comments to relevant tickets if you are interested in these features, or [file a new issue](https://github.com/datawire/telepresence/issues/new) if there is no existing ticket for a desired feature or bug report.

## Alternatives

Some alternatives to Telepresence:

* Minikube is a tool that lets you run a Kubernetes cluster locally.
  You won't have access to cloud resources, however, and your development cycle won't be as fast since access to local source code is harder.
  Finally, spinning up your full system may not be realistic if it's big enough.
* Docker Compose lets you spin up local containers, but won't match your production Kubernetes cluster.
  It also won't help you access cloud resources, you will need to emulate them.
* Pushing your code to the remote Kubernetes cluster.
  This is a somewhat slow process, and you won't be able to do the quick debug cycle you get from running code locally.
  
## Changelog

### 0.8 (unreleased)

Features:

* Basic logging of what Telepresence is doing, for easier debugging.
* Check for Kubernetes and Docker on startup, so problems are caught earlier.
* Better error reporting on crashes ([issue #19](https://github.com/datawire/telepresence/issues/19).

Bugfixes:

* Fixed bug where combination of `--rm` and `--detach` broke Telepresence on versions of Docker older than 1.13. Thanks to Jean-Paul Calderone for reporting the problem. ([issue #18](https://github.com/datawire/telepresence/issues/18)
