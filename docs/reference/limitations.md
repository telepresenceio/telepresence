---
layout: doc
weight: 4
title: "Limitations and workarounds"
categories: reference
---

### Incompatible programs

Because of the mechanism Telepresence uses to intercept networking calls:

* suid binaries won't work inside a Telepresence shell.
* Statically linked binaries won't work.
* Custom DNS resolvers that parse `/etc/resolv.conf` and do DNS lookups themselves won't work.

Thus command line tools like `ping`, `nslookup`, `dig`, `host` and `traceroute` won't work either because they do lower-level DNS or are suid.

However, this only impacts outgoing connections.
Incoming proxying (from Kubernetes) will still work with these binaries.

### Golang

Programs written with the Go programming language will not work by default.

Go uses a custom system call implementation and has its own DNS resolver.
This causes connections *to* Kubernetes not to work with the current networking implementation.
Incoming connections will still work.

To workaround these limitations you can do the following in your development environment (there is no need to change anything for production):

* Use `gccgo` instead of `go build`.
* Do `export GODEBUG=netdns=cgo` to [force Go to use the standard DNS lookup mechanism](https://golang.org/pkg/net/#hdr-Name_Resolution) rather than its own internal one.

Alternatively, if you only care about incoming connections just run the Go program in another shell in parallel to `telepresence`.

### Docker containers

A container run via `docker run` will not inherit the outgoing functionality of the Telepresence shell.
If you want to use Telepresence to proxy a containerized application you should install and run Telepresence inside the container itself.

### `localhost` and the pod

Telepresence proxies all IPs and DNS lookups via the remote proxy pod.
There is one exception, however.

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
