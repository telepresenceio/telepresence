Have you ever wanted the quick development cycle of local code while still having your code run within a remote Kubernetes cluster?
Telepresence allows you to run your code locally while still:

1. Giving your code access to Services in a remote Kubernetes cluster.
2. Giving your code access to cloud resources like AWS RDS or Google PubSub.
3. Allowing Kubernetes to access your code as if it were in a normal pod within the cluster.
4. Your code can run either as a normal process, or as a local docker container.

**IMPORTANT:** Telepresence is currently in the prototyping stage, and we expect it to change rapidly based on user feedback.

Please [file bugs and feature requests](https://github.com/datawire/telepresence/issues) or come [talk to us on Gitter](http://gitter.im/datawire/telepresence).

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

Telepresence works by running your code *locally*, as a normal local process, and then forwarding forwarding requests to/from the remote Kubernetes cluster.

<div class="mermaid">
graph TD
  subgraph Laptop
    code["Source code for servicename"]==>local["local process"]
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

This means development is fast: you only have to change your code and restart your process.
Many web frameworks also do automatic code reload, in which case you won't even need to restart.

You can also run your code in a Docker container, which means you just need to rebuild a local image and restart to test it out.
Even better, you can use a development-oriented Docker image or configuration, with live reloads of code changes or the ability to attach a debugger.

## Installing

You will need the following available on your machine:

* OS X or Linux.
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

If you want to proxy normal local processes, rather than just local Docker containers, you'll also need to install a tool called `torsocks`.

On OS X:

```console
$ brew install torsocks
```

On Ubuntu:

```console
$ sudo apt install --no-install-recommends torsocks
```

> **Need help?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

## Quickstart

We'll start out by using Telepresence with a newly created Kubernetes `Deployment`, just so it's clearer what is going on.
In the next section we'll discuss using Telepresence with an existing `Deployment` - you can [skip ahead](#in-depth-usage) if you want.

To get started we'll use `telepresence --new-deployment quickstart` to create a new `Deployment` and matching `Service`.
The client will connect to the remote Kubernetes cluster via that `Deployment` and then run a local Docker container that is proxied into the remote cluster.
You'll then use the `--run-shell` argument to start a shell that is proxied to the remote Kubernetes cluster.

**IMPORTANT:** `--run-shell` currently doesn't work on OS X.
Use the `--docker-run` command instead, [documented below](#proxying-docker-containers).

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

### Proxying Docker containers

Just like `telepresence` can proxy local processes, it can also proxy a local Docker container by using the `--docker-run` argument, followed by arguments that specify how that local container should be created: these arguments will match those passed to `docker run`.

For example, we can run a shell inside a container running the `busybox` Docker image:

```console
host$ telepresence --new-deployment quickstart --docker-run \
      --rm -i -t busybox /bin/sh
localcontainer$ wget -qO- "http://${HELLOWORLD_SERVICE_HOST}:${HELLOWORLD_SERVICE_PORT}/"
<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
...
```

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

### Proxying from Kubernetes to your local process or container

So far you've seen how local processes and local containers can access the remote Kubernetes cluster's services.

You can also run a local server (either in a local process or a a local container) that listens on port 8080 and it will be exposed and available inside the Kubernetes cluster.
Let's say you want to serve some static files from your local machine.
You can mount the current directory as a Docker volume, run a webserver on port 8080, and pass `--expose 8080` to Telepresence so it knows it needs to expose that port to the Kubernetes cluster:

```console
host$ echo "hello world" > file.txt
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
          --image=busybox --restart Never --command /bin/sh
k8s-pod# wget -qO- http://quickstart.default.svc.cluster.local:8080/file.txt
hello world
```

Similarly, you can listen on a port inside a process started via `--run-shell`.

**Important:** Your server needs to listen on all interfaces, not just `127.0.0.1`, e.g. by listening to interface `0.0.0.0`.
Otherwise it won't be exposed to the remote server.

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

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

You will also have a `Deployment` that actually runs your code, with labels that match the `Service` `selector`.
Let's assume your existing deployment uses a database at `somewhere.someplace.cloud.example.com` port 5432, so you pass that information to the container as an environment variable:

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

### Run the Telepresence proxy in Kubernetes

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

### Run the local Telepresence client on your machine

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

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

You are now running your own code locally inside Docker, attaching it to the network stack of the Telepresence client and using the environment variables Telepresence client extracted.
Your code is connected to the remote Kubernetes cluster.

### (Optional) Better local development with Docker

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
* TCP connections to other `Service` instances that existed when the proxy was started.
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
* `/var/run/secrets/kubernetes.io` credentials (used to the [access the Kubernetes( API](https://kubernetes.io/docs/user-guide/accessing-the-cluster/#accessing-the-api-from-a-pod)).

## Help us improve Telepresence!

We are considering various improvements to Telepresence, including:

* [Removing need for Kubernetes credentials](https://github.com/datawire/telepresence/issues/2)
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

### 0.17 (Unreleased)

Bug fixes:

* Fix problem with tmux and wrapping when using `--run-shell`.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#51](https://github.com/datawire/telepresence/issues/51))
* Fix problem with non-login shells, e.g. with gnome-terminal.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#52](https://github.com/datawire/telepresence/issues/52))
* Use the Deployment's namespace, not the Deployment's spec namespace since that may not have a namespace set.
  Thanks to Jean-Paul Calderone for the patch.
* Hide torsocks messages.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#50](https://github.com/datawire/telepresence/issues/50))

### 0.16 (March 20, 2017)

Bug fixes:

* Disable `--run-shell` on OS X, hopefully temporarily, since it has issues with System Integrity Protection.
* Fix Python 3 support for running `telepresence`.

### 0.14 (March 20, 2017)

Features:

* Added `--run-shell`, which allows proxying against local processes.
  ([#1](https://github.com/datawire/telepresence/issues/1))

### 0.13 (March 16, 2017)

Bug fixes:

* Increase time out for pods to start up; sometimes it takes more than 30 seconds due to time to download image.

### 0.12 (March 16, 2017)

Bug fixes:

* Better way to find matching pod for a Deployment.
  ([#43](https://github.com/datawire/telepresence/issues/43))

### 0.11 (March 16, 2017)

Bug fixes:

* Fixed race condition that impacted `--expose`.
  ([#40](https://github.com/datawire/telepresence/issues/40))

### 0.10 (March 15, 2017)

Bug fixes:

* Fixed race condition the first time Telepresence is run against a cluster.
  ([#33](https://github.com/datawire/telepresence/issues/33))

### 0.9 (March 15, 2017)

Features:

* Telepresence now detects unsupported Docker configurations and complain.
  ([#26](https://github.com/datawire/telepresence/issues/26))
* Better logging from Docker processes, for easier debugging.
  ([#29](https://github.com/datawire/telepresence/issues/29))

Bug fixes:

* Fix problem on OS X where Telepresence failed to work due to inability to share default location of temporary files.
  ([#25](https://github.com/datawire/telepresence/issues/25))

### 0.8 (March 14, 2017)

Features:

* Basic logging of what Telepresence is doing, for easier debugging.
* Check for Kubernetes and Docker on startup, so problems are caught earlier.
* Better error reporting on crashes. ([#19](https://github.com/datawire/telepresence/issues/19))

Bug fixes:

* Fixed bug where combination of `--rm` and `--detach` broke Telepresence on versions of Docker older than 1.13. Thanks to Jean-Paul Calderone for reporting the problem. ([#18](https://github.com/datawire/telepresence/issues/18))
