---
layout: doc
weight: 3
title: "What gets proxied"
categories: reference
---

### Networking access from the cluster

If you use the `--expose` option for `telepresence` with a given port the pod will forward traffic it receives on that port to your local process.
This allows the Kubernetes or OpenShift cluster to talk to your local process as if it was running in the pod.

By default the remote port and the local port match.
Here we expose port 8080 as port 8080 on the remote Deployment:

```console
$ telepresence --expose 8080 --new-deployment example \
    --run python3 -m http.server 8080
```

It is possible to expose a different local port than the remote port.
Here we expose port 8080 locally as port 80 on the remote Deployment:

```console
$ telepresence --expose 8080:80 --new-deployment example \
    --run python3 -m http.server 80
```

You can't expose ports <1024 on clusters that don't support running images as `root`.
This limitation is the default on OpenShift.

### Networking access to the cluster

The locally running process wrapped by `telepresence` has access to everything that a normal Kubernetes pod would have access to.
That means `Service` instances, their corresponding DNS entries, and any cloud resources you can normally access from Kubernetes.

To see this in action, let's start a `Service` and `Deployment` called `"helloworld"` in Kubernetes in the default namespace `"default"`, and wait until it's up and running.
The resulting `Service` will have three DNS records you can use:

1. `helloworld`, from a pod in the `default` namespace.
2. `helloworld.default` anywhere in the Kubernetes cluster.
3. `helloworld.default.svc.cluster.local` anywhere in the Kubernetes cluster.
   This last form will not work when using `telepresence` with `--method=vpn-tcp` on Linux (see [the relevant ticket](https://github.com/datawire/telepresence/issues/161) for details.)

We'll check the current Kubernetes context and then start a new pod:

```console
$ kubectl run --expose helloworld --image=nginx:alpine --port=80
```

Wait 30 seconds and make sure a new pod is available in `Running` state:

```console
$ kubectl get pod --selector=run=helloworld
NAME                          READY     STATUS    RESTARTS   AGE
helloworld-1333052153-63kkw   1/1       Running   0          33s
```

Now you can send queries to the new `Service` as if you were running inside Kubernetes:

```console
$ telepresence --new-deployment quickstart \
               --run curl http://helloworld.default
<!DOCTYPE html>
<html>
<head>
<title>Welcome to nginx!</title>
...
```

> **Having trouble?** Ask us a question in our [Gitter chatroom](https://gitter.im/datawire/telepresence).

### Networking access to cloud resources

When using `--method=inject-tcp`, the subprocess run by `telepresence` will have *all* of its traffic routed via the cluster.
That means transparent access to cloud resources like databases that are accessible from the Kubernetes cluster's private network or VPC.
It also means public servers like `google.com` will be routed via the cluster, but again only for the subprocess run by `telepresence` via `--run` or `--run-shell`.

When using `--method=vpn-tcp` *all* processes on the machine running `telepresence` will have access to the Kubernetes cluster.
Cloud resources will only be routed via the cluster if you explicitly specify them using `--also-proxy <hostname>`.
Access to public websites should not be affected or changed in any way.

### Environment variables

Environment variables set in the `Deployment` pod template will be available to your local process.
You also have access to all the environment variables Kubernetes sets automatically.
For example, here you can see the environment variables that get added for each `Service`:

```console
$ telepresence -m inject-tcp -n example --run env | grep KUBERNETES
KUBERNETES_PORT=tcp://10.0.0.1:443
KUBERNETES_SERVICE_PORT=443
KUBERNETES_PORT_443_TCP_ADDR=10.0.0.1
KUBERNETES_PORT_443_TCP_PORT=443
KUBERNETES_PORT_443_TCP_PROTO=tcp
KUBERNETES_PORT_443_TCP=tcp://10.0.0.1:443
KUBERNETES_SERVICE_HOST=10.0.0.1
```

### Volumes

Volumes configured in the `Deployment` pod template will also be made your local process.
This will work better with read-only volumes with small files like `Secret` and `ConfigMap`; a local database server writing to a remote volume will be slow.

Volume support requires a small amount of work on your part.
The root directory where all the volumes can be found will be set to the `TELEPRESENCE_ROOT` environment variable in the shell run by `telepresence`.
You will then need to use that env variable as the root for volume paths you are opening.

For example, all Kubernetes containers have a volume mounted at `/var/run/secrets` with the service account details.
Those files are accessible from Telepresence:

```console
$ telepresence -m inject-tcp -n myservice --run-shell
Starting proxy...
@minikube|$ echo $TELEPRESENCE_ROOT
/tmp/tmpk_svwt_5
@minikube|$ ls $TELEPRESENCE_ROOT/var/run/secrets/kubernetes.io/serviceaccount/
ca.crt  namespace  token
```

The files are available at a different path than they are on the actual Kubernetes environment.

One way to deal with that is to change your application's code slightly.
For example, let's say you have a volume that mounts a file called `/app/secrets`.
Normally you would just open it in your code like so:


```python
secret_file = open("/app/secrets")
```

To support volume proxying by Telepresence, you will need to change your code, for example:

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

### The complete list: what Telepresence proxies

#### `--method inject-tcp`

When using `--method inject-tcp`, Telepresence currently proxies the following:

* The [special environment variables](https://kubernetes.io/docs/user-guide/services/#environment-variables) that expose the addresses of `Service` instances.
  E.g. `REDIS_MASTER_SERVICE_HOST`.
* The standard [DNS entries for services](https://kubernetes.io/docs/user-guide/services/#dns).
  E.g. `redis-master` and `redis-master.default.svc.cluster.local` will resolve to a working IP address.
  These will work regardless of whether they existed when the proxy started.
* TCP connections to other `Service` instances, regardless of whether they existed when the proxy was started.
* Any environment variables that the `Deployment` explicitly configured for the pod.
* TCP connections to any hostname/port; all but `localhost` will be routed via Kubernetes.
  Typically this is useful for accessing cloud resources, e.g. a AWS RDS database.
* TCP connections *from* Kubernetes to your local machine, for ports specified on the command line using `--expose`
* Access to volumes, including those for `Secret` and `ConfigMap` Kubernetes objects.
* `/var/run/secrets/kubernetes.io` credentials (used to the [access the Kubernetes( API](https://kubernetes.io/docs/user-guide/accessing-the-cluster/#accessing-the-api-from-a-pod)).

Currently unsupported:

* SRV DNS records matching `Services`, e.g. `_http._tcp.redis-master.default`.
* UDP messages in any direction.

#### `--method vpn-tcp`

When using `--method vpn-tcp`, Telepresence currently proxies the following:

* The [special environment variables](https://kubernetes.io/docs/user-guide/services/#environment-variables) that expose the addresses of `Service` instances.
  E.g. `REDIS_MASTER_SERVICE_HOST`.
* The standard [DNS entries for services](https://kubernetes.io/docs/user-guide/services/#dns).
  E.g. `redis-master` and `redis-master.default`, but not those ending with `.local`. 
* Any environment variables that the `Deployment` explicitly configured for the pod.
* TCP connections to any `Service` in the cluster regardless of when they were started, as well as to any hosts explicitly listed with `--also-proxy`.
* TCP connections *from* Kubernetes to your local machine, for ports specified on the command line using `--expose`.
* Access to volumes, including those for `Secret` and `ConfigMap` Kubernetes objects.
* `/var/run/secrets/kubernetes.io` credentials (used to the [access the Kubernetes( API](https://kubernetes.io/docs/user-guide/accessing-the-cluster/#accessing-the-api-from-a-pod)).

Currently unsupported:

* Fully qualified Kubernetes DNS names that end with `.local`, e.g. `redis-master.default.svc.cluster.local`, won't work on Linux (see [the relevant ticket](https://github.com/datawire/telepresence/issues/161) for details.)
* UDP messages in any direction.
