Have you ever wanted the quick development cycle of local code while still having your code run within a remote Kubernetes cluster?
Telepresence allows you to run your code locally while still:

1. Giving your code access to Services in a remote Kubernetes cluster.
2. Giving your code access to cloud resources like AWS RDS or Google PubSub.
3. Allowing Kubernetes to access your code as if it were in a normal pod within the cluster.

## Theory of operation

Let's assume you have a web service which listens on port 8080, and has a Dockerfile which gets built to an image called `examplecom/yourcode`.
Your service depends on other Kubernetes `Service` instances (`thing1` and `thing2`), and on a cloud database.

The Kubernetes production environment looks like this:

<div class="mermaid">
graph LR
  subgraph Kubernetes in Cloud
    code["k8s.Pod: yourcode"]
    s1["k8s.Service: yourcode"]-->code
    code-->s2["k8s.Service: thing1"]
    code-->s3["k8s.Service: thing2"]
    code-->c1>"Cloud Database (AWS RDS)"]
  end
</div>

Currently Telepresence works by running your code locally in a Docker container, and forwarding requests to/from the remote Kubernetes cluster.

<div class="mermaid">
graph LR
  subgraph Laptop
    code["yourcode, in container"]---client[Telepresence client]
  end
  subgraph Kubernetes in Cloud
    client-.-proxy["k8s.Pod: Telepresence proxy"]
    s1["k8s.Service: yourcode"]-->proxy
    proxy-->s2["k8s.Service: thing1"]
    proxy-->s3["k8s.Service: thing2"]
    proxy-->c1>"Cloud Database (AWS RDS)"]
  end
</div>

(Future versions may allow you to run your code locally directly, without a local container.
[Let us know](https://github.com/datawire/telepresence/issues/1) if this a feature you want.)

## How to use Telepresence

Continuing the example above, your Kubernetes configuration will typically have a `Service`:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: yourcode-service
spec:
  ports:
    - port: 8080
      protocol: TCP
      targetPort: 8080
  selector:
    name: yourcode
```

You will also have a `Deployment` that actually runs your code, with labels that match the `Service` `selector`:

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: yourcode-deployment
spec:
  replicas: 3
  template:
    metadata:
      labels:
        name: yourcode
    spec:
      containers:
      - name: yourcode
        image: examplecom/yourcode:1.0.2
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
  name: yourcode-deployment
spec:
  replicas: 1  # <-- only one replica
  template:
    metadata:
      labels:
        name: yourcode
    spec:
      containers:
      - name: yourcode
        image: datawire/telepresence-remote:0.1  # <-- new image
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

1. Expose port 8080 in your code to Kuberentes.
2. Proxy `somewhere.someplace.cloud.example.com` port 5432 via Kubernetes, since it's probably not accessible outside of your cluster.
3. Connect specifically to the `yourcode-deployment` pod you created above, in case there are multiple Telepresence users in the cluster.

Services `thing1` and `thing2` will be available to your code automatically so no special parameters are needed for them.
You can do so with the following command line:

```console
$ docker run --rm --name=yourcode-deployment  -v $HOME/.kube:/opt/.kube:ro -v $HOME/.minikube:$HOME/.minikube:ro -v $PWD:/output datawire/local-telepresence --uid $UID --proxy somewhere.someplace.cloud.example.com:5432 --expose 8080 yourcode-deployment
```

XXX potential simplified version:

```console
$ telepresence --proxy somewhere.someplace.cloud.example.com:5432 \
               --expose 8080 \
               yourcode-deployment
A new environment file named `yourcode-deployment.env` was generated.
```

### 3. Run your code locally in a container

You can now run your own code locally inside Docker, attaching it to the network stack of the Telepresence client and using the environment variables Telepresence client extracted:

```console
$ docker run --net=container:yourcode-deployment \ 
             --env-file=yourcode-deployment.env \
             examplecom/yourcode:latest
```

Your code is now connected to the remote Kubernetes cluster.

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
* XXX NOT YET Any additional environment variables that a normal pod would have, with the exception of a few environment variables that are different in the local environment.
  E.g. UID and HOME.
* XXX NOT YET TCP connections to specific hostname/port combinations specified on the command line.
  Typically this would be used for cloud resources, e.g. a AWS RDS database.
* TCP connections *from* Kubernetes to your local code, for ports specified on the command line.

Currently unsupported:

* TCP connections, environment variables, DNS records for `Service` instances created *after* Telepresence is started.
* SRV DNS records matching `Services`, e.g. `_http._tcp.redis-master.default`.
* UDP messages in any direction.
* For proxied addresses, only one destination per specific port number is currently supported.
  E.g. you can't proxy `remote1.example.com:5432` and `remote2.example.com:5432` at the same time.

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
