---
layout: doc
weight: 2
title: "Features and Functionality"
categories: user-guide
---

If you haven't read the Getting Started guide yet you should [read that first](/user-guide/getting-started.html).

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

### Environment variables

Environment variables set in the `Deployment` pod template (as in the example above) will be available to your local process.

### Volumes

Volumes configured in the `Deployment` pod template will also be made your local process.
This is mostly intended for read-only volumes like `Secret` and `ConfigMap`, you probably don't want a local database writing to a remote volume.

Volume support requires a small amount of work on your part.
The root directory where all the volumes can be found will be set to the `TELEPRESENCE_ROOT` environment variable in the shell run by `telepresence`.
You will then need to use that env variable as the root for volume paths you are opening.

For example, all Kubernetes containers have a volume mounted at `/var/run/secrets` with the service account details.
Those files are accessible from Telepresence:

```console
$ cli/telepresence --new-deployment myservice --run-shell
Starting proxy...
@minikube|$ echo $TELEPRESENCE_ROOT
/tmp/tmpk_svwt_5
@minikube|$ ls $TELEPRESENCE_ROOT/var/run/secrets/kubernetes.io/serviceaccount/
ca.crt  namespace  token
```

Of course, the files are available at a different path than they are on the actual production Kubernetes environment.

One way to deal with that is to modify your application's code slightly.
For example, let's say you have a volume that mounts a file called `/app/secrets`.
Normally you would just open it in your code like so:


```python
secret_file = open("/app/secrets")
```

In order to support volume proxying by Telepresence, you will need to change
your code (note that this is not the most succinct way to express this, it's more verbose in order to be clear to non-Python programmers):

```python
volume_root = "/"
if "TELEPRESENCE_ROOT" in os.environ:
    volume_root = os.environ["TELEPRESENCE_ROOT"]
secret_file = open(os.path.join(volume_root, "app/secrets"))
```

By falling back to `/` when the environment variable is not set your code will continue to work in its normal Kubernetes setting.

Another way you can do this is by using the [proot](http://proot-me.github.io/) utility on Linux, which allows you to do fake bind mounts without being root.
For example, presuming you've installed `proot` (`apt install proot` on Ubuntu), in the following example we bind `$TELEPRESENCE_ROOT/var/run/secrets` to `/var/run/secrets`.
That means code doesn't need to be modified as the paths are in the expected location:

```console
@minikube|$ proot -b $TELEPRESENCE_ROOT/var/run/secrets/:/var/run/secrets bash
$ ls /var/run/secrets/kubernetes.io/serviceaccount/
ca.crt  namespace  token
```

### kubectl context

By default Telepresence uses whatever the current context is for `kubectl`.
If you want to choose a specific context you can use the `--context` option to `telepresence`.
For example:

```console
$ telepresence --context minikube --new-deployment myservice --run-shell
```

You can choose any context listed in `kubectl config get-contexts`.

If you've [set a namespace for the context](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/#setting-the-namespace-preference) then that namespace will be used to find/create the `Deployment`, but you can also choose a namespace explicitly, as shown in the next section.

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
