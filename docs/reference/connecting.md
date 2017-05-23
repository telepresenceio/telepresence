---
layout: doc
weight: 1
title: "Connecting to the cluster"
categories: reference
---

To use Telepresence with a cluster (Kubernetes or OpenShift, local or remote) you need to run a proxy inside the cluster.
There are three ways of doing so.

### Creating a new deployment.

By using the `--new-deployment` option `telepresence` can create a new deployment for you.
It will be deleted when the local `telepresence` process exits.
For example:

```console
$ telepresence --new-deployment myserver --run-shell
```

This will create two Kubernetes objects, a `Deployment` and a `Service`, both named `myserver`.
(On OpenShift a `DeploymentConfig` will be used instead of `Deployment`.)

If `telepresence` crashes badly enough (e.g. you used `kill -9`) you will need to manually delete the `Deployment` and `Service` that Telepresence created.

### Swapping out an existing deployment

If you already have your code running in the cluster you can use the `--swap-deployment` option to replace the existing deployment with the Telepresence proxy.
When the `telepresence` process exits it restores the earlier state of the `Deployment` (or `DeploymentConfig` on OpenShift).

```console
$ telepresence --swap-deployment myserver --run-shell
```

If you have more than one container in the pods created by the deployment you can also specify the container name:

```console
$ telepresence --swap-deployment myserver:containername --run-shell
```

If `telepresence` crashes badly enough (e.g. you used `kill -9`) you will need to manually restore the `Deployment`.


### Running Telepresence manually

You can also choose to run the Telepresence manually by starting a `Deployment` that runs the proxy in a pod.

The `Deployment` should only have 1 replica, and use the Telepresence different image:

```yaml
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: myservice
spec:
  replicas: 1  # <-- only one replica
  template:
    metadata:
      labels:
        name: myservice
    spec:
      containers:
      - name: myservice
        image: datawire/telepresence-k8s:{{ site.data.version.version }}  # <-- new image
```

You should apply this file to your cluster:

```console
$ kubectl apply -f telepresence-deployment.yaml
```

Next, you need to run the local Telepresence client on your machine, using `--deployment` to indicate the name of the `Deployment` object whose pod is running `telepresence/datawire-k8s`:

```console
$ telepresence --deployment myservice --run-shell
```

Telepresence will leave the deployment untouched when it exits.
