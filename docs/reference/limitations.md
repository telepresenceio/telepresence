---
layout: doc
weight: 4
title: "Generic limitations & workarounds"
categories: reference
---

### Method-specific limitations

For method-specific limitations see the documentation on the [available proxying methods](/reference/methods.html).

### General limitations

#### Docker containers

A container run via `docker run` will not inherit the outgoing functionality of the Telepresence shell.
If you want to use Telepresence to proxy a containerized application you should install and run Telepresence inside the container itself.

#### `localhost` and the pod

`localhost` and `127.0.0.1` will end up accessing the host machine—the machine where you run `telepresence`—*not* the pod.
This can be a problem in cases where you are running multiple containers in a pod and you need your process to access a different container in the same pod.

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
