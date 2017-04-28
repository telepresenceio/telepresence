---
layout: doc
weight: 3
title: "Limitations and Workarounds"
categories: user-guide
---

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
If you want to use Telepresence to proxy a containerized application you should install and run Telepresence inside the container itself.


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
