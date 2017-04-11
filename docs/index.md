## Introduction

Have you ever wanted the quick development cycle of local code while still having your code run within a remote Kubernetes cluster?
Telepresence allows you to develop locally, running your code on your machine, while still making your code appear as if it is running in Kubernetes.

1. **Your local process can talk to Kubernetes `Services` and cloud databases.**
   Your local process can access `Services` in the remote Kubernetes cluster, as well as cloud resources like AWS RDS even if they're in a private VPC.
2. Your local process has **the same environment variables and Kubernetes volumes** as the real pod.
   That means you can use `Secret`, `ConfigMap`, Downward API etc. even as your code runs locally.
3. **The Kubernetes cluster can talk to your local process.**
   Other `Services`, your `LoadBalancer` or your `Ingress` can send queries to the local process you are running.

**IMPORTANT:** Telepresence is currently in initial stages of development, so we expect it to change rapidly based on user feedback.

Please [file bugs and feature requests](https://github.com/datawire/telepresence/issues) or come [talk to us on Gitter](http://gitter.im/datawire/telepresence).

### How it works

Telepresence works by building a two-way network proxy (bootstrapped using `kubectl port-forward`) between a custom pod running inside a remote Kubernetes cluster and a process running on your development machine.
The custom pod is substituted for your normal pod that would run in production.

Environment variables from the remote pod are made available to your local process.
In addition, the local process has its networking transparently overridden such that DNS calls and TCP connections are routed over the proxy to the remote Kubernetes cluster.
This is implemented using `LD_PRELOAD`/`DYLD_INSERT_LIBRARIES` mechanism on Linux/OSX, where a shared library can be injected into a process and override library calls.

Volumes are proxied using [sshfs](https://github.com/libfuse/sshfs) and some clever bind mounting tricks ([bindfs](https://bindfs.org) on OS X, bind mounts on Linux.)

The result is that your local process has a similar environment to the remote Kubernetes cluster, while still being fully under your local control.

### Demo

<script type="text/javascript" src="https://asciinema.org/a/109183.js" id="asciicast-109183" async></script>

(The Privacy Badger browser extensions may hide the above; you can [see the demo here](https://asciinema.org/a/109183) too.)

## Why Telepresence: faster development, full control

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

### The slow status quo

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

### A fast development cycle with Telepresence

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

## Installing

You will need the following available on your machine:

* OS X or Linux.
* `kubectl` command line tool.
* Access to your Kubernetes cluster, with local credentials on your machine.
  You can do this test by running `kubectl get pod` - if this works you're all set.

You will then need to install the necessary additional dependencies:

* On OS X:

  ```
  brew install python3 torsocks sshfs bindfs
  ```
* On Ubuntu 16.04 or later:

  ```
  sudo apt install --no-install-recommends torsocks python3 openssh-client sshfs proot
  ```
* On Fedora:

  ```
  dnf install python3 torsocks openssh-clients sshfs libtalloc-devel
  wget https://github.com/proot-me/PRoot/archive/v5.1.0.tar.gz
  tar xzf v5.1.0.tar.gz
  cd PRoot-5.1.0/src
  make
  sudo make install
  ```

Then download Telepresence by running the following commands:

```
curl -L https://github.com/datawire/telepresence/raw/{{ site.data.version.version }}/cli/telepresence -o telepresence
chmod +x telepresence
```

Then move telepresence to somewhere in your `$PATH`, e.g.:

```
sudo 
mv telepresence /usr/local/bin
```


> **Need help?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

## Quickstart

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

**Important:** Your server needs to be listening on localhost, i.e. `127.0.0.1`.
Otherwise it won't be exposed to the remote server.

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

## Other features and functionality

### Using existing Deployments

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
        env:
        - name: YOUR_DATABASE_HOST
          value: somewhere.someplace.cloud.example.com
```

In order to run Telepresence you will need to do three things:

1. Replace your production `Deployment` with a custom `Deployment` that runs the Telepresence proxy.
2. Run the Telepresence client locally.
3. Run your own code inside the shell started the Telepresence client.

Let's go through these steps one by one.

First, instead of running the production `Deployment` above, you will need to run a different one that runs the Telepresence proxy instead.
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
        env:
        - name: YOUR_DATABASE_HOST
          value: somewhere.someplace.cloud.example.com
```

You should apply this file to your cluster:

```console
$ kubectl apply -f telepresence-deployment.yaml
```

Next, you need to run the local Telepresence client on your machine.
You want to do the following:

1. Expose port 8080 in your code to Kubernetes.
2. Connect specifically to the `servicename-deployment` pod you created above, in case there are multiple Telepresence users in the cluster.
3. Run a local process in the above setup.

Services `thing1` and `thing2` will be available to your code automatically so no special parameters are needed for them.
You can do so with the following command line, which uses `--deployment` instead of `--new-deployment` since there is an existing `Deployment` object:

```console
$ telepresence --deployment servicename-deployment \
               --expose 8080 \
               --run-shell
@yourcluster|$ python servicename.py --port=8080 
```

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

You are now running your own code locally, attaching it to the network stack of the Telepresence client and using the environment variables Telepresence client extracted.
Your code is connected to the remote Kubernetes cluster.

### Environment variables and volumes

Environment variables set in the `Deployment` pod template (as in the example above) will be available to your local process.

Likewise, volumes configured in the `Deployment` pod template will also be transparently available to your local process: no extra work needed.
This is mostly intended for read-only volumes like `Secret` and `ConfigMap`, you probably don't want a local database writing to a remote volume.

### Kubernetes namespaces

If you want to proxy to a Deployment in a non-default namespace you can pass the `--namespace` argument to Telepresence:

```console
$ telepresence --namespace yournamespace --deployment yourservice --run-shell
```

### Accessing the pod from your process

In general Telepresence proxies all IPs and DNS lookups via the remote proxy pod.
There is one exception, however.

`localhost` and `127.0.0.1` will end up accessing the host machine, the machine where you run `telepresence`, *not* the pod as is usually the case.
This mostly is a problem in cases where you are running multiple containers in a pod and you need your process to access a different container in the same pod.

The solution is to access the pod via its IP, rather than at `127.0.0.1`.
You can have the pod IP configured as an environment variable `$MY_POD_IP` in the Deployment using the Kubernetes [Downward API](https://kubernetes.io/docs/tasks/configure-pod-container/environment-variable-expose-pod-information/):

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: servicename
        image: datawire/telepresence-k8s:{{ site.data.version.version }}
        env:
        - name: MY_POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
```


## Limitations, caveats and workarounds

### Incompatible programs

Because of the mechanism Telepresence uses to intercept networking calls:

* suid binaries won't work inside a Telepresence shell.
* Statically linked binaries won't work.
* Custom DNS resolvers that parse `/etc/resolv.conf` and do DNS lookups themselves won't work.

However, this only impacts outgoing connections.
Incoming proxying (from Kubernetes) will still work with these binaries.

### Golang

Programs written with the Go programming language will not work by default, because Go uses a custom system call implementation and has its own DNS resolver.
Again, this only impacts outgoing connections, incoming connections will still work.

To workaround these limitations you can do the following in your development environment (there is no need to change anything for production):

* Use `gccgo` instead of `go build`.
* Do `export GODEBUG=netdns=cgo` to [force Go to use the standard DNS lookup mechanism](https://golang.org/pkg/net/#hdr-Name_Resolution) rather than its own internal one.

### Docker containers

A container run via `docker run` will not inherit the outgoing functionality of the Telepresence shell.
If you want to use Telepresence to proxy a containerized application you should run Telepresence *inside* the container.

### What Telepresence proxies

Telepresence currently proxies the following when using `--run-shell`:

* The [special environment variables](https://kubernetes.io/docs/user-guide/services/#environment-variables) that expose the addresses of `Service` instances.
  E.g. `REDIS_MASTER_SERVICE_HOST`.
* The standard [DNS entries for services](https://kubernetes.io/docs/user-guide/services/#dns).
  E.g. `redis-master` and `redis-master.default.svc.cluster.local` will resolve to a working IP address.
  These will work regardless of whether they existed when the proxy started.
* TCP connections to other `Service` instances, whether or not they existed when the proxy was started.
* Any additional environment variables that the `Deployment` explicitly configured for the pod.
* TCP connections to any hostname/port; all but `localhost` will be routed via Kubernetes.
  Typically this is useful for accessing cloud resources, e.g. a AWS RDS database.
* TCP connections *from* Kubernetes to your local code, for ports specified on the command line.
* Access to volumes, including those for `Secret` and `ConfigMap` Kubernetes objects.
* `/var/run/secrets/kubernetes.io` credentials (used to the [access the Kubernetes( API](https://kubernetes.io/docs/user-guide/accessing-the-cluster/#accessing-the-api-from-a-pod)).

Currently unsupported:

* SRV DNS records matching `Services`, e.g. `_http._tcp.redis-master.default`.
* UDP messages in any direction.

## Help us improve Telepresence!

We are considering various improvements to Telepresence, including:

* [Supporting running local Docker containers, not just local processes](https://github.com/datawire/telepresence/issues/76)
* [Removing need for Kubernetes credentials](https://github.com/datawire/telepresence/issues/2)

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

#### 0.27 (unreleased)

Features:

* Remote volumes are now accessible by the local process.
  ([#78](https://github.com/datawire/telepresence/issues/78))

#### 0.26 (April 6, 2017)

Backwards incompatible changes:

* New requirements: openssh client and Python 3 must be installed for Telepresence to work.
  Docker is no longer required.

Features:

* Docker is no longer required to run Telepresence.
  ([#78](https://github.com/datawire/telepresence/issues/78))
* Local servers just have to listen on localhost (127.0.0.1) in order to be accessible to Kubernetes; previously they had to listen on all interfaces.
  ([#77](https://github.com/datawire/telepresence/issues/77))

0.25 failed the release process due to some sort of mysterious mistake.

#### 0.24 (April 5, 2017)

Bug fixes:

* The `KUBECONFIG` environment variable will now be respected, so long as it points at a path inside your home directory.
  ([#84](https://github.com/datawire/telepresence/issues/84))
* Errors on startup are noticed, fixing issues with hanging indefinitely in the "Starting proxy..." phase.
  ([#83](https://github.com/datawire/telepresence/issues/83))

#### 0.23 (April 3, 2017)

Bug fixes:

* Telepresence no longer uses lots of CPU busy-looping.
  Thanks to Jean-Paul Calderone for the bug report.

#### 0.22 (March 30, 2017)

Features:

* Telepresence can now interact with any Kubernetes namespace, not just the default one.
  ([#74](https://github.com/datawire/telepresence/issues/74))

Backwards incompatible changes:

* Running Docker containers locally (`--docker-run`) is no longer supported.
  This feature will be reintroduced in the future, with a different implementation, if there is user interest.
  [Add comments here](https://github.com/datawire/telepresence/issues/76) if you're interested.

#### 0.21 (March 28, 2017)
  
Bug fixes:

* Telepresence exits when connection is lost to the Kubernetes cluster, rather than hanging.
* Telepresence notices when the proxy container exits and shuts down.
  ([#24](https://github.com/datawire/telepresence/issues/24))

#### 0.20 (March 27, 2017)

Bug fixes:

* Telepresence only copies environment variables explicitly configured in the `Deployment`, rather than copying all environment variables.
* If there is more than one container Telepresence copies the environment variables from the one running the `datawire/telepresence-k8s` image, rather than the first one.
  ([#38](https://github.com/datawire/telepresence/issues/38))

#### 0.19 (March 24, 2017)

Bug fixes:

* Fixed another issue with `--run-shell` on OS X.

#### 0.18 (March 24, 2017)

Features:

* Support `--run-shell` on OS X, allowing local processes to be proxied.
* Kubernetes-side Docker image is now smaller.
  ([#61](https://github.com/datawire/telepresence/issues/61))

Bug fixes:
  
* When using `--run-shell`, allow access to the local host.
  Thanks to Jean-Paul Calderone for the bug report.
  ([#58](https://github.com/datawire/telepresence/issues/58))

#### 0.17 (March 21, 2017)

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

#### 0.16 (March 20, 2017)

Bug fixes:

* Disable `--run-shell` on OS X, hopefully temporarily, since it has issues with System Integrity Protection.
* Fix Python 3 support for running `telepresence`.

#### 0.14 (March 20, 2017)

Features:

* Added `--run-shell`, which allows proxying against local processes.
  ([#1](https://github.com/datawire/telepresence/issues/1))

#### 0.13 (March 16, 2017)

Bug fixes:

* Increase time out for pods to start up; sometimes it takes more than 30 seconds due to time to download image.

#### 0.12 (March 16, 2017)

Bug fixes:

* Better way to find matching pod for a Deployment.
  ([#43](https://github.com/datawire/telepresence/issues/43))

#### 0.11 (March 16, 2017)

Bug fixes:

* Fixed race condition that impacted `--expose`.
  ([#40](https://github.com/datawire/telepresence/issues/40))

#### 0.10 (March 15, 2017)

Bug fixes:

* Fixed race condition the first time Telepresence is run against a cluster.
  ([#33](https://github.com/datawire/telepresence/issues/33))

#### 0.9 (March 15, 2017)

Features:

* Telepresence now detects unsupported Docker configurations and complain.
  ([#26](https://github.com/datawire/telepresence/issues/26))
* Better logging from Docker processes, for easier debugging.
  ([#29](https://github.com/datawire/telepresence/issues/29))

Bug fixes:

* Fix problem on OS X where Telepresence failed to work due to inability to share default location of temporary files.
  ([#25](https://github.com/datawire/telepresence/issues/25))

#### 0.8 (March 14, 2017)

Features:

* Basic logging of what Telepresence is doing, for easier debugging.
* Check for Kubernetes and Docker on startup, so problems are caught earlier.
* Better error reporting on crashes. ([#19](https://github.com/datawire/telepresence/issues/19))

Bug fixes:

* Fixed bug where combination of `--rm` and `--detach` broke Telepresence on versions of Docker older than 1.13. Thanks to Jean-Paul Calderone for reporting the problem. ([#18](https://github.com/datawire/telepresence/issues/18))
