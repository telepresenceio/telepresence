# How Telepresence works

## Goals

Our goals for Telepresence are:

1. **Transparency:** make the local proxied process match the Kubernetes environment as closely as possible.
2. **Isolation:** only the proxied process has its environment modified.
    This goal is much like that of a container: an isolated process-specific environment.
3. **Cross-platform:** Linux and macOS work the same way, when possible.
   Linux provides far more capabilities (mount namespaces, bind mounts, network namespaces) than macOS.
4. **Compatibility:** works with any program.

Achieving all these goals at the same time is not always possible.
We've therefore chosen to support more than one method of proxying, with the different methods each having its own [benefits and limitations](/reference/methods.html).

## How it works

Telepresence works by building a two-way network proxy (bootstrapped using `kubectl port-forward` or `oc port-forward`) between a custom pod running inside a remote (or local) Kubernetes cluster and a process running on your development machine.
The custom pod is substituted for your normal pod that would run in production.
Typically you'd want to do this to a testing or staging cluster, not your production cluster.

Environment variables from the remote pod are made available to your local process.
In addition, the local process has its networking transparently overridden such that DNS calls and TCP connections are routed over the proxy to the remote Kubernetes cluster.
This happens one of two ways:

* When using `--method vpn-tcp`, the default, a VPN-like tunnel is created using a program called [sshuttle](http://sshuttle.readthedocs.io/), which tunnels the packets over the SSH connection, and forwards DNS queries to a DNS proxy in the cluster.
* When using `--method inject-tcp` this is implemented using `LD_PRELOAD`/`DYLD_INSERT_LIBRARIES` mechanism on Linux/OSX, where a shared library can be injected into a process and override library calls.
  In particular, it overrides DNS resolution and TCP connection and routes them via a SOCKS proxy to the cluster.
  We wrote [a blog post](https://www.datawire.io/code-injection-on-linux-and-macos/) explaining LD_PRELOAD in more detail.
* When using `--method container` (which is implied by `--docker-run`) all network traffic from your container is forwarded to the cluster by running sshuttle in the same network namespace as your container.
* The experimental `--method teleproxy` works much like `--method vpn-tcp` but uses Teleproxy (FIXME: link) instead of sshuttle.
  One key difference is that DNS resolution is done locally rather than in the cluster.

Volumes are proxied using [sshfs](https://github.com/libfuse/sshfs), with their location available to the container as an environment variable.

The result is that your local process has a similar environment to the remote Kubernetes cluster, while still being fully under your local control.

### vpn-tcp method in detail

Telepresence figures out the CIDR for Kubernetes Pods and Services, and any cloud hosts specified with `--also-proxy`, and tells `sshuttle` to forward traffic to those IPs via the proxy Pod running in Kubernetes.
`sshuttle` effectively acts as a VPN for these packets.

For DNS the implementation is more complex, since base `sshuttle` is insufficient.
`sshuttle` will capture all packets going to your default nameservers, and instead forward them to a custom DNS server running inside the Telepresence Pod in the Kubernetes cluster.
For now we'll assume this is a remote cluster.
Some examples can help explain the process.

Whenever you do a DNS lookup your DNS client library may add a suffix, try to resolve that, and if that fails try the original hostname you provided.
For example, if you're working at the offices of Example.com then the DHCP server in your office may tell clients to add `example.com` to DNS lookups.
Thus, when you lookup the domain `myservice` your DNS client will first try `myservice.example.com` and then `myservice` if that doesn't work.

On startup:

1. `telepresence` does a lookup of `hellotelepresence`.
2. Your DNS client library turns this into a lookup of `hellotelepresence.example.com`.
3. `sshuttle` forwards this to the custom Telepresence DNS server.
4. The Telepresence DNS server recognizes the `hellotelepresence` marker, and so now it knows the suffix it needs to filter is `example.com`.

Next let's say you do:

```console
$ curl http://myservice:8080
```

inside a Telepresence-proxied shell.

1. Your DNS client library turns this into a lookup of `myservice.example.com`.
2. `sshuttle` forwards this to the custom Telepresence DNS server.
3. The custom Telepresence DNS server strips off `example.com`, and does a local DNS lookup of `myservice`.
4. The Kubernetes DNS server replies with the IP of the `myservice` Service.
5. The custom Telepresence DNS server hands this back, `sshuttle` forwards it back, and eventually `curl` gets the Service IP.
6. `curl` opens connection to that IP, `sshuttle` forwards it to the Kubernetes cluster.

#### Local clusters: minikube/minishift, Docker Desktop, etc.

There is an additional complication when running a cluster locally in a VM, using something like minikube, minishift, or Docker Desktop.
Let's say you lookup `google.com`.

1. `sshuttle` forwards `google.com` to Kubernetes (via Telepresence DNS server).
2. Kubernetes DNS server doesn't know about any such Service, so it does normal DNS lookup.
3. The normal DNS lookup might get routed via the host machine.
4. `sshuttle` captures all DNS lookups going from the host machine.
5. Your DNS lookup is now in an infinite loop.

To solve this Telepresence will detect minikube, minishift, and Docker Desktop.
When it does, the Telepresence DNS server will forward DNS requests that aren't Kubernetes-specific to an external DNS server that is different than the ones your host machine is using.
E.g. it might use Google's public DNS if your host isn't.
As a result these DNS lookups aren't captured by `sshuttle` and the infinite loop is prevented.

### teleproxy method in detail

Teleproxy talks to the cluster and figures out the CIDR for Kubernetes Pods and Services and forwards traffic to those IPs via the proxy Pod running in the cluster.
It works in a manner similar to sshuttle. Because Teleproxy is aware of the Services in the cluster, it can perform DNS resolution locally.
Support for `--also-proxy` will be added in the future.

### inject-tcp method in detail

A custom SOCKS proxy is run on the Kubernetes pod, which uses [Tor's extended SOCKSv5 protocol](https://gitweb.torproject.org/torsocks.git/tree/doc/socks/socks-extensions.txt) which adds support for DNS lookups.
`kubectl port-forward` creates a tunnel to this SOCKS proxy.

A subprocess is then run using `torsocks`, a library that uses `LD_PRELOAD` to override TCP and (most) DNS lookups so that they get routed via the SOCKSv5 proxy.
All network traffic from the subprocess goes to the cluster, so `--also-proxy` is not required.

### container method in detail

Telepresence can also proxy a Docker container, using a variant on the `vpn-tcp` method: `sshuttle` is run inside a Docker container.

The user's specified Docker container is then run using the same network namespace as the proxy container with `sshuttle`.
(In typical Docker usage, in contrast, each container gets its own separate network namespace by default.)
All network traffic from the container goes to the cluster, so `--also-proxy` is not required.
